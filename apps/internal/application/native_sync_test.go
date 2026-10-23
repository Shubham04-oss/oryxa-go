package application

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/nativeclient"
)

// fakeIntegrations is the integrations service's internal API: the
// connections it lists, the tools it offers by member, and what was asked.
type fakeIntegrations struct {
	mu          sync.Mutex
	connections []integrationsv1.Connection
	listStatus  int
	tools       map[string][]nativeclient.Tool // by member
	removed     []string
	requests    []string
}

func (fake *fakeIntegrations) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.requests = append(fake.requests, r.Method+" "+r.URL.RequestURI())
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/v1/connections":
		if fake.listStatus != 0 {
			w.WriteHeader(fake.listStatus)
			_, _ = w.Write([]byte(`{"error":{"code":"internal"}}`))
			return
		}
		list := []integrationsv1.Connection{}
		for _, connection := range fake.connections {
			if connection.OrganizationID == r.URL.Query().Get("org") {
				list = append(list, connection)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"connections": list})
	case r.Method == http.MethodGet && r.URL.Path == "/v1/tools":
		tools := []nativeclient.Tool{}
		for _, tool := range fake.tools[r.URL.Query().Get("member")] {
			if app := r.URL.Query().Get("app"); app == "" || tool.App == app {
				tools = append(tools, tool)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tools": tools})
	case r.Method == http.MethodDelete:
		fake.removed = append(fake.removed, r.PathValue("id"))
		_, _ = w.Write([]byte(`{"provider_revoked":true}`))
	default:
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":{"code":"not_found"}}`))
	}
}

func (fake *fakeIntegrations) sent() []string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]string(nil), fake.requests...)
}

func integrationsHarness(t *testing.T) (harness, *fakeIntegrations) {
	t.Helper()
	fake := &fakeIntegrations{tools: map[string][]nativeclient.Tool{}}
	mux := http.NewServeMux()
	mux.Handle("/v1/connections", fake)
	mux.Handle("DELETE /v1/connections/{id}", fake)
	mux.Handle("/v1/tools", fake)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	client, err := nativeclient.New(nativeclient.Config{BaseURL: server.URL, Signer: nativeSigner{}})
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t)
	h.service.native = client
	return h, fake
}

func listedConnection(id, member, status string, at time.Time) integrationsv1.Connection {
	return integrationsv1.Connection{
		ID: id, OrganizationID: "org_1", MemberID: member, Level: integrationsv1.LevelPersonal,
		App: "github", Mode: "oryxa", Status: status, UpdatedAt: at,
	}
}

func nativeRowOf(t *testing.T, h harness, nativeID string) (domain.Connection, bool) {
	t.Helper()
	for _, connection := range h.store.connections {
		if connection.NativeConnectionID == nativeID {
			return connection, true
		}
	}
	return domain.Connection{}, false
}

func TestReconcileNativeRepairsDriftedRows(t *testing.T) {
	t.Parallel()
	h, fake := integrationsHarness(t)
	ctx := context.Background()
	// icn_stale was made, then its needs_reauth and recovery were missed;
	// icn_gone was revoked and its removed event missed.
	for _, event := range []integrationsv1.Connection{
		listedConnection("icn_stale", "user-1", integrationsv1.StatusOK, testClock),
		listedConnection("icn_gone", "user-2", integrationsv1.StatusOK, testClock),
	} {
		if err := h.service.NativeConnectionChanged(ctx, event); err != nil {
			t.Fatal(err)
		}
	}
	slack := listedConnection("icn_missed", "user-3", integrationsv1.StatusNeedsReauth, testClock.Add(time.Minute))
	slack.App = "slack"
	org := listedConnection("icn_org", "", integrationsv1.StatusOK, testClock)
	org.Level = integrationsv1.LevelOrg
	fake.connections = []integrationsv1.Connection{
		listedConnection("icn_stale", "user-1", integrationsv1.StatusNeedsReauth, testClock.Add(2*time.Minute)),
		slack, // its created event never arrived
		org,
	}

	if err := h.service.ReconcileNative(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if row, _ := nativeRowOf(t, h, "icn_stale"); row.Status != domain.ConnectionExpired {
		t.Errorf("icn_stale = %s, want expired", row.Status)
	}
	if row, ok := nativeRowOf(t, h, "icn_missed"); !ok || row.Status != domain.ConnectionExpired || row.MemberID != "user-3" || row.Toolkit != "slack" {
		t.Errorf("icn_missed = %+v, %v", row, ok)
	}
	if row, _ := nativeRowOf(t, h, "icn_gone"); row.Status != domain.ConnectionRemoved {
		t.Errorf("icn_gone = %s, want removed", row.Status)
	}
	if _, ok := nativeRowOf(t, h, "icn_org"); ok {
		t.Error("an org-level connection got a row")
	}
	if len(fake.removed) != 0 {
		t.Errorf("reconciling revoked %v", fake.removed)
	}

	// A second pass changes nothing, and an older status in the list does
	// not undo a newer event.
	if err := h.service.NativeConnectionChanged(ctx, listedConnection("icn_stale", "user-1", integrationsv1.StatusOK, testClock.Add(3*time.Minute))); err != nil {
		t.Fatal(err)
	}
	before := len(h.store.connections)
	if err := h.service.ReconcileNative(ctx, "org_1"); err != nil {
		t.Fatal(err)
	}
	if row, _ := nativeRowOf(t, h, "icn_stale"); row.Status != domain.ConnectionActive || len(h.store.connections) != before {
		t.Errorf("icn_stale = %s, rows %d → %d", row.Status, before, len(h.store.connections))
	}
}

func TestReconcileNativeRetiresNothingWhenTheListFails(t *testing.T) {
	t.Parallel()
	h, fake := integrationsHarness(t)
	ctx := context.Background()
	if err := h.service.NativeConnectionChanged(ctx, listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock)); err != nil {
		t.Fatal(err)
	}
	fake.listStatus = http.StatusInternalServerError
	if err := h.service.ReconcileNative(ctx, "org_1"); err == nil {
		t.Fatal("a failed list reconciled")
	}
	if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionActive {
		t.Fatalf("icn_1 = %s, want still active", row.Status)
	}
}

