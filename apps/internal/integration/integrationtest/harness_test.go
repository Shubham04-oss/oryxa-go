package integrationtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// The harness here is the thing Conformance is written against, so it is a
// toolkit that exercises every part of it: a read tool that reads through a
// POST, a write, a reader, a polled event and a pushed one.

// probe is a toolkit built only out of the Session, exactly as a real
// integration must be: it never sets a credential, never opens one of its
// own, and makes no call the cassettes do not record.
type probe struct {
	// manifest, when set, replaces what the toolkit declares: it is how a
	// case breaks the manifest without a second whole integration.
	manifest *integration.Manifest
	// unmapped is what an answer outside the four refusals becomes. A real
	// integration has to map it itself, and one that does not is caught.
	unmapped error
	// firstPollEvents is how many events the first poll hands over, which it
	// must not do: a new subscriber would be given the app's whole history.
	firstPollEvents int
	// ignoreCursor is a reader that asks for the first page again, which is
	// what "cannot resume" looks like.
	ignoreCursor bool
	// refuseUnknown is a push Parse that answers an event with no id.
	refuseUnknown bool

	mu sync.Mutex
	// sent is every request the code under test made, in order, as
	// "METHOD /path".
	sent []string
	// listToken is what the reader asked the session for, which is how a test
	// says Session.Token reached the integration.
	listToken string
}

func (p *probe) note(method, path string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sent = append(p.sent, method+" "+path)
}

func (p *probe) requests() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.sent...)
}

func (p *probe) Manifest() integration.Manifest {
	if p.manifest != nil {
		return *p.manifest
	}
	return integration.Manifest{
		Toolkit: "acme", Version: 1, Name: "Acme", APIBase: "https://api.acme.test",
		Auth: integration.Auth{
			Methods: []integration.Method{
				{Kind: integration.KindOAuth2Code, OAuth2: &integration.OAuth2{
					Client: "acme", AuthURL: "https://acme.test/authorize", TokenURL: "https://acme.test/token",
					RevokeURL: "https://acme.test/revoke", Scopes: []string{"issues:read", "issues:write"},
					PKCE: true, ClientAuth: integration.ClientAuthBody,
				}},
			},
			Test: integration.Test{Path: "/me", Label: "/login", AccountRef: "/id"},
		},
		Webhook: &integration.Webhook{
			Scope: integration.HookScopeConnection,
			Verify: integration.Verify{
				Type:      integration.VerifyHMACSHA256,
				Signature: &integration.Locator{Header: "X-Acme-Signature", Prefix: "sha256="},
				Encoding:  integration.EncodingHex, Signed: "{body}",
			},
			DeliveryID: integration.Locator{Header: "X-Acme-Delivery"},
		},
		Tools: []integration.ToolSpec{
			{
				Name: "ACME_LIST_ISSUES", Description: "List issues", ReadOnly: true,
				ReadPOSTs:   []string{"/search"},
				Scopes:      []string{"issues:read"},
				InputSchema: json.RawMessage(`{"type":"object","required":["repo"]}`),
			},
			{
				Name: "ACME_CLOSE_ISSUE", Description: "Close an issue", Destructive: true,
				Scopes:      []string{"issues:write"},
				InputSchema: json.RawMessage(`{"type":"object","required":["repo","number"]}`),
			},
		},
		Readers: []integration.ReaderSpec{{
			Name: "issues", Kinds: []string{"repo"},
			TargetSchema: json.RawMessage(`{"type":"object","required":["repo"]}`),
		}},
		Triggers: []integration.TriggerSpec{
			{
				Slug: "ACME_ISSUE_EVENT", Name: "An issue changed", Kind: integration.TriggerPoll,
				ConfigSchema:  json.RawMessage(`{"type":"object"}`),
				PayloadSchema: json.RawMessage(`{"type":"object","required":["number"]}`),
				Sample:        json.RawMessage(`{"number":7}`),
			},
			{
				Slug: "ACME_PUSH_EVENT", Name: "Something was pushed", Kind: integration.TriggerPush,
				ConfigSchema:  json.RawMessage(`{"type":"object"}`),
				PayloadSchema: json.RawMessage(`{"type":"object","required":["ref"]}`),
				Sample:        json.RawMessage(`{"ref":"refs/heads/main"}`),
			},
		},
	}
}

