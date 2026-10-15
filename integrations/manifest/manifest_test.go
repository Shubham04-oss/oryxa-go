package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// valid is the design's GitHub example (README §8.2), filled in where the
// design elides schemas.
const valid = `{
  "id": "github", "version": 1, "display_name": "GitHub",
  "connection_levels": ["personal", "org"],
  "auth": {
    "credential_modes": ["oryxa", "byo", "user_key"],
    "oauth2": { "authorization_url": "https://github.com/login/oauth/authorize",
                "token_url": "https://github.com/login/oauth/access_token", "pkce": false,
                "token_kinds": [{ "name": "user", "scopes": ["repo", "read:org"] }],
                "refresh": { "rotating": true } },
    "user_key": { "fields": {"type": "object", "properties": {"token": {"type": "string"}}},
                  "inject": {"header": "Authorization", "value": "Bearer {token}"} },
    "github_app": { "manifest_template": "github-app.json" }
  },
  "base_url": "https://api.github.com",
  "egress_allowlist": ["api.github.com"],
  "verify": { "path": "/user", "token_kind": "user" },
  "rate_limit": { "scope": "connection", "per_hour": 5000, "max_concurrency": 4,
                  "headers": { "remaining": "x-ratelimit-remaining", "reset": "x-ratelimit-reset", "retry_after": "retry-after" } },
  "tools": [{ "name": "github.create_issue", "effect": "write",
              "description": "Create an issue in a repository. Use when the member asks to file or track a bug or task.",
              "input_schema": {"type": "object", "required": ["title"], "properties": {"title": {"type": "string", "pattern": "\\S"}}},
              "output_schema": {"type": "object", "properties": {"number": {"type": "integer"}}} }],
  "streams": [{ "name": "issues", "modes": ["full", "incremental"], "schedule": "rate(15 minutes)",
                "partition": "repos", "deletes": "webhook", "lookback": "PT5M",
                "document": { "title": "/title", "body": "/body", "url": "/html_url",
                              "author": "/user/login", "updated_at": "/updated_at" },
                "acl": "repo_collaborators" }],
  "webhooks": { "verification": { "scheme": "hmac_sha256_github" },
                "routing": "org_app", "route_by": ["/installation/id"],
                "events": { "issues": { "stream": "issues", "id": "/issue/id", "delete_on": ["deleted"] } },
                "fallback_poll_s": 900 },
  "reauth_on": [401]
}`

func TestParseValid(t *testing.T) {
	m, err := Parse([]byte(valid))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.ID != "github" || len(m.Tools) != 1 || m.Streams[0].Deletes != "webhook" || m.ReauthOn[0].Status != 401 {
		t.Fatalf("decoded wrong: %+v", m)
	}
}

