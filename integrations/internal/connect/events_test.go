package connect

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	eventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

type outboxEvent struct {
	DetailType string
	Raw        json.RawMessage
	Connection eventsv1.Connection
}

// events are the org's outbox rows in the order they were written (the rows
// are never claimed here, so they are never updated and ctid is insertion order).
func (r *rig) events(t *testing.T) []outboxEvent {
	t.Helper()
	rows, err := r.pool.Query(context.Background(), `SELECT detail_type, detail FROM integrations.outbox
		WHERE detail->>'organization_id' = $1 ORDER BY occurred_at, ctid`, r.org)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var all []outboxEvent
	for rows.Next() {
		var e outboxEvent
		if err := rows.Scan(&e.DetailType, &e.Raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(e.Raw, &e.Connection); err != nil {
			t.Fatal(err)
		}
		all = append(all, e)
	}
	return all
}

// kinds is "created ok", "updated needs_reauth"...: what each event says.
func kinds(events []outboxEvent) []string {
	var out []string
	for _, e := range events {
		out = append(out, strings.Split(strings.TrimSuffix(e.DetailType, ".v1"), ".")[2]+" "+e.Connection.Status)
	}
	return out
}

func wantKinds(t *testing.T, events []outboxEvent, want ...string) {
	t.Helper()
	if got := kinds(events); !slices.Equal(got, want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
}

func TestSignInWritesCreatedAndNoSecret(t *testing.T) {
	r := newRig(t)
	id := r.connected(t)
	wantKinds(t, r.events(t), "created ok")
	e := r.events(t)[0]
	var updatedAt time.Time
	if err := r.pool.QueryRow(context.Background(), `SELECT updated_at FROM integrations.connections WHERE id = $1`, id).Scan(&updatedAt); err != nil {
		t.Fatal(err)
	}
	c := e.Connection
	if e.DetailType != eventsv1.ConnectionCreatedDetailType || c.ID != id || c.OrganizationID != r.org || c.MemberID != "m1" ||
		c.Level != eventsv1.LevelPersonal || c.App != "google" || c.Mode != "byo" || !c.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("event = %s %+v, row updated_at %s", e.DetailType, c, updatedAt)
	}
	// Only the contract's fields: no token, scope or account data.
	var fields map[string]any
	if err := json.Unmarshal(e.Raw, &fields); err != nil {
		t.Fatal(err)
	}
	for name := range fields {
		if !slices.Contains([]string{"connection_id", "organization_id", "member_id", "level", "app", "mode", "status", "updated_at"}, name) {
			t.Errorf("the payload carries %q", name)
		}
	}
	for _, secret := range []string{"access-1", "refresh-1", "read write", clientSecret} {
		if strings.Contains(string(e.Raw), secret) {
			t.Errorf("the payload holds %q", secret)
		}
	}
	// A renewal of a healthy connection is not a change.
	r.connected(t)
	wantKinds(t, r.events(t), "created ok")
}

func TestStatusChangesWriteUpdated(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.connected(t)

	// The app refuses the grant: needs_reauth, once. Signing in again recovers.
	r.app.grantError = "invalid_grant"
	r.Refresh(ctx, id, "access", "access-1")
	r.Refresh(ctx, id, "access", "access-1")
	r.connected(t)
	wantKinds(t, r.events(t), "created ok", "updated needs_reauth", "updated ok")

	// Transient failures change nothing until the fifth degrades the connection.
	r.app.grantError, r.app.down = "", true
	for range degradedAfter {
		r.advance(maxCooldown + time.Second)
		r.Refresh(ctx, id, "access", "access-2")
	}
	wantKinds(t, r.events(t), "created ok", "updated needs_reauth", "updated ok", "updated degraded")

	// Recovery.
	r.app.down = false
	r.advance(maxCooldown + time.Second)
	if _, err := r.Refresh(ctx, id, "access", "access-2"); err != nil {
		t.Fatal(err)
	}
	// A routine refresh of a healthy connection adds nothing.
	if _, err := r.Refresh(ctx, id, "access", "access-3"); err != nil {
		t.Fatal(err)
	}
	wantKinds(t, r.events(t), "created ok", "updated needs_reauth", "updated ok", "updated degraded", "updated ok")
}

func TestRevokeWritesRemovedOnce(t *testing.T) {
	r := newRig(t)
	id := r.connected(t)
	if _, err := r.Revoke(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	r.Revoke(context.Background(), id)
	events := r.events(t)
	wantKinds(t, events, "created ok", "removed revoked")
	if events[1].Connection.ID != id {
		t.Fatalf("removed event is for %s", events[1].Connection.ID)
	}
}

// An event is part of the change: one that rolls back leaves no event.
func TestEventRollsBackWithTheChange(t *testing.T) {
	r := newRig(t)
	id := r.connected(t)
	tx, err := r.pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := r.emit(context.Background(), tx, id, eventsv1.ConnectionUpdatedDetailType); err != nil {
		t.Fatal(err)
	}
	tx.Rollback(context.Background())
	wantKinds(t, r.events(t), "created ok")
}

func (r *rig) connectedAs(t *testing.T, member, level string) string {
	t.Helper()
	raw, err := r.Start(context.Background(), StartRequest{BrowserHash: testBrowserHash, Org: r.org, Member: member, App: "google", Level: level})
	if err != nil {
		t.Fatal(err)
	}
	authorize, _ := url.Parse(raw)
	r.app.mu.Lock()
	r.app.challenge = authorize.Query().Get("code_challenge")
	r.app.mu.Unlock()
	done, err := r.Complete(context.Background(), Callback{Cookie: testCookie, State: authorize.Query().Get("state"), Code: "code-1"})
	if err != nil {
		t.Fatal(err)
	}
	return done.ConnectionID
}

func TestConnectionsAPI(t *testing.T) {
	r := newRig(t)
	server := httpserver.New(allowAll{}, nil)
	Routes(server, r.Service, nil)
	do := func(method, target string, internal bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, target, nil)
		if internal {
			request.Header.Set(serviceauth.Header, "t")
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder
	}
	ids := func(recorder *httptest.ResponseRecorder) []string {
		var body struct {
			Connections []eventsv1.Connection `json:"connections"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, c := range body.Connections {
			out = append(out, c.ID)
		}
		// Connections made in one instant have no set order.
		slices.Sort(out)
		return out
	}
	m1, m2, shared := r.connectedAs(t, "m1", "personal"), r.connectedAs(t, "m2", "personal"), r.connectedAs(t, "admin", "org")
	sorted := func(ids ...string) []string { slices.Sort(ids); return ids }
	base := "/v1/connections?org=" + url.QueryEscape(r.org)

	if got := do("GET", base, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("without a service token: %d", got.Code)
	}
	if got := do("GET", "/v1/connections", true); got.Code != http.StatusBadRequest {
		t.Fatalf("without an org: %d", got.Code)
	}
	// Without a member: all of the org's. With one: theirs and the org-level.
	if got := ids(do("GET", base, true)); !slices.Equal(got, sorted(m1, m2, shared)) {
		t.Fatalf("all = %v", got)
	}
	if got := ids(do("GET", base+"&member=m2", true)); !slices.Equal(got, sorted(m2, shared)) {
		t.Fatalf("m2 = %v", got)
	}
	if got := ids(do("GET", "/v1/connections?org=other_org", true)); len(got) != 0 {
		t.Fatalf("another org sees %v", got)
	}

	got := do("GET", "/v1/connections/"+m1+"?org="+url.QueryEscape(r.org), true)
	var one eventsv1.Connection
	if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &one) != nil || one.ID != m1 || one.MemberID != "m1" {
		t.Fatalf("get = %d %s", got.Code, got.Body)
	}
	if strings.Contains(got.Body.String(), "access-1") || strings.Contains(got.Body.String(), "scopes") {
		t.Fatalf("the view holds more than the contract: %s", got.Body)
	}

	// Another org's id is as missing as one that does not exist; deleting it
	// revokes nothing.
	for _, target := range []string{"/v1/connections/" + m1 + "?org=other_org", "/v1/connections/cn_nope?org=" + url.QueryEscape(r.org)} {
		if got := do("GET", target, true); got.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d", target, got.Code)
		}
		if got := do("DELETE", target, true); got.Code != http.StatusNotFound {
			t.Fatalf("DELETE %s = %d", target, got.Code)
		}
	}
	if status, _ := r.status(t, m1); status != "ok" {
		t.Fatalf("a cross-org delete changed the connection to %s", status)
	}

	got = do("DELETE", "/v1/connections/"+m1+"?org="+url.QueryEscape(r.org), true)
	if got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"provider_revoked":true`) {
		t.Fatalf("delete = %d %s", got.Code, got.Body)
	}
	if status, _ := r.status(t, m1); status != "revoked" {
		t.Fatalf("status = %s", status)
	}
	var removed []string
	for _, e := range r.events(t) {
		if e.DetailType == eventsv1.ConnectionRemovedDetailType {
			removed = append(removed, e.Connection.ID)
		}
	}
	if !slices.Equal(removed, []string{m1}) {
		t.Fatalf("removed events = %v", removed)
	}
	// A removed connection leaves the list.
	if got := ids(do("GET", base, true)); !slices.Equal(got, sorted(m2, shared)) {
		t.Fatalf("after delete: %v", got)
	}
}