func (p *probe) Tools() map[string]integration.ToolFunc {
	return map[string]integration.ToolFunc{
		"ACME_LIST_ISSUES": p.listIssues,
		"ACME_CLOSE_ISSUE": p.closeIssue,
	}
}

// listIssues reads through a POST, which is the read_post its manifest
// declares; closeIssue is a write, and a PATCH, which a read could never do.
func (p *probe) listIssues(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
	p.note(http.MethodPost, "/search")
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, session.BaseURL+"/search", strings.NewReader(`{"q":"is:open"}`))
	if err != nil {
		return nil, err
	}
	return p.call(ctx, session, request)
}

func (p *probe) closeIssue(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
	var input struct {
		Repo   string `json:"repo"`
		Number int    `json:"number"`
	}
	if json.Unmarshal(args, &input) != nil || input.Repo == "" {
		return nil, &integration.AppError{Message: "the arguments do not name an issue"}
	}
	path := "/repos/" + input.Repo + "/issues/" + strconv.Itoa(input.Number)
	p.note(http.MethodPatch, path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPatch, session.BaseURL+path, strings.NewReader(`{"state":"closed"}`))
	if err != nil {
		return nil, err
	}
	return p.call(ctx, session, request)
}

// Targets is the reader's listing, and it asks the session for its token the
// way an SDK with no client hook has to.
func (p *probe) Targets(ctx context.Context, session integration.Session) ([]integration.Target, error) {
	token, err := session.Token(ctx)
	p.mu.Lock()
	p.listToken = token
	p.mu.Unlock()
	if err != nil {
		return nil, err
	}
	p.note(http.MethodGet, "/user/repos")
	response, err := session.HTTP.Do(request(ctx, http.MethodGet, session.BaseURL+"/user/repos?per_page=100"))
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, p.refusal(response)
	}
	body, _ := io.ReadAll(response.Body)
	var listed []struct {
		FullName string `json:"full_name"`
	}
	if json.Unmarshal(body, &listed) != nil {
		return nil, errors.New("the repository list could not be read")
	}
	targets := make([]integration.Target, 0, len(listed))
	for _, repository := range listed {
		targets = append(targets, integration.Target{
			Kind: "repo", ID: repository.FullName, Name: repository.FullName,
			Target: json.RawMessage(`{"repo":"` + repository.FullName + `"}`),
		})
	}
	return targets, nil
}