func TestMaintenanceReconcilesOrganizationsWithNativeRowsHourly(t *testing.T) {
	t.Parallel()
	h, fake := integrationsHarness(t)
	ctx := context.Background()
	if err := h.service.NativeConnectionChanged(ctx, listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock)); err != nil {
		t.Fatal(err)
	}
	now := testClock
	h.service.now = func() time.Time { return now }
	lists := func() int {
		count := 0
		for _, request := range fake.sent() {
			if request == "GET /v1/connections?org=org_1" {
				count++
			}
		}
		return count
	}
	h.service.maintain(ctx)
	now = now.Add(15 * time.Minute)
	h.service.maintain(ctx)
	if lists() != 1 {
		t.Fatalf("lists after two sweeps 15 minutes apart = %d, want 1", lists())
	}
	if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionRemoved {
		t.Fatalf("icn_1 = %s, want removed: the service no longer lists it", row.Status)
	}
	now = now.Add(time.Hour)
	h.service.maintain(ctx)
	if lists() != 2 {
		t.Fatalf("lists an hour later = %d, want 2", lists())
	}
}

// A Composio connection to the same app turned active while the member was
// signing in natively: the one-current-connection index refuses the native
// row. The newer sign-in is kept and the other revoked at its broker, and a
// second delivery of the event changes nothing.
func TestTheNativeSignInRaceKeepsTheNewerConnection(t *testing.T) {
	t.Parallel()
	t.Run("native is newer", func(t *testing.T) {
		t.Parallel()
		h, fake := integrationsHarness(t)
		h.activeConnection("apc_composio", "user-1", "github", "ca_1") // connected at testClock
		event := listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock.Add(time.Second))
		for range 2 {
			if err := h.service.NativeConnectionChanged(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if composio := h.store.connections["apc_composio"]; composio.Status != domain.ConnectionRemoved || composio.StatusReason != RemovedReplaced {
			t.Fatalf("composio row = %s (%s), want removed as replaced", composio.Status, composio.StatusReason)
		}
		if len(h.composio.deleted) != 1 || h.composio.deleted[0] != "ca_1" || !h.composio.revoked[0] {
			t.Fatalf("composio deletes = %v revoked %v", h.composio.deleted, h.composio.revoked)
		}
		if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionActive {
			t.Fatalf("native row = %s, want active", row.Status)
		}
		if len(fake.removed) != 0 {
			t.Fatalf("the native connection was revoked: %v", fake.removed)
		}
	})
	t.Run("composio is newer", func(t *testing.T) {
		t.Parallel()
		h, fake := integrationsHarness(t)
		h.activeConnection("apc_composio", "user-1", "github", "ca_1")
		event := listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock.Add(-time.Second))
		for range 2 {
			if err := h.service.NativeConnectionChanged(context.Background(), event); err != nil {
				t.Fatal(err)
			}
		}
		if composio := h.store.connections["apc_composio"]; composio.Status != domain.ConnectionActive || len(h.composio.deleted) != 0 {
			t.Fatalf("composio row = %s, deletes %v: the newer connection was touched", composio.Status, h.composio.deleted)
		}
		if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionRemoved || row.StatusReason != RemovedReplaced {
			t.Fatalf("native row = %s (%s), want removed as replaced", row.Status, row.StatusReason)
		}
		if len(fake.removed) != 1 || fake.removed[0] != "icn_1" {
			t.Fatalf("integrations revoked %v, want icn_1 once", fake.removed)
		}
	})
	t.Run("a pending connection loses", func(t *testing.T) {
		t.Parallel()
		h, _ := integrationsHarness(t)
		pending := h.activeConnection("apc_pending", "user-1", "github", "")
		pending.Status, pending.ConnectedAt, pending.CreatedAt = domain.ConnectionPending, nil, testClock.Add(time.Hour)
		h.store.connections[pending.ID] = pending
		if err := h.service.NativeConnectionChanged(context.Background(), listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock)); err != nil {
			t.Fatal(err)
		}
		if h.store.connections["apc_pending"].Status != domain.ConnectionRemoved {
			t.Fatalf("pending row = %s, want removed", h.store.connections["apc_pending"].Status)
		}
		if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionActive {
			t.Fatalf("native row = %s, want active", row.Status)
		}
	})
	t.Run("a broker that fails is tried again", func(t *testing.T) {
		t.Parallel()
		h, _ := integrationsHarness(t)
		h.activeConnection("apc_composio", "user-1", "github", "ca_1")
		h.composio.deleteErr = errBoom
		event := listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock.Add(time.Second))
		if err := h.service.NativeConnectionChanged(context.Background(), event); err == nil {
			t.Fatal("a revoke that failed was not retried")
		}
		h.composio.deleteErr = nil
		if err := h.service.NativeConnectionChanged(context.Background(), event); err != nil {
			t.Fatal(err)
		}
		if row, _ := nativeRowOf(t, h, "icn_1"); row.Status != domain.ConnectionActive {
			t.Fatalf("native row = %s, want active on the retry", row.Status)
		}
	})
}

