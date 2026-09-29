package application

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// A service inside Oryxa sometimes has to read an app's own API rather than
// run a tool: knowledge pages through a repository's files, a channel's
// history, a workspace's pages. Composio proxies such a call and places the
// credential, so this is the one way that happens — the project API key stays
// in apps, and the account a call uses is still derived from who is asking.
//
// Only reads go through here. A change is a tool run, which is recorded, has
// an idempotency key and asks the member first; a proxy call has none of that,
// so allowing an arbitrary POST would be a way around every one of those
// rules.

// ProxyInput is one read of an app's own API, as a caller asks for it.
type ProxyInput struct {
	// ConnectionID picks the member's connection; without it, their one
	// active connection to Toolkit is used.
	ConnectionID string
	Toolkit      string
	// Endpoint is a path under the app's own API, such as
	// /repos/oryxa/oryxa-go/contents. An absolute URL is refused: the app a
	// call reaches is the connection's, not the caller's choice.
	Endpoint string
	Method   string
	Body     json.RawMessage
	// Headers are the request headers the app needs, such as a Notion version.
	// Authorization is never among them: Composio places the credential.
	Headers map[string]string
}

// ProxyResult is what the app's API answered.
type ProxyResult struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Data    json.RawMessage   `json:"data,omitempty"`
	// BinaryURL is where Composio put a non-JSON answer, such as a file.
	BinaryURL   string `json:"binary_url,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

// readPosts are the read endpoints that are POSTs anyway, per app. Notion's
// search and database queries take their filters in a body, so refusing every
// POST would refuse reading Notion at all. The path is matched after its
// variable segments are blanked, so a member's own ids cannot smuggle
// anything in.
var readPosts = map[string][]string{
	"notion": {"/v1/search", "/v1/databases/{}/query", "/v1/blocks/{}/children/list"},
}

// Slack exposes RPC actions even over GET (for example auth.revoke and
// conversations.inviteShared). HTTP method alone is not a read boundary there.
// Keep an explicit list so newly added RPCs cannot gain unconfirmed access.
var slackReads = []string{
	"/api/auth.test", "/api/auth.teams.list",
	"/api/conversations.history", "/api/conversations.info", "/api/conversations.list",
	"/api/conversations.members", "/api/conversations.replies",
	"/api/users.conversations", "/api/users.info", "/api/users.list",
	"/api/users.lookupByEmail", "/api/users.getPresence", "/api/users.profile.get",
	"/api/team.info", "/api/search.all", "/api/search.messages", "/api/search.files",
	"/api/files.info", "/api/files.list", "/api/chat.getPermalink",
	"/api/reactions.get", "/api/reactions.list", "/api/pins.list", "/api/bookmarks.list",
	"/api/emoji.list", "/api/usergroups.list", "/api/usergroups.users.list",
}

// forbiddenHeaders are headers a caller may not set: the credential is
// Composio's business, and a forged host would send the call elsewhere.
var forbiddenHeaders = []string{"authorization", "cookie", "host", "x-api-key", "proxy-authorization"}

// Proxy reads an app's own API as the caller's connection. It refuses
// anything that is not a read, whoever asks.
func (s *Service) Proxy(ctx context.Context, credentials authclient.Credentials, organizationID string, input ProxyInput) (ProxyResult, error) {
	caller, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return ProxyResult{}, err
	}
	method := strings.ToUpper(strings.TrimSpace(input.Method))
	if method == "" {
		method = http.MethodGet
	}
	endpoint := strings.TrimSpace(input.Endpoint)
	if endpoint == "" || !strings.HasPrefix(endpoint, "/") {
		return ProxyResult{}, fmt.Errorf("an endpoint is a path under the app's own API, such as /v1/search: %w", domain.ErrInvalid)
	}
	if strings.Contains(endpoint, "://") || strings.HasPrefix(endpoint, "//") {
		return ProxyResult{}, fmt.Errorf("an endpoint may not name another host: %w", domain.ErrInvalid)
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return ProxyResult{}, fmt.Errorf("the endpoint is not a path: %w", domain.ErrInvalid)
	}
	for _, segment := range strings.Split(parsed.Path, "/") {
		if segment == "." || segment == ".." {
			return ProxyResult{}, fmt.Errorf("an endpoint may not climb out of the app's API: %w", domain.ErrInvalid)
		}
	}
	for name := range input.Headers {
		if slices.Contains(forbiddenHeaders, strings.ToLower(strings.TrimSpace(name))) {
			return ProxyResult{}, fmt.Errorf("%s is not a header a caller may set: %w", name, domain.ErrInvalid)
		}
	}

	toolkit := ""
	if input.Toolkit == "" && strings.TrimSpace(input.ConnectionID) == "" {
		return ProxyResult{}, fmt.Errorf("a connection or an app is required: %w", domain.ErrInvalid)
	}
	if input.Toolkit != "" {
		if toolkit, err = domain.CheckToolkit(input.Toolkit); err != nil {
			return ProxyResult{}, err
		}
		// What the request asks for is wrong whether or not the app is
		// connected, and saying so first spares a caller debugging a missing
		// connection when the real answer is that it asked to write. With only
		// a connection named, the app is not known until it is read.
		if err := checkRead(toolkit, method, parsed.Path); err != nil {
			return ProxyResult{}, err
		}
	}
	connection, err := s.connectionFor(ctx, organizationID, caller.UserID, toolkit, strings.TrimSpace(input.ConnectionID))
	if err != nil {
		return ProxyResult{}, err
	}
	// The read proxy is Composio's, and Composio holds no credential for an
	// Oryxa connection. Knowledge reads one of those through the pages route
	// and its integration's own reader, never through Composio's key.
	if connection.Oryxa() {
		return ProxyResult{}, errOryxaNotYet(connection.Toolkit)
	}
	// The connection's own app decides, in case the caller named another.
	if err := checkRead(connection.Toolkit, method, parsed.Path); err != nil {
		return ProxyResult{}, err
	}
	if len(input.Body) > 0 && !json.Valid(input.Body) {
		return ProxyResult{}, fmt.Errorf("the body is not JSON: %w", domain.ErrInvalid)
	}

	parameters := make([]composio.ProxyParameter, 0, len(input.Headers))
	for name, value := range input.Headers {
		parameters = append(parameters, composio.ProxyParameter{Name: name, Value: value, Type: "header"})
	}
	answer, err := s.composio.Proxy(ctx, composio.ProxyRequest{
		ConnectedAccountID: connection.ComposioAccountID, Endpoint: endpoint,
		Method: method, Body: input.Body, Parameters: parameters,
	})
	if err != nil {
		return ProxyResult{}, composioError("read "+connection.Toolkit, err)
	}
	result := ProxyResult{Status: answer.Status, Headers: answer.Headers, Data: answer.Data}
	if answer.Binary != nil {
		result.BinaryURL, result.ContentType = answer.Binary.URL, answer.Binary.ContentType
	}
	return result, nil
}

// checkRead checks RPC actions explicitly; otherwise GET and HEAD read, and
// POST is permitted only for the named read APIs.
func checkRead(toolkit, method, path string) error {
	if toolkit == "slack" {
		if (method == http.MethodGet || method == http.MethodHead || method == http.MethodPost) && slices.Contains(slackReads, strings.TrimRight(path, "/")) {
			return nil
		}
		return fmt.Errorf("%s %s is not an approved read: %w", method, path, domain.ErrNotReadOnly)
	}
	switch method {
	case http.MethodGet, http.MethodHead:
		return nil
	case http.MethodPost:
		if slices.Contains(readPosts[toolkit], blankIDs(path)) {
			return nil
		}
	}
	return fmt.Errorf("%s %s is not a read, and a change is a tool run so it can be confirmed and recorded: %w",
		method, path, domain.ErrNotReadOnly)
}

// blankIDs replaces the segments that are identifiers with {}, so one
// allow-list entry covers every page or database a member has.
func blankIDs(path string) string {
	segments := strings.Split(strings.TrimRight(path, "/"), "/")
	for index, segment := range segments {
		if looksLikeID(segment) {
			segments[index] = "{}"
		}
	}
	return strings.Join(segments, "/")
}

// looksLikeID is true for a segment that is not part of the route: a UUID, a
// Notion id, a number. A word is left alone, since it names the route.
func looksLikeID(segment string) bool {
	if segment == "" {
		return false
	}
	digits, hyphens := 0, 0
	for _, character := range segment {
		switch {
		case character >= '0' && character <= '9':
			digits++
		case character == '-':
			hyphens++
		case (character >= 'a' && character <= 'f') || (character >= 'A' && character <= 'F'):
		default:
			return false
		}
	}
	// A hex-only segment as long as an id, or one that is all digits.
	return digits == len(segment) || len(segment)-hyphens >= 16
}
