// Package mcpserver offers a member's connected apps to agents as MCP tools.
//
// Composio's catalog runs to thousands of tools, far more than any model can
// hold, so the endpoint does not list them. It offers four instead: which
// apps the member connected, a search over one app's tools, and two ways to
// run one — a read, which the server refuses for any tool Composio does not
// mark read-only, and a write, which an agent runs only once its member
// confirms. The refusal is the server's, not the agent's, so a misbehaving
// agent still cannot write through the read tool.
package mcpserver

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/application"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/httpserver"
)

// Version is what the MCP endpoint calls itself: the service's contract
// version, not the protocol's, which the SDK settles per request.
const Version = "1"

// OrganizationHeader names the organization a request acts in when its path
// does not, so an MCP client keeps one fixed URL.
const OrganizationHeader = "X-Oryxa-Organization"

// The tools, all named after the apps toolbox.
const (
	ToolConnections = "apps_connections"
	ToolSearchTools = "apps_search_tools"
	ToolRunRead     = "apps_run_read_tool"
	ToolRunWrite    = "apps_run_write_tool"
)

// Service is what the endpoint asks of apps.
type Service interface {
	Connections(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit string, organization bool) ([]domain.Connection, error)
	SearchTools(ctx context.Context, credentials authclient.Credentials, organizationID, toolkit, query string, limit int) ([]application.ToolView, error)
	RunTool(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.RunToolInput) (application.ToolRun, error)
}

// call is one request's caller. The endpoint keeps no session, so every
// request carries its own.
type call struct {
	organizationID string
	credentials    authclient.Credentials
}

type callKey struct{}

// Handler serves the apps MCP endpoint statelessly: the 2026-07-28 protocol
// keeps no session, so any replica may answer any request.
type Handler struct {
	service  Service
	logger   *slog.Logger
	streamer *mcp.StreamableHTTPHandler
}

func New(service Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	handler := &Handler{service: service, logger: logger}
	handler.streamer = mcp.NewStreamableHTTPHandler(handler.server, &mcp.StreamableHTTPOptions{
		// Only a stateless server speaks 2026-07-28; a stateful one would
		// negotiate down and expect a session this service does not keep.
		Stateless: true,
	})
	return handler
}

// ServeHTTP answers one MCP request in the organization its path or
// OrganizationHeader names, as the member its credentials name.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	organizationID := request.PathValue("organizationID")
	if organizationID == "" {
		organizationID = strings.TrimSpace(request.Header.Get(OrganizationHeader))
	}
	if organizationID == "" {
		http.Error(writer, "the organization is required, in the path or the "+OrganizationHeader+" header", http.StatusBadRequest)
		return
	}
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if credentials.Authorization == "" {
		if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
			credentials.AccessCookie = cookie.Value
		}
	}
	if !credentials.Present() {
		// As an MCP server must, say how to authenticate rather than simply
		// refusing.
		writer.Header().Set("WWW-Authenticate", `Bearer realm="oryxa-apps"`)
		http.Error(writer, "a member's access token is required", http.StatusUnauthorized)
		return
	}
	handler.streamer.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), callKey{}, call{
		organizationID: organizationID, credentials: credentials,
	})))
}

// instructions ride along as the server's guidance, for a client that shows
// them; Krude carries its own copy in the toolbox's skill.
const instructions = `Tools in the apps the member connected to Oryxa, run as the member.
Find the member's connected apps with apps_connections, search an app's tools with apps_search_tools,
run a read with apps_run_read_tool, and a change with apps_run_write_tool and an idempotency key.
An agent cannot connect an app: on not_connected or needs_reconnect, ask the member to do it in Oryxa.`

