package connect

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	authv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
)

// signedIn signs member in at level and answers the connection's id.
func (r *rig) signedIn(t *testing.T, member, level string) string {
	t.Helper()
	raw, err := r.Start(context.Background(), StartRequest{Org: r.org, Member: member, App: "google", Level: level})
	if err != nil {
		t.Fatal(err)
	}
	authorize, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	r.app.mu.Lock()
	r.app.challenge = authorize.Query().Get("code_challenge")
	r.app.mu.Unlock()
	done, err := r.Complete(context.Background(), Callback{State: authorize.Query().Get("state"), Code: "code-1"})
	if err != nil {
		t.Fatal(err)
	}
	return done.ConnectionID
}

// providerRevoked is what the connection records of the app's answer: nil
// while it lives.
func (r *rig) providerRevoked(t *testing.T, id string) *bool {
	t.Helper()
	var revoked *bool
	if err := r.pool.QueryRow(context.Background(), `SELECT provider_revoked FROM integrations.connections WHERE id = $1`, id).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	return revoked
}

func (r *rig) credentials(t *testing.T, id string) int {
	t.Helper()
	var n int
	if err := r.pool.QueryRow(context.Background(), `SELECT count(*) FROM integrations.credentials WHERE connection_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (r *rig) removedEvents(t *testing.T) []string {
	t.Helper()
	var ids []string
	for _, e := range r.events(t) {
		if e.DetailType == "integrations.connection.removed.v1" {
			ids = append(ids, e.Connection.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

func (r *rig) wantStatus(t *testing.T, id, want string) {
	t.Helper()
	if status, _ := r.status(t, id); status != want {
		t.Fatalf("connection %s is %s, want %s", id, status, want)
	}
}

func (r *rig) memberRemoved(member string) authv1.MemberRemoved {
	return authv1.MemberRemoved{OrganizationID: r.org, UserID: member, Reason: authv1.MemberRemovedReasonLeft, RemovedAt: r.Service.now()}
}

func sorted(ids ...string) []string {
	slices.Sort(ids)
	return ids
}

func TestMemberRemovedRevokesOnlyTheirPersonalConnectionsInThatOrg(t *testing.T) {
	r := newRig(t)
	other := newRig(t)
	ctx := context.Background()
	mine := r.signedIn(t, "m1", "personal")
	theirs := r.signedIn(t, "m2", "personal")
	orgLevel := r.signedIn(t, "m1", "org")
	elsewhere := other.signedIn(t, "m1", "personal")
	// A sign-in m1 started and has not finished.
	pending, _ := r.start(t, "m1")

	if err := r.MemberRemoved(ctx, r.memberRemoved("m1")); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(t, mine, "revoked")
	if r.credentials(t, mine) != 0 {
		t.Fatal("a revoked connection kept its tokens")
	}
	if _, err := r.Token(ctx, mine, "access"); !errors.Is(err, ErrRevoked) {
		t.Fatalf("Token after the member left = %v", err)
	}
	if revoked := r.providerRevoked(t, mine); revoked == nil || !*revoked {
		t.Fatalf("provider_revoked = %v, want true", revoked)
	}
	r.app.mu.Lock()
	atApp := slices.Clone(r.app.revoked)
	r.app.mu.Unlock()
	if len(atApp) != 1 {
		t.Fatalf("the app revoked %v, want one grant", atApp)
	}
	// Another member's, the organization's, and m1's in another organization stay.
	r.wantStatus(t, theirs, "ok")
	r.wantStatus(t, orgLevel, "ok")
	other.wantStatus(t, elsewhere, "ok")
	if r.providerRevoked(t, theirs) != nil {
		t.Fatal("a live connection records a revocation")
	}
	// The removed event is published for the revoked one only.
	if got := r.removedEvents(t); !slices.Equal(got, []string{mine}) {
		t.Fatalf("removed events = %v, want %v", got, []string{mine})
	}
	if got := other.removedEvents(t); len(got) != 0 {
		t.Fatalf("another organization's removed events = %v", got)
	}
	// The unfinished sign-in cannot finish.
	if _, err := r.Complete(ctx, Callback{State: pending, Code: "code-1"}); !errors.Is(err, ErrBadState) {
		t.Fatalf("a departed member's sign-in completed: %v", err)
	}
}

func TestOrganizationDeletedRevokesAll(t *testing.T) {
	r := newRig(t)
	other := newRig(t)
	ctx := context.Background()
	ids := []string{r.signedIn(t, "m1", "personal"), r.signedIn(t, "m2", "personal"), r.signedIn(t, "m1", "org")}
	elsewhere := other.signedIn(t, "m1", "personal")
	pending, _ := r.start(t, "m3")

	if err := r.OrganizationDeleted(ctx, authv1.OrganizationDeleted{OrganizationID: r.org, DeletedAt: r.Service.now()}); err != nil {
		t.Fatal(err)
	}
	for _, id := range ids {
		r.wantStatus(t, id, "revoked")
		if r.credentials(t, id) != 0 {
			t.Fatalf("connection %s kept its tokens", id)
		}
	}
	other.wantStatus(t, elsewhere, "ok")
	if got := r.removedEvents(t); !slices.Equal(got, sorted(ids...)) {
		t.Fatalf("removed events = %v, want %v", got, sorted(ids...))
	}
	if _, err := r.Complete(ctx, Callback{State: pending, Code: "code-1"}); !errors.Is(err, ErrBadState) {
		t.Fatalf("a deleted organization's sign-in completed: %v", err)
	}
}

// Delivered again, or in either order, departures end the same, and each
// connection's removal is published once.
func TestDeparturesAreIdempotentInAnyOrder(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	mine := r.signedIn(t, "m1", "personal")
	orgLevel := r.signedIn(t, "m1", "org")
	deleted := authv1.OrganizationDeleted{OrganizationID: r.org, DeletedAt: r.Service.now()}
	removed := r.memberRemoved("m1")

	for _, depart := range []func() error{
		func() error { return r.OrganizationDeleted(ctx, deleted) },
		func() error { return r.MemberRemoved(ctx, removed) },
		func() error { return r.MemberRemoved(ctx, removed) },
		func() error { return r.OrganizationDeleted(ctx, deleted) },
	} {
		if err := depart(); err != nil {
			t.Fatal(err)
		}
	}
	r.wantStatus(t, mine, "revoked")
	r.wantStatus(t, orgLevel, "revoked")
	if got := r.removedEvents(t); !slices.Equal(got, sorted(mine, orgLevel)) {
		t.Fatalf("removed events = %v, want one each for %v", got, sorted(mine, orgLevel))
	}
	r.app.mu.Lock()
	defer r.app.mu.Unlock()
	if len(r.app.revoked) != 2 {
		t.Fatalf("the app was asked to revoke %v, want once per connection", r.app.revoked)
	}
}

// A member's removal that arrives late spares what they connected after
// coming back; what they had before it goes.
func TestALateRemovalSparesWhatTheMemberConnectedSince(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	before := r.signedIn(t, "m1", "personal")
	removed := r.memberRemoved("m1")
	r.advance(time.Hour)
	if _, err := r.Revoke(ctx, before); err != nil {
		t.Fatal(err)
	}
	// They came back and connected again; then the removal arrives.
	after := r.signedIn(t, "m1", "personal")
	pending, _ := r.start(t, "m1")
	if err := r.MemberRemoved(ctx, removed); err != nil {
		t.Fatal(err)
	}
	r.wantStatus(t, after, "ok")
	if _, err := r.Complete(ctx, Callback{State: pending, Code: "code-1"}); err != nil {
		t.Fatalf("a sign-in started after the removal was spent: %v", err)
	}
}

func TestAProviderFailureStillRevokesHere(t *testing.T) {
	t.Run("the app refuses", func(t *testing.T) {
		r := newRig(t)
		id := r.signedIn(t, "m1", "personal")
		r.app.mu.Lock()
		r.app.revokeStatus = http.StatusServiceUnavailable
		r.app.mu.Unlock()
		if err := r.MemberRemoved(context.Background(), r.memberRemoved("m1")); err != nil {
			t.Fatal(err)
		}
		r.wantStatus(t, id, "revoked")
		if revoked := r.providerRevoked(t, id); revoked == nil || *revoked {
			t.Fatalf("provider_revoked = %v, want false", revoked)
		}
		if got := r.removedEvents(t); !slices.Equal(got, []string{id}) {
			t.Fatalf("removed events = %v", got)
		}
	})
	t.Run("the app cannot be reached", func(t *testing.T) {
		r := newRig(t)
		id := r.signedIn(t, "m1", "personal")
		r.app.Close()
		if err := r.MemberRemoved(context.Background(), r.memberRemoved("m1")); err != nil {
			t.Fatal(err)
		}
		r.wantStatus(t, id, "revoked")
		if revoked := r.providerRevoked(t, id); revoked == nil || *revoked {
			t.Fatalf("provider_revoked = %v, want false", revoked)
		}
	})
	// Revoke cannot get as far as the app: the tokens do not open. The
	// connection is revoked here all the same.
	t.Run("the tokens cannot be opened", func(t *testing.T) {
		r := newRig(t)
		ctx := context.Background()
		id := r.signedIn(t, "m1", "personal")
		foreign, err := r.sealer.Seal(ctx, credentialWhere(r.org, "someone-else", "access"), []byte("x"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.pool.Exec(ctx, `UPDATE integrations.credentials SET sealed = $2 WHERE connection_id = $1 AND kind = 'access'`, id, foreign); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Revoke(ctx, id); err == nil {
			t.Fatal("Revoke opened a token sealed elsewhere")
		}
		if err := r.MemberRemoved(ctx, r.memberRemoved("m1")); err != nil {
			t.Fatal(err)
		}
		r.wantStatus(t, id, "revoked")
		if r.credentials(t, id) != 0 {
			t.Fatal("the tokens were kept")
		}
		if revoked := r.providerRevoked(t, id); revoked == nil || *revoked {
			t.Fatalf("provider_revoked = %v, want false", revoked)
		}
		if got := r.removedEvents(t); !slices.Equal(got, []string{id}) {
			t.Fatalf("removed events = %v", got)
		}
		r.app.mu.Lock()
		defer r.app.mu.Unlock()
		if len(r.app.revoked) != 0 {
			t.Fatalf("the app was asked to revoke %v", r.app.revoked)
		}
	})
}

// A departure that names no one is refused, rather than read as everyone.
func TestDeparturesNameWhoLeft(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	id := r.signedIn(t, "m1", "personal")
	for name, err := range map[string]error{
		"no member":       r.MemberRemoved(ctx, authv1.MemberRemoved{OrganizationID: r.org, RemovedAt: r.Service.now()}),
		"no organization": r.MemberRemoved(ctx, authv1.MemberRemoved{UserID: "m1", RemovedAt: r.Service.now()}),
		"no deleted org":  r.OrganizationDeleted(ctx, authv1.OrganizationDeleted{}),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	r.wantStatus(t, id, "ok")
}
