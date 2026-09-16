package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/secrets"
)

// The live tests run the whole service against a real provider: they create a
// real subscription, have the provider deliver a real event to integrations
// through a public URL, and remove what they created. Each one runs only when
// its own variables are set, alongside INTEGRATIONS_TEST_DATABASE_URL and
// INTEGRATIONS_LIVE_PUBLIC_URL. They only ever read, or create something they
// then remove, and they never print a credential.

// liveStack serves integrations on INTEGRATIONS_LIVE_ADDRESS, reached at
// INTEGRATIONS_LIVE_PUBLIC_URL, calling the real providers.
func liveStack(t *testing.T) *stack {
	t.Helper()
	publicURL := strings.TrimSpace(os.Getenv("INTEGRATIONS_LIVE_PUBLIC_URL"))
	if publicURL == "" {
		t.Skip("set INTEGRATIONS_LIVE_PUBLIC_URL to a public HTTPS URL that reaches INTEGRATIONS_LIVE_ADDRESS, such as a Cloudflare tunnel")
	}
	address := strings.TrimSpace(os.Getenv("INTEGRATIONS_LIVE_ADDRESS"))
	if address == "" {
		address = "127.0.0.1:8787"
	}
	s := newStackWith(t, stackOptions{publicURL: publicURL, listen: address, realClock: true})
	t.Logf("integrations is listening on %s, which %s must reach", s.apiURL, s.publicURL)
	return s
}

// liveValue is a variable a live test needs, or a reason to skip.
func liveValue(t *testing.T, name, what string) string {
	t.Helper()
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		t.Skipf("set %s to %s", name, what)
	}
	return value
}

// callProvider sends a JSON request to a provider and decodes its answer. It
// never logs the credential the header carries.
func callProvider(t *testing.T, method, endpoint string, header http.Header, body, target any) (int, []byte) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, endpoint, reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, values := range header {
		request.Header[name] = values
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := (&http.Client{Timeout: 30 * time.Second}).Do(request)
	if err != nil {
		t.Fatalf("%s %s: %v", method, endpoint, err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatal(err)
	}
	if target != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, target); err != nil {
			t.Fatalf("%s %s answered %d, which is not the JSON expected: %v", method, endpoint, response.StatusCode, err)
		}
	}
	return response.StatusCode, payload
}

// waitFor polls until something happens, or fails the test.
func waitFor(t *testing.T, within time.Duration, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("waited %s for %s", within, what)
}

// waitForTrigger waits for a trigger the provider delivered.
func (s *stack) waitForTrigger(within time.Duration, what string, match func(integrationsv1.TriggerReceived) bool) integrationsv1.TriggerReceived {
	s.t.Helper()
	var found integrationsv1.TriggerReceived
	waitFor(s.t, within, what, func() bool {
		for _, trigger := range eventsOf[integrationsv1.TriggerReceived](s, integrationsv1.TriggerReceivedDetailType) {
			if match(trigger) {
				found = trigger
				return true
			}
		}
		return false
	})
	return found
}

