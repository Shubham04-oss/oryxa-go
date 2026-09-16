package modelrouter

import (
	"context"
	"iter"
	"testing"

	"google.golang.org/adk/v2/model"
)

// namedModel is a model.LLM that exists only to be told apart.
type namedModel string

func (m namedModel) Name() string { return string(m) }

func (m namedModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(func(*model.LLMResponse, error) bool) {}
}

func TestDefaultPolicyPrefersLite(t *testing.T) {
	router, err := New(namedModel("main"), namedModel("lite"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	tests := map[Role]string{
		RoleChat:     "main",
		RoleResearch: "main",
		RolePlan:     "lite",
		RoleWork:     "lite",
	}
	for role, want := range tests {
		if got := router.For(role).Name(); got != want {
			t.Errorf("For(%q) = %q, want %q", role, got, want)
		}
		if got := router.Tier(role); got != Tier(want) {
			t.Errorf("Tier(%q) = %q, want %q", role, got, want)
		}
	}
}

func TestUnknownRoleFallsBackToLite(t *testing.T) {
	router, err := New(namedModel("main"), namedModel("lite"))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if got := router.For(Role("mystery")).Name(); got != "lite" {
		t.Fatalf("For(unknown) = %q, want lite", got)
	}
}

func TestNewValidatesModelsAndPolicy(t *testing.T) {
	if _, err := New(nil, namedModel("lite")); err == nil {
		t.Fatal("New() error = nil without a main model")
	}
	if _, err := New(namedModel("main"), nil); err == nil {
		t.Fatal("New() error = nil without a lite model")
	}
	policy := DefaultPolicy()
	delete(policy, RolePlan)
	if _, err := NewWithPolicy(namedModel("main"), namedModel("lite"), policy); err == nil {
		t.Fatal("NewWithPolicy() error = nil with an unrouted role")
	}
	policy = DefaultPolicy()
	policy[RoleWork] = Tier("turbo")
	if _, err := NewWithPolicy(namedModel("main"), namedModel("lite"), policy); err == nil {
		t.Fatal("NewWithPolicy() error = nil with an unknown tier")
	}
}