// server builds the MCP server for one request, bound to its caller.
func (handler *Handler) server(request *http.Request) *mcp.Server {
	current, _ := request.Context().Value(callKey{}).(call)
	server := mcp.NewServer(&mcp.Implementation{Name: "oryxa-apps", Version: Version},
		&mcp.ServerOptions{Instructions: instructions, Logger: handler.logger})
	destructive := true
	notDestructive := false

	server.AddTool(&mcp.Tool{
		Name:        ToolConnections,
		Description: "List the apps the member connected, optionally one app's, with the connection each tool would act through.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"toolkit":{"type":"string","description":"An app's slug, such as github."}},"additionalProperties":false}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &notDestructive},
	}, handler.connections(current))

	server.AddTool(&mcp.Tool{
		Name:        ToolSearchTools,
		Description: "Search one app's tools. Without a query it lists the app's featured tools. Each result says whether it only reads, and the arguments it takes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"toolkit":{"type":"string","description":"The app's slug, such as github or gmail."},` +
			`"query":{"type":"string","description":"What the tool should do, such as create an issue."},` +
			`"limit":{"type":"integer","minimum":1,"maximum":50}},"required":["toolkit"],"additionalProperties":false}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &notDestructive},
	}, handler.searchTools(current))

	server.AddTool(&mcp.Tool{
		Name:        ToolRunRead,
		Description: "Run a tool that only reads, such as listing issues or searching mail. Refused for any tool that changes something.",
		InputSchema: runSchema(false),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: &notDestructive},
	}, handler.run(current, true))

	server.AddTool(&mcp.Tool{
		Name:        ToolRunWrite,
		Description: "Run a tool that changes something in the member's app, such as sending a message. Needs an idempotency key: asked again with the same key, it answers what happened the first time.",
		InputSchema: runSchema(true),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: &destructive},
	}, handler.run(current, false))
	return server
}

func runSchema(write bool) json.RawMessage {
	properties := map[string]any{
		"tool":          map[string]any{"type": "string", "description": "The tool's slug, as apps_search_tools gives it, such as GITHUB_LIST_ISSUES."},
		"arguments":     map[string]any{"type": "object", "description": "The tool's input, matching its input_schema."},
		"connection_id": map[string]any{"type": "string", "description": "Which connection to act through; needed only when the member has several to the app."},
	}
	required := []string{"tool", "arguments"}
	if write {
		properties["idempotency_key"] = map[string]any{
			"type": "string", "minLength": domain.MinIdempotencyKey, "maxLength": domain.MaxIdempotencyKey,
			"description": "A key of your own for this change. Asking again with the same key answers what happened the first time rather than doing it twice.",
		}
		required = append(required, "idempotency_key")
	}
	encoded, _ := json.Marshal(map[string]any{
		"type": "object", "properties": properties, "required": required, "additionalProperties": false,
	})
	return encoded
}

func (handler *Handler) connections(current call) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Toolkit string `json:"toolkit"`
		}
		if err := decodeArguments(request, &input); err != nil {
			return failed(err), nil
		}
		connections, err := handler.service.Connections(ctx, current.credentials, current.organizationID, input.Toolkit, false)
		if err != nil {
			return failed(err), nil
		}
		listed := make([]map[string]any, 0, len(connections))
		for _, connection := range connections {
			// A pending or failed connection can do nothing; an expired one is
			// listed so the agent can tell the member to reconnect it.
			if connection.Status != domain.ConnectionActive && connection.Status != domain.ConnectionExpired {
				continue
			}
			listed = append(listed, map[string]any{
				"connection_id": connection.ID, "toolkit": connection.Toolkit,
				"label": connection.Label, "status": string(connection.Status),
			})
		}
		return answered(map[string]any{"connections": listed}), nil
	}
}

func (handler *Handler) searchTools(current call) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Toolkit string `json:"toolkit"`
			Query   string `json:"query"`
			Limit   int    `json:"limit"`
		}
		if err := decodeArguments(request, &input); err != nil {
			return failed(err), nil
		}
		tools, err := handler.service.SearchTools(ctx, current.credentials, current.organizationID, input.Toolkit, input.Query, input.Limit)
		if err != nil {
			return failed(err), nil
		}
		return answered(map[string]any{"tools": tools}), nil
	}
}

func (handler *Handler) run(current call, onlyReads bool) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var input struct {
			Tool           string          `json:"tool"`
			Arguments      json.RawMessage `json:"arguments"`
			ConnectionID   string          `json:"connection_id"`
			IdempotencyKey string          `json:"idempotency_key"`
		}
		if err := decodeArguments(request, &input); err != nil {
			return failed(err), nil
		}
		run, err := handler.service.RunTool(ctx, current.credentials, current.organizationID, application.RunToolInput{
			Tool: input.Tool, ConnectionID: input.ConnectionID, Arguments: input.Arguments,
			IdempotencyKey: input.IdempotencyKey, OnlyReads: onlyReads,
		})
		if err != nil {
			return failed(err), nil
		}
		view := httpserver.RunView(run)
		if run.Action.Status != domain.ActionSucceeded {
			// The app ran the tool and refused it: its reason is what a model
			// needs to try again differently.
			return failedWith("app_error", run.Action.Error, view), nil
		}
		return answered(view), nil
	}
}

// decodeArguments reads a tool call's arguments, refusing unknown fields as
// the schemas promise.
func decodeArguments(request *mcp.CallToolRequest, target any) error {
	arguments := request.Params.Arguments
	if len(arguments) == 0 {
		arguments = json.RawMessage(`{}`)
	}
	decoder := json.NewDecoder(strings.NewReader(string(arguments)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return domainInvalid("the tool's arguments do not match its schema")
	}
	return nil
}

func domainInvalid(message string) error {
	return &invalidArguments{message: message}
}

type invalidArguments struct{ message string }

func (err *invalidArguments) Error() string { return err.message + ": " + domain.ErrInvalid.Error() }
func (err *invalidArguments) Unwrap() error { return domain.ErrInvalid }

// answered is a tool's result, as text a model reads and as structured
// content a caller can act on.
func answered(value map[string]any) *mcp.CallToolResult {
	encoded, err := json.Marshal(value)
	if err != nil {
		return failed(err)
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: value,
	}
}

// failed tells the model what went wrong in the contract's words, so it can
// ask the member to reconnect, choose a connection, or stop, rather than
// guess from prose. A tool failure is a result, not a protocol error.
func failed(err error) *mcp.CallToolResult {
	_, code := httpserver.ErrorCode(err)
	return failedWith(code, httpserver.Message(err), nil)
}

func failedWith(code, message string, extra map[string]any) *mcp.CallToolResult {
	value := map[string]any{"error": code, "message": message}
	for key, item := range extra {
		value[key] = item
	}
	encoded, _ := json.Marshal(value)
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: value,
	}
}
