package domain

import (
	"errors"
	"testing"
)

func TestCheckBrokerKnowsEveryBroker(t *testing.T) {
	t.Parallel()
	for given, want := range map[string]string{
		"": BrokerComposio, " Composio ": BrokerComposio, "oryxa": BrokerOryxa, "PIPEDREAM": BrokerPipedream,
	} {
		if got, err := CheckBroker(given); err != nil || got != want {
			t.Errorf("CheckBroker(%q) = %q, %v, want %q", given, got, err, want)
		}
	}
	if _, err := CheckBroker("nango"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("CheckBroker(nango) error = %v, want invalid", err)
	}
}

// Composio's paths are guarded on Composio(), so it must be true for
// Composio's connections alone, the ones from before brokers included.
func TestOnlyComposiosConnectionsAreComposios(t *testing.T) {
	t.Parallel()
	for broker, want := range map[string]bool{
		"": true, BrokerComposio: true, BrokerOryxa: false, BrokerPipedream: false, "nango": false,
	} {
		if got := (Connection{Broker: broker}).Composio(); got != want {
			t.Errorf("Connection{Broker: %q}.Composio() = %v, want %v", broker, got, want)
		}
	}
}
