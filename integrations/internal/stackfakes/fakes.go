package stackfakes

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/oauthflow"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/webhooks"
	"github.com/Shubham04-oss/oryxa-go/test/identities"
)

// TokenAuthenticator maps the stack k6 tokens onto the user ids seeded
// into auth_memberships. Organization rights still come from those rows.
type TokenAuthenticator struct{}

func (TokenAuthenticator) Authenticate(request *http.Request) (string, error) {
	token, ok := identities.TokenFrom(request)
	if !ok {
		return "", httpserver.ErrUnauthenticated
	}
	person, exists := identities.People[token]
	if !exists {
		return "", httpserver.ErrUnauthenticated
	}
	return person.UserID, nil
}

type SlackProvider struct{}

func (SlackProvider) AuthorizationURL(request oauthflow.AuthorizationRequest) (string, error) {
	values := url.Values{"state": {request.State}, "client_id": {request.ClientID}}
	return "https://provider.example.test/authorize?" + values.Encode(), nil
}

func (SlackProvider) Exchange(context.Context, oauthflow.ExchangeRequest) (oauthflow.Grant, error) {
	return oauthflow.Grant{
		AccessToken: "access-token",
		Scopes:      []string{"channels:history", "channels:read", "chat:write", "groups:history", "groups:read", "reactions:write"},
		Data:        map[string]any{"team": map[string]any{"id": "T123", "name": "Oryxa"}, "authed_user": map[string]any{"id": "U123"}},
	}, nil
}

func (SlackProvider) ResolveIdentity(context.Context, oauthflow.Grant) (domain.ProviderIdentity, error) {
	return domain.ProviderIdentity{SubjectID: "U123", AccountID: "T123", TenantID: "T123", TenantName: "Oryxa"}, nil
}

type BoundKeys struct{}

func (BoundKeys) GenerateDataKey(_ context.Context, _ string, binding map[string]string) ([]byte, []byte, error) {
	key := bytesOf(32, 7)
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	return key, digest[:], nil
}

func (BoundKeys) DecryptDataKey(_ context.Context, wrapped []byte, binding map[string]string) ([]byte, error) {
	encoded, _ := json.Marshal(binding)
	digest := sha256.Sum256(encoded)
	if !hmac.Equal(wrapped, digest[:]) {
		return nil, errors.New("binding mismatch")
	}
	return bytesOf(32, 7), nil
}

func bytesOf(size int, value byte) []byte {
	result := make([]byte, size)
	for i := range result {
		result[i] = value
	}
	return result
}

type PieceRunner struct {
	ActionCalls int
}

func (PieceRunner) VerifyAndParse(_ context.Context, request webhooks.RunnerRequest) (webhooks.RunnerResult, error) {
	timestamp := request.Headers["x-slack-request-timestamp"]
	want := "v0=" + hex.EncodeToString(hmacSHA256(request.SigningSecret, []byte("v0:"+timestamp+":"+string(request.RawBody))))
	if !hmac.Equal([]byte(want), []byte(request.Headers["x-slack-signature"])) {
		return webhooks.RunnerResult{Verified: false}, nil
	}
	body, _ := request.Body.(map[string]any)
	event, _ := body["event"].(map[string]any)
	eventType, _ := event["type"].(string)
	teamID, _ := body["team_id"].(string)
	eventID, _ := body["event_id"].(string)
	return webhooks.RunnerResult{Verified: true, Event: eventType, IdentifierValue: teamID, ProviderEventID: eventID}, nil
}

func (PieceRunner) RunTrigger(_ context.Context, request piece.TriggerInvocation) (piece.TriggerResult, error) {
	if request.Operation == "trigger.enable" {
		return piece.TriggerResult{Output: json.RawMessage(`null`), Listeners: []piece.Listener{{Events: []string{"message"}, IdentifierValue: "T123"}}}, nil
	}
	if request.Operation == "trigger.disable" {
		return piece.TriggerResult{Output: json.RawMessage(`null`)}, nil
	}
	body, _ := request.Payload.Body.(map[string]any)
	output, err := json.Marshal([]any{body["event"]})
	return piece.TriggerResult{Output: output}, err
}

func (runner *PieceRunner) RunAction(_ context.Context, request piece.ActionInvocation) (piece.ActionResult, error) {
	runner.ActionCalls++
	if request.ActionName != "slack_post_message" {
		return piece.ActionResult{}, errors.New("unexpected action")
	}
	return piece.ActionResult{Output: json.RawMessage(`{"ok":true,"channel":"C123","ts":"171.2","message":{"text":"private action content"},"token":"must-not-leak"}`)}, nil
}

func hmacSHA256(key, message []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(message)
	return mac.Sum(nil)
}
