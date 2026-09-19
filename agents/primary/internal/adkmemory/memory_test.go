package adkmemory

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
)

const appName = "krude"

// conversation returns a stored session for userID holding texts, the shape
// a finished run hands to memory.
func conversation(t *testing.T, userID string, texts ...string) session.Session {
	t.Helper()
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: appName, UserID: userID})
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	for _, text := range texts {
		event := session.NewEvent(ctx, "invocation")
		event.Author = "user"
		event.Content = genai.NewContentFromText(text, genai.RoleUser)
		if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatalf("append event: %v", err)
		}
	}
	return created.Session
}

// remembered returns the text of every entry a search in scope returned.
func remembered(t *testing.T, service *Service, scope Scope, query string) []string {
	t.Helper()
	view, err := service.For(scope)
	if err != nil {
		t.Fatalf("For(%+v): %v", scope, err)
	}
	found, err := view.SearchMemory(context.Background(), &memory.SearchRequest{
		AppName: appName, UserID: scope.UserID, Query: query,
	})
	if err != nil {
		t.Fatalf("SearchMemory: %v", err)
	}
	var texts []string
	for _, entry := range found.Memories {
		if entry.Content == nil {
			continue
		}
		for _, part := range entry.Content.Parts {
			if part.Text != "" {
				texts = append(texts, part.Text)
			}
		}
	}
	return texts
}

func remember(t *testing.T, service *Service, scope Scope, stored session.Session) {
	t.Helper()
	view, err := service.For(scope)
	if err != nil {
		t.Fatalf("For(%+v): %v", scope, err)
	}
	if err := view.AddSessionToMemory(context.Background(), stored); err != nil {
		t.Fatalf("AddSessionToMemory: %v", err)
	}
}

func newService(t *testing.T) *Service {
	t.Helper()
	service, err := New(InMemory())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return service
}

// TestMemoryDoesNotCrossOrganizations is the reason this package exists: one
// member in two organizations must not be told in one what they said in the
// other, even though ADK addresses both by the same user.
func TestMemoryDoesNotCrossOrganizations(t *testing.T) {
	service := newService(t)
	const member = "user-1"
	acme := Scope{OrganizationID: "org-acme", UserID: member}
	initech := Scope{OrganizationID: "org-initech", UserID: member}

	remember(t, service, acme, conversation(t, member, "the acme migration ships in March"))

	if got := remembered(t, service, initech, "migration"); len(got) != 0 {
		t.Fatalf("a search in one organization returned another's memories: %q", got)
	}
	got := remembered(t, service, acme, "migration")
	if len(got) != 1 || !strings.Contains(got[0], "acme migration") {
		t.Fatalf("the organization that wrote the memory could not read it back: %q", got)
	}
}

// TestSearchRefusesAnotherMember checks the second guard: a request that
// names someone other than the scope's member fails instead of being
// answered from the scope it happens to hold.
func TestSearchRefusesAnotherMember(t *testing.T) {
	service := newService(t)
	view, err := service.For(Scope{OrganizationID: "org-acme", UserID: "user-1"})
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	_, err = view.SearchMemory(context.Background(), &memory.SearchRequest{
		AppName: appName, UserID: "user-2", Query: "anything",
	})
	if err == nil {
		t.Fatal("searching another member's memory was allowed")
	}
}

// TestRememberRefusesAnotherMember checks the same guard on the way in: a
// conversation is never recorded into a member it does not belong to.
func TestRememberRefusesAnotherMember(t *testing.T) {
	service := newService(t)
	view, err := service.For(Scope{OrganizationID: "org-acme", UserID: "user-1"})
	if err != nil {
		t.Fatalf("For: %v", err)
	}

	err = view.AddSessionToMemory(context.Background(), conversation(t, "user-2", "a secret"))
	if err == nil {
		t.Fatal("remembering another member's conversation was allowed")
	}
}

// TestForgettingAnOrganizationLeavesTheOthers covers the retention half:
// what a conversation taught Krude goes when the organization does, and only
// that organization's memories go with it.
func TestForgettingAnOrganizationLeavesTheOthers(t *testing.T) {
	service := newService(t)
	const member = "user-1"
	acme := Scope{OrganizationID: "org-acme", UserID: member}
	initech := Scope{OrganizationID: "org-initech", UserID: member}
	remember(t, service, acme, conversation(t, member, "the acme migration ships in March"))
	remember(t, service, initech, conversation(t, member, "the initech migration ships in May"))

	if err := service.Forget(context.Background(), "org-acme", ""); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	if got := remembered(t, service, acme, "migration"); len(got) != 0 {
		t.Fatalf("a forgotten organization still remembers: %q", got)
	}
	if got := remembered(t, service, initech, "migration"); len(got) != 1 {
		t.Fatalf("forgetting one organization took another's memories: %q", got)
	}
}

// TestForgettingOneMemberLeavesTheOthers covers a member leaving an
// organization that stays.
func TestForgettingOneMemberLeavesTheOthers(t *testing.T) {
	service := newService(t)
	const org = "org-acme"
	leaving := Scope{OrganizationID: org, UserID: "user-1"}
	staying := Scope{OrganizationID: org, UserID: "user-2"}
	remember(t, service, leaving, conversation(t, "user-1", "the migration ships in March"))
	remember(t, service, staying, conversation(t, "user-2", "the migration needs review"))

	if err := service.Forget(context.Background(), org, "user-1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}

	if got := remembered(t, service, leaving, "migration"); len(got) != 0 {
		t.Fatalf("a member who left is still remembered: %q", got)
	}
	if got := remembered(t, service, staying, "migration"); len(got) != 1 {
		t.Fatalf("forgetting one member took another's memories: %q", got)
	}
}

func TestForgettingNeedsAnOrganization(t *testing.T) {
	service := newService(t)
	if err := service.Forget(context.Background(), "", "user-1"); err == nil {
		t.Fatal("forgetting every organization's memory for a member was allowed")
	}
}

func TestScopeNeedsBothHalves(t *testing.T) {
	for name, scope := range map[string]Scope{
		"no organization":    {UserID: "user-1"},
		"no member":          {OrganizationID: "org-acme"},
		"blank organization": {OrganizationID: "  ", UserID: "user-1"},
		"blank member":       {OrganizationID: "org-acme", UserID: "\t"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := scope.Validate(); err == nil {
				t.Fatalf("Validate(%+v) allowed it", scope)
			}
			service := newService(t)
			if _, err := service.For(scope); err == nil {
				t.Fatalf("For(%+v) allowed it", scope)
			}
		})
	}
}

// TestScopeFieldsCarryBothHalves checks that neither dimension is dropped on
// the way to a backend: an organization missing from the map is a scope that
// matches every organization's memories.
func TestScopeFieldsCarryBothHalves(t *testing.T) {
	fields := Scope{OrganizationID: "org-acme", UserID: "user-1"}.Fields()
	if len(fields) != 2 ||
		fields[FieldOrganizationID] != "org-acme" || fields[FieldUserID] != "user-1" {
		t.Fatalf("Fields() = %v", fields)
	}
}
