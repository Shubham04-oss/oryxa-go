// Package modelrouter routes agent work to the model tier it belongs on: the
// main model for the member's conversation and grounded research, and the
// cheaper lite model for planning and single-turn bulk work. The lite model is
// preferred wherever it can carry the job.
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"google.golang.org/adk/v2/model"
)

// Tier names a model's cost and capability class.
type Tier string

const (
	// Main is the full model: the member's conversation and grounded research.
	Main Tier = "main"
	// Lite is the cheaper model: planning and bulk single-turn work.
	Lite Tier = "lite"
)

// Role is the kind of work being routed.
type Role string

const (
	// RoleChat is the member's conversation, the root agent.
	RoleChat Role = "chat"
	// RolePlan is structured planning work.
	RolePlan Role = "plan"
	// RoleResearch is grounded web research.
	RoleResearch Role = "research"
	// RoleWork is helpers, dynamic sub-agents and other single-turn work.
	RoleWork Role = "work"
)

var allRoles = []Role{RoleChat, RolePlan, RoleResearch, RoleWork}

// DefaultPolicy prefers the lite model wherever it can carry the work.
// Research stays on main because Google Search grounding is not available on
// every lite model.
func DefaultPolicy() map[Role]Tier {
	return map[Role]Tier{
		RoleChat:     Main,
		RolePlan:     Lite,
		RoleResearch: Main,
		RoleWork:     Lite,
	}
}

// Router hands out the model a role runs on.
type Router struct {
	tiers map[Tier]model.LLM
	// answers are the tiers' models for an agent whose reply reaches a
	// person: tiers, screened once [Router.Guarded] is applied.
	answers map[Tier]model.LLM
	policy  map[Role]Tier
}

// New routes with the default policy.
func New(mainModel, liteModel model.LLM) (*Router, error) {
	return NewWithPolicy(mainModel, liteModel, DefaultPolicy())
}

// NewWithPolicy routes with a caller's policy. Every role must be routed to a
// known tier, so a new role cannot silently miss a model.
func NewWithPolicy(mainModel, liteModel model.LLM, policy map[Role]Tier) (*Router, error) {
	if mainModel == nil || liteModel == nil {
		return nil, errors.New("a router needs both a main and a lite model")
	}
	for _, role := range allRoles {
		tier, routed := policy[role]
		if !routed {
			return nil, fmt.Errorf("the policy does not route %q", role)
		}
		if tier != Main && tier != Lite {
			return nil, fmt.Errorf("the policy routes %q to unknown tier %q", role, tier)
		}
	}
	tiers := map[Tier]model.LLM{Main: mainModel, Lite: liteModel}
	return &Router{tiers: tiers, answers: tiers, policy: policy}, nil
}

// For returns the model the role runs on. An unknown role gets the lite
// model, because lite is the default preference.
func (r *Router) For(role Role) model.LLM {
	if tier, routed := r.policy[role]; routed {
		return r.tiers[tier]
	}
	return r.tiers[Lite]
}

// Answer is the model the role runs on for an agent whose reply reaches a
// person: the run's answering agent (Krude, a specialist, the organization
// agent). On a [Router.Guarded] router it is screened; [Router.For] never is,
// so internal calls (planner, helpers, sub-agents, compaction) stay cheap and
// keep streaming. Their output reaches a person only through the answering
// agent, whose screen reads it as its newest input.
func (r *Router) Answer(role Role) model.LLM {
	return r.answers[r.Tier(role)]
}

// AnswerOn is [Router.Answer] for an agent whose tier is set on the agent
// itself rather than by its role: a specialist its owner moved to main. An
// unknown tier gets the lite model, as an unknown role does.
func (r *Router) AnswerOn(tier Tier) model.LLM {
	if llm, known := r.answers[tier]; known {
		return llm
	}
	return r.answers[Lite]
}

// Tier names the tier a role runs on, for logs and tests.
func (r *Router) Tier(role Role) Tier {
	if tier, routed := r.policy[role]; routed {
		return tier
	}
	return Lite
}

// Guarded is the router with every answering agent's model ([Router.Answer]),
// main and lite, screened by screen (the Bedrock guardrail). A call whose
// run's profile is not guarded ([WithGuard]) skips the screen; a call outside
// any run is screened.
func (r *Router) Guarded(screen func(model.LLM) model.LLM) *Router {
	answers := map[Tier]model.LLM{}
	for tier, llm := range r.tiers {
		answers[tier] = screened{LLM: llm, guarded: screen(llm)}
	}
	return &Router{tiers: r.tiers, answers: answers, policy: r.policy}
}

type guardKey struct{}

// WithGuard records whether the run's profile has its model screened
// (runprofile.Profile.Guarded), for a [Guarded] router to read on each call.
func WithGuard(ctx context.Context, guarded bool) context.Context {
	return context.WithValue(ctx, guardKey{}, guarded)
}

// screened calls guarded unless the run says it is not guarded.
type screened struct {
	model.LLM
	guarded model.LLM
}

func (s screened) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if guarded, set := ctx.Value(guardKey{}).(bool); set && !guarded {
		return s.LLM.GenerateContent(ctx, req, stream)
	}
	return s.guarded.GenerateContent(ctx, req, stream)
}
