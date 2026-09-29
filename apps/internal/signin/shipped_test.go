package signin

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations"
)

// rehost moves every URL of a manifest's sign-in onto the stub, keeping the
// app's host as the first path segment: https://api.github.com/user becomes
// {base}/api.github.com/user.
func rehost(manifest integration.Manifest, base string) integration.Manifest {
	move := func(raw string) string {
		if raw == "" {
			return ""
		}
		parsed, err := url.Parse(raw)
		if err != nil {
			panic(err)
		}
		return base + "/" + parsed.Host + parsed.Path
	}
	manifest.APIBase = move(manifest.APIBase)
	methods := make([]integration.Method, len(manifest.Auth.Methods))
	for index, method := range manifest.Auth.Methods {
		if method.OAuth2 != nil {
			oauth := *method.OAuth2
			oauth.AuthURL, oauth.TokenURL, oauth.RevokeURL = move(oauth.AuthURL), move(oauth.TokenURL), move(oauth.RevokeURL)
			method.OAuth2 = &oauth
		}
		methods[index] = method
	}
	manifest.Auth.Methods = methods
	return manifest
}

// answering builds a test call's answer in which each pointer resolves to
// its value.
func answering(values map[string]string) map[string]any {
	document := map[string]any{}
	for pointer, value := range values {
		node := document
		tokens := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
		for _, token := range tokens[:len(tokens)-1] {
			next, ok := node[token].(map[string]any)
			if !ok {
				next = map[string]any{}
				node[token] = next
			}
			node = next
		}
		node[tokens[len(tokens)-1]] = value
	}
	return document
}

func pathOf(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		panic(err)
	}
	return parsed.Path
}

// TestShippedManifestsSignInAndRevoke runs each shipped integration's own
// sign-in section through signin against the stub: its client
// authentication, PKCE or not, scopes and their separator, fixed
// authorization parameters, test call headers, label and ref pointers, and
// revoke style.
func TestShippedManifestsSignInAndRevoke(t *testing.T) {
	t.Parallel()
	for _, shipped := range integrations.All() {
		manifest := shipped.Manifest()
		t.Run(manifest.Toolkit, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, func(base string) integration.Manifest { return rehost(shipped.Manifest(), base) })
			moved := rehost(manifest, h.app.base)
			method := moved.Auth.Methods[0]
			oauth := method.OAuth2
			test := moved.Auth.Test
			label, ref := "Acme Workspace", "U0123"
			answer := map[string]string{test.Label: label}
			switch test.AccountRef {
			case "":
			case test.Label:
				// Google's address is both.
				ref = label
			default:
				answer[test.AccountRef] = ref
			}
			testPath := pathOf(moved.APIBase) + strings.SplitN(test.Path, "?", 2)[0]
			h.app.set(func(a *stubApp) {
				a.clientAuth, a.rotating = oauth.ClientAuth, oauth.Rotating
				a.meBody, a.requireHeaders = answering(answer), test.Headers
				a.fallback = func(w http.ResponseWriter, r *http.Request) {
					switch path := r.URL.Path; {
					case r.Method == http.MethodPost && path == pathOf(oauth.TokenURL):
						a.token(w, r)
					case r.Method == http.MethodGet && path == testPath:
						a.me(w, r)
					case oauth.RevokeURL != "" && path == pathOf(oauth.RevokeURL):
						a.revoke(w, r)
					case r.Method == http.MethodDelete && strings.HasPrefix(path, pathOf(moved.APIBase)+"/applications/"):
						a.revoke(w, r)
					default:
						t.Errorf("the stub was asked %s %s", r.Method, path)
						http.NotFound(w, r)
					}
				}
			})

			started := h.start("con_1", manifest.Toolkit)
			query := mustQuery(t, started.AuthorizationURL)
			separator := oauth.ScopeSep
			if separator == "" {
				separator = " "
			}
			if got, want := query.Get("scope"), strings.Join(oauth.Scopes, separator); got != want {
				t.Errorf("scope = %q, want %q", got, want)
			}
			if pkce := query.Get("code_challenge") != ""; pkce != oauth.PKCE {
				t.Errorf("PKCE sent = %v, manifest says %v", pkce, oauth.PKCE)
			}
			for name, value := range oauth.AuthParams {
				if query.Get(name) != value {
					t.Errorf("auth param %s = %q, want %q", name, query.Get(name), value)
				}
			}
			if !strings.HasPrefix(started.AuthorizationURL, oauth.AuthURL+"?") {
				t.Errorf("authorization URL = %s, want it under %s", started.AuthorizationURL, oauth.AuthURL)
			}

			code, state := h.app.authorize(t, started.AuthorizationURL)
			completion, err := h.service.CompleteOAuth(context.Background(), Callback{State: state, CookieState: started.State, Code: code})
			if err != nil {
				t.Fatalf("CompleteOAuth() = %v", err)
			}
			if completion.Account.Label != label || (test.AccountRef != "" && completion.Account.Ref != ref) {
				t.Fatalf("account = %+v, want %s / %s", completion.Account, label, ref)
			}
			if form := h.app.tokenForm(0); form.Has("code_verifier") != oauth.PKCE {
				t.Errorf("the code exchange sent a verifier = %v, manifest says PKCE %v", form.Has("code_verifier"), oauth.PKCE)
			}

			h.store.setStatus("con_1", "active")
			revocation, err := h.service.Revoke(context.Background(), "con_1")
			if err != nil || !revocation.ProviderRevoked {
				t.Fatalf("Revoke() = %+v, %v", revocation, err)
			}
			seen := h.app.seenRevocations()
			if len(seen) != 1 {
				t.Fatalf("revocations = %+v, want one", seen)
			}
			switch oauth.RevokeStyle {
			case integration.RevokeGitHubToken:
				if seen[0].method != http.MethodDelete || seen[0].basicID != defaultClientID {
					t.Errorf("github_token revoked with %+v", seen[0])
				}
			case integration.RevokeSlackAuthRevoke:
				if !strings.HasPrefix(seen[0].bearer, "at-") {
					t.Errorf("slack_auth_revoke revoked with %+v", seen[0])
				}
			case integration.RevokeRFC7009JSON:
				if !strings.HasPrefix(seen[0].body["token"], "at-") || seen[0].basicID != defaultClientID {
					t.Errorf("rfc7009_json revoked with %+v", seen[0])
				}
			case integration.RevokeRFC7009:
				if !strings.HasPrefix(seen[0].form.Get("token"), "rt-") || seen[0].form.Get("token_type_hint") != "refresh_token" {
					t.Errorf("rfc7009 revoked with %+v", seen[0])
				}
			default:
				t.Errorf("revoke style %q is not covered", oauth.RevokeStyle)
			}
		})
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Query()
}
