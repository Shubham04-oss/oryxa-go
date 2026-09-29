package runtime

import (
	"slices"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

// MergePolicies preserves omitted scalar policies and makes every submitted
// change visible in plan. A reviewed rollback replaces them exactly.
func MergePolicies(previous, submitted spec.Document, replace bool) (spec.Document, Plan, error) {
	effective := submitted
	plan := Plan{Changes: []Change{}, NeedsHuman: []HumanStep{}}
	var oldExpiry, nextExpiry *string
	if previous.Rules != nil && previous.Rules.Consent != nil {
		oldExpiry = previous.Rules.Consent.ExpireAfter
	}
	if submitted.Rules != nil && submitted.Rules.Consent != nil {
		nextExpiry = submitted.Rules.Consent.ExpireAfter
	}
	if !replace && nextExpiry == nil {
		nextExpiry = oldExpiry
	}
	if nextExpiry != nil {
		effective.Rules = &spec.Rules{Consent: &spec.ConsentRules{ExpireAfter: nextExpiry}}
	} else {
		effective.Rules = nil
	}
	if !equalString(oldExpiry, nextExpiry) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "rules.consent.expireAfter"})
	}
	var oldReview, nextReview *bool
	if previous.Safety != nil && previous.Safety.Guard != nil {
		oldReview = previous.Safety.Guard.HoldForReview
	}
	if submitted.Safety != nil && submitted.Safety.Guard != nil {
		nextReview = submitted.Safety.Guard.HoldForReview
	}
	if !replace && nextReview == nil {
		nextReview = oldReview
	}
	if !replace && nextReview == nil && submitted.Preset == "regulated" {
		required := true
		nextReview = &required
	}
	if nextReview != nil {
		effective.Safety = &spec.Safety{Guard: &spec.GuardSafety{HoldForReview: nextReview}}
	} else {
		effective.Safety = nil
	}
	if !equalBool(oldReview, nextReview) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "safety.guard.holdForReview"})
	}
	var oldProactive, nextProactive *string
	if previous.Agents != nil && previous.Agents.Personal != nil {
		oldProactive = previous.Agents.Personal.Proactive
	}
	if submitted.Agents != nil && submitted.Agents.Personal != nil {
		nextProactive = submitted.Agents.Personal.Proactive
	}
	if !replace && nextProactive == nil {
		nextProactive = oldProactive
	}
	if nextProactive != nil {
		if effective.Agents == nil {
			effective.Agents = &spec.Agents{}
		}
		effective.Agents.Personal = &spec.PersonalAgent{Proactive: nextProactive}
	} else if effective.Agents != nil {
		effective.Agents.Personal = nil
		if effective.Agents.Organization == nil {
			effective.Agents = nil
		}
	}
	if !equalString(oldProactive, nextProactive) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "agents.personal.proactive"})
	}
	var oldOffer, nextOffer *[]string
	if previous.Tools != nil {
		oldOffer = previous.Tools.Offer
	}
	if submitted.Tools != nil {
		nextOffer = submitted.Tools.Offer
	}
	if !replace && nextOffer == nil {
		nextOffer = oldOffer
	}
	if nextOffer != nil {
		if effective.Tools == nil {
			effective.Tools = &spec.Tools{}
		}
		effective.Tools.Offer = nextOffer
	} else if effective.Tools != nil {
		effective.Tools.Offer = nil
		if effective.Tools.Provider == "" && effective.Tools.AutoSubscribe == nil && effective.Tools.WakeUpWithin == nil {
			effective.Tools = nil
		}
	}
	if !equalList(oldOffer, nextOffer) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "tools.offer"})
	}
	var oldAutoSubscribe, nextAutoSubscribe *bool
	if previous.Tools != nil {
		oldAutoSubscribe = previous.Tools.AutoSubscribe
	}
	if submitted.Tools != nil {
		nextAutoSubscribe = submitted.Tools.AutoSubscribe
	}
	if !replace && nextAutoSubscribe == nil {
		nextAutoSubscribe = oldAutoSubscribe
	}
	if nextAutoSubscribe != nil {
		if effective.Tools == nil {
			effective.Tools = &spec.Tools{}
		}
		effective.Tools.AutoSubscribe = nextAutoSubscribe
	} else if effective.Tools != nil {
		effective.Tools.AutoSubscribe = nil
		if effective.Tools.Provider == "" && effective.Tools.Offer == nil && effective.Tools.WakeUpWithin == nil {
			effective.Tools = nil
		}
	}
	if !equalBool(oldAutoSubscribe, nextAutoSubscribe) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "tools.autoSubscribe"})
	}
	var oldWakeUp, nextWakeUp *string
	if previous.Tools != nil {
		oldWakeUp = previous.Tools.WakeUpWithin
	}
	if submitted.Tools != nil {
		nextWakeUp = submitted.Tools.WakeUpWithin
	}
	if !replace && nextWakeUp == nil {
		nextWakeUp = oldWakeUp
	}
	if nextWakeUp != nil {
		if effective.Tools == nil {
			effective.Tools = &spec.Tools{}
		}
		effective.Tools.WakeUpWithin = nextWakeUp
	} else if effective.Tools != nil {
		effective.Tools.WakeUpWithin = nil
		if effective.Tools.Provider == "" && effective.Tools.Offer == nil && effective.Tools.AutoSubscribe == nil {
			effective.Tools = nil
		}
	}
	if !equalString(oldWakeUp, nextWakeUp) {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "tools.wakeUpWithin"})
	}
	var oldSearchMode, nextSearchMode string
	if previous.Search != nil {
		oldSearchMode = previous.Search.Mode
	}
	if submitted.Search != nil {
		nextSearchMode = submitted.Search.Mode
	}
	if !replace && nextSearchMode == "" {
		nextSearchMode = oldSearchMode
	}
	if nextSearchMode != "" {
		effective.Search = &spec.Search{Mode: nextSearchMode}
	} else {
		effective.Search = nil
	}
	if oldSearchMode != nextSearchMode {
		plan.Changes = append(plan.Changes, Change{Op: "update", Kind: "policy", Name: "search.mode"})
	}
	return effective, plan, effective.Validate()
}

func equalList(a, b *[]string) bool {
	return a == nil && b == nil || a != nil && b != nil && slices.Equal(*a, *b)
}

func equalString(a, b *string) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}

func equalBool(a, b *bool) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
