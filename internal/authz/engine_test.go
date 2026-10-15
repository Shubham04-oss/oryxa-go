package authz

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeStore is a PolicyStore that counts how often it is asked and can be
// made to fail, for testing Engine's cache and its fail-closed behavior
// without a real database.
type fakeStore struct {
	mu       sync.Mutex
	policies map[string][]Policy
	err      error
	calls    int
}

func (store *fakeStore) OrganizationPolicies(ctx context.Context, organizationID string) ([]Policy, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.calls++
	if store.err != nil {
		return nil, store.err
	}
	return append([]Policy(nil), store.policies[organizationID]...), nil
}

func (store *fakeStore) callCount() int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.calls
}

// expectedKnowledgeBaseAllowed is a plain-Go restatement of
// policies/knowledge_bases.cedar's knowledge base rules, independent of
// Cedar, for TestAuthorizeTruthTable to check Engine against.
func expectedKnowledgeBaseAllowed(action Action, scope string, actsAsAdmin, teamMember, memberMatches bool) bool {
	readable := func() bool {
		switch scope {
		case "organization":
			return true
		case "member":
			return memberMatches || actsAsAdmin
		case "team":
			return actsAsAdmin || teamMember
		default:
			return false
		}
	}
	switch action {
	case Read, Search:
		return readable()
	case Administer:
		return actsAsAdmin
	default:
		return false
	}
}

// expectedDocumentAllowed is the same restatement for a document: its
// knowledge base must be readable, and its own access must not narrow it
// further.
func expectedDocumentAllowed(scope string, actsAsAdmin, teamMember, memberMatches bool, access string) bool {
	if !expectedKnowledgeBaseAllowed(Read, scope, actsAsAdmin, teamMember, memberMatches) {
		return false
	}
	switch access {
	case "", "members":
		return true
	case "admins":
		return actsAsAdmin
	default:
		return false
	}
}

// TestAuthorizeTruthTable checks the baseline alone (no stored policies)
// against every combination of principal kind, acts_as_admin, team_member,
// knowledge base scope (the three real ones plus an unknown one), whether
// the knowledge base's member_id matches the principal, every action, and
// every document access value (plus an unknown one) — comparing Engine's
// answer to expectedKnowledgeBaseAllowed/expectedDocumentAllowed on each.
func TestAuthorizeTruthTable(t *testing.T) {
	engine, err := New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const organizationID = "org-truth-table"
	const matchingMemberID = "member-1"
	const otherMemberID = "member-2"

	scopes := []string{"member", "team", "organization", "unknown-scope"}
	accessValues := []string{"", "members", "admins", "unknown-access"}
	actions := []Action{Read, Search, Administer}

	failures := 0
	for _, agent := range []bool{false, true} {
		for _, actsAsAdmin := range []bool{false, true} {
			for _, teamMember := range []bool{false, true} {
				for _, scope := range scopes {
					for _, memberMatches := range []bool{true, false} {
						principal := Principal{UserID: matchingMemberID, Agent: agent}
						memberID := matchingMemberID
						if !memberMatches {
							memberID = otherMemberID
						}
						knowledgeBase := KnowledgeBase{
							ID:             "kb-1",
							OrganizationID: organizationID,
							Scope:          scope,
							TeamID:         "team-1",
							MemberID:       memberID,
							CreatedBy:      matchingMemberID,
						}
						requestContext := Context{ActsAsAdmin: actsAsAdmin, TeamMember: teamMember}

						for _, action := range actions {
							decision, err := engine.Authorize(context.Background(), Request{
								OrganizationID: organizationID,
								Principal:      principal,
								Action:         action,
								Resource:       knowledgeBase,
								Context:        requestContext,
							})
							if err != nil {
								t.Fatalf("Authorize(knowledge base) error = %v", err)
							}
							want := expectedKnowledgeBaseAllowed(action, scope, actsAsAdmin, teamMember, memberMatches)
							if decision.Allowed != want && failures < 20 {
								failures++
								t.Errorf("knowledge base: agent=%v admin=%v team_member=%v scope=%s member_matches=%v action=%s: Allowed = %v, want %v (policies=%v)",
									agent, actsAsAdmin, teamMember, scope, memberMatches, action, decision.Allowed, want, decision.Policies)
							}

							for _, access := range accessValues {
								document := Document{KnowledgeBase: knowledgeBase, ID: "doc-1", Access: access}
								decision, err := engine.Authorize(context.Background(), Request{
									OrganizationID: organizationID,
									Principal:      principal,
									Action:         action,
									Resource:       document,
									Context:        requestContext,
								})
								if err != nil {
									t.Fatalf("Authorize(document) error = %v", err)
								}
								want := action == Read && expectedDocumentAllowed(scope, actsAsAdmin, teamMember, memberMatches, access)
								if decision.Allowed != want && failures < 20 {
									failures++
									t.Errorf("document: agent=%v admin=%v team_member=%v scope=%s member_matches=%v action=%s access=%s: Allowed = %v, want %v (policies=%v)",
										agent, actsAsAdmin, teamMember, scope, memberMatches, action, access, decision.Allowed, want, decision.Policies)
								}
							}
						}
					}
				}
			}
		}
	}
}

