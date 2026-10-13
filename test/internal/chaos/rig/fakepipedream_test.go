package rig

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
)

// TestFakePipedreamServesTheRealClient drives the fake with internal/pipedream,
// so the two cannot drift apart without a test failing.
func TestFakePipedreamServesTheRealClient(t *testing.T) {
	fake := NewFakePipedream()
	server := httptest.NewServer(fake)
	defer server.Close()
	client, err := pipedream.New(pipedream.Config{
		ProjectID: "proj_rig", Environment: pipedream.EnvironmentDevelopment,
		ClientID: "id", ClientSecret: "secret", BaseURL: server.URL + "/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	if token, err := client.CreateConnectToken(ctx, "user-1", "", "", 0); err != nil || !strings.HasPrefix(token.Token, "ctok_") {
		t.Fatalf("CreateConnectToken = %+v, %v", token, err)
	}
	if accounts, err := client.ListAccounts(ctx, "user-1", ""); err != nil || len(accounts) != 0 {
		t.Fatalf("ListAccounts(nobody yet) = %v, %v", accounts, err)
	}
	accountID := fake.Connect("user-1", "gmail")
	accounts, err := client.ListAccounts(ctx, "user-1", "gmail")
	if err != nil || len(accounts) != 1 || accounts[0].ID != accountID || !accounts[0].Healthy {
		t.Fatalf("ListAccounts = %+v, %v", accounts, err)
	}

	actions, err := client.ListActions(ctx, pipedream.ComponentQuery{App: "gmail"})
	if err != nil || len(actions.Items) != 2 || !actions.Items[0].ReadOnly() || actions.Items[1].ReadOnly() {
		t.Fatalf("ListActions = %+v, %v", actions, err)
	}
	props := map[string]any{"gmail": map[string]any{"authProvisionId": accountID}}
	result, err := client.RunAction(ctx, "gmail-find-email", "user-1", props)
	if err != nil || !strings.Contains(string(result.Ret), accountID) {
		t.Fatalf("RunAction = %+v, %v", result, err)
	}
	if _, err := client.RunAction(ctx, "gmail-find-email", "user-2", props); !errors.Is(err, pipedream.ErrApp) {
		t.Fatalf("RunAction(someone else's account) = %v, want ErrApp", err)
	}

	// A trigger's events arrive signed with the key the deploy answered.
	delivered := make(chan error, 1)
	var signingKey string
	webhook := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, _ := io.ReadAll(request.Body)
		delivered <- pipedream.VerifyWebhook(signingKey, request.Header, body, time.Now())
	}))
	defer webhook.Close()
	trigger, err := client.DeployTrigger(ctx, "gmail-new-email-received", "user-1", props, webhook.URL, time.Minute)
	if err != nil || trigger.SigningKey == "" {
		t.Fatalf("DeployTrigger = %+v, %v", trigger, err)
	}
	signingKey = trigger.SigningKey
	if status, err := fake.Fire(trigger.ID, map[string]any{"subject": "hello"}); err != nil || status != 200 {
		t.Fatalf("Fire = %d, %v", status, err)
	}
	if err := <-delivered; err != nil {
		t.Fatalf("the delivery did not verify: %v", err)
	}

	request, _ := http.NewRequest(http.MethodGet, "/gmail/v1/users/me/profile", nil)
	request.Header.Set("X-Trace", "t1")
	request.Header.Set("Authorization", "Bearer leaked")
	response, err := client.Proxy(ctx, "user-1", accountID, request)
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("Proxy = %v, %v", response, err)
	}
	var echo struct {
		URL     string            `json:"url"`
		Headers map[string]string `json:"headers"`
	}
	_ = json.NewDecoder(response.Body).Decode(&echo)
	if echo.URL != "/gmail/v1/users/me/profile" || echo.Headers["x-trace"] != "t1" || echo.Headers["authorization"] != "" {
		t.Fatalf("the proxy echoed %+v", echo)
	}

	// A dead account fails at the app, through actions and the proxy alike.
	fake.Kill(accountID)
	var failure *pipedream.Error
	if _, err := client.RunAction(ctx, "gmail-find-email", "user-1", props); !errors.As(err, &failure) || failure.UpstreamStatus != 401 {
		t.Fatalf("RunAction(dead) = %v", err)
	}
	if response, err := client.Proxy(ctx, "user-1", accountID, request); err != nil || response.StatusCode != 401 {
		t.Fatalf("Proxy(dead) = %v, %v", response, err)
	}

	fake.SetMode("429")
	if _, err := client.ListAccounts(ctx, "user-1", ""); !errors.Is(err, pipedream.ErrRateLimited) {
		t.Fatalf("ListAccounts(429) = %v", err)
	}
	fake.SetMode("500")
	if _, err := client.ListApps(ctx, pipedream.AppQuery{}); !errors.Is(err, pipedream.ErrUnavailable) {
		t.Fatalf("ListApps(500) = %v", err)
	}
	fake.SetMode("")

	if err := client.DeleteDeployedTrigger(ctx, trigger.ID, "user-1"); err != nil {
		t.Fatal(err)
	}
	if err := client.DeleteUser(ctx, "user-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := client.GetAccount(ctx, accountID); !errors.Is(err, pipedream.ErrNotFound) {
		t.Fatalf("GetAccount(after DeleteUser) = %v", err)
	}
}