func TestSearchToolsListsANativeConnectionsToolsForItsMemberOnly(t *testing.T) {
	t.Parallel()
	h, fake := integrationsHarness(t)
	ctx := context.Background()
	fake.tools["user-1"] = []nativeclient.Tool{
		{App: "github", Name: "github.list_issues", Description: "Lists a repository's issues", Effect: "read", InputSchema: json.RawMessage(`{"type":"object"}`)},
		{App: "github", Name: "github.create_issue", Description: "Opens an issue", Effect: "write"},
		{App: "github", Name: "github.delete_repo", Description: "Deletes a repository", Effect: "delete"},
		{App: "slack", Name: "slack.post", Description: "another app's", Effect: "write"},
	}
	fake.tools["user-2"] = fake.tools["user-1"]
	if err := h.service.NativeConnectionChanged(ctx, listedConnection("icn_1", "user-1", integrationsv1.StatusOK, testClock)); err != nil {
		t.Fatal(err)
	}

	views, err := h.service.SearchTools(ctx, memberToken, "org_1", "github", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 {
		t.Fatalf("views = %+v", views)
	}
	want := map[string][2]bool{ // read-only, destructive
		"GITHUB_LIST_ISSUES": {true, false}, "GITHUB_CREATE_ISSUE": {false, false}, "GITHUB_DELETE_REPO": {false, true},
	}
	for _, view := range views {
		marks, ok := want[view.Tool]
		if !ok || view.Toolkit != "github" || view.ReadOnly != marks[0] || view.Destructive != marks[1] {
			t.Errorf("view = %+v", view)
		}
	}
	if views[0].Tool != "GITHUB_LIST_ISSUES" || string(views[0].InputSchema) != `{"type":"object"}` {
		t.Errorf("first view = %+v", views[0])
	}
	if h.composio.searches != 0 {
		t.Fatal("Composio was asked for a native app's tools")
	}
	if sent := fake.sent(); len(sent) != 1 || sent[0] != "GET /v1/tools?app=github&member=user-1&org=org_1" {
		t.Fatalf("integrations was asked %v", sent)
	}

	views, err = h.service.SearchTools(ctx, memberToken, "org_1", "github", "issue", 0)
	if err != nil || len(views) != 2 {
		t.Fatalf("a query = %+v, %v", views, err)
	}
	// A listed slug is one RunTool routes to the member's native connection.
	if route, ok, err := h.service.nativeRoute(ctx, "org_1", "user-1", views[0].Tool); err != nil || !ok || !route.native {
		t.Fatalf("route of %s = %+v, %v, %v", views[0].Tool, route, ok, err)
	}

	// user-2 has no native connection: integrations is not asked for them,
	// and nothing of user-1's is offered.
	asked := len(fake.sent())
	views, err = h.service.SearchTools(ctx, otherToken, "org_1", "github", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if _, native := want[view.Tool]; native {
			t.Fatalf("user-2 was offered %s", view.Tool)
		}
	}
	if len(fake.sent()) != asked {
		t.Fatalf("integrations was asked for user-2: %v", fake.sent()[asked:])
	}

	// An expired native connection offers nothing natively either.
	if err := h.service.NativeConnectionChanged(ctx, listedConnection("icn_1", "user-1", integrationsv1.StatusNeedsReauth, testClock.Add(time.Minute))); err != nil {
		t.Fatal(err)
	}
	if _, err := h.service.SearchTools(ctx, memberToken, "org_1", "github", "", 0); err != nil {
		t.Fatal(err)
	}
	if len(fake.sent()) != asked {
		t.Fatalf("integrations was asked for an expired connection's tools: %v", fake.sent()[asked:])
	}
}

func TestSearchToolsSaysWhenTheIntegrationsServiceFails(t *testing.T) {
	t.Parallel()
	h, _ := nativeToolHarness(t, http.StatusInternalServerError, `{"error":{"code":"internal"}}`)
	if _, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "", 0); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}