// TestStoredForbidByMetadata is doc.go's first worked example: a document
// tagged confidential is unreadable except by an org admin, and only has
// guards it against documents that were never tagged at all.
func TestStoredForbidByMetadata(t *testing.T) {
	const organizationID = "org-metadata-forbid"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID:             "pol-confidential",
			OrganizationID: organizationID,
			Scope:          organizationScope(),
			Text: `forbid (
				principal,
				action == Oryxa::Action::"read",
				resource is Oryxa::Document
			) when {
				resource.metadata has confidential &&
				resource.metadata.confidential == "true"
			} unless {
				context.acts_as_admin
			};`,
		}},
	}}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}
	confidential := Document{KnowledgeBase: knowledgeBase, ID: "doc-confidential", Metadata: map[string]string{"confidential": "true"}}
	ordinary := Document{KnowledgeBase: knowledgeBase, ID: "doc-ordinary"}
	member := Principal{UserID: "member-1"}
	admin := Principal{UserID: "admin-1"}

	cases := []struct {
		name      string
		principal Principal
		context   Context
		resource  Resource
		allowed   bool
	}{
		{"member reads confidential", member, Context{}, confidential, false},
		{"member reads ordinary", member, Context{}, ordinary, true},
		{"admin reads confidential", admin, Context{ActsAsAdmin: true}, confidential, true},
		{"admin reads ordinary", admin, Context{ActsAsAdmin: true}, ordinary, true},
	}
	for _, testCase := range cases {
		decision, err := engine.Authorize(context.Background(), Request{
			OrganizationID: organizationID, Principal: testCase.principal, Action: Read, Resource: testCase.resource, Context: testCase.context,
		})
		if err != nil {
			t.Fatalf("%s: Authorize() error = %v", testCase.name, err)
		}
		if decision.Allowed != testCase.allowed {
			t.Errorf("%s: Allowed = %v, want %v (policies=%v)", testCase.name, decision.Allowed, testCase.allowed, decision.Policies)
		}
	}
}

