package rig

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// FakeComposio stands in for Composio's REST API, so the apps service can be
// run and attacked without a real project key or any traffic leaving the
// machine. It serves a tiny fixed catalog and a few in-memory connections and
// triggers, and can be told to misbehave the way a broken upstream would.
type FakeComposio struct {
	mu       sync.Mutex
	mode     string
	accounts map[string]map[string]any
	triggers map[string]map[string]any
	counter  atomic.Int64
	requests atomic.Int64
	keys     sync.Map // API keys seen, so a leak of the wrong key shows up
}

// NewFakeComposio returns a healthy fake.
func NewFakeComposio() *FakeComposio {
	return &FakeComposio{accounts: map[string]map[string]any{}, triggers: map[string]map[string]any{}}
}

// SetMode picks how the fake answers: "" (well), "garbage", "huge", "hang",
// "500", "429", "html", "empty", "slow".
func (fake *FakeComposio) SetMode(mode string) {
	fake.mu.Lock()
	fake.mode = mode
	fake.mu.Unlock()
}

// Requests is how many calls the fake has served.
func (fake *FakeComposio) Requests() int64 { return fake.requests.Load() }

func toolkit(slug, name string) map[string]any {
	return map[string]any{
		"slug": slug, "name": name, "type": "toolkit",
		"auth_schemes": []string{"OAUTH2"}, "composio_managed_auth_schemes": []string{"OAUTH2"}, "no_auth": false,
		"meta": map[string]any{
			"description": name + " for the chaos rig", "logo": "https://example.invalid/" + slug + ".png",
			"tools_count": 2, "triggers_count": 1, "version": "1",
			"categories": []map[string]string{{"id": "dev", "name": "Developer tools", "slug": "dev"}},
			"app_url":    "https://example.invalid/" + slug,
		},
	}
}

