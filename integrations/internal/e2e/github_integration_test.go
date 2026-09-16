package e2e

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type githubHook struct {
	id                                    int
	repository                            string
	events                                []string
	url, secret, contentType, insecureSSL string
}

// fakeGitHub keeps repository webhooks as GitHub does: it pings a webhook's
// URL when it is created, and delivers signed events to it.
type fakeGitHub struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	hooks   map[int]githubHook
	nextID  int
	pings   []int
	deleted []int
	// authorizations are the Authorization headers GitHub API calls carried.
	authorizations []string
	issues         []githubIssue
	comments       []string
}

// githubIssue is an issue the fake opened, as the request asked for it.
type githubIssue struct {
	repository, title, body string
	number                  int
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	github := &fakeGitHub{t: t, hooks: map[int]githubHook{}, nextID: 4000}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /repos/{owner}/{repo}/hooks", github.createHook)
	mux.HandleFunc("DELETE /repos/{owner}/{repo}/hooks/{hookID}", github.deleteHook)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues", github.createIssue)
	mux.HandleFunc("POST /repos/{owner}/{repo}/issues/{number}/comments", github.commentOnIssue)
	github.server = httptest.NewServer(mux)
	t.Cleanup(github.server.Close)
	return github
}

func (github *fakeGitHub) createHook(writer http.ResponseWriter, request *http.Request) {
	github.mu.Lock()
	github.authorizations = append(github.authorizations, request.Header.Get("Authorization"))
	github.mu.Unlock()
	if request.Header.Get("X-GitHub-Api-Version") == "" {
		http.Error(writer, `{"message":"API version required"}`, http.StatusBadRequest)
		return
	}
	var body struct {
		Name   string   `json:"name"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
			Secret      string `json:"secret"`
			InsecureSSL string `json:"insecure_ssl"`
		} `json:"config"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Name != "web" || !body.Active {
		http.Error(writer, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		return
	}
	github.mu.Lock()
	github.nextID++
	hook := githubHook{
		id: github.nextID, repository: request.PathValue("owner") + "/" + request.PathValue("repo"), events: body.Events,
		url: body.Config.URL, secret: body.Config.Secret, contentType: body.Config.ContentType, insecureSSL: body.Config.InsecureSSL,
	}
	github.hooks[hook.id] = hook
	github.mu.Unlock()

	// GitHub pings a new webhook.
	if status := github.deliver(hook, "ping", fmt.Sprintf("ping-%d", hook.id), map[string]any{"zen": "Design for failure.", "hook_id": hook.id}); status == http.StatusOK {
		github.mu.Lock()
		github.pings = append(github.pings, hook.id)
		github.mu.Unlock()
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": hook.id, "type": "Repository", "name": "web", "active": true, "events": body.Events,
		"config":     map[string]any{"url": body.Config.URL, "content_type": body.Config.ContentType, "insecure_ssl": body.Config.InsecureSSL},
		"updated_at": "2026-09-15T12:00:00Z", "created_at": "2026-09-15T12:00:00Z",
		"url": github.server.URL + request.URL.Path + "/" + strconv.Itoa(hook.id),
	})
}

func (github *fakeGitHub) deleteHook(writer http.ResponseWriter, request *http.Request) {
	id, _ := strconv.Atoi(request.PathValue("hookID"))
	github.mu.Lock()
	defer github.mu.Unlock()
	github.authorizations = append(github.authorizations, request.Header.Get("Authorization"))
	if _, ok := github.hooks[id]; !ok {
		http.Error(writer, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	delete(github.hooks, id)
	github.deleted = append(github.deleted, id)
	writer.WriteHeader(http.StatusNoContent)
}

// onlyHook is the one webhook GitHub holds.
func (github *fakeGitHub) onlyHook() githubHook {
	github.t.Helper()
	github.mu.Lock()
	defer github.mu.Unlock()
	if len(github.hooks) != 1 {
		github.t.Fatalf("GitHub holds %d webhooks, want 1", len(github.hooks))
	}
	for _, hook := range github.hooks {
		return hook
	}
	return githubHook{}
}

// deliver sends a webhook an event, signed with its secret, as GitHub does,
// and answers the status integrations replied with.
func (github *fakeGitHub) deliver(hook githubHook, event, deliveryID string, payload any) int {
	body, err := json.Marshal(payload)
	if err != nil {
		github.t.Fatal(err)
	}
	return github.send(hook.url, event, deliveryID, body, "sha256="+hmacHex([]byte(hook.secret), body))
}

func (github *fakeGitHub) send(webhookURL, event, deliveryID string, body []byte, signature string) int {
	status, _, _ := deliver(github.t, webhookURL, http.Header{
		"Content-Type": {"application/json"}, "User-Agent": {"GitHub-Hookshot/e2e"}, "X-GitHub-Event": {event},
		"X-GitHub-Delivery": {deliveryID}, "X-GitHub-Hook-ID": {"0"}, "X-Hub-Signature-256": {signature},
	}, body)
	return status
}

func TestGitHubSubscriptionEndToEnd(t *testing.T) {
	github := newFakeGitHub(t)
	s := newStack(t, github.server)
	ctx := context.Background()
	s.connections.add(domain.Connection{
		ID: "conn_github", AppID: "app_github", Provider: "github", CredentialType: "bearer", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "583231", Name: "octocat"}, Status: "active",
	})

	// A member subscribes their connection to a repository's issues and
	// comments. Integrations registers a webhook, which GitHub pings.
	created := s.subscribe("conn_github", map[string]string{"repository": "acme/api"}, []string{"issues", "issue_comment"})
	if created.Status != "active" || created.Provider != "github" || created.CreatedBy != "user-1" || created.ExpiresAt != nil {
		t.Fatalf("created subscription = %+v", created)
	}
	hook := github.onlyHook()
	route, ok := strings.CutPrefix(hook.url, s.publicURL+"/v1/webhooks/github/")
	if !ok || len(route) != 43 || hook.repository != "acme/api" || !slices.Equal(hook.events, []string{"issues", "issue_comment"}) {
		t.Fatalf("GitHub webhook = %+v", hook)
	}
	if len(hook.secret) != 64 || hook.contentType != "json" || hook.insecureSSL != "0" {
		t.Fatalf("GitHub webhook config = content type %q, insecure_ssl %q, secret of %d characters", hook.contentType, hook.insecureSSL, len(hook.secret))
	}
	if len(github.pings) != 1 {
		t.Fatal("integrations did not verify GitHub's ping while the webhook registered")
	}
	if github.authorizations[0] != "Bearer "+providerToken {
		t.Fatal("GitHub was not called with the credential connections issued")
	}
	if calls := s.connections.issued(); !slices.Equal(calls, []tokenCall{{service: "integrations", purpose: "trigger", member: "user-1"}}) {
		t.Fatalf("credentials issued = %+v, want one trigger credential for the member", calls)
	}
	if !s.connections.held("conn_github") {
		t.Fatal("the connection is not held while its webhook exists")
	}

	// GitHub delivers an issue being opened.
	issue := map[string]any{
		"action":     "opened",
		"issue":      map[string]any{"id": 1347, "number": 12, "title": "Webhook deliveries are late", "html_url": "https://github.com/acme/api/issues/12"},
		"repository": map[string]any{"id": 1296269, "full_name": "acme/api"},
		"sender":     map[string]any{"id": 583231, "login": "octocat"},
	}
	if status := github.deliver(hook, "issues", "72d3162e-cc78-11e3-81ab-4c9367dc0958", issue); status != http.StatusOK {
		t.Fatalf("issues delivery = %d, want 200", status)
	}
	triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType)
	if len(triggers) != 1 {
		t.Fatalf("triggers = %d, want 1", len(triggers))
	}
	trigger := triggers[0]
	if trigger.SubscriptionID != created.ID || trigger.OrganizationID != organization || trigger.ConnectionID != "conn_github" || trigger.Event != "github.issues.opened" ||
		trigger.Scope != "member" || trigger.MemberID != "user-1" || trigger.CreatedBy != "user-1" || trigger.ProviderAccount.Name != "octocat" || trigger.DeliveryID != "72d3162e-cc78-11e3-81ab-4c9367dc0958" {
		t.Fatalf("trigger = %+v", trigger)
	}
	if trigger.Resource != (integrationsv1.Resource{Type: "issue", ID: "1347", Name: "Webhook deliveries are late", URL: "https://github.com/acme/api/issues/12"}) {
		t.Fatalf("trigger resource = %+v", trigger.Resource)
	}
	if trigger.Actor == nil || *trigger.Actor != (integrationsv1.Actor{ID: "583231", Name: "octocat"}) {
		t.Fatalf("trigger actor = %+v", trigger.Actor)
	}
	var payload map[string]any
	if err := json.Unmarshal(trigger.Payload, &payload); err != nil || payload["action"] != "opened" || trigger.PayloadOmitted {
		t.Fatalf("trigger payload = %s, omitted = %v", trigger.Payload, trigger.PayloadOmitted)
	}

	// GitHub redelivers it, a forger posts without the secret, and a delivery
	// names another webhook's route: only the first delivery is published.
	if status := github.deliver(hook, "issues", "72d3162e-cc78-11e3-81ab-4c9367dc0958", issue); status != http.StatusOK {
		t.Fatalf("redelivery = %d, want 200", status)
	}
	forged, _ := json.Marshal(issue)
	if status := github.send(hook.url, "issues", "forged-1", forged, "sha256="+hmacHex([]byte("not-the-secret"), forged)); status != http.StatusUnauthorized {
		t.Fatalf("forged delivery = %d, want 401", status)
	}
	if status := github.send(hook.url, "issues", "unsigned-1", forged, ""); status != http.StatusUnauthorized {
		t.Fatalf("unsigned delivery = %d, want 401", status)
	}
	if status := github.send(s.publicURL+"/v1/webhooks/github/"+strings.Repeat("x", 43), "issues", "stray-1", forged, "sha256="+hmacHex([]byte(hook.secret), forged)); status != http.StatusNotFound {
		t.Fatalf("delivery to a route that names nothing = %d, want 404", status)
	}
	if status := github.send(strings.Replace(hook.url, "/github/", "/linear/", 1), "issues", "wrong-provider-1", forged, "sha256="+hmacHex([]byte(hook.secret), forged)); status != http.StatusNotFound {
		t.Fatalf("delivery to the route under another provider = %d, want 404", status)
	}
	if triggers := eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType); len(triggers) != 1 {
		t.Fatalf("triggers = %d after a redelivery and refused deliveries, want 1", len(triggers))
	}
	s.noSecrets(hook.secret, providerToken)

	// Only the member who made it, or an admin, may stop it; the member does.
	if status, body := s.do(http.MethodGet, "/subscriptions/"+created.ID, adminToken, nil); status != http.StatusOK || !strings.Contains(string(body), created.ID) {
		t.Fatalf("GET subscription as an admin = %d %s", status, body)
	}
	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
	if stored := s.stored(created.ID); stored.Status != domain.SubscriptionStopped || stored.CleanupAt == nil {
		t.Fatalf("stopped subscription = %+v, want its webhook queued for removal", stored)
	}
	if !s.connections.held("conn_github") {
		t.Fatal("the hold was released before the webhook was removed")
	}
	// GitHub may still deliver until the webhook is removed: integrations
	// acknowledges and drops it.
	if status := github.deliver(hook, "issue_comment", "late-1", map[string]any{"action": "created"}); status != http.StatusOK {
		t.Fatalf("delivery to a stopped subscription = %d, want 200", status)
	}

	// The cleanup worker removes the webhook with a cleanup credential for the
	// member, then releases the hold.
	s.service.CleanupDue(ctx)
	if !slices.Equal(github.deleted, []int{hook.id}) || github.authorizations[len(github.authorizations)-1] != "Bearer "+providerToken {
		t.Fatalf("deleted webhooks = %v", github.deleted)
	}
	if calls := s.connections.issued(); calls[len(calls)-1] != (tokenCall{service: "integrations", purpose: "cleanup", member: "user-1"}) {
		t.Fatalf("cleanup credential = %+v", calls[len(calls)-1])
	}
	if s.connections.held("conn_github") || s.stored(created.ID).CleanupAt != nil {
		t.Fatal("after cleanup the connection is still held or a removal is still due")
	}
	s.service.CleanupDue(ctx)
	if len(github.deleted) != 1 {
		t.Fatal("cleanup ran twice")
	}

	var detailTypes []string
	for _, event := range s.outbox() {
		detailTypes = append(detailTypes, event.DetailType)
	}
	slices.Sort(detailTypes)
	if want := []string{integrationsv1.SubscriptionCreatedDetailType, integrationsv1.SubscriptionStoppedDetailType, integrationsv1.TriggerReceivedDetailType}; !slices.Equal(detailTypes, want) {
		t.Fatalf("published = %v, want %v", detailTypes, want)
	}
}

