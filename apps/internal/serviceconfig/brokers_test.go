package serviceconfig

import (
	"maps"
	"strings"
	"testing"
)

func TestToolkitBrokersPinToolkitsToABroker(t *testing.T) {
	t.Parallel()
	values := required()
	values["APPS_TOOLKIT_BROKERS"] = " Linear=pipedream, github=COMPOSIO,notion=oryxa"
	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"linear": "pipedream", "github": "composio", "notion": "oryxa"}
	if !maps.Equal(config.ToolkitBrokers, want) {
		t.Fatalf("ToolkitBrokers = %v, want %v", config.ToolkitBrokers, want)
	}

	unset, err := FromEnv(environment(required()))
	if err != nil || unset.ToolkitBrokers != nil {
		t.Fatalf("unset = %v, %v", unset.ToolkitBrokers, err)
	}
}

func TestToolkitBrokersRefuseWhatTheyCannotRead(t *testing.T) {
	t.Parallel()
	for name, raw := range map[string]string{
		"no broker":         "linear",
		"an empty broker":   "linear=",
		"an unknown broker": "linear=nango",
		"not a toolkit":     "not a slug=pipedream",
		"a toolkit twice":   "linear=pipedream,linear=composio",
		"no toolkit at all": "=pipedream",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values := required()
			values["APPS_TOOLKIT_BROKERS"] = raw
			if _, err := FromEnv(environment(values)); err == nil || !strings.Contains(err.Error(), "APPS_TOOLKIT_BROKERS") {
				t.Fatalf("FromEnv() error = %v", err)
			}
		})
	}
}
