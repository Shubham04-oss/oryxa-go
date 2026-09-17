package composio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Tool hints Composio tags tools with, the same four MCP defines.
const (
	HintReadOnly    = "readOnlyHint"
	HintDestructive = "destructiveHint"
	HintIdempotent  = "idempotentHint"
	HintOpenWorld   = "openWorldHint"
)

// Tool is one action a toolkit offers, such as GITHUB_CREATE_AN_ISSUE.
type Tool struct {
	Slug        string
	Name        string
	Description string
	Toolkit     string
	Version     string
	// InputParameters and OutputParameters are JSON Schemas.
	InputParameters  json.RawMessage
	OutputParameters json.RawMessage
	Tags             []string
	Scopes           []string
	NoAuth           bool
	Deprecated       bool
}

// ReadOnly says Composio marks the tool as only reading. A tool without the
// mark is treated as one that writes.
func (tool Tool) ReadOnly() bool { return slices.Contains(tool.Tags, HintReadOnly) }

// Destructive says Composio marks the tool as one that may destroy data.
func (tool Tool) Destructive() bool { return slices.Contains(tool.Tags, HintDestructive) }

type toolWire struct {
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Toolkit     struct {
		Slug string `json:"slug"`
	} `json:"toolkit"`
	Version          string          `json:"version"`
	InputParameters  json.RawMessage `json:"input_parameters"`
	OutputParameters json.RawMessage `json:"output_parameters"`
	Tags             []string        `json:"tags"`
	Scopes           []string        `json:"scopes"`
	NoAuth           bool            `json:"no_auth"`
	IsDeprecated     bool            `json:"is_deprecated"`
}

func (wire toolWire) tool() Tool {
	return Tool{
		Slug: wire.Slug, Name: wire.Name, Description: wire.Description, Toolkit: wire.Toolkit.Slug,
		Version: wire.Version, InputParameters: wire.InputParameters, OutputParameters: wire.OutputParameters,
		Tags: wire.Tags, Scopes: wire.Scopes, NoAuth: wire.NoAuth, Deprecated: wire.IsDeprecated,
	}
}

// ToolQuery narrows a tool listing.
type ToolQuery struct {
	Toolkit string
	// Search matches a tool's name and description.
	Search string
	// Important keeps only the tools Composio features for the toolkit.
	Important bool
	// Slugs names tools outright, overriding the other filters.
	Slugs  []string
	Tags   []string
	Cursor string
	Limit  int
}

// Tools lists tools, a page at a time.
func (client *Client) Tools(ctx context.Context, query ToolQuery) (Page[Tool], error) {
	values := url.Values{}
	if query.Toolkit != "" {
		values.Set("toolkit_slug", query.Toolkit)
	}
	if query.Search != "" {
		values.Set("query", query.Search)
	}
	if query.Important {
		values.Set("important", "true")
	}
	if len(query.Slugs) > 0 {
		values.Set("tool_slugs", strings.Join(query.Slugs, ","))
	}
	addAll(values, "tags", query.Tags)
	var answer struct {
		Items      []toolWire `json:"items"`
		NextCursor *string    `json:"next_cursor"`
	}
	if err := client.call(ctx, http.MethodGet, "/tools", pageQuery(values, query.Cursor, query.Limit), nil, &answer); err != nil {
		return Page[Tool]{}, err
	}
	page := Page[Tool]{NextCursor: cursorOf(answer.NextCursor)}
	for _, wire := range answer.Items {
		page.Items = append(page.Items, wire.tool())
	}
	return page, nil
}

// Tool reads one tool.
func (client *Client) Tool(ctx context.Context, slug string) (Tool, error) {
	var wire toolWire
	if err := client.call(ctx, http.MethodGet, "/tools/"+pathEscape(slug), nil, nil, &wire); err != nil {
		return Tool{}, err
	}
	return wire.tool(), nil
}

// ExecuteRequest runs a tool as one connected account.
type ExecuteRequest struct {
	// ConnectedAccountID pins the account the tool acts with. UserID is sent
	// alongside it, so Composio can refuse an account that is not the user's.
	ConnectedAccountID string
	UserID             string
	// Arguments are the tool's input, matching its input parameters.
	Arguments json.RawMessage
	// Version pins the tool's version. Empty runs the latest.
	Version string
}

