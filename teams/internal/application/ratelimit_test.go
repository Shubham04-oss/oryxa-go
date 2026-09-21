package application

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/teams/internal/domain"
)

func TestCallToolIsBudgetedPerMember(t *testing.T) {
	service, store, _ := newService()
	ctx := context.Background()
	team, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.AddMember(ctx, creds("user-owner"), "org_1", team.ID, AddMemberInput{UserID: "user-member", Role: domain.RoleMember}); err != nil {
		t.Fatal(err)
	}
	query := json.RawMessage(`{"query":"Engineering"}`)

	// The budget is spent by the caller, one token a call, whatever the tool.
	accepted := 0
	for range toolCallBudget {
		if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); err == nil {
			accepted++
		}
	}
	if accepted != toolCallBudget {
		t.Fatalf("accepted %d of %d calls inside the budget", accepted, toolCallBudget)
	}

	// The next call is refused, and nothing was written for it.
	events := len(store.events)
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("error past the budget = %v, want too many requests", err)
	}
	if len(store.events) != events {
		t.Fatalf("a refused call wrote %d events", len(store.events)-events)
	}

	// Another member's budget is their own.
	if _, err := service.CallTool(ctx, creds("user-member"), "org_1", domain.ToolCallTeam, query); err != nil {
		t.Fatalf("a second member's first call failed: %v", err)
	}
}

func TestCallToolBudgetRefillsOverTheWindow(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"}); err != nil {
		t.Fatal(err)
	}
	query := json.RawMessage(`{"query":"Engineering"}`)

	for range toolCallBudget {
		if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); err != nil {
			t.Fatalf("a call inside the budget failed: %v", err)
		}
	}
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("error at the edge of the budget = %v, want too many requests", err)
	}

	// A full window later, the budget is whole again.
	base := service.now
	service.now = func() time.Time { return base().Add(toolCallWindow) }
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); err != nil {
		t.Fatalf("a call one window later failed: %v", err)
	}
}

func TestCallToolBudgetIsPerOrganization(t *testing.T) {
	service, _, _ := newService()
	ctx := context.Background()
	if _, err := service.CreateTeam(ctx, creds("user-owner"), "org_1", CreateTeamInput{Name: "Engineering"}); err != nil {
		t.Fatal(err)
	}
	query := json.RawMessage(`{"query":"Engineering"}`)

	for range toolCallBudget {
		if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); err != nil {
			t.Fatalf("a call inside the budget failed: %v", err)
		}
	}
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_1", domain.ToolCallTeam, query); !errors.Is(err, domain.ErrTooManyRequests) {
		t.Fatalf("error past the budget = %v, want too many requests", err)
	}

	// A membership in another organization does not share org_1's bucket.
	directory := service.directory.(*fakeDirectory)
	directory.callers["Bearer user-owner"] = domain.OrgMembership{OrganizationID: "org_2", OrganizationName: "Other", UserID: "user-owner", Email: "owner@example.com", Role: domain.OrgRoleOwner}
	if _, err := service.CallTool(ctx, creds("user-owner"), "org_2", domain.ToolCallTeam, query); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a call in another organization = %v, want not found (empty org, not refused)", err)
	}
}
