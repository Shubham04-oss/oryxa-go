package rig

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/pipedream"
)

// FakePipedream stands in for Pipedream Connect's REST API under /v1, so the
// apps service can be run and attacked without a real OAuth client or any
// traffic leaving the machine. It serves a tiny fixed catalog, keeps accounts
// and deployed triggers in memory, delivers signed trigger events on demand,
// echoes proxy requests, and can be told to misbehave the way a broken
// upstream would.
type FakePipedream struct {
	mu       sync.Mutex
	mode     string
	accounts map[string]map[string]any
	triggers map[string]*fakeTrigger
	counter  atomic.Int64
	requests atomic.Int64
	// Deliver sends trigger events; it defaults to a short-timeout client.
	Deliver *http.Client
}

type fakeTrigger struct {
	id, key, user, webhookURL, signingKey string
}

// NewFakePipedream returns a healthy fake.
func NewFakePipedream() *FakePipedream {
	return &FakePipedream{
		accounts: map[string]map[string]any{}, triggers: map[string]*fakeTrigger{},
		Deliver: &http.Client{Timeout: 10 * time.Second},
	}
}

// SetMode picks how the fake answers everything but the token: "" (well),
// "429", "500", "garbage" or "hang".
func (fake *FakePipedream) SetMode(mode string) {
	fake.mu.Lock()
	fake.mode = mode
	fake.mu.Unlock()
}

// Requests is how many calls the fake has served.
func (fake *FakePipedream) Requests() int64 { return fake.requests.Load() }

// Connect makes an account, as a member finishing the hosted flow would, and
// answers its ID.
func (fake *FakePipedream) Connect(externalUserID, app string) string {
	id := "apn_" + itoa(fake.counter.Add(1))
	fake.mu.Lock()
	fake.accounts[id] = map[string]any{
		"id": id, "name": externalUserID + "@example.invalid", "external_id": externalUserID,
		"healthy": true, "dead": nil, "error": nil, "app": fakeApp(app),
		"created_at": time.Now().UTC().Format(time.RFC3339), "updated_at": time.Now().UTC().Format(time.RFC3339),
	}
	fake.mu.Unlock()
	return id
}

// Kill marks an account dead, as Pipedream does once it gives up refreshing
// it: it still lists, unhealthy, and every action or proxy call through it
// fails at the app.
func (fake *FakePipedream) Kill(accountID string) {
	fake.mu.Lock()
	if account, ok := fake.accounts[accountID]; ok {
		account["healthy"], account["dead"], account["error"] = false, true, "token refresh failed"
	}
	fake.mu.Unlock()
}

// Fire delivers event to deployed trigger id's webhook, signed with its key
// the way Pipedream signs one, and answers the webhook's status.
func (fake *FakePipedream) Fire(id string, event any) (int, error) {
	fake.mu.Lock()
	trigger, ok := fake.triggers[id]
	fake.mu.Unlock()
	if !ok {
		return 0, fmt.Errorf("no deployed trigger %s", id)
	}
	body, err := json.Marshal(event)
	if err != nil {
		return 0, err
	}
	request, err := http.NewRequest(http.MethodPost, trigger.webhookURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(pipedream.SignatureHeader, pipedream.SignWebhook(trigger.signingKey, body, time.Now()))
	response, err := fake.Deliver.Do(request)
	if err != nil {
		return 0, err
	}
	_ = response.Body.Close()
	return response.StatusCode, nil
}

// SigningKey is deployed trigger id's key, for a test checking a delivery.
func (fake *FakePipedream) SigningKey(id string) string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if trigger, ok := fake.triggers[id]; ok {
		return trigger.signingKey
	}
	return ""
}

func fakeApp(slug string) map[string]any {
	return map[string]any{
		"id": "oa_" + slug, "name_slug": slug, "name": strings.ToUpper(slug[:1]) + slug[1:], "auth_type": "oauth",
		"description": slug + " for the chaos rig", "img_src": "https://example.invalid/" + slug + ".png",
		"custom_fields_json": "[]", "categories": []string{"Productivity"}, "featured_weight": 1, "scope_profiles": []any{},
	}
}

func fakeComponent(key, kind string, readOnly *bool) map[string]any {
	component := map[string]any{
		"key": key, "name": key, "version": "0.1.0", "component_type": kind, "description": key + " for the chaos rig",
		"configurable_props": []any{
			map[string]any{"name": strings.SplitN(key, "-", 2)[0], "type": "app", "app": strings.SplitN(key, "-", 2)[0]},
		},
	}
	if readOnly != nil {
		component["annotations"] = map[string]any{"readOnlyHint": *readOnly}
	}
	return component
}