func TestLiveGitHubWebhook(t *testing.T) {
	token := liveValue(t, "INTEGRATIONS_LIVE_GITHUB_TOKEN", "a GitHub token that may manage a repository's webhooks (repo, or admin:repo_hook)")
	repository := liveValue(t, "INTEGRATIONS_LIVE_GITHUB_REPOSITORY", "the owner/name of a repository with at least one push to subscribe")
	s := liveStack(t)
	ctx := context.Background()
	s.connections.issues(token, bearerPlacement)
	s.connections.add(domain.Connection{
		ID: "conn_live_github", AppID: "app_live_github", Provider: "github", CredentialType: "bearer", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "live", Name: strings.SplitN(repository, "/", 2)[0]}, Status: "active",
	})
	github := func(method, path string, body, target any) int {
		status, payload := callProvider(t, method, "https://api.github.com"+path, http.Header{
			"Authorization": {"Bearer " + token}, "Accept": {"application/vnd.github+json"}, "X-GitHub-Api-Version": {"2022-11-28"},
		}, body, target)
		if status >= 500 {
			t.Fatalf("GitHub %s %s = %d %s", method, path, status, payload)
		}
		return status
	}

	// A member subscribes the repository's pushes. GitHub creates the webhook
	// and pings it at once.
	created := s.subscribe("conn_live_github", map[string]string{"repository": repository}, []string{"push"})
	stored := s.stored(created.ID)
	t.Cleanup(func() {
		if hookID := s.stored(created.ID).RegistrationID; hookID != "" {
			github(http.MethodDelete, "/repos/"+repository+"/hooks/"+hookID, nil, nil)
		}
	})
	t.Logf("GitHub webhook %s on %s", stored.RegistrationID, repository)

	var hook struct {
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL         string `json:"url"`
			ContentType string `json:"content_type"`
		} `json:"config"`
	}
	if status := github(http.MethodGet, "/repos/"+repository+"/hooks/"+stored.RegistrationID, nil, &hook); status != http.StatusOK {
		t.Fatalf("GET the webhook GitHub created = %d", status)
	}
	if want := s.publicURL + "/v1/webhooks/github/" + stored.Route; hook.Config.URL != want || !hook.Active || hook.Config.ContentType != "json" {
		t.Fatalf("GitHub webhook = %+v, want it active at %s", hook, want)
	}
	waitFor(t, 30*time.Second, "GitHub's ping to reach integrations", func() bool { return s.deliveries.Load() > 0 })

	// GitHub redelivers the repository's latest push to the webhook, which
	// integrations verifies and publishes.
	if status := github(http.MethodPost, "/repos/"+repository+"/hooks/"+stored.RegistrationID+"/tests", nil, nil); status != http.StatusNoContent {
		t.Fatalf("POST the webhook's test delivery = %d", status)
	}
	trigger := s.waitForTrigger(2*time.Minute, "GitHub's push delivery", func(trigger integrationsv1.TriggerReceived) bool {
		return trigger.Event == "github.push"
	})
	if trigger.SubscriptionID != created.ID || trigger.Resource.Type != "push" || trigger.PayloadOmitted || !strings.Contains(string(trigger.Payload), repository) {
		t.Fatalf("trigger = %+v", trigger)
	}
	t.Logf("GitHub delivery %s published %s for %s", trigger.DeliveryID, trigger.Event, trigger.Resource.Name)

	// Stopping the subscription removes the webhook from GitHub.
	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
	s.service.CleanupDue(ctx)
	if status := github(http.MethodGet, "/repos/"+repository+"/hooks/"+stored.RegistrationID, nil, nil); status != http.StatusNotFound {
		t.Fatalf("the webhook is still at GitHub: GET = %d", status)
	}
}

