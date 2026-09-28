package serviceconfig

import (
	"fmt"
	"strings"
	"testing"
)

// oryxaOn is what switching Oryxa's own sign-in on takes, where the web app
// calls apps through its own origin as web-next does.
func oryxaOn() map[string]string {
	values := required()
	values["APPS_ALLOWED_ORIGINS"] = "https://app.oryxa.test"
	values["APPS_ORYXA_TOOLKITS"] = "github, Slack ,github"
	values["APPS_SIGNIN_KMS_KEY_ID"] = "alias/oryxa-apps-signin"
	values["APPS_SIGNIN_REDIRECT_URI"] = "https://app.oryxa.test/v1/apps/oauth/callback"
	return values
}

func TestOryxaIsOffUnlessToolkitsAreNamed(t *testing.T) {
	t.Parallel()
	config, err := FromEnv(environment(required()))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.OryxaToolkits) != 0 || config.SigninKMSKeyID != "" || config.SigninRedirectURI != "" ||
		config.HookSecrets != nil || config.HookValues != nil || len(config.ServiceCallers) != 0 {
		t.Fatalf("config = %+v", config)
	}
}

func TestOryxaReadsWhatSwitchesItOn(t *testing.T) {
	t.Parallel()
	values := oryxaOn()
	values["APPS_HOOK_SECRETS"] = `{"slack": [" current-secret ", "previous-secret"], "notion": ["only"]}`
	values["APPS_HOOK_VALUES"] = `{"pubsub_push_account": " gmail-push@oryxa.iam.gserviceaccount.com "}`
	values["APPS_SERVICE_CALLERS"] = `[{"service":"operator","key_id":"alias/oryxa-operator","purposes":["hooks"]}]`

	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(config.OryxaToolkits, ",") != "github,slack" {
		t.Fatalf("toolkits = %v", config.OryxaToolkits)
	}
	if config.SigninKMSKeyID != "alias/oryxa-apps-signin" || config.SigninRedirectURI != "https://app.oryxa.test/v1/apps/oauth/callback" {
		t.Fatalf("config = %+v", config)
	}
	if slack := config.HookSecrets["slack"]; len(slack) != 2 || slack[0].Reveal() != "current-secret" || slack[1].Reveal() != "previous-secret" {
		t.Fatalf("slack secrets = %d", len(slack))
	}
	if config.HookValues["pubsub_push_account"] != "gmail-push@oryxa.iam.gserviceaccount.com" {
		t.Fatalf("values = %v", config.HookValues)
	}
	if len(config.ServiceCallers) != 1 || config.ServiceCallers[0].Service != "operator" {
		t.Fatalf("callers = %+v", config.ServiceCallers)
	}
	// However the config is printed, no secret shows.
	if printed := fmt.Sprintf("%v %+v %#v", config, config, config); strings.Contains(printed, "current-secret") {
		t.Fatal("printing the config shows a webhook secret")
	}
}

func TestOryxaRefusesToStartWithoutWhatItNeeds(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		change func(map[string]string)
		names  string
	}{
		"toolkits without a key": {
			change: func(values map[string]string) { delete(values, "APPS_SIGNIN_KMS_KEY_ID") },
			names:  "APPS_SIGNIN_KMS_KEY_ID",
		},
		"a key without a redirect URI": {
			change: func(values map[string]string) { delete(values, "APPS_SIGNIN_REDIRECT_URI") },
			names:  "APPS_SIGNIN_REDIRECT_URI",
		},
		"a toolkit that is not a slug": {
			change: func(values map[string]string) { values["APPS_ORYXA_TOOLKITS"] = "github,not a slug" },
			names:  "APPS_ORYXA_TOOLKITS",
		},
		"hook secrets that are not JSON": {
			change: func(values map[string]string) { values["APPS_HOOK_SECRETS"] = "slack=abc" },
			names:  "APPS_HOOK_SECRETS",
		},
		"three secrets for one toolkit": {
			change: func(values map[string]string) { values["APPS_HOOK_SECRETS"] = `{"slack":["a","b","c"]}` },
			names:  "APPS_HOOK_SECRETS",
		},
		"an empty secret": {
			change: func(values map[string]string) { values["APPS_HOOK_SECRETS"] = `{"slack":["  "]}` },
			names:  "APPS_HOOK_SECRETS",
		},
		"a hook value without a name": {
			change: func(values map[string]string) { values["APPS_HOOK_VALUES"] = `{"Push Account":"x"}` },
			names:  "APPS_HOOK_VALUES",
		},
		"callers that are not callers": {
			change: func(values map[string]string) { values["APPS_SERVICE_CALLERS"] = `[{"service":"operator"}]` },
			names:  "APPS_SERVICE_CALLERS",
		},
	} {
		t.Run(name, func(t *testing.T) {
			values := oryxaOn()
			test.change(values)
			_, err := FromEnv(environment(values))
			if err == nil || !strings.Contains(err.Error(), test.names) {
				t.Fatalf("error = %v, want it to name %s", err, test.names)
			}
		})
	}
	// A secret never appears in the message that refuses it.
	values := oryxaOn()
	values["APPS_HOOK_SECRETS"] = `{"Not A Toolkit":["super-secret-value"]}`
	if _, err := FromEnv(environment(values)); err == nil || strings.Contains(err.Error(), "super-secret-value") {
		t.Fatalf("error = %v", err)
	}
}

