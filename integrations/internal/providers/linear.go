package providers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// linearTolerance is how far a delivery's webhookTimestamp may be from now, as
// Linear recommends, before it is refused as a replay.
const linearTolerance = time.Minute

// linearResourceTypes are the Linear resources a subscription may ask for:
// Linear's webhook resource types, less those only an OAuth app's own
// installation sends.
var linearResourceTypes = []string{
	"Attachment", "Comment", "Customer", "CustomerNeed", "Cycle", "Document", "Initiative", "InitiativeUpdate",
	"Issue", "IssueLabel", "IssueSLA", "Project", "ProjectLabel", "ProjectUpdate", "Reaction", "Release", "ReleaseNote", "User",
}

// linearDefaultResourceTypes are what a subscription that names no events
// receives: the resources Linear documents webhooks for.
var linearDefaultResourceTypes = []string{
	"Attachment", "Comment", "Customer", "Cycle", "Document", "Initiative", "Issue", "IssueLabel", "Project",
	"ProjectUpdate", "Reaction", "User",
}

var (
	linearTeamPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	// linearIDPattern is a Linear ID, a UUID; a team named otherwise is named
	// by its key.
	linearIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
)

// Linear delivers a workspace's changes to a webhook integrations creates, for
// one team or every public team, signed with the subscription's secret. Only a
// workspace admin's API key, or an OAuth connection with the admin scope, may
// create one.
func Linear() Provider {
	return linearProvider("https://api.linear.app/graphql")
}

func linearProvider(graphqlURL string) Provider {
	return Provider{
		Name:     "linear",
		Delivery: DeliveryWebhook,
		Target:   linearTarget,
		Register: func(ctx context.Context, client *http.Client, request RegisterRequest) (Registration, error) {
			return linearRegister(ctx, client, graphqlURL, request)
		},
		Unregister: func(ctx context.Context, client *http.Client, request UnregisterRequest) error {
			return linearUnregister(ctx, client, graphqlURL, request)
		},
		Receive: linearReceive,
	}
}

// linearTarget takes a team, by ID or key, or no target for every public team,
// and Linear resource types, such as Issue or Comment.
func linearTarget(target map[string]string, events []string) (map[string]string, []string, error) {
	team, hasTeam := target["team"]
	if len(target) > 1 || (len(target) == 1 && !hasTeam) || (hasTeam && !linearTeamPattern.MatchString(team)) {
		return nil, nil, fmt.Errorf("a Linear subscription's target is {\"team\": \"ID or key\"}, or none for every public team: %w", domain.ErrInvalid)
	}
	for _, event := range events {
		if !slices.Contains(linearResourceTypes, event) {
			return nil, nil, fmt.Errorf("%q is not a Linear resource type: %w", event, domain.ErrInvalid)
		}
	}
	checked := map[string]string{}
	if hasTeam {
		checked["team"] = team
	}
	return checked, events, nil
}

type linearAnswer struct {
	Data   json.RawMessage `json:"data"`
	Errors []struct {
		Message    string `json:"message"`
		Extensions struct {
			Type string `json:"type"`
			Code string `json:"code"`
		} `json:"extensions"`
	} `json:"errors"`
}

// linearGraphQL sends a GraphQL operation and decodes its data. Linear answers
// most failures with errors in the body rather than a status.
func linearGraphQL(ctx context.Context, client *http.Client, graphqlURL string, credential domain.Credential, query string, variables map[string]any, target any) error {
	var answer linearAnswer
	if err := sendJSON(ctx, client, http.MethodPost, graphqlURL, credential, map[string]any{"query": query, "variables": variables}, &answer); err != nil {
		return err
	}
	if len(answer.Errors) > 0 {
		failure := answer.Errors[0]
		kind := strings.ToLower(failure.Extensions.Type + " " + failure.Extensions.Code)
		switch {
		case strings.Contains(kind, "authentication"):
			return fmt.Errorf("%w: Linear refused the credential", domain.ErrCredentialRejected)
		case strings.Contains(kind, "forbidden"):
			return fmt.Errorf("%w: Linear refused the operation", domain.ErrForbidden)
		case strings.Contains(kind, "ratelimit"):
			return fmt.Errorf("%w: Linear rate limit", domain.ErrProviderUnavailable)
		case strings.Contains(strings.ToLower(failure.Message), "not found"):
			return fmt.Errorf("%w: Linear found nothing", domain.ErrNotFound)
		default:
			return fmt.Errorf("%w: Linear refused the request", domain.ErrInvalid)
		}
	}
	if target != nil && json.Unmarshal(answer.Data, target) != nil {
		return fmt.Errorf("%w: Linear response is not valid", domain.ErrProviderUnavailable)
	}
	return nil
}

