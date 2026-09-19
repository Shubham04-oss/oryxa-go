package pricing

import (
	"testing"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

func TestDefaultTablePricesTheDefaultModels(t *testing.T) {
	table, err := NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	usage := agentsv1.Usage{InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000}
	cost, prices, priced := table.Cost("gemini-3.5-flash-lite", usage)
	if !priced {
		t.Fatal("the default model is not priced")
	}
	if cost.InputMicros != 300_000 || cost.OutputMicros != 2_500_000 || cost.CachedMicros != 0 || cost.TotalMicros != 2_800_000 {
		t.Fatalf("cost = %+v", cost)
	}
	if prices.Currency != "USD" || prices.InputPerMillionMicros != 300_000 || prices.OutputPerMillionMicros != 2_500_000 || prices.CachedPerMillionMicros != 30_000 {
		t.Fatalf("prices = %+v", prices)
	}
}

func TestCostCountsCachedTokensAtTheCachedRate(t *testing.T) {
	table, err := NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	usage := agentsv1.Usage{InputTokens: 1_000_000, CachedTokens: 400_000, OutputTokens: 0, TotalTokens: 1_000_000}
	cost, _, priced := table.Cost("gemini-3.5-flash-lite", usage)
	if !priced {
		t.Fatal("the default model is not priced")
	}
	// 600k fresh at $0.30 + 400k cached at $0.03 = $0.18 + $0.012.
	if cost.InputMicros != 180_000 || cost.CachedMicros != 12_000 || cost.TotalMicros != 192_000 {
		t.Fatalf("cost = %+v", cost)
	}
}

func TestCostClampsCachedTokensToInput(t *testing.T) {
	table, err := NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	cost, _, _ := table.Cost("gemini-3.5-flash-lite", agentsv1.Usage{InputTokens: 100, CachedTokens: 500})
	if cost.InputMicros != 0 || cost.TotalMicros != cost.CachedMicros {
		t.Fatalf("cost = %+v", cost)
	}
}

func TestUnknownModelIsNotPriced(t *testing.T) {
	table, err := NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, priced := table.Cost("mystery-model", agentsv1.Usage{}); priced {
		t.Fatal("an unknown model was priced")
	}
}

func TestOverridesReplaceAndAddRates(t *testing.T) {
	overrides, err := ParseOverrides(`{"gemini-3.5-flash-lite":{"input":1.00,"output":4.00,"cached":0.25},"custom-model":{"input":0.50}}`)
	if err != nil {
		t.Fatal(err)
	}
	table, err := NewTable(overrides)
	if err != nil {
		t.Fatal(err)
	}
	rate, priced := table.Rates("gemini-3.5-flash-lite")
	if !priced || rate.InputMicros != 1_000_000 || rate.OutputMicros != 4_000_000 || rate.CachedMicros != 250_000 {
		t.Fatalf("flash-lite rates = %+v (priced %t)", rate, priced)
	}
	rate, priced = table.Rates("custom-model")
	if !priced || rate.InputMicros != 500_000 || rate.CachedMicros != 0 {
		t.Fatalf("custom rates = %+v (priced %t)", rate, priced)
	}
}

func TestParseOverridesRejectsBadInput(t *testing.T) {
	if _, err := ParseOverrides("not json"); err == nil {
		t.Fatal("ParseOverrides() error = nil for a non-JSON value")
	}
	if _, err := ParseOverrides(`{"model":{"input":-1}}`); err == nil {
		t.Fatal("ParseOverrides() error = nil for a negative rate")
	}
	if _, err := ParseOverrides(`{"":{"input":1}}`); err == nil {
		t.Fatal("ParseOverrides() error = nil for an empty model name")
	}
	empty, err := ParseOverrides("  ")
	if err != nil || empty != nil {
		t.Fatalf("ParseOverrides(empty) = %v, %v", empty, err)
	}
}
