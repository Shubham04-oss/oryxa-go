package runtime

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func TestLegacyPolicyCanBeExplicitlyRepairedAfterValidationTightens(t *testing.T) {
	previous, err := spec.ParseStored([]byte("apiVersion: oryxa.in/v1\nkind: Workspace\nmetadata: {organization: obsolete_slug}\norganization: {name: ''}\nrules: {consent: {expireAfter: 99999h}}\nchat: {future: true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := MergePolicies(previous, contextDocument(), false); err == nil || !strings.Contains(err.Error(), "expireAfter") {
		t.Fatalf("invalid retained policy escaped effective validation: %v", err)
	}
	submitted := contextDocument()
	submitted.Unset = []string{"rules.consent.expireAfter"}
	effective, plan, err := MergePolicies(previous, submitted, false)
	if err != nil || effective.Rules != nil || effective.Chat != nil || len(plan.Changes) != 1 || plan.Changes[0].Name != "rules.consent.expireAfter" {
		t.Fatalf("legacy repair = %+v %+v, %v", effective, plan, err)
	}
}

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
	if err != nil || effective.Safety == nil || effective.Safety.Guard == nil || effective.Safety.Guard.HoldForReview == nil || !*effective.Safety.Guard.HoldForReview || len(plan.Changes) != 2 {
		t.Fatalf("regulated policy = %+v %+v %v", effective.Safety, plan, err)
	}
}

func TestAnswerGuardPoliciesArePreservedReplacedAndPlanned(t *testing.T) {
	owner, peer, relevance := "secrets", "strict", "shadow"
	previous := contextDocument()
	previous.Safety = &spec.Safety{Guard: &spec.GuardSafety{
		PII: &spec.GuardPII{Minimum: &spec.GuardPIIMinimum{Owner: &owner, Peer: &peer}}, Relevance: &relevance,
	}}
	// A file that says nothing about them keeps them, and plans nothing.
	effective, plan, err := MergePolicies(previous, contextDocument(), false)
	if err != nil || len(plan.Changes) != 0 || effective.Safety == nil || effective.Safety.Guard == nil ||
		effective.Safety.Guard.PII == nil || effective.Safety.Guard.PII.Minimum == nil ||
		*effective.Safety.Guard.PII.Minimum.Owner != "secrets" || *effective.Safety.Guard.PII.Minimum.Peer != "strict" ||
		effective.Safety.Guard.Relevance == nil || *effective.Safety.Guard.Relevance != "shadow" {
		t.Fatalf("omitted answer guard policies: %+v %+v %v", effective.Safety, plan, err)
	}
	// A reviewed rollback replaces them exactly: with nothing.
	effective, plan, err = MergePolicies(previous, contextDocument(), true)
	if err != nil || effective.Safety != nil || len(plan.Changes) != 3 {
		t.Fatalf("rolled back answer guard policies: %+v %+v %v", effective.Safety, plan, err)
	}
	// Changing one plans that one, and keeps the others.
	loosened := "secrets"
	submitted := contextDocument()
	submitted.Safety = &spec.Safety{Guard: &spec.GuardSafety{PII: &spec.GuardPII{Minimum: &spec.GuardPIIMinimum{Peer: &loosened}}}}
	effective, plan, err = MergePolicies(previous, submitted, false)
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Name != "safety.guard.pii.minimum.peer" ||
		*effective.Safety.Guard.PII.Minimum.Peer != "secrets" || *effective.Safety.Guard.PII.Minimum.Owner != "secrets" ||
		*effective.Safety.Guard.Relevance != "shadow" {
		t.Fatalf("changed peer minimum: %+v %+v %v", effective.Safety, plan, err)
	}
	// holdForReview alongside them is still carried.
	hold := true
	submitted = contextDocument()
	submitted.Safety = &spec.Safety{Guard: &spec.GuardSafety{HoldForReview: &hold}}
	effective, plan, err = MergePolicies(previous, submitted, false)
	if err != nil || len(plan.Changes) != 1 || plan.Changes[0].Name != "safety.guard.holdForReview" ||
		effective.Safety.Guard.HoldForReview == nil || !*effective.Safety.Guard.HoldForReview || effective.Safety.Guard.Relevance == nil {
		t.Fatalf("hold with the others: %+v %+v %v", effective.Safety, plan, err)
	}
}

func TestExplicitUnsetResetsOnlyNamedPoliciesAndPreservesGuardFields(t *testing.T) {
	previous := contextDocument()
	offer := []string{"gmail"}
	expiry, wake, peer := "6h", "30m", "strict"
	previous.Tools = &spec.Tools{Offer: &offer, WakeUpWithin: &wake}
	previous.Rules = &spec.Rules{Consent: &spec.ConsentRules{ExpireAfter: &expiry}}
	previous.Safety = &spec.Safety{Guard: &spec.GuardSafety{PII: &spec.GuardPII{Minimum: &spec.GuardPIIMinimum{Peer: &peer}}}}
	submitted := contextDocument()
	submitted.Unset = []string{"tools.offer", "tools.wakeUpWithin", "rules.consent.expireAfter"}
	effective, plan, err := MergePolicies(previous, submitted, false)
	if err != nil || len(plan.Changes) != 3 || effective.Tools != nil || effective.Rules != nil || len(effective.Unset) != 0 || *effective.Safety.Guard.PII.Minimum.Peer != "strict" {
		t.Fatalf("unset result: %+v %+v %v", effective, plan, err)
	}
	if previous.Tools.Offer == nil || previous.Rules.Consent.ExpireAfter == nil {
		t.Fatal("merge mutated previous saved document")
	}
	encoded, err := effective.YAML()
	if err != nil {
		t.Fatal(err)
	}
	pulled, err := spec.Parse(encoded)
	if err != nil {
		t.Fatal(err)
	}
	again, noop, err := MergePolicies(pulled, submitted, false)
	if err != nil || len(noop.Changes) != 0 || again.Tools != nil {
		t.Fatalf("unset did not converge: %+v %v", noop, err)
	}
}

func TestRegulatedPresetOmissionPreservesAndWeakeningIsSensitive(t *testing.T) {
	previous := contextDocument()
	previous.Preset = "regulated"
	hold := true
	previous.Safety = &spec.Safety{Guard: &spec.GuardSafety{HoldForReview: &hold}}
	effective, plan, err := MergePolicies(previous, contextDocument(), false)
	if err != nil || effective.Preset != "regulated" || len(plan.Changes) != 0 {
		t.Fatalf("omission weakened preset: %+v %+v %v", effective, plan, err)
	}
	submitted := contextDocument()
	submitted.Preset = "team"
	effective, plan, err = MergePolicies(previous, submitted, false)
	if err != nil || effective.Preset != "team" || len(SensitiveChanges(plan)) != 1 || SensitiveChanges(plan)[0].Name != "preset" {
		t.Fatalf("preset weakening lacked approval: %+v %v", plan, err)
	}
	effective, plan, err = MergePolicies(previous, contextDocument(), true)
	if err != nil || effective.Preset != "" || len(SensitiveChanges(plan)) != 2 {
		t.Fatalf("rollback must reset preset and review policy: %+v %v", plan, err)
	}
}
