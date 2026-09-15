// Package pricing turns a model's token usage into what it cost, in
// millionths of a US dollar, from a table of rates per million tokens. Rates
// default to the known Gemini tiers and can be overridden per model from
// configuration, so a price change is a deployment change, not a code change.
package pricing

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
)

// Rates are what a model charges per million tokens, in micros of a US
// dollar. A micro is a millionth, so $0.30 per million is 300_000 micros.
type Rates struct {
	InputMicros  int64
	OutputMicros int64
	CachedMicros int64
}

// DefaultRates are the prices the configured default models are billed at.
var DefaultRates = map[string]Rates{
	"gemini-3.8-flash":      {InputMicros: 300_000, OutputMicros: 2_500_000, CachedMicros: 75_000},
	"gemini-3.8-flash-lite": {InputMicros: 100_000, OutputMicros: 400_000, CachedMicros: 25_000},
}

// Table prices models, applying override rates over the defaults.
type Table struct {
	rates map[string]Rates
}

// NewTable returns a table of the defaults with the overrides applied.
func NewTable(overrides map[string]Rates) (*Table, error) {
	rates := make(map[string]Rates, len(DefaultRates)+len(overrides))
	for model, rate := range DefaultRates {
		rates[model] = rate
	}
	for model, rate := range overrides {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, errors.New("a price override needs a model name")
		}
		if rate.InputMicros < 0 || rate.OutputMicros < 0 || rate.CachedMicros < 0 {
			return nil, fmt.Errorf("the price override for %q has a negative rate", model)
		}
		rates[model] = rate
	}
	return &Table{rates: rates}, nil
}

// Rates returns the rates of a model.
func (table *Table) Rates(model string) (Rates, bool) {
	rate, priced := table.rates[model]
	return rate, priced
}

// Cost prices a usage. It reports false for a model the table does not know,
// so a caller never reports a wrong cost as a real one. Cached tokens are
// part of the input tokens and are billed at the cached rate instead.
func (table *Table) Cost(model string, usage agentsv1.Usage) (agentsv1.Cost, agentsv1.ModelPrices, bool) {
	rate, priced := table.Rates(model)
	if !priced {
		return agentsv1.Cost{}, agentsv1.ModelPrices{}, false
	}
	cached := usage.CachedTokens
	if cached > usage.InputTokens {
		cached = usage.InputTokens
	}
	fresh := usage.InputTokens - cached

	cost := agentsv1.Cost{
		InputMicros:  micros(fresh, rate.InputMicros),
		OutputMicros: micros(usage.OutputTokens, rate.OutputMicros),
		CachedMicros: micros(cached, rate.CachedMicros),
	}
	cost.TotalMicros = cost.InputMicros + cost.OutputMicros + cost.CachedMicros

	prices := agentsv1.ModelPrices{
		Currency:               "USD",
		InputPerMillionMicros:  rate.InputMicros,
		OutputPerMillionMicros: rate.OutputMicros,
		CachedPerMillionMicros: rate.CachedMicros,
	}
	return cost, prices, true
}

// micros prices tokens at rate per million, in micros.
func micros(tokens int32, rate int64) int64 {
	return int64(tokens) * rate / 1_000_000
}

// ParseOverrides reads a JSON object of model to USD-per-million rates, such
// as {"gemini-3.8-flash":{"input":0.30,"output":2.50,"cached":0.075}}.
func ParseOverrides(raw string) (map[string]Rates, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var parsed map[string]struct {
		Input  float64 `json:"input"`
		Output float64 `json:"output"`
		Cached float64 `json:"cached"`
	}
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil, fmt.Errorf("model prices must be a JSON object of per-million USD rates: %w", err)
	}
	overrides := make(map[string]Rates, len(parsed))
	for model, rate := range parsed {
		if model == "" {
			return nil, errors.New("a price override needs a model name")
		}
		if rate.Input < 0 || rate.Output < 0 || rate.Cached < 0 {
			return nil, fmt.Errorf("the price override for %q has a negative rate", model)
		}
		overrides[model] = Rates{
			InputMicros:  usdToMicros(rate.Input),
			OutputMicros: usdToMicros(rate.Output),
			CachedMicros: usdToMicros(rate.Cached),
		}
	}
	return overrides, nil
}

func usdToMicros(usd float64) int64 {
	return int64(usd*1_000_000 + 0.5)
}