// ServeHTTP implements http.Handler.
func (fake *FakeComposio) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.requests.Add(1)
	if key := request.Header.Get("x-api-key"); key != "" {
		fake.keys.Store(key, true)
	}
	fake.mu.Lock()
	mode := fake.mode
	fake.mu.Unlock()
	switch mode {
	case "garbage":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"items\":[{\"slug\":\x00\xff"))
		return
	case "html":
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
		return
	case "500":
		writeJSON(writer, 500, map[string]any{"error": map[string]any{"message": "boom", "code": 500}})
		return
	case "429":
		writer.Header().Set("Retry-After", "1")
		writeJSON(writer, 429, map[string]any{"error": map[string]any{"message": "slow down", "code": 429}})
		return
	case "empty":
		return
	case "hang":
		<-request.Context().Done()
		return
	case "slow":
		select {
		case <-time.After(20 * time.Second):
		case <-request.Context().Done():
			return
		}
	case "huge":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"items":[],"pad":"`))
		chunk := []byte(strings.Repeat("Z", 64<<10))
		for range 400 { // ~25 MiB
			if _, err := writer.Write(chunk); err != nil {
				return
			}
		}
		_, _ = writer.Write([]byte(`"}`))
		return
	}

	path := strings.TrimPrefix(request.URL.Path, "/api/v3.1")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	switch {
	case path == "/toolkits" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{
			"items":       []any{toolkit("github", "GitHub"), toolkit("gmail", "Gmail"), toolkit("slack", "Slack")},
			"next_cursor": nil,
		})
	case len(segments) == 2 && segments[0] == "toolkits" && request.Method == http.MethodGet:
		switch segments[1] {
		case "github", "gmail", "slack":
			writeJSON(writer, 200, toolkit(segments[1], strings.ToUpper(segments[1][:1])+segments[1][1:]))
		default:
			writeJSON(writer, 404, map[string]any{"error": map[string]any{"message": "toolkit not found", "code": 404}})
		}
	case path == "/auth_configs" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"items": []any{
			map[string]any{"id": "ac_github", "name": "GitHub", "toolkit": map[string]string{"slug": "github"}, "auth_scheme": "OAUTH2", "is_composio_managed": true, "status": "ENABLED"},
			map[string]any{"id": "ac_gmail", "name": "Gmail", "toolkit": map[string]string{"slug": "gmail"}, "auth_scheme": "OAUTH2", "is_composio_managed": true, "status": "ENABLED"},
		}, "next_cursor": nil})
	case path == "/auth_configs" && request.Method == http.MethodPost:
		writeJSON(writer, 201, map[string]any{"auth_config": map[string]any{"id": "ac_new", "auth_scheme": "OAUTH2", "is_composio_managed": true}})
	case path == "/connected_accounts/link" && request.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&body)
		id := "ca_" + itoa(fake.counter.Add(1))
		fake.mu.Lock()
		fake.accounts[id] = map[string]any{
			"id": id, "user_id": body["user_id"], "toolkit": map[string]string{"slug": "github"},
			"auth_config": map[string]any{"id": body["auth_config_id"], "auth_scheme": "OAUTH2", "is_composio_managed": true},
			"status":      "ACTIVE", "is_disabled": false,
		}
		fake.mu.Unlock()
		writeJSON(writer, 201, map[string]any{
			"redirect_url": "https://example.invalid/connect/" + id, "connected_account_id": id,
			"expires_at": time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339),
		})
	case path == "/connected_accounts" && request.Method == http.MethodGet:
		fake.mu.Lock()
		items := make([]any, 0, len(fake.accounts))
		for _, account := range fake.accounts {
			items = append(items, account)
		}
		fake.mu.Unlock()
		writeJSON(writer, 200, map[string]any{"items": items, "next_cursor": nil})
	case len(segments) == 2 && segments[0] == "connected_accounts":
		fake.mu.Lock()
		account, found := fake.accounts[segments[1]]
		if request.Method == http.MethodDelete {
			delete(fake.accounts, segments[1])
			found, account = true, map[string]any{}
		}
		fake.mu.Unlock()
		if !found {
			writeJSON(writer, 404, map[string]any{"error": map[string]any{"message": "account not found", "code": 404}})
			return
		}
		writeJSON(writer, 200, account)
	case path == "/triggers_types" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"items": []any{map[string]any{
			"slug": "GITHUB_PUSH_EVENT", "name": "Push", "description": "A push", "toolkit": map[string]string{"slug": "github"},
			"type": "webhook", "config": map[string]any{}, "payload": map[string]any{},
		}}, "next_cursor": nil})
	case path == "/tools" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"items": []any{map[string]any{
			"slug": "GITHUB_GET_REPO", "name": "Get repo", "description": "Read a repository", "toolkit": map[string]string{"slug": "github"},
			"input_parameters": map[string]any{"type": "object", "properties": map[string]any{"repo": map[string]string{"type": "string"}}},
			"tags":             []string{"read"},
		}}, "next_cursor": nil})
	case strings.HasPrefix(path, "/tools/execute/") && request.Method == http.MethodPost:
		var body map[string]any
		_ = json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&body)
		writeJSON(writer, 200, map[string]any{"data": map[string]any{"echo": body}, "successful": true, "error": nil})
	case path == "/trigger_instances/active" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"items": []any{}, "next_cursor": nil})
	case strings.HasPrefix(path, "/trigger_instances/") && strings.HasSuffix(path, "/upsert") && request.Method == http.MethodPost:
		id := "ti_" + itoa(fake.counter.Add(1))
		writeJSON(writer, 200, map[string]any{"trigger_id": id, "id": id})
	case path == "/webhook_subscriptions" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"items": []any{}, "next_cursor": nil})
	case path == "/webhook_subscriptions" && request.Method == http.MethodPost:
		writeJSON(writer, 201, map[string]any{"id": "ws_1", "secret": "whsec_chaos", "webhook_url": "x"})
	default:
		writeJSON(writer, 200, map[string]any{"items": []any{}, "next_cursor": nil})
	}
}

func itoa(value int64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	var out []byte
	for value > 0 {
		out = append([]byte{digits[value%10]}, out...)
		value /= 10
	}
	return string(out)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
