// Package modelrouter routes agent work to the model tier it belongs on: the
// main model for the member's conversation and grounded research, and the
// cheaper lite model for planning and single-turn bulk work. The lite model is
// preferred wherever it can carry the job.
package modelrouter

import (
	"errors"
	"fmt"

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
	tiers  map[Tier]model.LLM
	policy map[Role]Tier
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
	return &Router{
		tiers:  map[Tier]model.LLM{Main: mainModel, Lite: liteModel},
		policy: policy,
	}, nil
}

// For returns the model the role runs on. An unknown role gets the lite
// model, because lite is the default preference.
func (r *Router) For(role Role) model.LLM {
	if tier, routed := r.policy[role]; routed {
		return r.tiers[tier]
	}
	return r.tiers[Lite]
}

// Tier names the tier a role runs on, for logs and tests.
func (r *Router) Tier(role Role) Tier {
	if tier, routed := r.policy[role]; routed {
		return tier
	}
	return Lite
}
