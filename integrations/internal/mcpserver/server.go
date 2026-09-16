// Package mcpserver offers integrations' actions as MCP tools: one stateless
// endpoint per provider, carrying that provider's skill as the server's
// instructions, so an agent loads a provider's tools and the guidance for
// using them together.
package mcpserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/application"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

// Version is what the MCP endpoint calls itself. It is the service's contract
// version, not the protocol's: the SDK settles the protocol per request.
const Version = "1"

// Service is what an MCP endpoint asks of integrations.
type Service interface {
	Catalog() []providers.Description
	RunAction(ctx context.Context, credentials authclient.Credentials, organizationID string, input application.RunActionInput) (domain.Action, error)
	ProviderConnections(ctx context.Context, credentials authclient.Credentials, organizationID, provider string) ([]domain.Connection, error)
}

// call is one request's caller: the organization the endpoint names and the
// member whose credentials it carried. An MCP endpoint keeps no session, so
// every request carries its own.
type call struct {
	organizationID string
	credentials    authclient.Credentials
	provider       providers.Description
}

// Handler serves MCP for one provider at a time, statelessly: the 2026-07-28
// protocol keeps no session, so each request stands alone and any replica may
// answer it.
type Handler struct {
	service   Service
	logger    *slog.Logger
	providers map[string]providers.Description
	streamer  *mcp.StreamableHTTPHandler
}

func New(service Service, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	handler := &Handler{service: service, logger: logger, providers: map[string]providers.Description{}}
	for _, described := range service.Catalog() {
		handler.providers[described.Name] = described
	}
	handler.streamer = mcp.NewStreamableHTTPHandler(handler.server, &mcp.StreamableHTTPOptions{
		// Only a stateless server speaks 2026-07-28; a stateful one would
		// negotiate down and expect a session this service does not keep.
		Stateless: true,
	})
	return handler
}

// OrganizationHeader names the organization a request acts in when its path
// does not, so an MCP client can keep one fixed URL per provider.
const OrganizationHeader = "X-Oryxa-Organization"

// ServeHTTP answers an MCP request for the provider its path names, in the
// organization its path or OrganizationHeader names, as the member its
// credentials name.
func (handler *Handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	described, ok := handler.providers[request.PathValue("provider")]
	if !ok || len(described.Actions) == 0 {
		http.NotFound(writer, request)
		return
	}
	organizationID := request.PathValue("organizationID")
	if organizationID == "" {
		organizationID = strings.TrimSpace(request.Header.Get(OrganizationHeader))
	}
	if organizationID == "" {
		http.Error(writer, "the organization is required, in the path or the "+OrganizationHeader+" header", http.StatusBadRequest)
		return
	}
	credentials := authclient.Credentials{Authorization: request.Header.Get("Authorization")}
	if cookie, err := request.Cookie(authclient.AccessCookieName); err == nil {
		credentials.AccessCookie = cookie.Value
	}
	if !credentials.Present() {
		// As an MCP server must, say how to authenticate rather than simply
		// refusing.
		writer.Header().Set("WWW-Authenticate", `Bearer realm="oryxa-integrations"`)
		http.Error(writer, "a member's access token is required", http.StatusUnauthorized)
		return
	}
	handler.streamer.ServeHTTP(writer, request.WithContext(context.WithValue(request.Context(), callKey{}, call{
		organizationID: organizationID, credentials: credentials, provider: described,
	})))
}

type callKey struct{}

