package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

type nativeSigner struct{}

func (nativeSigner) Token(context.Context, string, string) (string, error) {
	return "service-token", nil
}

// nativeHarness routes github to a fake integrations service, which answers
// every connect with answer and records what it was sent.
func nativeHarness(t *testing.T, status int, answer string) (harness, *[]nativeclient.ConnectRequest) {
	t.Helper()
	var received []nativeclient.ConnectRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nativeclient.ConnectRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		received = append(received, request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	client, err := nativeclient.New(nativeclient.Config{BaseURL: server.URL, Signer: nativeSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.service.native, h.service.nativeToolkits = client, []string{"github"}
	return h, &received
}

func TestANativeToolkitConnectsThroughTheIntegrationsService(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/login/oauth/authorize?state=s"}`)

	if broker, err := h.service.BrokerFor(context.Background(), "github"); err != nil || broker != domain.BrokerNative {
		t.Fatalf("BrokerFor() = %q, %v", broker, err)
	}
	connection, redirect, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{BrowserNonce: "nonce-1", Toolkit: "github", ReturnTo: "https://app.oryxa.test/settings"})
	if err != nil || redirect != "https://github.com/login/oauth/authorize?state=s" || connection.ID != "" {
		t.Fatalf("Connect() = %+v, %q, %v", connection, redirect, err)
	}
	want := nativeclient.ConnectRequest{Org: "org_1", Member: "user-1", App: "github", Level: "personal", ReturnTo: "https://app.oryxa.test/settings", BrowserHash: hashState("nonce-1")}
	if len(*received) != 1 || (*received)[0] != want {
		t.Fatalf("integrations received %+v, want %+v", *received, want)
	}
	if len(h.composio.links) != 0 || len(h.store.connections) != 0 {
		t.Fatal("a native connect reached Composio or recorded a connection")
	}
}

func TestANativeConnectKeepsEveryConnectCheck(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/x"}`)
	if _, _, err := h.service.Connect(context.Background(), agentToken, "org_1", ConnectInput{Toolkit: "github"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an agent's connect = %v, want forbidden", err)
	}
	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1",
		ConnectInput{Toolkit: "github", ReturnTo: "https://evil.example/"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a return page off the allowed origins = %v, want invalid", err)
	}
	if len(*received) != 0 {
		t.Fatalf("integrations was called for refused connects: %+v", *received)
	}
}

func TestANativeConnectSaysWhyTheServiceRefused(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status  int
		body    string
		want    error
		wantNot error
	}{
		"not set up":        {http.StatusConflict, `{"error":{"code":"not_set_up"}}`, domain.ErrNotConnectable, nil},
		"a broken org app":  {http.StatusConflict, `{"error":{"code":"app_broken"}}`, domain.ErrNotConnectable, nil},
		"no such connector": {http.StatusNotFound, `{"error":{"code":"not_found"}}`, domain.ErrNotConnectable, nil},
		"the service fails": {http.StatusInternalServerError, `{"error":{"code":"internal"}}`, domain.ErrUnavailable, nil},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, _ := nativeHarness(t, testCase.status, testCase.body)
			if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{BrowserNonce: "nonce-1", Toolkit: "github"}); !errors.Is(err, testCase.want) {
				t.Fatalf("Connect() error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestOnlyNamedAndUnpinnedToolkitsGoNative(t *testing.T) {
	t.Parallel()
	h, received := nativeHarness(t, http.StatusOK, `{"url":"https://github.com/x"}`)
	if broker, err := h.service.BrokerFor(context.Background(), "linear"); err != nil || broker != domain.BrokerComposio {
		t.Fatalf("an unnamed toolkit = %q, %v, want composio", broker, err)
	}
	h.service.toolkitBrokers = map[string]string{"github": domain.BrokerComposio}
	h.composio.toolkits["github"] = composio.Toolkit{
		Slug: "github", AuthSchemes: []string{"OAUTH2"}, ComposioManagedAuthSchemes: []string{"OAUTH2"},
	}
	if broker, err := h.service.BrokerFor(context.Background(), "github"); err != nil || broker != domain.BrokerComposio {
		t.Fatalf("a pinned toolkit = %q, %v, want composio", broker, err)
	}
	if _, _, err := h.service.Connect(context.Background(), memberToken, "org_1", ConnectInput{BrowserNonce: "nonce-1", Toolkit: "github"}); err != nil {
		t.Fatalf("a pinned toolkit's connect = %v", err)
	}
	if len(*received) != 0 {
		t.Fatalf("integrations was called for a pinned toolkit: %+v", *received)
	}
}

func TestNewNeedsTheServiceForNativeToolkits(t *testing.T) {
	t.Parallel()
	if _, err := checkNativeToolkits([]string{"github"}, nil); err == nil {
		t.Error("native toolkits without a service were accepted")
	}
	if _, err := checkNativeToolkits([]string{"NOT A SLUG"}, nativeStub{}); !errors.Is(err, domain.ErrInvalid) {
		t.Errorf("a bad slug = %v, want invalid", err)
	}
	if got, err := checkNativeToolkits(nil, nil); err != nil || len(got) != 0 {
		t.Errorf("none = %v, %v", got, err)
	}
}

type nativeStub struct{}

func (nativeStub) Connect(context.Context, nativeclient.ConnectRequest) (string, error) {
	return "", nil
}

func (nativeStub) CallTool(context.Context, nativeclient.CallRequest) (nativeclient.CallResult, error) {
	return nativeclient.CallResult{}, nil
}

func (nativeStub) Remove(context.Context, string, string) error { return nil }

func (nativeStub) ListConnections(context.Context, string, string) ([]integrationsv1.Connection, error) {
	return []integrationsv1.Connection{}, nil
}

func (nativeStub) ListTools(context.Context, string, string, string) ([]nativeclient.Tool, error) {
	return nil, nil
}

func nativeEvent(status string, at time.Time) integrationsv1.Connection {
	return integrationsv1.Connection{
		ID: "icn_1", OrganizationID: "org_1", MemberID: "user-1", Level: integrationsv1.LevelPersonal,
		App: "github", Mode: "oryxa", Status: status, UpdatedAt: at,
	}
}

func onlyConnection(t *testing.T, h harness) domain.Connection {
	t.Helper()
	if len(h.store.connections) != 1 {
		t.Fatalf("connections = %d, want 1", len(h.store.connections))
	}
	for _, connection := range h.store.connections {
		return connection
	}
	return domain.Connection{}
}

func TestNativeConnectionEventsCreateUpdateAndRetireTheRow(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	ctx := context.Background()
	if err := h.service.NativeConnectionChanged(ctx, nativeEvent(integrationsv1.StatusOK, testClock)); err != nil {
		t.Fatal(err)
	}
	row := onlyConnection(t, h)
	if row.Broker != domain.BrokerNative || row.NativeConnectionID != "icn_1" || row.Status != domain.ConnectionActive || row.Toolkit != "github" {
		t.Fatalf("row = %+v", row)
	}
	if err := h.service.NativeConnectionChanged(ctx, nativeEvent(integrationsv1.StatusNeedsReauth, testClock.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if row = onlyConnection(t, h); row.Status != domain.ConnectionExpired {
		t.Fatalf("status after needs_reauth = %s", row.Status)
	}
	if err := h.service.NativeConnectionRemoved(ctx, nativeEvent(integrationsv1.StatusRevoked, testClock.Add(2*time.Minute))); err != nil {
		t.Fatal(err)
	}
	if row = onlyConnection(t, h); row.Status != domain.ConnectionRemoved {
		t.Fatalf("status after removed = %s", row.Status)
	}
}

func TestNativeConnectionEventsThatNameNoRowAreDropped(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	org := nativeEvent(integrationsv1.StatusOK, testClock)
	org.Level, org.MemberID = integrationsv1.LevelOrg, ""
	bad := nativeEvent(integrationsv1.StatusOK, testClock)
	bad.App = "NOT A SLUG"
	for _, event := range []integrationsv1.Connection{org, bad} {
		if err := h.service.NativeConnectionChanged(context.Background(), event); err != nil || len(h.store.connections) != 0 {
			t.Fatalf("event %+v: err = %v, rows = %d", event, err, len(h.store.connections))
		}
	}
}

// nativeToolHarness has github connected natively for user-1, and a fake
// integrations service that answers every tool call with status and answer.
func nativeToolHarness(t *testing.T, status int, answer string) (harness, *[]nativeclient.CallRequest) {
	t.Helper()
	var received []nativeclient.CallRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request nativeclient.CallRequest
		_ = json.NewDecoder(r.Body).Decode(&request)
		received = append(received, request)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	client, err := nativeclient.New(nativeclient.Config{BaseURL: server.URL, Signer: nativeSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.service.native = client
	if err := h.service.NativeConnectionChanged(context.Background(), nativeEvent(integrationsv1.StatusOK, testClock)); err != nil {
		t.Fatal(err)
	}
	return h, &received
}

func TestRunToolRunsANativeToolThroughTheIntegrationsService(t *testing.T) {
	t.Parallel()
	h, received := nativeToolHarness(t, http.StatusOK, `{"output":{"issues":[]}}`)
	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: "github_list_issues", Arguments: json.RawMessage(`{"repo":"oryxa"}`),
	})
	if err != nil || run.Action.Status != domain.ActionSucceeded || string(run.Data) != `{"issues":[]}` {
		t.Fatalf("run = %+v, %v", run, err)
	}
	want := nativeclient.CallRequest{
		Org: "org_1", Member: "user-1", Agent: "member", Role: "member", Connection: "icn_1",
		Tool: "github.list_issues", Input: json.RawMessage(`{"repo":"oryxa"}`), Confirmed: true,
	}
	if len(*received) != 1 || (*received)[0].Tool != want.Tool || (*received)[0].Connection != want.Connection ||
		(*received)[0].Member != want.Member || !(*received)[0].Confirmed || string((*received)[0].Input) != string(want.Input) {
		t.Fatalf("integrations received %+v, want %+v", *received, want)
	}
	if len(h.composio.executed) != 0 {
		t.Fatal("a native tool reached Composio")
	}
}

func TestRunToolLeavesAnUnconfirmedCallerUnconfirmed(t *testing.T) {
	t.Parallel()
	h, received := nativeToolHarness(t, http.StatusConflict, `{"error":{"code":"confirmation_required"}}`)
	_, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{Tool: "GITHUB_CREATE_ISSUE", OnlyReads: true})
	if !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("error = %v, want not read-only", err)
	}
	if len(*received) != 1 || (*received)[0].Confirmed {
		t.Fatalf("received = %+v, want one unconfirmed call", *received)
	}
}

func TestRunToolMapsTheIntegrationsServiceErrors(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		status int
		body   string
		want   error
	}{
		"no such tool":    {http.StatusNotFound, `{"error":{"code":"not_found"}}`, domain.ErrNotFound},
		"needs reauth":    {http.StatusConflict, `{"error":{"code":"needs_reauth"}}`, domain.ErrNeedsReconnect},
		"unavailable":     {http.StatusConflict, `{"error":{"code":"connection_unavailable"}}`, domain.ErrNotConnected},
		"invalid":         {http.StatusBadRequest, `{"error":{"code":"invalid_request"}}`, domain.ErrInvalid},
		"output too big":  {http.StatusBadGateway, `{"error":{"code":"output_too_large"}}`, domain.ErrUnavailable},
		"service is down": {http.StatusInternalServerError, `{"error":{"code":"internal"}}`, domain.ErrUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, _ := nativeToolHarness(t, testCase.status, testCase.body)
			_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: "GITHUB_LIST_ISSUES"})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestRunToolAnswersAnAppsRefusalAsAFailedRun(t *testing.T) {
	t.Parallel()
	h, _ := nativeToolHarness(t, http.StatusOK, `{"tool_error":{"code":"rate_limited","message":"slow down"}}`)
	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: "GITHUB_LIST_ISSUES"})
	if err != nil || run.Action.Status != domain.ActionFailed || run.Action.Error != "slow down" {
		t.Fatalf("run = %+v, %v", run, err)
	}
}

func TestANativeSlugIsOnlyTheMembersOwn(t *testing.T) {
	t.Parallel()
	h, received := nativeToolHarness(t, http.StatusOK, `{"output":{}}`)
	// user-2 has no native github connection, so the slug is read as Composio's.
	_, err := h.service.RunTool(context.Background(), otherToken, "org_1", RunToolInput{Tool: "GITHUB_LIST_ISSUES"})
	if err == nil || len(*received) != 0 {
		t.Fatalf("error = %v, integrations calls = %d", err, len(*received))
	}
}

// Removing a native connection revokes it at the integrations service first;
// the row is retired only once the service has, or no longer knows it.
func TestRemovingANativeConnectionRevokesItAtTheIntegrationsService(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		status      int
		answer      string
		wantErr     error
		wantRetired bool
	}{
		"revoked":         {http.StatusOK, `{"provider_revoked":true}`, nil, true},
		"already gone":    {http.StatusNotFound, `{"error":{"code":"not_found"}}`, nil, true},
		"service failing": {http.StatusInternalServerError, `{"error":{"code":"internal"}}`, domain.ErrUnavailable, false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			var method, path string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				method, path = r.Method, r.URL.RequestURI()
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.answer))
			}))
			t.Cleanup(server.Close)
			client, err := nativeclient.New(nativeclient.Config{BaseURL: server.URL, Signer: nativeSigner{}})
			if err != nil {
				t.Fatal(err)
			}
			h := newHarness(t)
			h.service.native = client
			if err := h.service.NativeConnectionChanged(context.Background(), nativeEvent(integrationsv1.StatusOK, testClock)); err != nil {
				t.Fatal(err)
			}
			var id string
			for id = range h.store.connections {
			}
			err = h.service.RemoveConnection(context.Background(), memberToken, "org_1", id)
			if !errors.Is(err, c.wantErr) && !(c.wantErr == nil && err == nil) {
				t.Fatalf("remove = %v, want %v", err, c.wantErr)
			}
			if method != http.MethodDelete || path != "/v1/connections/icn_1?org=org_1" {
				t.Fatalf("integrations saw %s %s", method, path)
			}
			if retired := h.store.connections[id].Status == domain.ConnectionRemoved; retired != c.wantRetired {
				t.Fatalf("retired = %v, want %v", retired, c.wantRetired)
			}
		})
	}
}
