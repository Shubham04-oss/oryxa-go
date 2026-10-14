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

// screenedModel answers "screened <name>", so a test sees which calls passed
// the screen.
type screenedModel struct{ model.LLM }

func (s screenedModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(&model.LLMResponse{ErrorMessage: "screened " + s.Name()}, nil)
	}
}

// answered is what llm answers in ctx: "screened <model>", or "unscreened"
// when the call reached the model itself.
func answered(ctx context.Context, llm model.LLM) string {
	for response := range llm.GenerateContent(ctx, &model.LLMRequest{}, false) {
		return response.ErrorMessage
	}
	return "unscreened"
}

// A guarded router screens the answering agents' models on both tiers, unless
// the run is not guarded, and never the internal calls For hands out.
func TestAGuardedRouterScreensOnlyAnswers(t *testing.T) {
	router, err := New(namedModel("main"), namedModel("lite"))
	if err != nil {
		t.Fatal(err)
	}
	guarded := router.Guarded(func(llm model.LLM) model.LLM { return screenedModel{llm} })
	background := context.Background()
	for role, name := range map[Role]string{RoleChat: "main", RoleWork: "lite"} {
		if got := answered(background, guarded.Answer(role)); got != "screened "+name {
			t.Errorf("%s answer outside a run: %q, want screened", role, got)
		}
		if got := answered(WithGuard(background, true), guarded.Answer(role)); got != "screened "+name {
			t.Errorf("%s answer in a guarded run: %q, want screened", role, got)
		}
		if got := answered(WithGuard(background, false), guarded.Answer(role)); got != "unscreened" {
			t.Errorf("%s answer in an unguarded run: %q, want unscreened", role, got)
		}
		if got := answered(background, router.Answer(role)); got != "unscreened" {
			t.Errorf("Guarded changed the router it was called on: %s %q", role, got)
		}
	}
	for _, role := range allRoles {
		if got := answered(background, guarded.For(role)); got != "unscreened" {
			t.Errorf("internal %s call: %q, want unscreened", role, got)
		}
	}
}