// The state cookie is set on the connect request's answer and sent back only
// to that host, so the callback must be where the browser sends the connect
// request: through the web app's own origin (web-next calls apps as
// app.oryxa.in/v1/…) or at apps' public host. A redirect URI anywhere else
// could never read its cookie, and apps refuses to start with one.
func TestOryxasRedirectURIMustBeWhereTheConnectRequestIsSent(t *testing.T) {
	t.Parallel()
	for uri, wantOK := range map[string]bool{
		"https://app.oryxa.test/v1/apps/oauth/callback":              true,
		"https://APP.oryxa.test:8443/v1/apps/oauth/callback":         true,
		"https://api.oryxa.test/v1/apps/oauth/callback":              true,
		"https://evil.example/v1/apps/oauth/callback":                false,
		"https://oryxa.test/v1/apps/oauth/callback":                  false,
		"https://app.oryxa.test/v1/apps/connect/callback":            false,
		"https://app.oryxa.test/v1/apps/oauth/callback/":             false,
		"https://app.oryxa.test/v1/apps/oauth/callback?x=1":          false,
		"https://app.oryxa.test/v1/apps/oauth/callback#frag":         false,
		"https://user@app.oryxa.test/v1/apps/oauth/callback":         false,
		"http://app.oryxa.test/v1/apps/oauth/callback":               false,
		"app.oryxa.test/v1/apps/oauth/callback":                      false,
		"ftp://app.oryxa.test/v1/apps/oauth/callback":                false,
		"https://app.oryxa.test.evil.example/v1/apps/oauth/callback": false,
	} {
		values := oryxaOn()
		values["APPS_SIGNIN_REDIRECT_URI"] = uri
		_, err := FromEnv(environment(values))
		if (err == nil) != wantOK {
			t.Errorf("%s: error = %v, want ok %v", uri, err, wantOK)
		}
	}
	// Developing locally, the web app's dev server proxies /v1 to apps on
	// another port of the same host; a cookie is a host's, not a port's.
	values := required()
	values["APPS_PUBLIC_BASE_URL"] = "http://localhost:8080"
	values["APPS_ALLOWED_ORIGINS"] = "http://localhost:3000"
	values["APPS_ORYXA_TOOLKITS"] = "github"
	values["APPS_SIGNIN_KMS_KEY_ID"] = "alias/local"
	values["APPS_SIGNIN_REDIRECT_URI"] = "http://localhost:3000/v1/apps/oauth/callback"
	if _, err := FromEnv(environment(values)); err != nil {
		t.Fatalf("local development = %v", err)
	}
}

func TestTheCallbackPathIsAppsOwn(t *testing.T) {
	t.Parallel()
	if SigninCallbackPath != "/v1/apps/oauth/callback" {
		t.Fatalf("callback path = %q", SigninCallbackPath)
	}
}

func TestOryxasOwnOAuthAppsComeFromConfiguration(t *testing.T) {
	t.Parallel()
	values := oryxaOn()
	values["APPS_DEFAULT_OAUTH_CLIENTS"] = `{"slack": {"client_id": "1.2", "client_secret": " slack-secret "}, "github": {"client_id": "Iv1.oryxa", "client_secret": "gh-secret"}}`
	config, err := FromEnv(environment(values))
	if err != nil {
		t.Fatal(err)
	}
	if len(config.DefaultOAuthClients) != 2 || config.DefaultOAuthClients[0].Client != "github" ||
		config.DefaultOAuthClients[1].ClientSecret.Reveal() != "slack-secret" {
		t.Fatalf("defaults = %+v", config.DefaultOAuthClients)
	}
	if printed := fmt.Sprintf("%v %+v", config, config); strings.Contains(printed, "gh-secret") {
		t.Fatal("printing the config shows a client secret")
	}

	for name, raw := range map[string]string{
		"not JSON":             `github=Iv1`,
		"no secret":            `{"github": {"client_id": "Iv1.oryxa"}}`,
		"not a registration":   `{"Git Hub": {"client_id": "Iv1.oryxa", "client_secret": "gh-secret"}}`,
		"an unknown field too": `{"github": ["Iv1.oryxa", "gh-secret"]}`,
	} {
		values := oryxaOn()
		values["APPS_DEFAULT_OAUTH_CLIENTS"] = raw
		_, err := FromEnv(environment(values))
		if err == nil || !strings.Contains(err.Error(), "APPS_DEFAULT_OAUTH_CLIENTS") || strings.Contains(err.Error(), "gh-secret") {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	// Their secrets are sealed, which takes the key.
	values = required()
	values["APPS_DEFAULT_OAUTH_CLIENTS"] = `{"github": {"client_id": "Iv1.oryxa", "client_secret": "gh-secret"}}`
	if _, err := FromEnv(environment(values)); err == nil || !strings.Contains(err.Error(), "APPS_SIGNIN_KMS_KEY_ID") {
		t.Fatalf("without a key = %v", err)
	}
}