func TestParseInvalid(t *testing.T) {
	tests := []struct {
		name   string
		change func(m map[string]any)
		want   string
	}{
		{"bad id", func(m map[string]any) { m["id"] = "Git-Hub" }, "id:"},
		{"version zero", func(m map[string]any) { m["version"] = 0 }, "version:"},
		{"unknown level", func(m map[string]any) { m["connection_levels"] = []any{"workspace"} }, "connection_levels"},
		{"duplicate level", func(m map[string]any) { m["connection_levels"] = []any{"org", "org"} }, "listed twice"},
		{"platform credential mode", func(m map[string]any) { auth(m)["credential_modes"] = []any{"platform"} }, "credential_modes"},
		{"user_key mode without user_key", func(m map[string]any) { delete(auth(m), "user_key") }, `"user_key" needs auth.user_key`},
		{"oryxa mode without oauth", func(m map[string]any) {
			delete(auth(m), "oauth2")
			delete(auth(m), "github_app")
		}, "needs auth.oauth2 or auth.github_app"},
		{"http authorization url", func(m map[string]any) { oauth(m)["authorization_url"] = "http://github.com/login" }, "authorization_url"},
		{"no token kinds", func(m map[string]any) { oauth(m)["token_kinds"] = []any{} }, "token_kinds"},
		{"duplicate token kind", func(m map[string]any) {
			oauth(m)["token_kinds"] = []any{map[string]any{"name": "user"}, map[string]any{"name": "user"}}
		}, "declared twice"},
		{"output pointer", func(m map[string]any) {
			oauth(m)["outputs"] = []any{map[string]any{"name": "x", "pointer": "id"}}
		}, "not a JSON pointer"},
		{"template path", func(m map[string]any) { auth(m)["github_app"] = map[string]any{"manifest_template": "../x.json"} }, "manifest_template"},
		{"user key header", func(m map[string]any) {
			auth(m)["user_key"].(map[string]any)["inject"] = map[string]any{"header": "Bad Header", "value": "x"}
		}, "inject.header"},
		{"base url http", func(m map[string]any) { m["base_url"] = "http://api.github.com" }, "base_url"},
		{"base url host not allowed", func(m map[string]any) { m["base_url"] = "https://evil.example.com" }, "not in egress_allowlist"},
		{"placeholder cannot widen the allowlist", func(m map[string]any) { m["base_url"] = "https://{site}.github.com" }, "not in egress_allowlist"},
		{"user key base url host not allowed", func(m map[string]any) {
			auth(m)["user_key"].(map[string]any)["base_url"] = "https://evil.example.com"
		}, "auth.user_key.base_url"},
		{"wildcard only", func(m map[string]any) { m["egress_allowlist"] = []any{"*"} }, "egress_allowlist[0]"},
		{"ip allowlist", func(m map[string]any) { m["egress_allowlist"] = []any{"169.254.169.254"} }, "IP address"},
		{"allowlist with scheme", func(m map[string]any) { m["egress_allowlist"] = []any{"https://api.github.com"} }, "egress_allowlist[0]"},
		{"empty allowlist", func(m map[string]any) { m["egress_allowlist"] = []any{} }, "needs at least one host"},
		{"verify path", func(m map[string]any) { m["verify"] = map[string]any{"path": "user"} }, "verify.path"},
		{"verify token kind", func(m map[string]any) { m["verify"] = map[string]any{"path": "/user", "token_kind": "bot"} }, "not a token kind"},
		{"rate limit scope", func(m map[string]any) { m["rate_limit"].(map[string]any)["scope"] = "global" }, "rate_limit.scope"},
		{"negative per hour", func(m map[string]any) { m["rate_limit"].(map[string]any)["per_hour"] = -1 }, "negative"},
		{"rate limit header", func(m map[string]any) {
			m["rate_limit"].(map[string]any)["headers"] = map[string]any{"reset": "a b"}
		}, "rate_limit.headers.reset"},
		{"tool prefix", func(m map[string]any) { tool(m)["name"] = "gitlab.create_issue" }, "must start with"},
		{"tool name shape", func(m map[string]any) { tool(m)["name"] = "create_issue" }, "<app>.<action>"},
		{"duplicate tool", func(m map[string]any) { m["tools"] = []any{tool(m), tool(m)} }, "declared twice"},
		{"tool effect", func(m map[string]any) { tool(m)["effect"] = "mutate" }, "tools[0].effect"},
		{"tool without description", func(m map[string]any) { tool(m)["description"] = " " }, "description"},
		{"input schema not object", func(m map[string]any) { tool(m)["input_schema"] = map[string]any{"type": "string"} }, `"type": "object"`},
		{"output schema missing", func(m map[string]any) { delete(tool(m), "output_schema") }, "output_schema: is required"},
		{"schema not a schema", func(m map[string]any) {
			tool(m)["input_schema"] = map[string]any{"type": "object", "properties": 5}
		}, "not a JSON Schema"},
		{"tool token kind", func(m map[string]any) { tool(m)["token_kind"] = "bot" }, "not a token kind"},
		{"no tools or streams", func(m map[string]any) {
			m["tools"] = []any{}
			delete(m, "streams")
			delete(m, "webhooks")
		}, "at least one tool or stream"},
		{"stream mode", func(m map[string]any) { stream(m)["modes"] = []any{"poll"} }, "streams[0].modes"},
		{"stream schedule", func(m map[string]any) { stream(m)["schedule"] = "every 15 minutes" }, "rate(15 minutes)"},
		{"stream deletes", func(m map[string]any) { stream(m)["deletes"] = "magic" }, "streams[0].deletes"},
		{"lookback format", func(m map[string]any) { stream(m)["lookback"] = "5m" }, "ISO 8601"},
		{"lookback without incremental", func(m map[string]any) { stream(m)["modes"] = []any{"full"} }, "only an incremental stream"},
		{"stream without document or acl", func(m map[string]any) {
			delete(stream(m), "document")
			delete(stream(m), "acl")
		}, "document mapping or an acl"},
		{"document pointer", func(m map[string]any) { stream(m)["document"] = map[string]any{"title": "title"} }, "streams[0].document.title"},
		{"webhook mode without webhooks", func(m map[string]any) {
			stream(m)["modes"] = []any{"incremental", "webhook"}
			delete(m, "webhooks")
		}, "needs a webhooks section"},
		{"webhook deletes without event", func(m map[string]any) {
			hooks(m)["events"] = map[string]any{}
		}, "webhook deletes need"},
		{"unknown scheme", func(m map[string]any) { hooks(m)["verification"] = map[string]any{"scheme": "md5"} }, "verification.scheme"},
		{"channel_token without header", func(m map[string]any) {
			hooks(m)["verification"] = map[string]any{"scheme": "channel_token"}
		}, "needs a header name"},
		{"routing", func(m map[string]any) { hooks(m)["routing"] = "global" }, "webhooks.routing"},
		{"no route", func(m map[string]any) { delete(hooks(m), "route_by") }, "route_by"},
		{"subscription renew", func(m map[string]any) {
			hooks(m)["subscription"] = map[string]any{"max_ttl_s": 60, "renew_at": 1.5}
		}, "webhooks.subscription"},
		{"event for unknown stream", func(m map[string]any) {
			hooks(m)["events"] = map[string]any{"pr": map[string]any{"stream": "prs", "id": "/id"}}
		}, "not a stream"},
		{"event without target", func(m map[string]any) { hooks(m)["events"] = map[string]any{"pr": map[string]any{}} }, "needs a stream"},
		{"re-fetch without id", func(m map[string]any) {
			hooks(m)["events"] = map[string]any{"issues": map[string]any{"stream": "issues", "delete_on": []any{"deleted"}}}
		}, "pointer to the object id"},
		{"reauth status", func(m map[string]any) { m["reauth_on"] = []any{99} }, "reauth_on[0]"},
		{"reauth empty code", func(m map[string]any) { m["reauth_on"] = []any{""} }, "reauth_on[0]"},
		{"unknown field", func(m map[string]any) { m["track_deletes"] = true }, "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(mutated(t, tc.change))
			if err == nil {
				t.Fatalf("Parse accepted a manifest that should fail with %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestParseReportsEveryProblem(t *testing.T) {
	_, err := Parse(mutated(t, func(m map[string]any) {
		m["id"] = "Bad"
		m["version"] = 0
		tool(m)["effect"] = "mutate"
	}))
	if err == nil {
		t.Fatal("Parse accepted an invalid manifest")
	}
	for _, want := range []string{"id:", "version:", "tools[0].effect"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

func TestParseRejectsMalformedJSON(t *testing.T) {
	for name, in := range map[string]string{
		"not json":      `{"id": `,
		"trailing data": valid + ` {}`,
		"wrong type":    `{"id": 5}`,
	} {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: Parse accepted %q", name, in)
		}
	}
}

func TestStreamlessConnectorIsValid(t *testing.T) {
	// A tool-only connector (no streams, no webhooks) is a normal shape.
	_, err := Parse(mutated(t, func(m map[string]any) {
		delete(m, "streams")
		delete(m, "webhooks")
	}))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
}

// TestPlatformManifests checks that oryxa-platform's manifests, with its names
// mapped to ours, validate. The copies in testdata/platform are unchanged.
func TestPlatformManifests(t *testing.T) {
	files, err := filepath.Glob("testdata/platform/*.json")
	if err != nil || len(files) != 5 {
		t.Fatalf("want the 5 platform manifests, got %v (%v)", files, err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			raw, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]any
			if err := json.Unmarshal(raw, &m); err != nil {
				t.Fatal(err)
			}
			mapPlatformNames(m)
			mapped, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Parse(mapped); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
}

// TestPlatformNamesAreRejectedUnmapped keeps the mapping honest: the platform's
// own names are not ours, so a manifest that skips the mapping fails.
func TestPlatformNamesAreRejectedUnmapped(t *testing.T) {
	raw, err := os.ReadFile("testdata/platform/slack.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Parse(raw); err == nil {
		t.Fatal("Parse accepted a manifest in oryxa-platform's names")
	}
}

// mapPlatformNames rewrites a manifest from oryxa-platform's names to ours:
//   - credential_modes platform → oryxa, tenant_app → byo
//   - connection_levels workspace → org
//   - refresh.rotates → refresh.rotating
//   - rate_limit.retry_headers → headers.retry_after
//   - streams: track_deletes → deletes, acl:true → an acl source, and a
//     document mapping for streams that carry records
//   - webhooks: routing pointers → route_by, routing "org_app" (ours), the
//     slack "hmac" scheme → slack_v0, and ingress / routing_per_site dropped
func mapPlatformNames(m map[string]any) {
	rename := func(list any, from, to string) {
		for i, s := range list.([]any) {
			if s == from {
				list.([]any)[i] = to
			}
		}
	}
	rename(m["connection_levels"], "workspace", "org")
	a := m["auth"].(map[string]any)
	rename(a["credential_modes"], "platform", "oryxa")
	rename(a["credential_modes"], "tenant_app", "byo")
	if r, ok := a["oauth2"].(map[string]any)["refresh"].(map[string]any); ok {
		r["rotating"] = r["rotates"]
		delete(r, "rotates")
	}
	if rl, ok := m["rate_limit"].(map[string]any); ok {
		if h, ok := rl["retry_headers"].([]any); ok {
			rl["headers"] = map[string]any{"retry_after": h[0]}
			delete(rl, "retry_headers")
		}
	}
	for _, s := range m["streams"].([]any) {
		s := s.(map[string]any)
		if s["track_deletes"] == true {
			s["deletes"] = "reconcile"
		}
		delete(s, "track_deletes")
		if s["acl"] == true {
			s["acl"] = "provider"
		} else {
			s["document"] = map[string]any{"title": "/title"}
		}
	}
	if w, ok := m["webhooks"].(map[string]any); ok {
		delete(w, "ingress")
		delete(w, "routing_per_site")
		if p, ok := w["routing"]; ok {
			w["route_by"] = p
		}
		w["routing"] = "org_app"
		if v := w["verification"].(map[string]any); v["scheme"] == "hmac" {
			v["scheme"] = "slack_v0"
		}
	}
}

func mutated(t *testing.T, change func(m map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(valid), &m); err != nil {
		t.Fatal(err)
	}
	change(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func auth(m map[string]any) map[string]any  { return m["auth"].(map[string]any) }
func oauth(m map[string]any) map[string]any { return auth(m)["oauth2"].(map[string]any) }
func hooks(m map[string]any) map[string]any { return m["webhooks"].(map[string]any) }
func tool(m map[string]any) map[string]any  { return m["tools"].([]any)[0].(map[string]any) }
func stream(m map[string]any) map[string]any {
	return m["streams"].([]any)[0].(map[string]any)
}
