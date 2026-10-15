package serviceconfig

import "testing"

func env(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestOnlyTrueSwitchesTheServiceOn(t *testing.T) {
	for value, want := range map[string]bool{"": false, "false": false, "1": false, "yes": false, "true": true, " TRUE ": true} {
		if got := Enabled(env(map[string]string{"INTEGRATIONS_ENABLED": value})); got != want {
			t.Errorf("INTEGRATIONS_ENABLED=%q: enabled is %v, want %v", value, got, want)
		}
	}
}

func TestOffReadsNothing(t *testing.T) {
	config, err := FromEnv(env(map[string]string{"INTEGRATIONS_SERVICE_CALLERS": "not json"}))
	if err != nil || len(config.ServiceCallers) != 0 {
		t.Fatalf("off, config = %+v, err = %v; want it unread", config, err)
	}
}

func TestOnReadsCallersAndRefusesMalformedOnes(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"INTEGRATIONS_ENABLED":         "true",
		"INTEGRATIONS_SERVICE_CALLERS": `[{"service":"apps","key_id":"alias/apps","purposes":["connect"]}]`,
	}))
	if err != nil || len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "apps" {
		t.Fatalf("config = %+v, err = %v", config, err)
	}
	if _, err := FromEnv(env(map[string]string{"INTEGRATIONS_ENABLED": "true", "INTEGRATIONS_SERVICE_CALLERS": "not json"})); err == nil {
		t.Fatal("malformed callers were accepted")
	}
}