// Read is one page of one repository's issues, and it asks for the next with
// the cursor the last page answered, which is what makes a resumption test
// possible at all.
func (p *probe) Read(ctx context.Context, session integration.Session, asked integration.PageRequest) (integration.Page, error) {
	path := "/repos/" + asked.Target["repo"] + "/issues"
	if asked.Cursor != "" && !p.ignoreCursor {
		path += "?page=" + asked.Cursor
	}
	p.note(http.MethodGet, path)
	response, err := session.HTTP.Do(request(ctx, http.MethodGet, session.BaseURL+path))
	if err != nil {
		return integration.Page{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return integration.Page{}, p.refusal(response)
	}
	body, _ := io.ReadAll(response.Body)
	var issues []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if json.Unmarshal(body, &issues) != nil {
		return integration.Page{}, errors.New("the issue list could not be read")
	}
	page := integration.Page{Items: make([]integration.Item, 0, len(issues))}
	for _, issue := range issues {
		if issue.Number == 0 {
			return integration.Page{}, &integration.AppError{Message: "an issue has no number"}
		}
		number := strconv.Itoa(issue.Number)
		page.Items = append(page.Items, integration.Item{
			ExternalID: "acme/api#" + number, Kind: "issue", Title: issue.Title,
			Content: []byte(`{"number":` + number + `}`),
		})
	}
	if asked.Cursor == "" && len(issues) == 1 {
		// A pass under way is told there is more; a drained one is not.
		page.Cursor = "2"
	}
	return page, nil
}

// Poll asks the app what has changed since its cursor, and the first ask has
// no cursor at all: a new subscription starts from now, not from the app's
// whole history.
func (p *probe) Poll(ctx context.Context, session integration.Session, _ string, _ json.RawMessage, cursor string) (integration.Batch, error) {
	if cursor != "" {
		// A pass that has been round once already has nothing to ask, which
		// is what a cursor the app gave is for.
		return integration.Batch{Cursor: cursor}, nil
	}
	p.note(http.MethodGet, "/issues/updates?since=")
	response, err := session.HTTP.Do(request(ctx, http.MethodGet, session.BaseURL+"/issues/updates?since="))
	if err != nil {
		return integration.Batch{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return integration.Batch{}, p.refusal(response)
	}
	body, _ := io.ReadAll(response.Body)
	var answer struct {
		Cursor string `json:"cursor"`
	}
	if json.Unmarshal(body, &answer) != nil {
		return integration.Batch{}, errors.New("the updates could not be read")
	}
	batch := integration.Batch{Cursor: answer.Cursor}
	for i := range p.firstPollEvents {
		batch.Events = append(batch.Events, integration.Event{
			ID: fmt.Sprintf("E%d", i+1), Trigger: "ACME_ISSUE_EVENT", OccurredAt: time.Now(),
			Data: json.RawMessage(`{"number":1}`),
		})
	}
	return batch, nil
}

// Subscribe registers the URL and the secret the core made, and answers the
// app's own id for the subscription; Unsubscribe takes it away again. Neither
// sees the secret afterwards: the core keeps it, and only the core uses it.
func (p *probe) Subscribe(_ context.Context, _ integration.Session, _ string, _ json.RawMessage, hook integration.Hook) (string, error) {
	if hook.URL == "" || hook.Secret == "" {
		return "", errors.New("the hook has no URL or no secret")
	}
	return "hook-" + strings.TrimPrefix(hook.URL, "https://"), nil
}

func (p *probe) Unsubscribe(_ context.Context, _ integration.Session, remoteID string) error {
	if remoteID == "" {
		return errors.New("there is no subscription to remove")
	}
	return nil
}

// Parse is what hooks' ingress calls over a delivery it has already verified:
// it reads and nothing else, and never sees a signature.
func (p *probe) Parse(_ context.Context, _ http.Header, body []byte) ([]integration.Event, error) {
	var delivery struct {
		EventID string `json:"event_id"`
		At      string `json:"occurred_at"`
		Ref     string `json:"ref"`
	}
	if json.Unmarshal(body, &delivery) != nil {
		return nil, errors.New("the delivery could not be read")
	}
	at, err := time.Parse(time.RFC3339, delivery.At)
	if err != nil {
		return nil, errors.New("the delivery carries no time")
	}
	// Whatever the payload schema requires, the event carries: a delivery
	// that names no ref publishes no ref, rather than an empty one.
	payload := map[string]string{}
	if delivery.Ref != "" {
		payload["ref"] = delivery.Ref
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return []integration.Event{{
		ID: delivery.EventID, Trigger: "ACME_PUSH_EVENT", AccountRef: "U01", OccurredAt: at,
		Data: data,
	}}, nil
}

func (p *probe) call(ctx context.Context, session integration.Session, request *http.Request) (json.RawMessage, error) {
	response, err := session.HTTP.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, p.refusal(response)
	}
	body, _ := io.ReadAll(response.Body)
	return body, nil
}

func request(ctx context.Context, method, target string) *http.Request {
	built, _ := http.NewRequestWithContext(ctx, method, target, nil)
	return built
}

// refusal is the four ways a call can be refused, mapped to the sentinels the
// core acts on: a refused credential, a rate limit, a 404, and a refusal in
// the app's own words.
func (p *probe) refusal(response *http.Response) error {
	switch response.StatusCode {
	case http.StatusUnauthorized:
		return integration.ErrNeedsReconnect
	case http.StatusTooManyRequests:
		return &integration.RateLimited{RetryAfter: 30 * time.Second}
	case http.StatusNotFound:
		return integration.ErrNotFound
	}
	if response.StatusCode >= 400 && p.unmapped != nil {
		return p.unmapped
	}
	body, _ := io.ReadAll(response.Body)
	var answer struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &answer)
	return &integration.AppError{Message: answer.Message}
}

// writeCassette records a transcript under dir, so the tests below can be
// read as the cassettes they run over.
func writeCassette(t *testing.T, dir, name string, arguments string, interactions ...string) {
	t.Helper()
	path := filepath.Join(dir, "cassettes", name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	document := `{"interactions":[` + strings.Join(interactions, ",") + `]}`
	if arguments != "" {
		document = `{"arguments":` + arguments + `,"interactions":[` + strings.Join(interactions, ",") + `]}`
	}
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
}

// exchange is one recorded request and its recorded answer. A request with a
// body of its own records it, as a cassette has to: a request matches on its
// body, so one that leaves it out would match nothing.
func exchange(method, target string, status int, body string) string {
	return fmt.Sprintf(`{"request":{"method":%q,"url":%q},"response":{"status":%d,"body":%s}}`,
		method, target, status, body)
}

// posted is the same, for the reads that read through a POST.
func posted(target string, status int, request, response string) string {
	return fmt.Sprintf(`{"request":{"method":"POST","url":%q,"body":%s},"response":{"status":%d,"body":%s}}`,
		target, request, status, response)
}

// patched is the same, for a write.
func patched(target string, status int, request, response string) string {
	return fmt.Sprintf(`{"request":{"method":"PATCH","url":%q,"body":%s},"response":{"status":%d,"body":%s}}`,
		target, request, status, response)
}

// wellBuilt records every cassette a toolkit that does everything right
// needs, and is what the tests below start from and then break one thing in.
func wellBuilt(t *testing.T, dir string) {
	t.Helper()
	writeCassette(t, dir, "ACME_LIST_ISSUES.ok.json", "",
		posted("https://api.acme.test/search", 200, `{"q":"is:open"}`, `[{"number":7,"title":"Flaky test"}]`))
	writeCassette(t, dir, "ACME_LIST_ISSUES.app_error.json", "",
		posted("https://api.acme.test/search", 400, `{"q":"is:open"}`, `{"message":"the query is not one we take"}`))
	writeCassette(t, dir, "ACME_LIST_ISSUES.unauthorized.json", "",
		posted("https://api.acme.test/search", 401, `{"q":"is:open"}`, `{"message":"bad credentials"}`))
	writeCassette(t, dir, "ACME_LIST_ISSUES.rate_limited.json", "",
		posted("https://api.acme.test/search", 429, `{"q":"is:open"}`, `{"message":"slow down"}`))
	writeCassette(t, dir, "ACME_CLOSE_ISSUE.ok.json", `{"repo":"acme/api","number":7}`,
		patched("https://api.acme.test/repos/acme/api/issues/7", 200, `{"state":"closed"}`, `{"number":7,"state":"closed"}`))
	writeCassette(t, dir, "ACME_CLOSE_ISSUE.app_error.json", `{"repo":"acme/api","number":7}`,
		patched("https://api.acme.test/repos/acme/api/issues/7", 422, `{"state":"closed"}`, `{"message":"it is already closed"}`))
	writeCassette(t, dir, "ACME_CLOSE_ISSUE.unauthorized.json", `{"repo":"acme/api","number":7}`,
		patched("https://api.acme.test/repos/acme/api/issues/7", 401, `{"state":"closed"}`, `{"message":"bad credentials"}`))
	writeCassette(t, dir, "ACME_CLOSE_ISSUE.rate_limited.json", `{"repo":"acme/api","number":7}`,
		patched("https://api.acme.test/repos/acme/api/issues/7", 429, `{"state":"closed"}`, `{"message":"slow down"}`))
	writeCassette(t, dir, "issues.targets.json", "",
		exchange("GET", "https://api.acme.test/user/repos?per_page=100", 200, `[{"full_name":"acme/api"}]`))
	writeCassette(t, dir, "issues.page1.json", "",
		exchange("GET", "https://api.acme.test/repos/acme/api/issues", 200, `[{"number":7,"title":"Flaky test"}]`))
	writeCassette(t, dir, "issues.page2.json", "",
		exchange("GET", "https://api.acme.test/repos/acme/api/issues?page=2", 200, `[{"number":8,"title":"Slow test"}]`))
	writeCassette(t, dir, "ACME_ISSUE_EVENT.first.json", "",
		exchange("GET", "https://api.acme.test/issues/updates?since=", 200, `{"cursor":"since-2026-09-17"}`))
	writeDelivery(t, dir, "ACME_PUSH_EVENT", "branch_pushed.json",
		`{"event_id":"E1","occurred_at":"2026-09-17T12:00:00Z","ref":"refs/heads/main"}`)
}

func writeDelivery(t *testing.T, dir, slug, name, body string) {
	t.Helper()
	path := filepath.Join(dir, "hooks", slug, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func remove(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.Remove(filepath.Join(dir, "cassettes", name)); err != nil {
		t.Fatal(err)
	}
}

// A toolkit that does everything right passes. This is the shape of the
// cassettes every phase 2 worker writes, and the check that the harness reads
// them all as intended.
func TestConformancePassesForAWellBuiltIntegration(t *testing.T) {
	dir := t.TempDir()
	wellBuilt(t, dir)
	Conformance(t, &probe{}, dir)
}

// Conformance must say what is wrong rather than pass a broken toolkit, so
// each of its rules is shown refusing a toolkit that breaks it.
func TestConformanceSaysWhatIsWrong(t *testing.T) {
	cases := []struct {
		name    string
		toolkit func(t *testing.T) *probe
		break_  func(t *testing.T, dir string)
		says    string
	}{
		{
			name: "a manifest that does not validate",
			toolkit: func(*testing.T) *probe {
				own := &probe{}
				manifest := own.Manifest()
				// A tool named without the toolkit's prefix is the mistake
				// that breaks a member's existing skill files, since those
				// read tools by the names Composio gave them.
				manifest.Tools[0].Name = "LIST_ISSUES"
				manifest.Tools[0].InputSchema = json.RawMessage(`{}`)
				own.manifest = &manifest
				return own
			},
			break_: func(t *testing.T, dir string) { wellBuilt(t, dir) },
			says:   "does not validate",
		},
		{
			name:    "a tool with no ok cassette",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				remove(t, dir, "ACME_LIST_ISSUES.ok.json")
			},
			says: "has no ok cassette",
		},
		{
			name:    "a tool with no app_error cassette",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				remove(t, dir, "ACME_CLOSE_ISSUE.app_error.json")
			},
			says: "has no app_error cassette",
		},
		{
			name: "a status the integration does not map",
			toolkit: func(*testing.T) *probe {
				return &probe{unmapped: errors.New("the app answered 500")}
			},
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_CLOSE_ISSUE.app_error.json", `{"repo":"acme/api","number":7}`,
					patched("https://api.acme.test/repos/acme/api/issues/7", 500, `{"state":"closed"}`, `{}`))
			},
			says: "want an *integration.AppError",
		},
		{
			name:    "a 401 mapped to something else",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_LIST_ISSUES.unauthorized.json", "",
					posted("https://api.acme.test/search", 404, `{"q":"is:open"}`, `{"message":"gone"}`))
			},
			says: "want ErrNeedsReconnect",
		},
		{
			name:    "a 429 mapped to something else",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_LIST_ISSUES.rate_limited.json", "",
					posted("https://api.acme.test/search", 200, `{"q":"is:open"}`, `[]`))
			},
			says: "says the app refused",
		},
		{
			name:    "a read tool whose cassettes write",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_LIST_ISSUES.ok.json", "",
					posted("https://api.acme.test/search", 200, `{"q":"is:open"}`, `[]`),
					exchange("PATCH", "https://api.acme.test/repos/acme/api/issues/7", 200, `{}`))
			},
			says: "a read tool may only GET, HEAD",
		},
		{
			name:    "a read tool posting outside its read_posts",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_LIST_ISSUES.ok.json", "",
					posted("https://api.acme.test/search", 200, `{"q":"is:open"}`, `[]`),
					posted("https://api.acme.test/repos/acme/api/issues/7/comments", 200, `{"body":"x"}`, `{}`))
			},
			says: "is not one of ACME_LIST_ISSUES's read_posts",
		},
		{
			name:    "a reader that ignores the cursor",
			toolkit: func(*testing.T) *probe { return &probe{ignoreCursor: true} },
			break_:  func(t *testing.T, dir string) { wellBuilt(t, dir) },
			says:    "is not in it",
		},
		{
			name:    "a first poll that hands over the app's history",
			toolkit: func(*testing.T) *probe { return &probe{firstPollEvents: 2} },
			break_:  func(t *testing.T, dir string) { wellBuilt(t, dir) },
			says:    "it must answer none",
		},
		{
			name:    "a first poll with no cursor",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "ACME_ISSUE_EVENT.first.json", "",
					exchange("GET", "https://api.acme.test/issues/updates?since=", 200, `{}`))
			},
			says: "answers no cursor",
		},
		{
			name:    "a pushed event whose data breaks its own schema",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeDelivery(t, dir, "ACME_PUSH_EVENT", "branch_pushed.json",
					`{"event_id":"E1","occurred_at":"2026-09-17T12:00:00Z"}`)
			},
			says: "does not match ACME_PUSH_EVENT",
		},
		{
			name:    "a pushed event with no delivery recorded",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				if err := os.Remove(filepath.Join(dir, "hooks", "ACME_PUSH_EVENT", "branch_pushed.json")); err != nil {
					t.Fatal(err)
				}
			},
			says: "needs at least one delivery",
		},
		{
			name:    "a stale cassette",
			toolkit: func(*testing.T) *probe { return &probe{} },
			break_: func(t *testing.T, dir string) {
				wellBuilt(t, dir)
				writeCassette(t, dir, "issues.page1.json", "",
					exchange("GET", "https://api.acme.test/repos/acme/api/issues", 200,
						`[{"number":7,"title":"Flaky test"}]`),
					exchange("GET", "https://api.acme.test/repos/acme/web/issues", 200, `[]`))
			},
			says: "which nothing asked for",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			test.break_(t, dir)
			problems := Check(test.toolkit(t), dir)
			if len(problems) == 0 {
				t.Fatalf("Conformance passed a toolkit where %s", test.name)
			}
			if test.says != "" && !strings.Contains(strings.Join(problems, "\n"), test.says) {
				t.Fatalf("Conformance said %v, which does not mention %q", problems, test.says)
			}
		})
	}
}

