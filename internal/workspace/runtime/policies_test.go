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
	employeeConnections := false
	wakeUpWithin := "30m"
	previous := contextDocument()
	previous.Rules = &spec.Rules{Consent: &spec.ConsentRules{ExpireAfter: &expiry}}
	previous.Safety = &spec.Safety{Guard: &spec.GuardSafety{HoldForReview: &hold}}
	previous.Agents = &spec.Agents{Personal: &spec.PersonalAgent{Proactive: &proactive}}
	previous.Tools = &spec.Tools{Offer: &offer, EmployeeConnections: &employeeConnections, AutoSubscribe: &autoSubscribe, WakeUpWithin: &wakeUpWithin}
	previous.Search = &spec.Search{Mode: "native"}
	submitted := contextDocument()
	effective, plan, err := MergePolicies(previous, submitted, false)
	if err != nil || len(plan.Changes) != 0 || effective.Rules == nil || effective.Safety == nil || effective.Agents == nil || effective.Tools == nil || effective.Search == nil || effective.Search.Mode != "native" {
		t.Fatalf("omitted policies: %+v %+v %v", effective, plan, err)
	}
	effective, plan, err = MergePolicies(previous, submitted, true)
	if err != nil || len(plan.Changes) != 8 || effective.Rules != nil || effective.Safety != nil || effective.Agents != nil || effective.Tools != nil || effective.Search != nil {
		t.Fatalf("rollback policies: %+v %+v %v", effective, plan, err)
	}
}

func TestSearchModeChangeAppearsInPlan(t *testing.T) {
	submitted := contextDocument()
	submitted.Search = &spec.Search{Mode: "none"}
	effective, plan, err := MergePolicies(contextDocument(), submitted, false)
	if err != nil || effective.Search == nil || effective.Search.Mode != "none" || len(plan.Changes) != 1 || plan.Changes[0].Name != "search.mode" {
		t.Fatalf("search plan = %+v %+v %v", effective.Search, plan, err)
	}
}

func TestWakeUpWithinChangeAppearsInPlan(t *testing.T) {
	wake := "20m"
	submitted := contextDocument()
	submitted.Tools = &spec.Tools{WakeUpWithin: &wake}
	effective, plan, err := MergePolicies(contextDocument(), submitted, false)
	if err != nil || effective.Tools == nil || effective.Tools.WakeUpWithin == nil || *effective.Tools.WakeUpWithin != "20m" || len(plan.Changes) != 1 || plan.Changes[0].Name != "tools.wakeUpWithin" {
		t.Fatalf("wake-up plan = %+v %+v %v", effective.Tools, plan, err)
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
