package serviceconfig

import (
	"fmt"
	"strings"
	"testing"
)

const (
	slackSecretJSON = `{"client_id":"1234567890.0987654321","client_secret":"client-secret-0123456789abcdef","signing_secret":"signing-secret-0123456789abcdef"}`
)

// slackOn is what switching the Slack surface on takes: the sign-in's vault
// key, a service allowed the purpose slack, and the app's own settings.
func slackOn() map[string]string {
	values := oryxaOn()
	values["APPS_SLACK_SURFACE"] = "true"
	values["APPS_SLACK_APP"] = slackSecretJSON
	values["APPS_SERVICE_CALLERS"] = `[{"service":"agents","key_id":"alias/oryxa-agents","purposes":["slack"]}]`
	return values
}

func TestTheSlackSurfaceIsOffUnlessSwitchedOnAndThenReadsNothingElse(t *testing.T) {
	t.Parallel()
	config, err := FromEnv(environment(required()))
	if err != nil {
		t.Fatal(err)
	}
	if config.SlackSurface || config.SlackApp != (SlackApp{}) || config.SlackAPIBaseURL != "" {
		t.Fatalf("config = %+v", config)
	}
	// With it off, what the surface would read is not read, so a deployment
	// that has not adopted it is not stopped by it.
	for name, values := range map[string]map[string]string{
		"a bad secret":      {"APPS_SLACK_APP": "not json"},
		"a bad base URL":    {"APPS_SLACK_API_BASE_URL": "https://evil.example"},
		"an explicit false": {"APPS_SLACK_SURFACE": "false", "APPS_SLACK_APP": "not json"},
		"an empty switch":   {"APPS_SLACK_SURFACE": "  ", "APPS_SLACK_APP": "not json"},
	} {
		merged := required()
		for key, value := range values {
			merged[key] = value
		}
		config, err := FromEnv(environment(merged))
		if err != nil || config.SlackSurface || config.SlackApp != (SlackApp{}) {
			t.Errorf("%s: config = %+v, err = %v", name, config, err)
		}
	}
	// Something that is not a boolean is refused rather than taken for off.
	values := required()
	values["APPS_SLACK_SURFACE"] = "yes please"
	if _, err := FromEnv(environment(values)); err == nil || !strings.Contains(err.Error(), "APPS_SLACK_SURFACE") {
		t.Fatalf("a switch that is no boolean = %v", err)
	}
}

func TestTheSlackSurfaceReadsTheAppFromItsSecret(t *testing.T) {
	t.Parallel()
	values := slackOn()
	values["APPS_SLACK_APP"] = `{"client_id":" 1234567890.0987654321 ","client_secret":" client-secret-0123456789abcdef ",` +
		`"signing_secret":"signing-secret-0123456789abcdef","app_id":"A0IGNORED"}`
	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if !config.SlackSurface || config.SlackApp.ClientID != "1234567890.0987654321" ||
		config.SlackApp.ClientSecret.Reveal() != "client-secret-0123456789abcdef" ||
		config.SlackApp.SigningSecret.Reveal() != "signing-secret-0123456789abcdef" {
		t.Fatalf("app = %+v", config.SlackApp)
	}
	if config.SlackAPIBaseURL != "" {
		t.Fatalf("base URL = %q, want Slack itself", config.SlackAPIBaseURL)
	}
	// However the config is printed, no secret shows.
	printed := fmt.Sprintf("%v %+v %#v", config, config, config)
	if strings.Contains(printed, "client-secret-0123456789abcdef") || strings.Contains(printed, "signing-secret-0123456789abcdef") {
		t.Fatal("printing the config shows a Slack secret")
	}
}

func TestTheSlackSurfaceRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		change func(map[string]string)
		names  string
	}{
		"no app": {
			change: func(values map[string]string) { delete(values, "APPS_SLACK_APP") },
			names:  "APPS_SLACK_APP",
		},
		"an app that is not JSON": {
			change: func(values map[string]string) { values["APPS_SLACK_APP"] = "8f742231b10e8888abcd99yyyzzz85a5" },
			names:  "APPS_SLACK_APP",
		},
		"no client id": {
			change: func(values map[string]string) {
				values["APPS_SLACK_APP"] = `{"client_secret":"client-secret-0123456789abcdef","signing_secret":"signing-secret-0123456789abcdef"}`
			},
			names: "client_id",
		},
		"no signing secret": {
			change: func(values map[string]string) {
				values["APPS_SLACK_APP"] = `{"client_id":"1.2","client_secret":"client-secret-0123456789abcdef"}`
			},
			names: "signing_secret",
		},
		"a signing secret too short": {
			change: func(values map[string]string) {
				values["APPS_SLACK_APP"] = `{"client_id":"1.2","client_secret":"client-secret-0123456789abcdef","signing_secret":"short"}`
			},
			names: "signing_secret",
		},
		"a client secret with a space in it": {
			change: func(values map[string]string) {
				values["APPS_SLACK_APP"] = `{"client_id":"1.2","client_secret":"client secret 0123456789abcdef","signing_secret":"signing-secret-0123456789abcdef"}`
			},
			names: "client_secret",
		},
		"no vault key": {
			change: func(values map[string]string) {
				delete(values, "APPS_SIGNIN_KMS_KEY_ID")
				delete(values, "APPS_ORYXA_TOOLKITS")
				delete(values, "APPS_SIGNIN_REDIRECT_URI")
			},
			names: "APPS_SIGNIN_KMS_KEY_ID",
		},
		"no callers": {
			change: func(values map[string]string) { delete(values, "APPS_SERVICE_CALLERS") },
			names:  "APPS_SERVICE_CALLERS",
		},
		"callers with no slack purpose": {
			change: func(values map[string]string) {
				values["APPS_SERVICE_CALLERS"] = `[{"service":"operator","key_id":"alias/oryxa-operator","purposes":["hooks"]}]`
			},
			names: "purpose slack",
		},
		"a public URL Slack cannot reach": {
			change: func(values map[string]string) { values["APPS_PUBLIC_BASE_URL"] = "http://api.oryxa.test" },
			names:  "APPS_PUBLIC_BASE_URL",
		},
		"an API base URL that is not loopback": {
			change: func(values map[string]string) { values["APPS_SLACK_API_BASE_URL"] = "https://slack.com/api" },
			names:  "APPS_SLACK_API_BASE_URL",
		},
		"an API base URL on a private address": {
			change: func(values map[string]string) { values["APPS_SLACK_API_BASE_URL"] = "http://10.0.0.5:8080/api" },
			names:  "APPS_SLACK_API_BASE_URL",
		},
	} {
		values := slackOn()
		test.change(values)
		_, err := FromEnv(environment(values))
		if err == nil {
			t.Errorf("%s: started", name)
			continue
		}
		if !strings.Contains(err.Error(), test.names) {
			t.Errorf("%s: error = %q, want it to name %s", name, err, test.names)
		}
		// No message quotes a secret.
		for _, secret := range []string{"client-secret-0123456789abcdef", "signing-secret-0123456789abcdef", "8f742231b10e8888abcd99yyyzzz85a5"} {
			if strings.Contains(err.Error(), secret) {
				t.Errorf("%s: the error quotes a secret: %v", name, err)
			}
		}
	}
}

func TestTheSlackAPIBaseURLIsLoopbackOrEmpty(t *testing.T) {
	t.Parallel()
	for raw, ok := range map[string]bool{
		"":                               true,
		"http://127.0.0.1:41234/api":     true,
		"http://localhost:9000/api/":     true,
		"http://[::1]:9000/api":          true,
		"https://slack.com/api":          false,
		"http://169.254.169.254/latest":  false,
		"http://localhost.evil.test/api": false,
	} {
		values := slackOn()
		values["APPS_SLACK_API_BASE_URL"] = raw
		config, err := FromEnv(environment(values))
		if (err == nil) != ok {
			t.Errorf("%q: err = %v, want ok = %v", raw, err, ok)
			continue
		}
		if ok && strings.HasSuffix(config.SlackAPIBaseURL, "/") {
			t.Errorf("%q: base URL = %q, want the trailing slash trimmed", raw, config.SlackAPIBaseURL)
		}
	}
	// A loopback public base URL is fine over http, for a local run.
	values := slackOn()
	values["APPS_PUBLIC_BASE_URL"] = "http://localhost:8080"
	values["APPS_SIGNIN_REDIRECT_URI"] = "http://localhost:8080/v1/apps/oauth/callback"
	if _, err := FromEnv(environment(values)); err != nil {
		t.Fatalf("a local run = %v", err)
	}
}
