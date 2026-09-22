package specialistonboarding

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestDraftUsesContextAndReturnsReviewablePlan(t *testing.T) {
	planner := New(func(_ context.Context, prompt string) (string, error) {
		for _, want := range []string{"Revenue Analyst", "Odd Works", "github", "untrusted member-visible context"} {
			if !strings.Contains(prompt, want) {
				t.Errorf("prompt omitted %q", want)
			}
		}
		return `{"purpose":"Review revenue","instructions":"Verify every number.","cycle":[{"title":"Meet","detail":"Ask for goals"},{"title":"Research","detail":"Read sources"},{"title":"Review","detail":"Report"}],"integrations":["github"],"first_message":"What should I review?"}`, nil
	})
	plan, err := planner.Draft(context.Background(), Input{Name: "Revenue Analyst", Context: Context{OrganizationName: "Odd Works", ConnectedApps: []string{"github"}}})
	if err != nil || plan.Instructions != "Verify every number." || len(plan.Cycle) != 3 {
		t.Fatalf("Draft() = %+v, %v", plan, err)
	}
}

func TestDraftRejectsInvalidNameAndModelOutput(t *testing.T) {
	planner := New(func(context.Context, string) (string, error) { return `{}`, nil })
	if _, err := planner.Draft(context.Background(), Input{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing name error = %v, want ErrInvalid", err)
	}
	if _, err := planner.Draft(context.Background(), Input{Name: "Research"}); err == nil {
		t.Fatal("incomplete model response should fail")
	}
}

func TestDraftDoesNotInventIntegrationsWithNoInventory(t *testing.T) {
	planner := New(func(context.Context, string) (string, error) {
		return `{"purpose":"Research","instructions":"Cite evidence","cycle":[{"title":"Ask","detail":"Clarify"},{"title":"Read","detail":"Verify"},{"title":"Report","detail":"Summarize"}],"integrations":["Imaginary Search Pro"],"first_message":"What should I research?"}`, nil
	})
	plan, err := planner.Draft(context.Background(), Input{Name: "Researcher"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(plan.Integrations, " "), "Imaginary") {
		t.Fatalf("invented integration survived: %v", plan.Integrations)
	}
}