// ExecuteResult is what the tool did. A tool that ran and failed at the
// provider is a result with Successful false, not an error.
type ExecuteResult struct {
	Successful bool
	Data       json.RawMessage
	Error      string
	LogID      string
}

// ExecuteTool runs one tool.
func (client *Client) ExecuteTool(ctx context.Context, slug string, request ExecuteRequest) (ExecuteResult, error) {
	arguments := request.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	if !json.Valid(arguments) {
		return ExecuteResult{}, fmt.Errorf("%w: the tool's arguments are not valid JSON", ErrInvalid)
	}
	body := map[string]any{"arguments": arguments}
	if request.ConnectedAccountID != "" {
		body["connected_account_id"] = request.ConnectedAccountID
	}
	if request.UserID != "" {
		body["user_id"] = request.UserID
	}
	if request.Version != "" {
		body["version"] = request.Version
	}
	var answer struct {
		Data       json.RawMessage `json:"data"`
		Error      *string         `json:"error"`
		Successful bool            `json:"successful"`
		LogID      string          `json:"log_id"`
	}
	if err := client.call(ctx, http.MethodPost, "/tools/execute/"+pathEscape(slug), nil, body, &answer); err != nil {
		return ExecuteResult{}, err
	}
	result := ExecuteResult{Successful: answer.Successful, Data: answer.Data, LogID: answer.LogID}
	if answer.Error != nil {
		result.Error = *answer.Error
	}
	return result, nil
}

// ProxyParameter is a header or query parameter a proxied request carries.
type ProxyParameter struct {
	Name  string `json:"name"`
	Value string `json:"value"`
	// Type is "header" or "query".
	Type string `json:"type"`
}

// ProxyRequest calls a toolkit's own API as a connected account, with
// Composio placing the credential. The endpoint must be on the toolkit's own
// domain.
type ProxyRequest struct {
	ConnectedAccountID string
	// Endpoint is an absolute URL, or a path under the toolkit's base URL.
	Endpoint   string
	Method     string
	Body       json.RawMessage
	Parameters []ProxyParameter
}

// ProxyResponse is what the toolkit's API answered.
type ProxyResponse struct {
	Status  int
	Headers map[string]string
	// Data is the answer's body when it was JSON.
	Data json.RawMessage
	// Binary is set instead when the answer was a file, which Composio
	// stores and links to rather than returning inline.
	Binary *BinaryData
}

// BinaryData is a file a proxied request answered with.
type BinaryData struct {
	URL         string
	ContentType string
	Size        int64
	ExpiresAt   string
}

// Proxy sends one request to a toolkit's API as a connected account. A
// non-2xx answer from the toolkit is a response, not an error; an error means
// Composio itself refused or failed.
func (client *Client) Proxy(ctx context.Context, request ProxyRequest) (ProxyResponse, error) {
	body := map[string]any{"endpoint": request.Endpoint, "method": strings.ToUpper(request.Method)}
	if request.ConnectedAccountID != "" {
		body["connected_account_id"] = request.ConnectedAccountID
	}
	if len(request.Body) > 0 {
		if !json.Valid(request.Body) {
			return ProxyResponse{}, fmt.Errorf("%w: the proxied body is not valid JSON", ErrInvalid)
		}
		body["body"] = request.Body
	}
	if len(request.Parameters) > 0 {
		body["parameters"] = request.Parameters
	}
	var answer struct {
		Status     float64         `json:"status"`
		Headers    map[string]any  `json:"headers"`
		Data       json.RawMessage `json:"data"`
		BinaryData *struct {
			URL         string  `json:"url"`
			ContentType string  `json:"content_type"`
			Size        float64 `json:"size"`
			ExpiresAt   string  `json:"expires_at"`
		} `json:"binary_data"`
	}
	if err := client.call(ctx, http.MethodPost, "/tools/execute/proxy", nil, body, &answer); err != nil {
		return ProxyResponse{}, err
	}
	response := ProxyResponse{Status: int(answer.Status), Data: answer.Data, Headers: map[string]string{}}
	for name, value := range answer.Headers {
		response.Headers[name] = fmt.Sprint(value)
	}
	if answer.BinaryData != nil && answer.BinaryData.URL != "" {
		response.Binary = &BinaryData{
			URL: answer.BinaryData.URL, ContentType: answer.BinaryData.ContentType,
			Size: int64(answer.BinaryData.Size), ExpiresAt: answer.BinaryData.ExpiresAt,
		}
	}
	return response, nil
}