// TestStoredForbidBarsAgentsFromAdminDocuments is doc.go's second worked
// example. The baseline already keeps an agent off an admins-only document
// as long as callers honor Context.ActsAsAdmin's rule that an agent never
// carries it (see Context); this forbid is what still catches the request
// if a caller ever got that wrong, which is why the test sets ActsAsAdmin on
// the agent's own request to show the forbid, not the baseline, is what
// denies it.
func TestStoredForbidBarsAgentsFromAdminDocuments(t *testing.T) {
	const organizationID = "org-agent-admin-docs"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID:             "pol-agent-admin-docs",
			OrganizationID: organizationID,
			Scope:          organizationScope(),
			Text:           `forbid (principal is Oryxa::Agent, action, resource is Oryxa::Document) when { resource.access == "admins" };`,
		}},
	}}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}
	document := Document{KnowledgeBase: knowledgeBase, ID: "doc-1", Access: "admins"}
	agent := Principal{UserID: "admin-1", Agent: true}
	admin := Principal{UserID: "admin-1"}

	decision, err := engine.Authorize(context.Background(), Request{
		OrganizationID: organizationID, Principal: agent, Action: Read, Resource: document, Context: Context{ActsAsAdmin: true},
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if decision.Allowed {
		t.Errorf("agent Allowed = true, want the stored forbid to bar an agent from an admins-only document regardless of context")
	}

	decision, err = engine.Authorize(context.Background(), Request{
		OrganizationID: organizationID, Principal: admin, Action: Read, Resource: document, Context: Context{ActsAsAdmin: true},
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if !decision.Allowed {
		t.Errorf("admin Allowed = false, want the admin's own session unaffected by the agent-only forbid")
	}
}

// TestStoredForbidRestrictsSearchToAdmins is "only admins may search this
// knowledge base": a forbid on search alone, leaving read untouched.
func TestStoredForbidRestrictsSearchToAdmins(t *testing.T) {
	const organizationID = "org-search-admins-only"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID:             "pol-search-admins-only",
			OrganizationID: organizationID,
			Scope:          organizationScope(),
			Text:           `forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };`,
		}},
	}}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}
	member := Principal{UserID: "member-1"}

	decision, err := engine.Authorize(context.Background(), Request{
		OrganizationID: organizationID, Principal: member, Action: Search, Resource: knowledgeBase,
	})
	if err != nil {
		t.Fatalf("Authorize(search) error = %v", err)
	}
	if decision.Allowed {
		t.Errorf("member search Allowed = true, want search restricted to admins")
	}

	decision, err = engine.Authorize(context.Background(), Request{
		OrganizationID: organizationID, Principal: member, Action: Read, Resource: knowledgeBase,
	})
	if err != nil {
		t.Fatalf("Authorize(read) error = %v", err)
	}
	if !decision.Allowed {
		t.Errorf("member read Allowed = false, want the search-only forbid to leave read untouched")
	}

	decision, err = engine.Authorize(context.Background(), Request{
		OrganizationID: organizationID, Principal: member, Action: Search, Resource: knowledgeBase, Context: Context{ActsAsAdmin: true},
	})
	if err != nil {
		t.Fatalf("Authorize(admin search) error = %v", err)
	}
	if !decision.Allowed {
		t.Errorf("admin search Allowed = false, want an org admin unaffected by the forbid's unless clause")
	}
}

// TestStoredPolicyScopingIsPerKnowledgeBase checks that a policy attached to
// one knowledge base's Scope never affects another, even in the same
// organization, while an organization-scoped policy affects both.
func TestStoredPolicyScopingIsPerKnowledgeBase(t *testing.T) {
	const organizationID = "org-per-kb-scoping"
	knowledgeBaseA := KnowledgeBase{ID: "kb-a", OrganizationID: organizationID, Scope: "organization"}
	knowledgeBaseB := KnowledgeBase{ID: "kb-b", OrganizationID: organizationID, Scope: "organization"}
	member := Principal{UserID: "member-1"}
	forbidEverythingUnlessAdmin := `forbid (principal, action, resource) unless { context.acts_as_admin };`

	t.Run("knowledge base scoped policy stays on its own knowledge base", func(t *testing.T) {
		store := &fakeStore{policies: map[string][]Policy{
			organizationID: {{
				ID: "pol-kb-a", OrganizationID: organizationID,
				Scope: Scope{Type: "knowledge_base", ID: knowledgeBaseA.ID},
				Text:  forbidEverythingUnlessAdmin,
			}},
		}}
		engine, err := New(store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		decision, err := engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Read, Resource: knowledgeBaseA})
		if err != nil {
			t.Fatalf("Authorize(kb-a) error = %v", err)
		}
		if decision.Allowed {
			t.Errorf("kb-a Allowed = true, want the kb-a-scoped forbid to apply to kb-a")
		}
		decision, err = engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Read, Resource: knowledgeBaseB})
		if err != nil {
			t.Fatalf("Authorize(kb-b) error = %v", err)
		}
		if !decision.Allowed {
			t.Errorf("kb-b Allowed = false, want a kb-a-scoped forbid to leave kb-b alone")
		}
	})

	t.Run("organization scoped policy applies to every knowledge base", func(t *testing.T) {
		store := &fakeStore{policies: map[string][]Policy{
			organizationID: {{ID: "pol-org", OrganizationID: organizationID, Scope: organizationScope(), Text: forbidEverythingUnlessAdmin}},
		}}
		engine, err := New(store)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		for _, knowledgeBase := range []KnowledgeBase{knowledgeBaseA, knowledgeBaseB} {
			decision, err := engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Read, Resource: knowledgeBase})
			if err != nil {
				t.Fatalf("Authorize(%s) error = %v", knowledgeBase.ID, err)
			}
			if decision.Allowed {
				t.Errorf("%s Allowed = true, want the organization-scoped forbid to apply everywhere", knowledgeBase.ID)
			}
		}
	})
}

