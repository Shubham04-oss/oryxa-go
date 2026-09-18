package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// A connection knows what it can reach; a member should not have to type it.
//
// Indexing a repository, a channel or a database used to mean knowing its
// identifier — a Slack channel id, a Notion database id — and typing it into
// a box that could not tell a wrong one from a right one until the first pass
// failed. The connection can simply be asked, through the same read proxy
// everything else reads an app's own API with.

// Target is one thing inside a connection that can be indexed: a repository,
// a channel, a database. Target is the payload knowledge registers a source
// with, so a caller passes it on rather than assembling it.
type Target struct {
	// Kind is repository, channel or database — what the app calls this sort
	// of thing.
	Kind string `json:"kind"`
	// ID is what the app knows it by, and what Target carries.
	ID string `json:"id"`
	// Name is what a member reads, which is not always the id: a Slack
	// channel's name rather than C0123456789.
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Target      json.RawMessage `json:"target"`
}

// maxTargets bounds one listing. A member with a thousand repositories is
// choosing from a search box, not scrolling, and the app's own paging is not
// worth carrying through for a picker.
const maxTargets = 200

// ConnectionTargets lists what a member could index through one of their own
// connections, read from the app itself.
//
// An app nothing can be listed for answers an empty list rather than an
// error: it is a fact about the app, not a failure, and a caller showing a
// picker wants to show "nothing to index here" rather than a problem.
func (s *Service) ConnectionTargets(ctx context.Context, credentials authclient.Credentials, organizationID, connectionID string) ([]Target, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	connection, err := s.store.Connection(ctx, organizationID, strings.TrimSpace(connectionID))
	if err != nil {
		return nil, err
	}
	if connection.MemberID != caller.UserID && !caller.Role.IsOrgAdmin() {
		return nil, domain.ErrNotFound
	}
	switch connection.Status {
	case domain.ConnectionActive:
	case domain.ConnectionExpired:
		return nil, fmt.Errorf("the connection expired: %w", domain.ErrNeedsReconnect)
	default:
		return nil, fmt.Errorf("the connection is %s: %w", connection.Status, domain.ErrNotConnected)
	}
	return s.targetsOf(ctx, connection)
}

// targetsOf reads one connection's targets from its app. It acts as the
// connection, so a caller that has already decided who may use it — a
// member's own request, or connecting the app — can share this.
func (s *Service) targetsOf(ctx context.Context, connection domain.Connection) ([]Target, error) {
	switch connection.Toolkit {
	case "github":
		return s.githubTargets(ctx, connection)
	case "slack":
		return s.slackTargets(ctx, connection)
	case "notion":
		return s.notionTargets(ctx, connection)
	default:
		// Every other app connects and delivers events; nothing here knows
		// how to index one yet.
		return []Target{}, nil
	}
}

// read makes one read through a connection, without asking who the caller is
// again: targetsOf's callers have already decided that.
func (s *Service) read(ctx context.Context, connection domain.Connection, method, endpoint string, body json.RawMessage, headers map[string]string) (json.RawMessage, error) {
	parameters := make([]composio.ProxyParameter, 0, len(headers))
	for name, value := range headers {
		parameters = append(parameters, composio.ProxyParameter{Name: name, Value: value, Type: "header"})
	}
	answer, err := s.composio.Proxy(ctx, composio.ProxyRequest{
		ConnectedAccountID: connection.ComposioAccountID, Endpoint: endpoint,
		Method: method, Body: body, Parameters: parameters,
	})
	if err != nil {
		return nil, composioError("read "+connection.Toolkit, err)
	}
	if answer.Status >= 400 {
		// The app refused the read. That is the app's answer about this
		// connection, not a fault here.
		return nil, fmt.Errorf("%s answered %d when asked what it holds: %w",
			connection.Toolkit, answer.Status, domain.ErrUnavailable)
	}
	return answer.Data, nil
}