func TestLiveSlackEvents(t *testing.T) {
	signingSecret := liveValue(t, "INTEGRATIONS_LIVE_SLACK_SIGNING_SECRET", "the Slack app's signing secret")
	botToken := liveValue(t, "INTEGRATIONS_LIVE_SLACK_BOT_TOKEN", "a Slack bot token that may post in the channel")
	channel := liveValue(t, "INTEGRATIONS_LIVE_SLACK_CHANNEL", "the ID of a channel the app is in, to post a message the app then receives")
	s := liveStack(t)
	ctx := context.Background()
	slack := func(method, path string, body, target any) {
		status, payload := callProvider(t, method, "https://slack.com/api/"+path, http.Header{"Authorization": {"Bearer " + botToken}}, body, target)
		if status != http.StatusOK {
			t.Fatalf("Slack %s = %d %s", path, status, payload)
		}
	}

	// Who the bot token belongs to is the workspace the subscription watches.
	var identity struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
		Team   string `json:"team"`
	}
	slack(http.MethodPost, "auth.test", nil, &identity)
	if !identity.OK {
		t.Fatalf("Slack refused the bot token: %s", identity.Error)
	}
	s.connections.issues(botToken, bearerPlacement)
	s.connections.add(domain.Connection{
		ID: "conn_live_slack", AppID: "app_live_slack", Provider: "slack", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: identity.TeamID + ":" + identity.UserID, Name: identity.Team}, Status: "active",
	})

	// The app's signing secret goes at a route the test can be told to keep,
	// so the Request URL set on the Slack app stays the same between runs.
	requestURL := ""
	if route := strings.TrimSpace(os.Getenv("INTEGRATIONS_LIVE_SLACK_ROUTE")); route != "" {
		if len(route) < 32 || len(route) > 64 {
			t.Fatal("INTEGRATIONS_LIVE_SLACK_ROUTE must be 32 to 64 characters")
		}
		sealed, err := s.sealer.Seal(ctx, secrets.Binding{OrganizationID: organization, ResourceType: secrets.ResourceAppWebhook, ResourceID: "app_live_slack"}, []byte(signingSecret))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		if _, err := s.store.PutAppWebhook(ctx, domain.AppWebhook{
			AppID: "app_live_slack", OrganizationID: organization, Provider: "slack", Route: route,
			UpdatedBy: "user-admin", CreatedAt: now, UpdatedAt: now, EncryptedSecret: sealed,
		}); err != nil {
			t.Fatal(err)
		}
		requestURL = s.publicURL + "/v1/webhooks/slack/" + route
	} else {
		status, body := s.do(http.MethodPut, "/apps/app_live_slack/webhook", adminToken, map[string]string{"secret": signingSecret})
		var webhook struct {
			URL string `json:"url"`
		}
		if status != http.StatusOK || json.Unmarshal(body, &webhook) != nil {
			t.Fatalf("PUT the app's webhook secret = %d %s", status, body)
		}
		requestURL = webhook.URL
	}
	t.Logf("the Slack app's Request URL must be %s, and it must subscribe to message events", requestURL)

	// The member subscribes to the workspace's messages, and the bot posts
	// one, which Slack delivers to the app.
	created := s.subscribe("conn_live_slack", nil, []string{"message"})
	text := fmt.Sprintf("Oryxa integrations live test %d", time.Now().UnixNano())
	var posted struct {
		OK      bool   `json:"ok"`
		Error   string `json:"error"`
		TS      string `json:"ts"`
		Channel string `json:"channel"`
	}
	slack(http.MethodPost, "chat.postMessage", map[string]string{"channel": channel, "text": text}, &posted)
	if !posted.OK {
		t.Fatalf("Slack refused the message: %s", posted.Error)
	}
	t.Cleanup(func() {
		var deleted struct {
			OK bool `json:"ok"`
		}
		slack(http.MethodPost, "chat.delete", map[string]string{"channel": posted.Channel, "ts": posted.TS}, &deleted)
	})

	trigger := s.waitForTrigger(2*time.Minute, "Slack's message event (is the app's Request URL set to the URL above?)", func(trigger integrationsv1.TriggerReceived) bool {
		return trigger.Event == "slack.message" && strings.Contains(string(trigger.Payload), text)
	})
	if trigger.SubscriptionID != created.ID || trigger.Resource.Type != "message" || trigger.Resource.ID != posted.TS {
		t.Fatalf("trigger = %+v, want the message the bot posted", trigger)
	}
	if strings.Contains(string(trigger.Payload), signingSecret) {
		t.Fatal("the published payload carries the signing secret")
	}
	t.Logf("Slack delivery %s published %s in %s", trigger.DeliveryID, trigger.Event, trigger.Resource.Name)

	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
}

