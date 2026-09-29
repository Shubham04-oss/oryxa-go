package runtime

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func contextDocument() spec.Document {
	return spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: "acme"}}
}

func contextState() Snapshot {
	return Snapshot{Members: map[string]Member{
		"chief@acme.com": {Email: "chief@acme.com"},
		"lead@acme.com":  {Email: "lead@acme.com"},
		"dev@acme.com":   {Email: "dev@acme.com"},
	}}
}

func TestContextMergeKeepsOmittedFactsAndPrunesOnlyWhenAsked(t *testing.T) {
	old := contextDocument()
	old.Ownership.Items = []spec.OwnerHint{{Owner: "dev@acme.com", System: "billing"}}
	old.OrgChart = &spec.OrgChart{Relationships: []spec.Relationship{{From: "lead@acme.com", Relation: "manages", To: "dev@acme.com"}}, Authority: []string{"manages"}}
	submitted := contextDocument()
	effective, plan, err := MergeContext(old, submitted, contextState(), false)
	if err != nil || len(plan.Changes) != 0 || len(effective.Ownership.Items) != 1 || len(effective.OrgChart.Relationships) != 1 {
		t.Fatalf("omitted facts: effective=%+v plan=%+v err=%v", effective, plan, err)
	}
	submitted.Ownership.Prune = true
	submitted.OrgChart = &spec.OrgChart{Prune: true}
	effective, plan, err = MergeContext(old, submitted, contextState(), false)
	if err != nil || len(plan.Changes) != 3 || len(effective.Ownership.Items) != 0 || len(effective.OrgChart.Relationships) != 0 {
		t.Fatalf("pruned facts: effective=%+v plan=%+v err=%v", effective, plan, err)
	}
	if !RemovalsDiffer(plan, Plan{}) || RemovalsDiffer(plan, plan) {
		t.Fatalf("removal approval did not detect context deletions: %+v", plan)
	}
}

func TestContextAuthorityUsesExplicitEdgesAndRejectsContradictions(t *testing.T) {
	chart := spec.OrgChart{
		Relationships: []spec.Relationship{
			{From: "chief@acme.com", Relation: "manages", To: "lead@acme.com"},
			{From: "lead@acme.com", Relation: "manages", To: "dev@acme.com"},
		},
		Authority: []string{"manages"}, Executives: []string{"chief@acme.com"},
	}
	if !Above(chart, "chief@acme.com", "dev@acme.com") || !Above(chart, "lead@acme.com", "dev@acme.com") || Above(chart, "dev@acme.com", "chief@acme.com") {
		t.Fatalf("wrong authority result: %+v", chart)
	}
	submitted := contextDocument()
	submitted.OrgChart = &chart
	if _, _, err := MergeContext(contextDocument(), submitted, contextState(), false); err != nil {
		t.Fatal(err)
	}
	chart.Relationships = append(chart.Relationships, spec.Relationship{From: "dev@acme.com", Relation: "manages", To: "chief@acme.com"})
	submitted.OrgChart = &chart
	if _, _, err := MergeContext(contextDocument(), submitted, contextState(), false); err == nil {
		t.Fatal("accepted contradictory executive authority")
	}
}

func TestContextRejectsPruningItsOwnerMember(t *testing.T) {
	old := contextDocument()
	old.Ownership.Items = []spec.OwnerHint{{Owner: "dev@acme.com", System: "billing"}}
	submitted := contextDocument()
	submitted.People = &spec.People{Prune: true, Members: []spec.Member{{Email: "chief@acme.com", Role: "owner"}}}
	if _, _, err := MergeContext(old, submitted, contextState(), false); err == nil {
		t.Fatal("accepted an ownership hint for a member being removed")
	}
}