var (
	pdYes, pdNo     = true, false
	fakePDApps      = []string{"gmail", "github", "slack"}
	fakePDActions   = []map[string]any{fakeComponent("gmail-find-email", "action", &pdYes), fakeComponent("gmail-send-email", "action", &pdNo), fakeComponent("github-get-repo", "action", &pdYes), fakeComponent("github-create-issue", "action", nil)}
	fakePDTriggerKs = []map[string]any{fakeComponent("gmail-new-email-received", "source", nil), fakeComponent("github-new-issue", "source", nil)}
)

func pdError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]any{"error": message})
}

// ServeHTTP implements http.Handler.
func (fake *FakePipedream) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	fake.requests.Add(1)
	path := strings.TrimPrefix(request.URL.Path, "/v1")
	if path == "/oauth/token" && request.Method == http.MethodPost {
		writeJSON(writer, 200, map[string]any{"access_token": "pd_fake_" + itoa(fake.counter.Add(1)), "token_type": "Bearer", "expires_in": 3600})
		return
	}
	if !strings.HasPrefix(request.Header.Get("Authorization"), "Bearer pd_fake_") {
		pdError(writer, 401, "unauthorized")
		return
	}
	fake.mu.Lock()
	mode := fake.mode
	fake.mu.Unlock()
	switch mode {
	case "429":
		writer.Header().Set("Retry-After", "1")
		pdError(writer, 429, "Throttled")
		return
	case "500":
		pdError(writer, 500, "boom")
		return
	case "garbage":
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte("{\"data\":[\x00\xff"))
		return
	case "hang":
		<-request.Context().Done()
		return
	}

	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) >= 3 && segments[0] == "connect" && strings.HasPrefix(segments[1], "proj_") {
		if request.Header.Get("x-pd-environment") == "" {
			pdError(writer, 400, "x-pd-environment is required")
			return
		}
		fake.project(writer, request, segments[2:])
		return
	}
	switch {
	case path == "/connect/apps" && request.Method == http.MethodGet:
		data := []any{}
		for _, slug := range fakePDApps {
			if query := request.URL.Query().Get("q"); query == "" || strings.Contains(slug, query) {
				data = append(data, fakeApp(slug))
			}
		}
		writeJSON(writer, 200, map[string]any{"data": data, "page_info": map[string]any{"count": len(data), "total_count": len(data), "end_cursor": nil}})
	case path == "/connect/usage" && request.Method == http.MethodGet:
		writeJSON(writer, 200, map[string]any{"data": []any{}})
	default:
		pdError(writer, 404, "Not Found")
	}
}