func TestLiveLinearWebhook(t *testing.T) {
	token := liveValue(t, "INTEGRATIONS_LIVE_LINEAR_TOKEN", "a Linear API key of a workspace admin, or an OAuth token with the admin scope")
	team := liveValue(t, "INTEGRATIONS_LIVE_LINEAR_TEAM", "the key or ID of a Linear team to subscribe, where an issue may be created and deleted")
	s := liveStack(t)
	ctx := context.Background()
	// A Linear API key goes on the Authorization header with no prefix.
	keyPlacement := placement{In: "header", Name: "Authorization"}
	s.connections.issues(token, keyPlacement)
	s.connections.add(domain.Connection{
		ID: "conn_live_linear", AppID: "app_live_linear", Provider: "linear", CredentialType: "api_key", Scope: "organization",
		ProviderAccount: domain.ProviderAccount{ID: "live", Name: "Linear"}, Status: "active",
	})
	linear := func(query string, variables map[string]any, target any) {
		var answer struct {
			Data   json.RawMessage `json:"data"`
			Errors []struct {
				Message string `json:"message"`
			} `json:"errors"`
		}
		status, payload := callProvider(t, http.MethodPost, "https://api.linear.app/graphql",
			http.Header{"Authorization": {token}}, map[string]any{"query": query, "variables": variables}, &answer)
		if status != http.StatusOK || len(answer.Errors) > 0 {
			t.Fatalf("Linear answered %d: %s", status, payload)
		}
		if target != nil && json.Unmarshal(answer.Data, target) != nil {
			t.Fatalf("Linear answered %s, which is not what the test expected", answer.Data)
		}
	}

	// The team an issue is created in, by key or ID, as the provider resolves it.
	var teams struct {
		Teams struct {
			Nodes []struct {
				ID  string `json:"id"`
				Key string `json:"key"`
			} `json:"nodes"`
		} `json:"teams"`
	}
	linear(`query TeamByKey($key: String!) { teams(filter: { key: { eqIgnoreCase: $key } }, first: 1) { nodes { id key } } }`, map[string]any{"key": team}, &teams)
	if len(teams.Teams.Nodes) == 0 {
		t.Fatalf("Linear has no team %q", team)
	}
	teamID := teams.Teams.Nodes[0].ID

	// A member subscribes the team's issues; Linear creates the webhook.
	created := s.subscribe("conn_live_linear", map[string]string{"team": team}, []string{"Issue"})
	stored := s.stored(created.ID)
	t.Cleanup(func() {
		if registration := s.stored(created.ID).RegistrationID; registration != "" {
			linear(`mutation DeleteWebhook($id: String!) { webhookDelete(id: $id) { success } }`, map[string]any{"id": registration}, nil)
		}
	})
	t.Logf("Linear webhook %s for team %s", stored.RegistrationID, teams.Teams.Nodes[0].Key)

	// An issue is created in the team, which Linear delivers.
	title := fmt.Sprintf("Oryxa integrations live test %d", time.Now().UnixNano())
	var issue struct {
		IssueCreate struct {
			Success bool `json:"success"`
			Issue   struct {
				ID         string `json:"id"`
				Identifier string `json:"identifier"`
			} `json:"issue"`
		} `json:"issueCreate"`
	}
	linear(`mutation CreateIssue($teamId: String!, $title: String!) { issueCreate(input: { teamId: $teamId, title: $title }) { success issue { id identifier } } }`,
		map[string]any{"teamId": teamID, "title": title}, &issue)
	if !issue.IssueCreate.Success || issue.IssueCreate.Issue.ID == "" {
		t.Fatal("Linear did not create the test issue")
	}
	t.Cleanup(func() {
		linear(`mutation DeleteIssue($id: String!) { issueDelete(id: $id) { success } }`, map[string]any{"id": issue.IssueCreate.Issue.ID}, nil)
	})

	trigger := s.waitForTrigger(2*time.Minute, "Linear's issue delivery", func(trigger integrationsv1.TriggerReceived) bool {
		return trigger.Event == "linear.issue.create" && trigger.Resource.ID == issue.IssueCreate.Issue.ID
	})
	if trigger.SubscriptionID != created.ID || trigger.Resource.Name != title {
		t.Fatalf("trigger = %+v, want the issue just created", trigger)
	}
	t.Logf("Linear delivery %s published %s for %s", trigger.DeliveryID, trigger.Event, issue.IssueCreate.Issue.Identifier)

	// Stopping the subscription removes the webhook from Linear.
	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
	s.service.CleanupDue(ctx)
	var webhooks struct {
		Webhooks struct {
			Nodes []struct {
				ID string `json:"id"`
			} `json:"nodes"`
		} `json:"webhooks"`
	}
	linear(`query { webhooks(first: 100) { nodes { id } } }`, nil, &webhooks)
	for _, webhook := range webhooks.Webhooks.Nodes {
		if webhook.ID == stored.RegistrationID {
			t.Fatalf("the webhook %s is still at Linear", webhook.ID)
		}
	}
}