// githubTargets lists the repositories the connection can see, most recently
// pushed first, which is the order someone looking for one wants them in.
func (s *Service) githubTargets(ctx context.Context, connection domain.Connection) ([]Target, error) {
	data, err := s.read(ctx, connection, http.MethodGet,
		"/user/repos?per_page=100&sort=pushed&affiliation=owner,collaborator,organization_member", nil,
		map[string]string{"X-GitHub-Api-Version": "2022-11-28"})
	if err != nil {
		return nil, err
	}
	var repositories []struct {
		FullName    string `json:"full_name"`
		Description string `json:"description"`
		Archived    bool   `json:"archived"`
	}
	if err := json.Unmarshal(data, &repositories); err != nil {
		return nil, fmt.Errorf("github's repository list could not be read: %w", domain.ErrUnavailable)
	}
	targets := make([]Target, 0, len(repositories))
	for _, repository := range repositories {
		// An archived repository takes nothing new, so indexing it is work
		// for a snapshot that will not change.
		if repository.FullName == "" || repository.Archived {
			continue
		}
		targets = append(targets, Target{
			Kind: "repository", ID: repository.FullName, Name: repository.FullName,
			Description: repository.Description,
			Target:      sourceTarget("repository", repository.FullName),
		})
		if len(targets) == maxTargets {
			break
		}
	}
	return targets, nil
}

// slackTargets lists the channels the connection is in. Slack answers ok
// false with its own reason rather than an HTTP status, so that is read too.
func (s *Service) slackTargets(ctx context.Context, connection domain.Connection) ([]Target, error) {
	data, err := s.read(ctx, connection, http.MethodGet,
		"/api/conversations.list?limit=200&exclude_archived=true&types=public_channel,private_channel", nil, nil)
	if err != nil {
		return nil, err
	}
	var answer struct {
		OK       bool   `json:"ok"`
		Error    string `json:"error"`
		Channels []struct {
			ID      string `json:"id"`
			Name    string `json:"name"`
			Purpose struct {
				Value string `json:"value"`
			} `json:"purpose"`
			IsArchived bool `json:"is_archived"`
		} `json:"channels"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, fmt.Errorf("slack's channel list could not be read: %w", domain.ErrUnavailable)
	}
	if !answer.OK {
		return nil, fmt.Errorf("slack refused to list channels (%s): %w",
			truncate(answer.Error, maxComposioMessage), domain.ErrUnavailable)
	}
	targets := make([]Target, 0, len(answer.Channels))
	for _, channel := range answer.Channels {
		if channel.ID == "" || channel.IsArchived {
			continue
		}
		targets = append(targets, Target{
			Kind: "channel", ID: channel.ID, Name: "#" + channel.Name,
			Description: channel.Purpose.Value, Target: sourceTarget("channel", channel.ID),
		})
		if len(targets) == maxTargets {
			break
		}
	}
	return targets, nil
}

// notionAPIVersion is the version Notion's API is asked for, matching what
// knowledge's own Notion source reads with.
const notionAPIVersion = "2025-09-03"

// notionTargets lists the data sources the connection can see. Notion split
// a database into data sources in 2025-09-03, and a data source is what a
// query reads, so that is what a member picks.
func (s *Service) notionTargets(ctx context.Context, connection domain.Connection) ([]Target, error) {
	body, err := json.Marshal(map[string]any{
		"filter":    map[string]string{"property": "object", "value": "data_source"},
		"page_size": 100,
	})
	if err != nil {
		return nil, err
	}
	data, err := s.read(ctx, connection, http.MethodPost, "/v1/search", body,
		map[string]string{"Notion-Version": notionAPIVersion})
	if err != nil {
		return nil, err
	}
	var answer struct {
		Results []struct {
			ID    string `json:"id"`
			Title []struct {
				PlainText string `json:"plain_text"`
			} `json:"title"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		return nil, fmt.Errorf("notion's search could not be read: %w", domain.ErrUnavailable)
	}
	targets := make([]Target, 0, len(answer.Results))
	for _, result := range answer.Results {
		if result.ID == "" {
			continue
		}
		name := ""
		for _, part := range result.Title {
			name += part.PlainText
		}
		if strings.TrimSpace(name) == "" {
			name = "Untitled"
		}
		targets = append(targets, Target{
			Kind: "database", ID: result.ID, Name: name, Target: sourceTarget("database", result.ID),
		})
		if len(targets) == maxTargets {
			break
		}
	}
	return targets, nil
}

// sourceTarget is the payload knowledge registers a source with, built here
// so a caller never has to know each provider's spelling of it.
func sourceTarget(field, value string) json.RawMessage {
	encoded, err := json.Marshal(map[string]string{field: value})
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}