func TestGitHubSubscriptionStoppedByDisconnectRemovesItsWebhook(t *testing.T) {
	github := newFakeGitHub(t)
	s := newStack(t, github.server)
	ctx := context.Background()
	s.connections.add(domain.Connection{
		ID: "conn_github", AppID: "app_github", Provider: "github", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "583231"}, Status: "active",
	})
	first := s.subscribe("conn_github", map[string]string{"repository": "acme/api"}, []string{"push"})
	second := s.subscribe("conn_github", map[string]string{"repository": "acme/web"}, nil)
	if len(github.hooks) != 2 {
		t.Fatalf("GitHub holds %d webhooks, want 2", len(github.hooks))
	}

	// The member disconnects: connections holds the credentials, and
	// integrations stops both subscriptions and removes both webhooks.
	s.connections.setStatus("conn_github", "disconnected")
	s.clock.Advance(time.Minute)
	if err := s.service.ConnectionDisconnected(ctx, connectionDisconnected("conn_github", "user-1")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{first.ID, second.ID} {
		if stored := s.stored(id); stored.Status != domain.SubscriptionStopped || stored.Reason != integrationsv1.SubscriptionStoppedReasonDisconnected || stored.StoppedBy != "user-1" {
			t.Fatalf("subscription after disconnect = %+v", stored)
		}
	}
	s.service.CleanupDue(ctx)
	if len(github.hooks) != 0 || len(github.deleted) != 2 {
		t.Fatalf("GitHub still holds %d webhooks after cleanup", len(github.hooks))
	}
	if s.connections.held("conn_github") {
		t.Fatal("the hold outlived both webhooks")
	}

	// A new subscription on the disconnected connection is refused.
	status, body := s.do(http.MethodPost, "/subscriptions", memberToken, map[string]any{"connection_id": "conn_github", "target": map[string]string{"repository": "acme/api"}})
	if status != http.StatusConflict || !strings.Contains(string(body), `"conflict"`) {
		t.Fatalf("POST subscriptions on a disconnected connection = %d %s", status, body)
	}
}