func TestLiveMicrosoftGraphSubscription(t *testing.T) {
	token := liveValue(t, "INTEGRATIONS_LIVE_MICROSOFT_TOKEN", "a Microsoft Graph delegated access token with Mail.ReadWrite")
	s := liveStack(t)
	ctx := context.Background()
	s.connections.issues(token, bearerPlacement)
	s.connections.add(domain.Connection{
		ID: "conn_live_microsoft", AppID: "app_live_microsoft", Provider: "microsoft", CredentialType: "oauth2", Scope: "member", MemberID: "user-1",
		ProviderAccount: domain.ProviderAccount{ID: "live", Name: "Microsoft"}, Status: "active",
	})
	graph := func(method, path string, body, target any) int {
		status, payload := callProvider(t, method, "https://graph.microsoft.com/v1.0"+path, http.Header{"Authorization": {"Bearer " + token}}, body, target)
		if status >= 500 {
			t.Fatalf("Graph %s %s = %d %s", method, path, status, payload)
		}
		return status
	}

	// A member subscribes to new mail. Graph reaches the public URL to
	// validate it before it creates the subscription.
	created := s.subscribe("conn_live_microsoft", map[string]string{"resource": "me/messages"}, []string{"created"})
	stored := s.stored(created.ID)
	t.Cleanup(func() {
		if registration := s.stored(created.ID).RegistrationID; registration != "" {
			graph(http.MethodDelete, "/subscriptions/"+registration, nil, nil)
		}
	})
	t.Logf("Graph subscription %s expires at %v", stored.RegistrationID, created.ExpiresAt)
	if s.deliveries.Load() == 0 {
		t.Fatal("Graph created the subscription without validating the notification URL")
	}

	// A draft message is created, which Graph notifies about.
	subject := fmt.Sprintf("Oryxa integrations live test %d", time.Now().UnixNano())
	var draft struct {
		ID string `json:"id"`
	}
	if status := graph(http.MethodPost, "/me/messages", map[string]any{
		"subject": subject, "body": map[string]string{"contentType": "Text", "content": "This draft is created and deleted by a live test."},
	}, &draft); status != http.StatusCreated || draft.ID == "" {
		t.Fatalf("POST a draft message = %d", status)
	}
	t.Cleanup(func() { graph(http.MethodDelete, "/me/messages/"+draft.ID, nil, nil) })

	trigger := s.waitForTrigger(3*time.Minute, "Graph's message notification", func(trigger integrationsv1.TriggerReceived) bool {
		return trigger.Event == "microsoft.message.created" && trigger.Resource.ID == draft.ID
	})
	if trigger.SubscriptionID != created.ID || strings.Contains(string(trigger.Payload), "clientState") {
		t.Fatalf("trigger = %+v", trigger)
	}
	t.Logf("Graph published %s for message %s", trigger.Event, trigger.Resource.ID)

	// Renewal extends the subscription at Graph.
	if err := s.store.ScheduleRenewal(ctx, created.ID, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	s.service.RenewDue(ctx)
	renewed := s.stored(created.ID)
	if renewed.Status != domain.SubscriptionActive || renewed.ExpiresAt == nil || !renewed.ExpiresAt.After(*stored.ExpiresAt) {
		t.Fatalf("renewed subscription = %+v, want a later expiry than %v", renewed, stored.ExpiresAt)
	}
	t.Logf("Graph subscription renewed until %v", renewed.ExpiresAt)

	// Stopping the subscription removes it from Graph.
	if status, body := s.do(http.MethodDelete, "/subscriptions/"+created.ID, memberToken, nil); status != http.StatusNoContent {
		t.Fatalf("DELETE subscription = %d %s", status, body)
	}
	s.service.CleanupDue(ctx)
	if status := graph(http.MethodGet, "/subscriptions/"+stored.RegistrationID, nil, nil); status != http.StatusNotFound {
		t.Fatalf("the subscription is still at Graph: GET = %d", status)
	}
}