// The harness's own promises, checked without a toolkit: an unmatched request
// is refused, a query is matched however it is spelled, a session carries the
// connection's own settings, and a cassette that holds a credential never
// loads.
func TestTheSessionRefusesWhatACassetteDoesNotRecord(t *testing.T) {
	dir := t.TempDir()
	transcript := filepath.Join(dir, "call.json")
	if err := os.WriteFile(transcript, []byte(`{"interactions":[
	  {"request":{"method":"GET","url":"https://api.acme.test/repos/acme/api/issues?state=open&per_page=50"},
	   "response":{"status":200,"body":[{"number":7}]}}]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("a query is matched however it is spelled", func(t *testing.T) {
		session := Session(t, transcript, nil)
		response, err := session.HTTP.Get(session.BaseURL + "/repos/acme/api/issues?per_page=50&state=open")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		if response.StatusCode != http.StatusOK || !strings.Contains(string(body), `"number":7`) {
			t.Fatalf("the cassette answered %d %s", response.StatusCode, body)
		}
	})

	t.Run("a path is matched, not a prefix of it", func(t *testing.T) {
		caught := &collector{}
		session := Session(caught, transcript, nil)
		if _, err := session.HTTP.Get(session.BaseURL + "/repos/acme/api/other?per_page=50&state=open"); err == nil {
			t.Fatal("a path the cassette does not record was served")
		}
		if !strings.Contains(strings.Join(caught.found, "\n"), "is not in it") {
			t.Fatalf("the refusal said %v", caught.found)
		}
	})

	t.Run("a session carries the connection's own settings", func(t *testing.T) {
		session := Session(t, transcript, map[string]string{"subdomain": "acme"})
		// The cassette is read once here too, so it is not left holding an
		// exchange this test never asked for.
		if response, err := session.HTTP.Get(session.BaseURL + "/repos/acme/api/issues?per_page=50&state=open"); err != nil {
			t.Fatal(err)
		} else {
			response.Body.Close()
		}
		if session.Settings["subdomain"] != "acme" {
			t.Fatalf("settings = %v", session.Settings)
		}
		if session.Settings[integration.SettingAccountLabel] == "" {
			t.Fatalf("the session carries no account label: %v", session.Settings)
		}
		token, err := session.Token(context.Background())
		if err != nil || token != FakeToken {
			t.Fatalf("Token() = %q, %v", token, err)
		}
	})

	t.Run("a cassette that carries a credential never loads", func(t *testing.T) {
		leaked := filepath.Join(dir, "leaked.json")
		if err := os.WriteFile(leaked, []byte(`{"interactions":[
		  {"request":{"method":"GET","url":"https://api.acme.test/user"},
		   "response":{"status":200,"headers":{"Authorization":["Bearer ghp_realtoken"]},"body":{}}}]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		caught := &collector{}
		load(caught, leaked)
		if !strings.Contains(strings.Join(caught.found, "\n"), "never recorded") {
			t.Fatalf("loading a cassette with a credential said %v", caught.found)
		}
	})
}
