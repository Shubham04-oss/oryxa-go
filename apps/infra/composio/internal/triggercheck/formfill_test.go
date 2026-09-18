package triggercheck

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// These cases are the ones in web/src/lib/apps/client.test.ts, kept in step
// on purpose: this mirrors that rule so the catalog can be measured against
// it, and a measurement taken against a different rule would say nothing.
func TestFormFillableMatchesWhatTheWebAppCanCollect(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		schema string
		want   bool
	}{
		"no schema at all":                {schema: ``, want: true},
		"a schema naming no properties":   {schema: `{"type":"object","properties":{}}`, want: true},
		"flat scalars":                    {schema: `{"type":"object","properties":{"repo":{"type":"string"},"n":{"type":"integer"},"on":{"type":"boolean"}},"required":["repo","n","on"]}`, want: true},
		"a list of scalars":               {schema: `{"type":"object","properties":{"labels":{"type":"array","items":{"type":"string"}}},"required":["labels"]}`, want: true},
		"a list with no items described":  {schema: `{"type":"object","properties":{"labels":{"type":"array"}},"required":["labels"]}`, want: true},
		"an enum":                         {schema: `{"type":"object","properties":{"state":{"type":"string","enum":["open","closed"]}},"required":["state"]}`, want: true},
		"a list of enum values":           {schema: `{"type":"object","properties":{"s":{"type":"array","items":{"enum":["open","closed"]}}},"required":["s"]}`, want: true},
		"a nullable scalar":               {schema: `{"type":"object","properties":{"since":{"type":["string","null"]}},"required":["since"]}`, want: true},
		"one of several shapes":           {schema: `{"type":"object","properties":{"t":{"anyOf":[{"type":"string"},{"type":"integer"}]}},"required":["t"]}`, want: true},
		"an optional object, skipped":     {schema: `{"type":"object","properties":{"filter":{"type":"object"},"repo":{"type":"string"}},"required":["repo"]}`, want: true},
		"a schema that is not JSON":       {schema: `{"type":"object",`, want: true},
		"a required object":               {schema: `{"type":"object","properties":{"filter":{"type":"object"}},"required":["filter"]}`, want: false},
		"a required list of objects":      {schema: `{"type":"object","properties":{"rules":{"type":"array","items":{"type":"object"}}},"required":["rules"]}`, want: false},
		"a required field never declared": {schema: `{"type":"object","properties":{},"required":["missing"]}`, want: false},
		"a required reference":            {schema: `{"type":"object","properties":{"f":{"$ref":"#/$defs/F"}},"required":["f"]}`, want: false},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := formFillable(json.RawMessage(testCase.schema)); got != testCase.want {
				t.Fatalf("formFillable() = %v, want %v", got, testCase.want)
			}
		})
	}
}

// TestTheReportSaysHowMuchOfAnAppWorksWithoutAnybodyDoingAnything is what the
// counts are for: how much of a catalog a member gets by connecting, and how
// much the web app can offer them beyond that.
func TestTheReportSaysHowMuchOfAnAppWorksWithoutAnybodyDoingAnything(t *testing.T) {
	t.Parallel()
	catalog := &fakeCatalog{
		toolkits: []composio.Toolkit{{Slug: "mixed", TriggersCount: 4}},
	}
	catalog.triggers = map[string][]composio.TriggerType{
		"mixed": {
			// Subscribed by connecting: nothing to supply.
			{Slug: "MIXED_A", Toolkit: "mixed", Kind: "webhook", Config: json.RawMessage(`{"type":"object","properties":{}}`)},
			// A form can collect it, but a member has to.
			{Slug: "MIXED_B", Toolkit: "mixed", Kind: "webhook",
				Config: json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}},"required":["repo"]}`)},
			// A form can collect this one too, now that lists are offered.
			{Slug: "MIXED_C", Toolkit: "mixed", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"labels":{"type":"array","items":{"type":"string"}}},"required":["labels"]}`)},
			// Nothing a form can do: a required object.
			{Slug: "MIXED_D", Toolkit: "mixed", Kind: "poll",
				Config: json.RawMessage(`{"type":"object","properties":{"filter":{"type":"object"}},"required":["filter"]}`)},
		},
	}

	report, err := Run(context.Background(), catalog, Config{})
	if err != nil {
		t.Fatal(err)
	}
	if report.Triggers != 4 {
		t.Fatalf("triggers = %d", report.Triggers)
	}
	if report.AutoSubscribable != 1 {
		t.Fatalf("auto subscribable = %d, want the one needing nothing", report.AutoSubscribable)
	}
	if report.FormFillable != 3 {
		t.Fatalf("form fillable = %d, want all but the required object", report.FormFillable)
	}
	if len(report.NeedsSetup) != 1 || report.NeedsSetup[0].Trigger != "MIXED_D" {
		t.Fatalf("needs setup = %+v, want just the required object", report.NeedsSetup)
	}
	// None of that is a finding: these triggers all work, they just ask
	// different amounts of a member.
	if !report.Sound() {
		t.Fatalf("findings against a sound catalog: %+v", report.Findings)
	}
}
