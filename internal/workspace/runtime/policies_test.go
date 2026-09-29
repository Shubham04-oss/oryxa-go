package runtime

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestPolicyOmissionPreservesAndRollbackReplaces(t *testing.T) {
	expiry := "6h"
	hold := true
	proactive := "off"
	offer := []string{"github"}
	autoSubscribe := false
	previous := contextDocument()
	previous.Rules = &spec.Rules{Consent: &spec.ConsentRules{ExpireAfter: &expiry}}
	previous.Safety = &spec.Safety{Guard: &spec.GuardSafety{HoldForReview: &hold}}
	previous.Agents = &spec.Agents{Personal: &spec.PersonalAgent{Proactive: &proactive}}
	previous.Tools = &spec.Tools{Offer: &offer, AutoSubscribe: &autoSubscribe}
	submitted := contextDocument()
	effective, plan, err := MergePolicies(previous, submitted, false)
	if err != nil || len(plan.Changes) != 0 || effective.Rules == nil || effective.Safety == nil || effective.Agents == nil || effective.Tools == nil {
		t.Fatalf("omitted policies: %+v %+v %v", effective, plan, err)
	}
	effective, plan, err = MergePolicies(previous, submitted, true)
	if err != nil || len(plan.Changes) != 5 || effective.Rules != nil || effective.Safety != nil || effective.Agents != nil || effective.Tools != nil {
		t.Fatalf("rollback policies: %+v %+v %v", effective, plan, err)
	}
}

func TestRegulatedPresetHoldsCrossMemberAnswersByDefault(t *testing.T) {
	document := contextDocument()
	document.Preset = "regulated"
	effective, plan, err := MergePolicies(contextDocument(), document, false)
	if err != nil || effective.Safety == nil || effective.Safety.Guard == nil || effective.Safety.Guard.HoldForReview == nil || !*effective.Safety.Guard.HoldForReview || len(plan.Changes) != 1 {
		t.Fatalf("regulated policy = %+v %+v %v", effective.Safety, plan, err)
	}
}
