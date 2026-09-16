package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/providers"
)

// runAction asks integrations to carry out an action as the member.
func (s *stack) runAction(action, connectionID, key string, input map[string]any) (int, domain.Action, []byte) {
	s.t.Helper()
	body := map[string]any{"connection_id": connectionID, "action": action, "input": input}
	if key != "" {
		body["idempotency_key"] = key
	}
	status, payload := s.do(http.MethodPost, "/actions", memberToken, body)
	var ran domain.Action
	if status == http.StatusOK && json.Unmarshal(payload, &ran) != nil {
		s.t.Fatalf("POST actions answered %s", payload)
	}
	return status, ran, payload
}

func completed(s *stack) []integrationsv1.ActionCompleted {
	return eventsOf[integrationsv1.ActionCompleted](s, integrationsv1.ActionCompletedDetailType)
}

func TestGitHubActionEndToEnd(t *testing.T) {
	github := newFakeGitHub(t)
	s := newStack(t, github.server)
	s.connections.add(domain.Connection{
		ID: "conn_github", AppID: "app_github", Provider: "github", CredentialType: "bearer", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "583231", Name: "octocat"}, Status: "active",
	})

	// The member opens an issue with their connection.
	status, action, body := s.runAction("create_issue", "conn_github", "chat-turn-7788", map[string]any{
		"repository": "acme/api", "title": "Webhook deliveries are late", "body": "Seen twice this week.",
	})
	if status != http.StatusOK {
		t.Fatalf("POST actions = %d %s", status, body)
	}
	if action.Status != domain.ActionCompleted || action.Provider != "github" || action.Name != "create_issue" || action.RequestedBy != "user-1" {
		t.Fatalf("action = %+v", action)
	}
	if action.Resource == nil || action.Resource.Type != "issue" || action.Resource.URL == "" {
		t.Fatalf("action resource = %+v", action.Resource)
	}
	if created := github.created(); len(created) != 1 || created[0].repository != "acme/api" || created[0].title != "Webhook deliveries are late" {
		t.Fatalf("GitHub was asked for %+v", created)
	}
	if calls := s.connections.issued(); len(calls) != 1 || calls[0].purpose != "action" || calls[0].member != "user-1" {
		t.Fatalf("credentials issued = %+v, want one action credential for the member", calls)
	}

	// The same key again answers what happened the first time, and GitHub is
	// not asked twice.
	_, again, _ := s.runAction("create_issue", "conn_github", "chat-turn-7788", map[string]any{
		"repository": "acme/api", "title": "Webhook deliveries are late",
	})
	if again.ID != action.ID || len(github.created()) != 1 {
		t.Fatalf("running it again = %+v, GitHub asked %d times", again, len(github.created()))
	}

	published := completed(s)
	if len(published) != 1 {
		t.Fatalf("action.completed events = %d, want 1", len(published))
	}
	if event := published[0]; event.Action != "github.create_issue" || event.ActionID != action.ID || event.Service != "integrations" ||
		event.RequestedBy != "user-1" || event.Resource == nil || event.Resource.Type != "issue" {
		t.Fatalf("action.completed = %+v", event)
	}
	s.noSecrets(providerToken)

	// An action GitHub refuses is recorded and published as failed, with the
	// reason a caller can act on.
	status, _, body = s.runAction("comment_on_issue", "conn_github", "chat-turn-7789", map[string]any{
		"repository": "acme/api", "number": 4041, "body": "nothing here",
	})
	if status != http.StatusNotFound {
		t.Fatalf("commenting on an issue GitHub does not have = %d %s, want 404", status, body)
	}
	failed := eventsOf[integrationsv1.ActionFailed](s, integrationsv1.ActionFailedDetailType)
	if len(failed) != 1 || failed[0].Error != integrationsv1.ActionErrorNotFound || failed[0].Retryable {
		t.Fatalf("action.failed = %+v", failed)
	}
}

func TestActionRefusedForAConnectionTheMemberCannotUse(t *testing.T) {
	github := newFakeGitHub(t)
	s := newStack(t, github.server)
	s.connections.add(domain.Connection{
		ID: "conn_other", AppID: "app_github", Provider: "github", Scope: "member", MemberID: "user-admin",
		ProviderAccount: domain.ProviderAccount{ID: "1"}, Status: "active",
	})

	status, _, body := s.runAction("create_issue", "conn_other", "chat-turn-1", map[string]any{"repository": "acme/api", "title": "no"})

	if status != http.StatusNotFound {
		t.Fatalf("POST actions on another member's connection = %d %s, want 404", status, body)
	}
	if len(github.created()) != 0 {
		t.Fatal("GitHub was called for a connection the member cannot use")
	}
}

// fakeSlackAPI answers the Web API calls Slack actions make.
type fakeSlackAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	posted []map[string]any
	// failWith, when set, is the error Slack answers with on HTTP 200.
	failWith string
}