// TestStoredPolicyMultipleStatements checks that one Policy's Text holding
// two forbid statements compiles and applies both, independently.
func TestStoredPolicyMultipleStatements(t *testing.T) {
	const organizationID = "org-multi-statement"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID: "pol-multi", OrganizationID: organizationID, Scope: organizationScope(),
			Text: `forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };
				forbid (principal, action == Oryxa::Action::"read", resource is Oryxa::Document) when { resource.metadata has secret };`,
		}},
	}}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}
	secret := Document{KnowledgeBase: knowledgeBase, ID: "doc-secret", Metadata: map[string]string{"secret": "true"}}
	ordinary := Document{KnowledgeBase: knowledgeBase, ID: "doc-ordinary"}
	member := Principal{UserID: "member-1"}

	if decision, err := engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Search, Resource: knowledgeBase}); err != nil {
		t.Fatalf("Authorize(search) error = %v", err)
	} else if decision.Allowed {
		t.Errorf("search Allowed = true, want the first statement to forbid it")
	}

	if decision, err := engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Read, Resource: secret}); err != nil {
		t.Fatalf("Authorize(read secret) error = %v", err)
	} else if decision.Allowed {
		t.Errorf("read secret Allowed = true, want the second statement to forbid it")
	}

	if decision, err := engine.Authorize(context.Background(), Request{OrganizationID: organizationID, Principal: member, Action: Read, Resource: ordinary}); err != nil {
		t.Fatalf("Authorize(read ordinary) error = %v", err)
	} else if !decision.Allowed {
		t.Errorf("read ordinary Allowed = false, want neither statement to touch an untagged document")
	}
}

// TestEngineCacheTTLAndInvalidate checks that Engine asks PolicyStore once,
// serves the cache within the TTL, asks again once the TTL passes, and asks
// again immediately after Invalidate.
func TestEngineCacheTTLAndInvalidate(t *testing.T) {
	const organizationID = "org-cache"
	store := &fakeStore{}
	engine, err := New(store, WithCacheTTL(time.Minute))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	now := time.Now()
	engine.now = func() time.Time { return now }

	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}
	authorize := func(t *testing.T) {
		t.Helper()
		if _, err := engine.Authorize(context.Background(), Request{
			OrganizationID: organizationID, Principal: Principal{UserID: "u1"}, Action: Read, Resource: knowledgeBase,
		}); err != nil {
			t.Fatalf("Authorize() error = %v", err)
		}
	}

	authorize(t)
	if got := store.callCount(); got != 1 {
		t.Fatalf("calls = %d, want 1 after the first Authorize", got)
	}

	authorize(t)
	if got := store.callCount(); got != 1 {
		t.Fatalf("calls = %d, want still 1 within the TTL", got)
	}

	now = now.Add(2 * time.Minute)
	authorize(t)
	if got := store.callCount(); got != 2 {
		t.Fatalf("calls = %d, want 2 once the TTL has passed", got)
	}

	authorize(t)
	if got := store.callCount(); got != 2 {
		t.Fatalf("calls = %d, want still 2 within the refreshed TTL", got)
	}

	engine.Invalidate(organizationID)
	authorize(t)
	if got := store.callCount(); got != 3 {
		t.Fatalf("calls = %d, want 3 right after Invalidate", got)
	}
}