func linearRegister(ctx context.Context, client *http.Client, graphqlURL string, request RegisterRequest) (Registration, error) {
	events := request.Events
	if len(events) == 0 {
		events = linearDefaultResourceTypes
	}
	input := map[string]any{
		"url": request.CallbackURL, "resourceTypes": events, "secret": request.Secret, "label": "Oryxa", "enabled": true,
	}
	if team, ok := request.Target["team"]; ok {
		teamID, err := linearTeamID(ctx, client, graphqlURL, request.Credential, team)
		if err != nil {
			return Registration{}, err
		}
		input["teamId"] = teamID
	} else {
		input["allPublicTeams"] = true
	}
	var created struct {
		WebhookCreate struct {
			Success bool `json:"success"`
			Webhook struct {
				ID string `json:"id"`
			} `json:"webhook"`
		} `json:"webhookCreate"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`mutation CreateWebhook($input: WebhookCreateInput!) { webhookCreate(input: $input) { success webhook { id } } }`,
		map[string]any{"input": input}, &created); err != nil {
		return Registration{}, err
	}
	if !created.WebhookCreate.Success || created.WebhookCreate.Webhook.ID == "" {
		return Registration{}, fmt.Errorf("%w: Linear did not create the webhook", domain.ErrInvalid)
	}
	return Registration{ID: created.WebhookCreate.Webhook.ID}, nil
}

// linearTeamID is the ID of a team a target names by ID or by key, such as
// ENG: a webhook names its team by ID.
func linearTeamID(ctx context.Context, client *http.Client, graphqlURL string, credential domain.Credential, team string) (string, error) {
	if linearIDPattern.MatchString(team) {
		return team, nil
	}
	var found struct {
		Teams struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	if err := linearGraphQL(ctx, client, graphqlURL, credential,
		`query TeamByKey($key: String!) { teams(filter: { key: { eqIgnoreCase: $key } }, first: 1) { nodes { id } } }`,
		map[string]any{"key": team}, &found); err != nil {
		return "", err
	}
	if len(found.Teams.Nodes) == 0 || found.Teams.Nodes[0].ID == "" {
		return "", fmt.Errorf("Linear has no team %q the connection can see: %w", team, domain.ErrInvalid)
	}
	return found.Teams.Nodes[0].ID, nil
}

func linearUnregister(ctx context.Context, client *http.Client, graphqlURL string, request UnregisterRequest) error {
	var deleted struct {
		WebhookDelete struct {
			Success bool `json:"success"`
		} `json:"webhookDelete"`
	}
	err := linearGraphQL(ctx, client, graphqlURL, request.Credential,
		`mutation DeleteWebhook($id: String!) { webhookDelete(id: $id) { success } }`,
		map[string]any{"id": request.RegistrationID}, &deleted)
	switch {
	case err == nil && !deleted.WebhookDelete.Success:
		return fmt.Errorf("%w: Linear did not delete the webhook", domain.ErrProviderUnavailable)
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return err
	}
	return nil
}

type linearPayload struct {
	Action           string          `json:"action"`
	Type             string          `json:"type"`
	Data             json.RawMessage `json:"data"`
	URL              string          `json:"url"`
	CreatedAt        time.Time       `json:"createdAt"`
	WebhookTimestamp int64           `json:"webhookTimestamp"`
	WebhookID        string          `json:"webhookId"`
	Actor            *struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"actor"`
}

// linearReceive verifies a delivery's Linear-Signature and that it was sent
// within the last minute, and reads its change.
func linearReceive(delivery Delivered, secret []byte, now time.Time) (Reception, error) {
	if !ValidHMACSHA256(secret, delivery.Body, delivery.Header.Get("Linear-Signature")) {
		return Reception{}, fmt.Errorf("%w: Linear signature", ErrUnverified)
	}
	var payload linearPayload
	if err := json.Unmarshal(delivery.Body, &payload); err != nil {
		return Reception{}, fmt.Errorf("%w: Linear payload is not valid", domain.ErrInvalid)
	}
	if age := now.Sub(time.UnixMilli(payload.WebhookTimestamp)); payload.WebhookTimestamp <= 0 || age > linearTolerance || age < -linearTolerance {
		return Reception{}, fmt.Errorf("%w: Linear delivery is too old", ErrUnverified)
	}
	if payload.Type == "" || payload.Action == "" {
		return Reception{}, fmt.Errorf("%w: Linear payload names no change", domain.ErrInvalid)
	}
	resourceType := snakeCase(payload.Type)
	var data struct {
		ID         string `json:"id"`
		Title      string `json:"title"`
		Name       string `json:"name"`
		Identifier string `json:"identifier"`
		Body       string `json:"body"`
	}
	_ = json.Unmarshal(payload.Data, &data)
	deliveryID := delivery.Header.Get("Linear-Delivery")
	key := deliveryID
	if key == "" {
		key = Digest(delivery.Body)
	}
	event := Event{
		Name:           resourceType + "." + payload.Action,
		DeliveryID:     deliveryID,
		Key:            key,
		RegistrationID: payload.WebhookID,
		Resource: integrationsv1.Resource{
			Type: resourceType, ID: data.ID, Name: firstNonEmpty(data.Title, data.Name, data.Identifier), URL: payload.URL,
		},
		OccurredAt: payload.CreatedAt,
		Payload:    delivery.Body,
	}
	if payload.Actor != nil && payload.Actor.ID != "" {
		event.Actor = &integrationsv1.Actor{ID: payload.Actor.ID, Name: payload.Actor.Name}
	}
	return Reception{Events: []Event{event}}, nil
}

// snakeCase writes a name like IssueLabel as issue_label, and IssueSLA as
// issue_sla.
func snakeCase(name string) string {
	runes := []rune(name)
	var result strings.Builder
	for index, character := range runes {
		if unicode.IsUpper(character) && index > 0 {
			previousLower := unicode.IsLower(runes[index-1])
			nextLower := index+1 < len(runes) && unicode.IsLower(runes[index+1])
			if previousLower || (nextLower && unicode.IsUpper(runes[index-1])) {
				result.WriteByte('_')
			}
		}
		result.WriteRune(unicode.ToLower(character))
	}
	return result.String()
}
