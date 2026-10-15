package serviceconfig

import (
	"slices"
	"strings"
	"testing"
)

func TestNativeToolkitsAreReadWithTheirService(t *testing.T) {
	t.Parallel()
	values := required()
	values["APPS_NATIVE_TOOLKITS"] = " GitHub, notion,github"
	values["APPS_INTEGRATIONS_URL"] = "https://integrations.oryxa.in/"
	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"github", "notion"}; !slices.Equal(config.NativeToolkits, want) || config.IntegrationsURL != "https://integrations.oryxa.in" {
		t.Fatalf("native = %v at %q", config.NativeToolkits, config.IntegrationsURL)
	}

	unset, err := FromEnv(environment(required()))
	if err != nil || unset.NativeToolkits != nil || unset.IntegrationsURL != "" {
		t.Fatalf("unset = %v, %q, %v", unset.NativeToolkits, unset.IntegrationsURL, err)
	}
}

func TestNativeToolkitsRefuseWhatTheyCannotUse(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct{ toolkits, url, want string }{
		"toolkits without a service": {"github", "", "APPS_INTEGRATIONS_URL"},
		"not a toolkit":              {"not a slug", "https://integrations.oryxa.in", "APPS_NATIVE_TOOLKITS"},
		"a plain http service":       {"github", "http://integrations.internal", "APPS_INTEGRATIONS_URL"},
		"a relative service":         {"", "/integrations", "APPS_INTEGRATIONS_URL"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			values := required()
			values["APPS_NATIVE_TOOLKITS"] = testCase.toolkits
			values["APPS_INTEGRATIONS_URL"] = testCase.url
			if _, err := FromEnv(environment(values)); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("FromEnv() error = %v, want one naming %s", err, testCase.want)
			}
		})
	}
	values := required()
	values["APPS_INTEGRATIONS_URL"] = "http://127.0.0.1:8080"
	if _, err := FromEnv(environment(values)); err != nil {
		t.Fatalf("loopback http refused: %v", err)
	}
}