// TestEngineFailsClosedOnStoreError checks that Authorize and AuthorizeEach
// both deny, rather than fall back to the baseline alone, when PolicyStore
// errors.
func TestEngineFailsClosedOnStoreError(t *testing.T) {
	store := &fakeStore{err: errors.New("store unavailable")}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: "org-store-error", Scope: "organization"}

	decision, err := engine.Authorize(context.Background(), Request{
		OrganizationID: "org-store-error", Principal: Principal{UserID: "u1"}, Action: Read, Resource: knowledgeBase,
	})
	if err == nil {
		t.Fatal("Authorize() error = nil, want the store's error")
	}
	if decision.Allowed {
		t.Fatal("Authorize() Allowed = true, want a store error to fail closed")
	}

	allowed, err := engine.AuthorizeEach(context.Background(), "org-store-error", Principal{UserID: "u1"}, Read, []Resource{knowledgeBase}, Context{})
	if err == nil {
		t.Fatal("AuthorizeEach() error = nil, want the store's error")
	}
	if len(allowed) != 1 || allowed[0] {
		t.Fatalf("AuthorizeEach() = %v, want a single false result on a store error", allowed)
	}
}

// TestAuthorizeEachAgreesWithAuthorize checks that AuthorizeEach's answer for
// each resource matches what Authorize says about that same resource, one
// at a time, with a stored forbid in play so the comparison is not just
// against the baseline.
func TestAuthorizeEachAgreesWithAuthorize(t *testing.T) {
	const organizationID = "org-each-agrees"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID: "pol-secret", OrganizationID: organizationID, Scope: organizationScope(),
			Text: `forbid (principal, action == Oryxa::Action::"read", resource is Oryxa::Document) when { resource.metadata has secret };`,
		}},
	}}
	engine, err := New(store)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	knowledgeBaseOrg := KnowledgeBase{ID: "kb-org", OrganizationID: organizationID, Scope: "organization"}
	knowledgeBaseMember := KnowledgeBase{ID: "kb-member", OrganizationID: organizationID, Scope: "member", MemberID: "member-1"}
	knowledgeBaseOther := KnowledgeBase{ID: "kb-other-member", OrganizationID: organizationID, Scope: "member", MemberID: "member-2"}
	resources := []Resource{
		knowledgeBaseOrg,
		knowledgeBaseMember,
		knowledgeBaseOther,
		Document{KnowledgeBase: knowledgeBaseOrg, ID: "doc-members", Access: "members"},
		Document{KnowledgeBase: knowledgeBaseOrg, ID: "doc-admins", Access: "admins"},
		Document{KnowledgeBase: knowledgeBaseOrg, ID: "doc-secret", Metadata: map[string]string{"secret": "true"}},
	}
	principal := Principal{UserID: "member-1"}
	requestContext := Context{ActsAsAdmin: false, TeamMember: false}

	got, err := engine.AuthorizeEach(context.Background(), organizationID, principal, Read, resources, requestContext)
	if err != nil {
		t.Fatalf("AuthorizeEach() error = %v", err)
	}
	if len(got) != len(resources) {
		t.Fatalf("AuthorizeEach() returned %d results, want %d", len(got), len(resources))
	}

	for i, resource := range resources {
		decision, err := engine.Authorize(context.Background(), Request{
			OrganizationID: organizationID, Principal: principal, Action: Read, Resource: resource, Context: requestContext,
		})
		if err != nil {
			t.Fatalf("Authorize(resource %d) error = %v", i, err)
		}
		if got[i] != decision.Allowed {
			t.Errorf("resource %d: AuthorizeEach = %v, Authorize = %v", i, got[i], decision.Allowed)
		}
	}
}

// TestEngineConcurrentAccessIsRaceFree drives Authorize, AuthorizeEach and
// Invalidate from many goroutines at once, under a cache TTL short enough
// that reloads and invalidations overlap with reads, for go test -race to
// check.
func TestEngineConcurrentAccessIsRaceFree(t *testing.T) {
	const organizationID = "org-concurrent"
	store := &fakeStore{policies: map[string][]Policy{
		organizationID: {{
			ID: "pol-1", OrganizationID: organizationID, Scope: organizationScope(),
			Text: `forbid (principal, action == Oryxa::Action::"search", resource is Oryxa::KnowledgeBase) unless { context.acts_as_admin };`,
		}},
	}}
	engine, err := New(store, WithCacheTTL(time.Millisecond))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: organizationID, Scope: "organization"}

	var group sync.WaitGroup
	for i := 0; i < 20; i++ {
		group.Go(func() {
			for j := 0; j < 25; j++ {
				if _, err := engine.Authorize(context.Background(), Request{
					OrganizationID: organizationID, Principal: Principal{UserID: "u1"}, Action: Read, Resource: knowledgeBase,
				}); err != nil {
					t.Errorf("Authorize() error = %v", err)
				}
				if _, err := engine.AuthorizeEach(context.Background(), organizationID, Principal{UserID: "u1"}, Search, []Resource{knowledgeBase}, Context{}); err != nil {
					t.Errorf("AuthorizeEach() error = %v", err)
				}
				engine.Invalidate(organizationID)
			}
		})
	}
	group.Wait()
}