// project serves the project-scoped endpoints, whose path follows
// /connect/{project_id}.
func (fake *FakePipedream) project(writer http.ResponseWriter, request *http.Request, segments []string) {
	method, query := request.Method, request.URL.Query()
	var body map[string]any
	if request.Body != nil && method == http.MethodPost && segments[0] != "proxy" {
		_ = json.NewDecoder(io.LimitReader(request.Body, 1<<20)).Decode(&body)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	switch {
	case len(segments) == 1 && segments[0] == "tokens" && method == http.MethodPost:
		token := fmt.Sprintf("ctok_%032d", fake.counter.Add(1))
		writeJSON(writer, 200, map[string]any{
			"token": token, "expires_at": time.Now().Add(4 * time.Hour).UTC().Format(time.RFC3339),
			"connect_link_url": "https://pipedream.example.invalid/_static/connect.html?token=" + token + "&connectLink=true",
		})
	case len(segments) == 3 && segments[0] == "users" && segments[2] == "accounts" && method == http.MethodGet:
		items, known := []any{}, false
		for _, account := range fake.sortedAccounts() {
			if account["external_id"] != segments[1] {
				continue
			}
			known = true
			if query.Get("app") == "" || account["app"].(map[string]any)["name_slug"] == query.Get("app") {
				items = append(items, account)
			}
		}
		if !known {
			pdError(writer, 404, "Not Found") // as the live API does for a user with nothing connected
			return
		}
		writeJSON(writer, 200, items)
	case len(segments) == 2 && segments[0] == "users" && method == http.MethodDelete:
		for id, account := range fake.accounts {
			if account["external_id"] == segments[1] {
				delete(fake.accounts, id)
			}
		}
		for id, trigger := range fake.triggers {
			if trigger.user == segments[1] {
				delete(fake.triggers, id)
			}
		}
		writer.WriteHeader(204)
	case len(segments) == 2 && segments[0] == "accounts":
		account, ok := fake.accounts[segments[1]]
		if !ok {
			pdError(writer, 404, "record not found")
			return
		}
		if method == http.MethodDelete {
			delete(fake.accounts, segments[1])
			writer.WriteHeader(204)
			return
		}
		writeJSON(writer, 200, account)
	case len(segments) == 1 && (segments[0] == "actions" || segments[0] == "triggers") && method == http.MethodGet:
		source := fakePDActions
		if segments[0] == "triggers" {
			source = fakePDTriggerKs
		}
		data := []any{}
		for _, component := range source {
			if app := query.Get("app"); app == "" || strings.HasPrefix(component["key"].(string), app+"-") {
				data = append(data, component)
			}
		}
		writeJSON(writer, 200, map[string]any{"data": data, "page_info": map[string]any{"count": len(data), "end_cursor": nil}})
	case len(segments) == 2 && segments[0] == "actions" && segments[1] == "run" && method == http.MethodPost:
		if account, err := fake.accountFor(body); err != nil {
			writeJSON(writer, 200, map[string]any{"exports": map[string]any{}, "ret": nil, "error": map[string]any{
				"name": "Error", "message": err.Error(), "attribution": map[string]any{"origin": "component_code"},
			}})
		} else if account["dead"] == true {
			writeJSON(writer, 200, map[string]any{"exports": map[string]any{}, "ret": nil, "error": map[string]any{
				"name": "AxiosError", "message": "Request failed with status code 401",
				"attribution": map[string]any{"origin": "upstream_api", "last_call": map[string]any{"status": 401}},
			}})
		} else {
			writeJSON(writer, 200, map[string]any{"exports": map[string]any{"$summary": "ran " + fmt.Sprint(body["id"])}, "ret": map[string]any{"echo": body["configured_props"]}})
		}
	case len(segments) == 2 && segments[0] == "triggers" && segments[1] == "deploy" && method == http.MethodPost:
		if _, err := fake.accountFor(body); err != nil {
			pdError(writer, 400, err.Error())
			return
		}
		trigger := &fakeTrigger{
			id: "dc_" + itoa(fake.counter.Add(1)), key: fmt.Sprint(body["id"]), user: fmt.Sprint(body["external_user_id"]),
			webhookURL: fmt.Sprint(body["webhook_url"]), signingKey: "whsk_fake_" + itoa(fake.counter.Add(1)),
		}
		fake.triggers[trigger.id] = trigger
		writeJSON(writer, 200, map[string]any{"data": map[string]any{
			"type": "DeployedComponent", "id": trigger.id, "owner_id": "o_fake", "component_id": "sc_" + trigger.key,
			"component_key": trigger.key, "configurable_props": []any{}, "configured_props": body["configured_props"],
			"active": true, "created_at": time.Now().UnixMilli(), "updated_at": time.Now().UnixMilli(),
			"name": trigger.key, "name_slug": trigger.key, "webhook_signing_key": trigger.signingKey,
		}})
	case len(segments) == 2 && segments[0] == "deployed-triggers" && method == http.MethodDelete:
		trigger, ok := fake.triggers[segments[1]]
		if !ok || trigger.user != query.Get("external_user_id") {
			pdError(writer, 404, "record not found")
			return
		}
		delete(fake.triggers, segments[1])
		writer.WriteHeader(204)
	case len(segments) == 2 && segments[0] == "proxy":
		target, err := base64.RawURLEncoding.DecodeString(segments[1])
		account, found := fake.accounts[query.Get("account_id")]
		switch {
		case err != nil:
			pdError(writer, 400, "the URL is not URL-safe base64")
		case !found || account["external_id"] != query.Get("external_user_id"):
			pdError(writer, 404, "account not found")
		case account["dead"] == true:
			writeJSON(writer, 401, map[string]any{"error": "invalid_grant"}) // what the app would answer
		default:
			forwarded := map[string]string{}
			for name := range request.Header {
				if strings.HasPrefix(strings.ToLower(name), "x-pd-proxy-") {
					forwarded[strings.ToLower(strings.TrimPrefix(strings.ToLower(name), "x-pd-proxy-"))] = request.Header.Get(name)
				}
			}
			payload, _ := io.ReadAll(io.LimitReader(request.Body, 1<<20))
			writeJSON(writer, 200, map[string]any{"method": method, "url": string(target), "headers": forwarded, "body": string(payload)})
		}
	default:
		pdError(writer, 404, "Not Found")
	}
}

// accountFor finds the account an action's or trigger's app prop names, which
// must belong to the external user it runs as. The caller holds fake.mu.
func (fake *FakePipedream) accountFor(body map[string]any) (map[string]any, error) {
	props, _ := body["configured_props"].(map[string]any)
	for _, value := range props {
		if prop, ok := value.(map[string]any); ok {
			if id, ok := prop["authProvisionId"].(string); ok {
				account, found := fake.accounts[id]
				if !found || account["external_id"] != body["external_user_id"] {
					return nil, errors.New("the account does not exist for this user")
				}
				return account, nil
			}
		}
	}
	return nil, errors.New("no app prop names an account")
}

// sortedAccounts lists accounts in a stable order. The caller holds fake.mu.
func (fake *FakePipedream) sortedAccounts() []map[string]any {
	ids := make([]string, 0, len(fake.accounts))
	for id := range fake.accounts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	accounts := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		accounts = append(accounts, fake.accounts[id])
	}
	return accounts
}