// server builds the MCP server for one request: the provider's tools, bound to
// the member who sent it.
func (handler *Handler) server(request *http.Request) *mcp.Server {
	current, _ := request.Context().Value(callKey{}).(call)
	server := mcp.NewServer(&mcp.Implementation{
		Name: "oryxa-integrations-" + current.provider.Name, Version: Version,
	}, &mcp.ServerOptions{Instructions: current.provider.Skill, Logger: handler.logger})
	for _, action := range current.provider.Actions {
		server.AddTool(&mcp.Tool{
			Name:        current.provider.Name + "_" + action.Name,
			Description: action.Summary,
			InputSchema: toolSchema(action),
			Annotations: &mcp.ToolAnnotations{ReadOnlyHint: !action.Writes, DestructiveHint: &action.Writes},
		}, handler.runAction(current, action))
	}
	server.AddTool(&mcp.Tool{
		Name:        current.provider.Name + "_connections",
		Description: "List the member's " + current.provider.Name + " connections, which say what account an action would act as.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, handler.listConnections(current))
	return server
}

// runAction carries one tool call through to the action it names.
func (handler *Handler) runAction(current call, action providers.ActionDescription) mcp.ToolHandler {
	return func(ctx context.Context, request *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, connectionID, key, err := splitArguments(request.Params.Arguments)
		if err != nil {
			return failed(err), nil
		}
		ran, err := handler.service.RunAction(ctx, current.credentials, current.organizationID, application.RunActionInput{
			ConnectionID: connectionID, Provider: current.provider.Name, Action: action.Name, Input: input, IdempotencyKey: key,
		})
		if err != nil {
			return failed(err), nil
		}
		return answered(map[string]any{
			"action_id": ran.ID, "status": string(ran.Status), "resource": ran.Resource, "result": ran.Result,
		}), nil
	}
}

func (handler *Handler) listConnections(current call) mcp.ToolHandler {
	return func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		connections, err := handler.service.ProviderConnections(ctx, current.credentials, current.organizationID, current.provider.Name)
		if err != nil {
			return failed(err), nil
		}
		listed := make([]map[string]any, 0, len(connections))
		for _, connection := range connections {
			listed = append(listed, map[string]any{
				"connection_id": connection.ID, "account": connection.ProviderAccount.Name, "scope": connection.Scope,
				"status": connection.Status,
			})
		}
		return answered(map[string]any{"connections": listed}), nil
	}
}

// reserved are the fields every tool takes beside its action's own, so a
// caller says which connection to act with and under what key.
const (
	connectionField  = "connection_id"
	idempotencyField = "idempotency_key"
)

// toolSchema is an action's schema with the fields every tool takes.
func toolSchema(action providers.ActionDescription) json.RawMessage {
	var schema map[string]any
	if json.Unmarshal(action.Input, &schema) != nil {
		return action.Input
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		properties = map[string]any{}
	}
	properties[connectionField] = map[string]any{
		"type":        "string",
		"description": "Which connection to act with. Without it, the member's one connection to this provider is used; ask for the connections first when there may be several.",
	}
	if action.Writes {
		properties[idempotencyField] = map[string]any{
			"type":        "string",
			"description": "A key of your own for this attempt, at least 8 characters. Asking again with the same key answers what happened the first time rather than doing it twice.",
		}
		if required, ok := schema["required"].([]any); ok {
			schema["required"] = append(required, idempotencyField)
		} else {
			schema["required"] = []any{idempotencyField}
		}
	}
	schema["properties"] = properties
	encoded, err := json.Marshal(schema)
	if err != nil {
		return action.Input
	}
	return encoded
}

// splitArguments takes the fields the tool adds out of what the action reads,
// since an action refuses input it does not recognise.
func splitArguments(arguments json.RawMessage) (input json.RawMessage, connectionID, key string, err error) {
	fields := map[string]json.RawMessage{}
	if len(arguments) > 0 && json.Unmarshal(arguments, &fields) != nil {
		return nil, "", "", fmt.Errorf("%w: the tool's arguments are not an object", domain.ErrInvalid)
	}
	if raw, ok := fields[connectionField]; ok {
		delete(fields, connectionField)
		if json.Unmarshal(raw, &connectionID) != nil {
			return nil, "", "", fmt.Errorf("%w: %s is a string", domain.ErrInvalid, connectionField)
		}
	}
	if raw, ok := fields[idempotencyField]; ok {
		delete(fields, idempotencyField)
		if json.Unmarshal(raw, &key) != nil {
			return nil, "", "", fmt.Errorf("%w: %s is a string", domain.ErrInvalid, idempotencyField)
		}
	}
	input, err = json.Marshal(fields)
	if err != nil {
		return nil, "", "", fmt.Errorf("%w: the tool's arguments are not valid", domain.ErrInvalid)
	}
	return input, connectionID, key, nil
}

// answered is a tool's result, as text a model reads and as the structured
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

// failed tells the model what went wrong in the words the contract uses, so it
// can reconnect, ask for a scope, or stop trying, rather than guessing from
// prose. A tool failure is a result, not a protocol error.
func failed(err error) *mcp.CallToolResult {
	reason := "provider_error"
	switch {
	case errors.Is(err, domain.ErrNeedsReconnect):
		reason = "needs_reconnect"
	case errors.Is(err, domain.ErrCredentialRejected):
		reason = "credential_rejected"
	case errors.Is(err, domain.ErrNotFound):
		reason = "not_found"
	case errors.Is(err, domain.ErrForbidden):
		reason = "forbidden"
	case errors.Is(err, domain.ErrUnauthorized):
		reason = "unauthorized"
	case errors.Is(err, domain.ErrInvalid):
		reason = "invalid_request"
	case errors.Is(err, domain.ErrConflict):
		reason = "conflict"
	case errors.Is(err, domain.ErrProviderUnavailable):
		reason = "provider_unavailable"
	}
	message := strings.TrimSuffix(err.Error(), ": "+domain.ErrInvalid.Error())
	if reason == "provider_error" {
		// An error nothing recognises may carry anything, so only its shape
		// reaches the model.
		message = "the provider could not carry this out"
	}
	encoded, _ := json.Marshal(map[string]string{"error": reason, "message": message})
	return &mcp.CallToolResult{
		IsError:           true,
		Content:           []mcp.Content{&mcp.TextContent{Text: string(encoded)}},
		StructuredContent: map[string]string{"error": reason, "message": message},
	}
}