// TestEngineNilStoreIsBaselineOnly checks that a nil store never prevents
// what the baseline itself permits, and that Invalidate on it is harmless.
func TestEngineNilStoreIsBaselineOnly(t *testing.T) {
	engine, err := New(nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	knowledgeBase := KnowledgeBase{ID: "kb-1", OrganizationID: "org-nil-store", Scope: "organization"}
	decision, err := engine.Authorize(context.Background(), Request{
		OrganizationID: "org-nil-store", Principal: Principal{UserID: "u1"}, Action: Read, Resource: knowledgeBase,
	})
	if err != nil {
		t.Fatalf("Authorize() error = %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("Allowed = false, want a nil store to leave the baseline permit in effect")
	}
	engine.Invalidate("org-nil-store")
}

// TestParseBaselineRejectsAStatementWithoutAnID guards the assumption every
// other test relies on: every baseline statement carries a unique @id.
func TestParseBaselineRejectsAStatementWithoutAnID(t *testing.T) {
	_, _, err := parseBaselineFromText(`permit (principal, action, resource);`)
	if err == nil {
		t.Fatal("parseBaselineFromText() error = nil, want a missing @id to be refused")
	}
}

// TestParseBaselineRejectsADuplicateID guards the same assumption against a
// copy-paste mistake inside policies/knowledge_bases.cedar.
func TestParseBaselineRejectsADuplicateID(t *testing.T) {
	text := `@id("dup") permit (principal, action, resource);
		@id("dup") forbid (principal, action, resource);`
	_, _, err := parseBaselineFromText(text)
	if err == nil {
		t.Fatal("parseBaselineFromText() error = nil, want a duplicate @id to be refused")
	}
}

// blockingStore holds its first read until released, so a test can
// invalidate while a load is in flight.
type blockingStore struct {
	mu       sync.Mutex
	policies []Policy
	started  chan struct{}
	release  chan struct{}
	blocked  bool
}

func (store *blockingStore) OrganizationPolicies(_ context.Context, _ string) ([]Policy, error) {
	store.mu.Lock()
	policies := append([]Policy(nil), store.policies...)
	block := !store.blocked
	store.blocked = true
	store.mu.Unlock()
	if block {
		close(store.started)
		<-store.release
	}
	return policies, nil
}

func TestEngineDoesNotCacheALoadThatAnInvalidationOvertook(t *testing.T) {
	store := &blockingStore{started: make(chan struct{}), release: make(chan struct{})}
	engine, err := New(store)
	if err != nil {
		t.Fatal(err)
	}
	kb := KnowledgeBase{ID: "kb-1", OrganizationID: "org-1", Scope: "organization"}
	request := Request{OrganizationID: "org-1", Principal: Principal{UserID: "u-1"}, Action: Read, Resource: kb}

	done := make(chan Decision)
	go func() {
		decision, err := engine.Authorize(context.Background(), request)
		if err != nil {
			t.Error(err)
		}
		done <- decision
	}()
	<-store.started
	// An admin adds a policy while the first load is still reading the
	// store's old contents, and the engine is told.
	store.mu.Lock()
	store.policies = []Policy{{
		ID: "pol-1", OrganizationID: "org-1", Scope: Scope{Type: "knowledge_base", ID: "kb-1"},
		Text: `forbid (principal, action, resource is Oryxa::KnowledgeBase);`,
	}}
	store.mu.Unlock()
	engine.Invalidate("org-1")
	close(store.release)
	if first := <-done; !first.Allowed {
		t.Fatal("the load that began before the policy existed should still allow")
	}

	// The overtaken load must not have been cached: the next decision reads
	// the store again and sees the new forbid.
	second, err := engine.Authorize(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second.Allowed {
		t.Fatal("a stale load was cached over the invalidation; the new forbid was missed")
	}
}
