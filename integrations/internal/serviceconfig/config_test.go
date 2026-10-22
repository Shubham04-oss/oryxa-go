package serviceconfig

import (
	"strings"
	"testing"
)

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

func TestOnReadsTheKeyAndOryxasAppsAndNeverEchoesTheirSecrets(t *testing.T) {
	on := map[string]string{
		"INTEGRATIONS_ENABLED":    "true",
		"INTEGRATIONS_KMS_KEY_ID": " alias/integrations ",
		"INTEGRATIONS_ORYXA_APPS": `[{"app":"github","client_id":"Iv1.abc","secret":"s3cret-value","extra":{"app_id":"7"}}]`,
	}
	config, err := FromEnv(env(on))
	if err != nil || config.KMSKeyID != "alias/integrations" || len(config.OryxaApps) != 1 ||
		config.OryxaApps[0].Secret.Reveal() != "s3cret-value" || config.OryxaApps[0].Extra["app_id"] != "7" {
		t.Fatalf("config = %+v, err = %v", config, err)
	}
	for _, bad := range []string{`not json`, `[{"app":"github","secret":"s3cret-value","surprise":1}]`} {
		on["INTEGRATIONS_ORYXA_APPS"] = bad
		_, err := FromEnv(env(on))
		if err == nil || strings.Contains(err.Error(), "s3cret-value") {
			t.Fatalf("apps %q: err = %v; want a refusal that does not quote the value", bad, err)
		}
	}
}

func TestOnReadsWhereBrowsersReachTheServiceAndWhereTheyMayReturnTo(t *testing.T) {
	config, err := FromEnv(env(map[string]string{
		"INTEGRATIONS_ENABLED":        "true",
		"INTEGRATIONS_PUBLIC_URL":     " https://integrations.example.com/ ",
		"INTEGRATIONS_RETURN_ORIGINS": "https://app.example.com/, ,https://staging.example.com",
	}))
	if err != nil || config.PublicURL != "https://integrations.example.com" ||
		len(config.ReturnOrigins) != 2 || config.ReturnOrigins[0] != "https://app.example.com" || config.ReturnOrigins[1] != "https://staging.example.com" {
		t.Fatalf("config = %+v, err = %v", config, err)
	}
}

func TestOnReadsConnectorsEgressKeysAndTheBrokerAddress(t *testing.T) {
	key := strings.Repeat("s", 32)
	config, err := FromEnv(env(map[string]string{
		"INTEGRATIONS_ENABLED":        "true",
		"INTEGRATIONS_CONNECTORS_DIR": " /connectors ",
		"INTEGRATIONS_EGRESS_KEYS":    `{"current":"k1","keys":{"k1":"` + key + `"}}`,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if config.ConnectorsDir != "/connectors" || config.EgressKeys.Current != "k1" || config.BrokerAddress != "127.0.0.1:8091" {
		t.Fatalf("config = %+v", config)
	}
	for name, raw := range map[string]string{
		"not JSON":   key,
		"short key":  `{"current":"k1","keys":{"k1":"short"}}`,
		"no current": `{"current":"k2","keys":{"k1":"` + key + `"}}`,
	} {
		_, err := FromEnv(env(map[string]string{"INTEGRATIONS_ENABLED": "true", "INTEGRATIONS_EGRESS_KEYS": raw}))
		if err == nil {
			t.Errorf("%s: accepted", name)
		} else if strings.Contains(err.Error(), key) {
			t.Errorf("%s: the error quotes the key: %v", name, err)
		}
	}
}