func (github *fakeGitHub) createIssue(writer http.ResponseWriter, request *http.Request) {
	var body struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	if err := json.NewDecoder(request.Body).Decode(&body); err != nil || body.Title == "" {
		http.Error(writer, `{"message":"Validation Failed"}`, http.StatusUnprocessableEntity)
		return
	}
	repository := request.PathValue("owner") + "/" + request.PathValue("repo")
	github.mu.Lock()
	issue := githubIssue{repository: repository, title: body.Title, body: body.Body, number: 11 + len(github.issues)}
	github.issues = append(github.issues, issue)
	github.authorizations = append(github.authorizations, request.Header.Get("Authorization"))
	github.mu.Unlock()
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": 1000 + issue.number, "number": issue.number, "title": issue.title, "body": issue.body, "state": "open",
		"html_url": "https://github.com/" + repository + "/issues/" + strconv.Itoa(issue.number),
		"url":      github.server.URL + "/repos/" + repository + "/issues/" + strconv.Itoa(issue.number),
		"user":     map[string]any{"id": 583231, "login": "octocat"},
		"comments": 0, "labels": []any{},
	})
}

func (github *fakeGitHub) commentOnIssue(writer http.ResponseWriter, request *http.Request) {
	number, _ := strconv.Atoi(request.PathValue("number"))
	var body struct {
		Body string `json:"body"`
	}
	_ = json.NewDecoder(request.Body).Decode(&body)
	github.mu.Lock()
	known := number-11 >= 0 && number-11 < len(github.issues)
	if known {
		github.comments = append(github.comments, body.Body)
	}
	github.mu.Unlock()
	if !known {
		writer.Header().Set("Content-Type", "application/json; charset=utf-8")
		http.Error(writer, `{"message":"Not Found"}`, http.StatusNotFound)
		return
	}
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(writer).Encode(map[string]any{
		"id": 99, "body": body.Body, "html_url": "https://github.com/" + request.PathValue("owner") + "/" + request.PathValue("repo") +
			"/issues/" + strconv.Itoa(number) + "#issuecomment-99",
		"user": map[string]any{"id": 583231, "login": "octocat"},
	})
}

// created are the issues the fake opened.
func (github *fakeGitHub) created() []githubIssue {
	github.mu.Lock()
	defer github.mu.Unlock()
	return append([]githubIssue(nil), github.issues...)
}