func newFakeSlackAPI(t *testing.T) *fakeSlackAPI {
	t.Helper()
	slack := &fakeSlackAPI{t: t}
	slack.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+providerToken {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = writer.Write([]byte(`{"ok":false,"error":"invalid_auth"}`))
			return
		}
		var body map[string]any
		if request.Body != nil {
			_ = json.NewDecoder(request.Body).Decode(&body)
		}
		slack.mu.Lock()
		failWith := slack.failWith
		if strings.HasSuffix(request.URL.Path, "/chat.postMessage") {
			slack.posted = append(slack.posted, body)
		}
		slack.mu.Unlock()
		writer.Header().Set("Content-Type", "application/json")
		if failWith != "" {
			_, _ = writer.Write([]byte(`{"ok":false,"error":"` + failWith + `"}`))
			return
		}
		switch {
		case strings.HasSuffix(request.URL.Path, "/chat.postMessage"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "channel": "C0001", "ts": "1789992000.000200"})
		case strings.HasSuffix(request.URL.Path, "/conversations.list"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true, "channels": []map[string]any{
				{"id": "C0001", "name": "deploys", "is_private": false, "is_member": true},
			}})
		case strings.HasSuffix(request.URL.Path, "/reactions.add"):
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": true})
		default:
			_ = json.NewEncoder(writer).Encode(map[string]any{"ok": false, "error": "unknown_method"})
		}
	}))
	t.Cleanup(slack.server.Close)
	return slack
}

func (slack *fakeSlackAPI) messages() []map[string]any {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	return append([]map[string]any(nil), slack.posted...)
}

func TestSlackActionEndToEnd(t *testing.T) {
	slack := newFakeSlackAPI(t)
	s := newStack(t, slack.server)
	s.connections.add(domain.Connection{
		ID: "conn_slack", AppID: "app_slack", Provider: "slack", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "T0001:U0001", Name: "Acme"}, Status: "active",
	})

	status, action, body := s.runAction("post_message", "conn_slack", "chat-turn-4242", map[string]any{
		"channel": "C0001", "text": "the deploy is green",
	})

	if status != http.StatusOK {
		t.Fatalf("POST actions = %d %s", status, body)
	}
	if action.Status != domain.ActionCompleted || action.Provider != "slack" || action.Resource == nil || action.Resource.ID != "1789992000.000200" {
		t.Fatalf("action = %+v, resource = %+v", action, action.Resource)
	}
	posted := slack.messages()
	if len(posted) != 1 || posted[0]["channel"] != "C0001" || posted[0]["text"] != "the deploy is green" {
		t.Fatalf("Slack was asked to post %+v", posted)
	}
	if published := completed(s); len(published) != 1 || published[0].Action != "slack.post_message" {
		t.Fatalf("action.completed = %+v", published)
	}

	// Slack refusing on HTTP 200 still fails the action.
	slack.mu.Lock()
	slack.failWith = "channel_not_found"
	slack.mu.Unlock()
	status, _, body = s.runAction("post_message", "conn_slack", "chat-turn-4243", map[string]any{"channel": "C9999", "text": "nobody"})
	if status != http.StatusNotFound {
		t.Fatalf("posting to a channel Slack does not have = %d %s, want 404", status, body)
	}
	failed := eventsOf[integrationsv1.ActionFailed](s, integrationsv1.ActionFailedDetailType)
	if len(failed) != 1 || failed[0].Error != integrationsv1.ActionErrorNotFound {
		t.Fatalf("action.failed = %+v", failed)
	}
}

func TestCatalogEndToEnd(t *testing.T) {
	s := newStack(t, nil)

	response, err := s.server.Client().Get(s.apiURL + "/v1/catalog")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var answered struct {
		Providers []providers.Description `json:"providers"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answered); err != nil || response.StatusCode != http.StatusOK {
		t.Fatalf("GET catalog = %d, %v", response.StatusCode, err)
	}

	described := map[string]providers.Description{}
	for _, provider := range answered.Providers {
		described[provider.Name] = provider
	}
	for _, name := range []string{"github", "slack", "linear", "microsoft"} {
		provider, ok := described[name]
		if !ok {
			t.Fatalf("the catalogue does not describe %s", name)
		}
		if provider.Skill == "" || len(provider.Events.Names) == 0 {
			t.Fatalf("%s is described as %+v", name, provider)
		}
	}
	if len(described["github"].Target) != 1 || described["github"].Target[0].Name != "repository" || !described["github"].Target[0].Required {
		t.Fatalf("github's target = %+v", described["github"].Target)
	}
	for _, name := range []string{"github", "slack"} {
		if len(described[name].Actions) == 0 {
			t.Fatalf("%s has no actions in the catalogue", name)
		}
		for _, action := range described[name].Actions {
			var schema map[string]any
			if err := json.Unmarshal(action.Input, &schema); err != nil || schema["type"] != "object" {
				t.Fatalf("%s %s input schema = %s, %v", name, action.Name, action.Input, err)
			}
			if action.Summary == "" || !providers.ActionNamePattern.MatchString(action.Name) {
				t.Fatalf("%s action = %+v", name, action)
			}
		}
	}
}
