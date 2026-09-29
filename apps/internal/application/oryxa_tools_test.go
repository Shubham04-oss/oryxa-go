package application

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// The integrations this build ships declare how to sign in and declare no
// tools, readers or triggers, so nothing about the Oryxa route can be
// exercised without one. testIntegration is a toolkit that carries all three,
// and every tool, reader and trigger answers from fields a test sets.

// testIntegration is a toolkit whose tools, reader and poll a test drives.
type testIntegration struct {
	mu sync.Mutex
	// run is what each tool's function does. A nil entry answers the slug,
	// so a test only sets the one it is about.
	run map[string]func(ctx context.Context, args json.RawMessage) (json.RawMessage, error)
	// called records each run, by tool, with the arguments it was given and
	// the idempotency key in its context.
	called []testCall
	// targets is what the reader lists, and pages the pages it answers, by
	// the cursor a request carries; "" is the first page.
	targets []integration.Target
	pages   map[string]integration.Page
	// poll is what a poll answers, by cursor.
	poll map[string]integration.Batch
}

type testCall struct {
	Tool      string
	Arguments string
	Key       string
	// BaseURL and Settings are what the session carried, so a test can see
	// the core's own work reach the tool.
	BaseURL  string
	Settings string
}

func newTestIntegration() *testIntegration {
	return &testIntegration{
		run:   map[string]func(context.Context, json.RawMessage) (json.RawMessage, error){},
		pages: map[string]integration.Page{}, poll: map[string]integration.Batch{},
	}
}

func (i *testIntegration) record(tool string, session integration.Session, args json.RawMessage, key string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.called = append(i.called, testCall{
		Tool: tool, Arguments: string(args), Key: key,
		BaseURL: session.BaseURL, Settings: string(mustJSON(session.Settings)),
	})
}

func (i *testIntegration) calls(tool string) []testCall {
	i.mu.Lock()
	defer i.mu.Unlock()
	var matched []testCall
	for _, call := range i.called {
		if call.Tool == tool {
			matched = append(matched, call)
		}
	}
	return matched
}

func (i *testIntegration) allCalls() []testCall {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.called)
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}

// Manifest is a toolkit with a read tool, a write tool and a read that
// reads through a POST, a reader and a polled trigger, which is the smallest
// manifest that exercises every part of the route.
func (i *testIntegration) Manifest() integration.Manifest {
	return integration.Manifest{
		Toolkit: "acme", Version: 1, Name: "Acme", APIBase: "https://api.acme.test",
		Auth: integration.Auth{
			Methods: []integration.Method{{Kind: integration.KindAPIToken, Place: &integration.Place{
				In: integration.InHeader, Name: "Authorization", Template: "Bearer {token}"}}},
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
				Name: "ACME_LIST_ISSUES", Description: "List the issues", ReadOnly: true,
				ReadPOSTs: []string{"/search"}, Scopes: []string{"read"},
				InputSchema: json.RawMessage(`{"type":"object","required":["owner"],
					"properties":{"owner":{"type":"string"},"state":{"type":"string","enum":["open","closed"]}}}`),
			},
			{
				Name: "ACME_CREATE_AN_ISSUE", Description: "Open an issue", Destructive: true, Scopes: []string{"write"},
				InputSchema: json.RawMessage(`{"type":"object","required":["owner","title"],
					"properties":{"owner":{"type":"string"},"title":{"type":"string"},"body":{"type":"string"}}}`),
			},
		},
		Readers: []integration.ReaderSpec{{
			Name: "issues", Kinds: []string{"issue"},
			TargetSchema: json.RawMessage(`{"type":"object","required":["repo"]}`),
		}},
		Triggers: []integration.TriggerSpec{{
			Slug: "ACME_ISSUE_EVENT", Name: "An issue changed", Kind: integration.TriggerPoll,
			ConfigSchema:  json.RawMessage(`{"type":"object","properties":{"repo":{"type":"string"}}}`),
			PayloadSchema: json.RawMessage(`{"type":"object","required":["number"]}`),
			Sample:        json.RawMessage(`{"number":7}`),
		}},
	}
}

func (i *testIntegration) Tools() map[string]integration.ToolFunc {
	return map[string]integration.ToolFunc{
		"ACME_LIST_ISSUES": func(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
			i.record("ACME_LIST_ISSUES", session, args, integration.IdempotencyKey(ctx))
			return i.answer("ACME_LIST_ISSUES", ctx, args)
		},
		"ACME_CREATE_AN_ISSUE": func(ctx context.Context, session integration.Session, args json.RawMessage) (json.RawMessage, error) {
			i.record("ACME_CREATE_AN_ISSUE", session, args, integration.IdempotencyKey(ctx))
			return i.answer("ACME_CREATE_AN_ISSUE", ctx, args)
		},
	}
}

func (i *testIntegration) answer(tool string, ctx context.Context, args json.RawMessage) (json.RawMessage, error) {
	i.mu.Lock()
	run, ok := i.run[tool]
	i.mu.Unlock()
	if ok {
		return run(ctx, args)
	}
	return json.RawMessage(`{"ok":true}`), nil
}

// Targets and Read are the reader, and a Poller over the same integration.
func (i *testIntegration) Targets(context.Context, integration.Session) ([]integration.Target, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	return slices.Clone(i.targets), nil
}

func (i *testIntegration) Read(_ context.Context, _ integration.Session, request integration.PageRequest) (integration.Page, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	page, ok := i.pages[request.Cursor]
	if !ok {
		return integration.Page{}, integration.ErrNotFound
	}
	return page, nil
}

func (i *testIntegration) Poll(_ context.Context, _ integration.Session, _ string, _ json.RawMessage, cursor string) (integration.Batch, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	batch, ok := i.poll[cursor]
	if !ok {
		return integration.Batch{}, integration.ErrNotFound
	}
	return batch, nil
}

// newToolHarness is the application with Oryxa's own sign-in on for acme,
// over a registry holding the test integration.
func newToolHarness(t *testing.T) (oryxaHarness, *testIntegration) {
	t.Helper()
	own := newTestIntegration()
	registry := integration.NewRegistry()
	if err := registry.Register(own); err != nil {
		t.Fatal(err)
	}
	return newOryxaHarnessWith(t, registry, "acme"), own
}

// A member on an Oryxa connection is answered from the registry: the same
// ToolView JSON, matched the same way, and Composio is never asked.
func TestSearchToolsOverAnOryxaConnectionAnswersFromTheRegistry(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	// Composio knows the same tools, and must not be asked for them.
	h.composio.tools["ACME_LIST_ISSUES"] = composio.Tool{
		Slug: "ACME_LIST_ISSUES", Toolkit: "acme", Name: "List issues", Tags: []string{"important"},
	}
	ctx := context.Background()

	views, err := h.service.SearchTools(ctx, memberToken, "org_1", "acme", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %+v, want both of the manifest's tools", views)
	}
	if views[0].Tool != "ACME_LIST_ISSUES" || !views[0].ReadOnly || views[0].Destructive {
		t.Fatalf("the first view is %+v", views[0])
	}
	if views[1].Tool != "ACME_CREATE_AN_ISSUE" || views[1].ReadOnly || !views[1].Destructive {
		t.Fatalf("the second view is %+v", views[1])
	}
	for _, view := range views {
		if view.Toolkit != "acme" || view.InputSchema == nil {
			t.Fatalf("view = %+v, want the toolkit and the tool's own schema", view)
		}
	}
	// The match is on name and description, and ignores case.
	views, err = h.service.SearchTools(ctx, memberToken, "org_1", "acme", "OPEN AN", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Tool != "ACME_CREATE_AN_ISSUE" {
		t.Fatalf("a description search = %+v", views)
	}
	views, err = h.service.SearchTools(ctx, memberToken, "org_1", "acme", "nothing here", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("a search that matches nothing = %+v", views)
	}
	// A limit is a limit.
	views, err = h.service.SearchTools(ctx, memberToken, "org_1", "acme", "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("a limited search = %+v", views)
	}
	if h.composio.searches != 0 {
		t.Fatalf("composio was searched %d times", h.composio.searches)
	}
	_ = own
}

// A toolkit the registry has tools for is still Composio's for a member who
// has not reconnected: a member keeps Composio until they do.
func TestSearchToolsOverAComposioConnectionIsStillComposios(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.composioConnection("apc_composio", "user-1", "acme")
	h.composio.tools["ACME_LIST_ISSUES"] = composio.Tool{
		Slug: "ACME_LIST_ISSUES", Toolkit: "acme", Name: "List issues", Tags: []string{"important", composio.HintReadOnly},
	}
	views, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "acme", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Tool != "ACME_LIST_ISSUES" {
		t.Fatalf("views = %+v, want composio's own answer", views)
	}
	if h.composio.searches == 0 {
		t.Fatal("composio was never searched")
	}
}

// An Oryxa connection whose toolkit this build carries no tools for answers
// an empty list, never Composio's, because a tool that cannot be run for the
// member must not be offered.
func TestSearchToolsOverAnOryxaConnectionNeverFallsBackToComposio(t *testing.T) {
	t.Parallel()
	h := newOryxaHarness(t, "github")
	h.oryxaConnection("apc_oryxa", "user-1", "github", domain.ConnectionActive, integration.KindAPIToken)
	h.composio.tools["GITHUB_LIST_ISSUES"] = composio.Tool{
		Slug: "GITHUB_LIST_ISSUES", Toolkit: "github", Name: "List issues", Tags: []string{"important"},
	}
	views, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 0 {
		t.Fatalf("views = %+v, want none", views)
	}
	if h.composio.searches != 0 {
		t.Fatalf("composio was searched %d times", h.composio.searches)
	}
}

// A run over an Oryxa connection is recorded exactly as a Composio run is,
// and the tool's own function does the work.
func TestRunToolOverAnOryxaConnectionRunsTheIntegrationsOwn(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.answer = func(_ context.Context, connectionID string) (integration.Session, error) {
		return integration.Session{
			BaseURL: "https://api.acme.test", Settings: map[string]string{integration.SettingAccountLabel: "acme_house"},
		}, nil
	}
	ctx := context.Background()

	run, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionSucceeded || string(run.Data) != `{"ok":true}` {
		t.Fatalf("run = %+v", run)
	}
	calls := own.calls("ACME_LIST_ISSUES")
	if len(calls) != 1 || calls[0].Arguments != `{"owner":"acme"}` {
		t.Fatalf("the tool was called %+v", calls)
	}
	if calls[0].Key != "" {
		t.Fatalf("a read carried an idempotency key: %q", calls[0].Key)
	}
	if !strings.Contains(calls[0].Settings, integration.SettingAccountLabel) {
		t.Fatalf("the session carried no account label: %s", calls[0].Settings)
	}
	// The run is in apps_actions, against the member and the connection.
	h.store.mu.Lock()
	recorded, ok := h.store.actions[run.Action.ID]
	h.store.mu.Unlock()
	if !ok || recorded.Tool != "ACME_LIST_ISSUES" || recorded.Toolkit != "acme" ||
		recorded.ConnectionID != "apc_oryxa" || recorded.MemberID != "user-1" || !recorded.ReadOnly {
		t.Fatalf("the recorded action is %+v", recorded)
	}
	// A read announces nothing, as it does not for Composio.
	if types := h.eventTypes(); len(types) != 0 {
		t.Fatalf("a read published %v", types)
	}
	// The session was asked for by the connection, through signin.
	if got := h.signin.sessionCalls(); len(got) != 1 || got[0] != "apc_oryxa" {
		t.Fatalf("signin was asked for sessions %v", got)
	}
	if len(h.composio.executed) != 0 {
		t.Fatalf("composio ran the tool: %+v", h.composio.executed)
	}
}

// A write takes the caller's idempotency key, hands it to the tool, keeps
// what the app answered, and announces apps.action.completed.
func TestRunToolOverAnOryxaConnectionRecordsAWrite(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()

	run, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: "ACME_CREATE_AN_ISSUE", Arguments: json.RawMessage(`{"owner":"acme","title":"Flaky test"}`),
		IdempotencyKey: "issue-2026-09-29-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionSucceeded || string(run.Data) != `{"ok":true}` {
		t.Fatalf("run = %+v", run)
	}
	if got := own.calls("ACME_CREATE_AN_ISSUE")[0].Key; got != "issue-2026-09-29-1" {
		t.Fatalf("the tool saw the key %q", got)
	}
	// A write keeps what the app answered, so a replay can answer it.
	h.store.mu.Lock()
	recorded := h.store.actions[run.Action.ID]
	h.store.mu.Unlock()
	if string(recorded.Result) != `{"ok":true}` || recorded.IdempotencyKey != "issue-2026-09-29-1" {
		t.Fatalf("the recorded action is %+v", recorded)
	}
	types := h.eventTypes()
	if len(types) != 1 || types[0] != appsv1.ActionCompletedDetailType {
		t.Fatalf("a write published %v", types)
	}
	var completed appsv1.ActionCompleted
	decodeLast(t, h.store, appsv1.ActionCompletedDetailType, &completed)
	if completed.ActionID != run.Action.ID || completed.Tool != "ACME_CREATE_AN_ISSUE" || completed.ReadOnly {
		t.Fatalf("completed = %+v", completed)
	}

	// Asking again under the same key answers the first run rather than
	// running it again.
	again, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: "ACME_CREATE_AN_ISSUE", Arguments: json.RawMessage(`{"owner":"acme","title":"Flaky test"}`),
		IdempotencyKey: "issue-2026-09-29-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Action.ID != run.Action.ID || string(again.Data) != `{"ok":true}` {
		t.Fatalf("the replay = %+v", again)
	}
	if len(own.calls("ACME_CREATE_AN_ISSUE")) != 1 {
		t.Fatal("the tool ran twice under one key")
	}
	if types := h.eventTypes(); len(types) != 1 {
		t.Fatalf("a replay published %v", types)
	}
}

// Arguments are checked against the tool's own input_schema before anything
// is sent, and a refusal names the field and the rule, never the value.
func TestRunToolOverAnOryxaConnectionChecksItsArguments(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	ctx := context.Background()

	cases := map[string]string{
		"a field the tool needs is missing": `{"repo":"api"}`,
		"a field of the wrong type":         `{"owner":7}`,
		"a value outside an enum":           `{"owner":"acme","state":"merged"}`,
		"arguments that are not an object":  `["acme"]`,
	}
	for name, arguments := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
				Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(arguments),
			})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("RunTool = %v, want invalid", err)
			}
		})
	}
	if len(own.allCalls()) != 0 {
		t.Fatalf("a refused run reached the tool: %+v", own.allCalls())
	}
	h.store.mu.Lock()
	actions := len(h.store.actions)
	h.store.mu.Unlock()
	if actions != 0 {
		t.Fatalf("%d refused runs were recorded", actions)
	}
}

// An app that refuses the call in its own words is answered, not refused: the
// action failed, the caller is told the run did not succeed, and the reason
// is the app's own. It is never a token, and never a log line.
func TestRunToolAnswersAnAppRefusalAsAppError(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	own.run["ACME_LIST_ISSUES"] = func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, &integration.AppError{Message: "the repository does not exist"}
	}
	ctx := context.Background()

	run, err := h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`),
	})
	if err != nil {
		t.Fatalf("a refusal was answered as a failure: %v", err)
	}
	if run.Action.Status != domain.ActionFailed {
		t.Fatalf("the action is %+v, want failed", run.Action)
	}
	if run.Action.Error != "the repository does not exist" {
		t.Fatalf("the reason is %q, want the app's own", run.Action.Error)
	}
	// A read announces nothing, so nothing carries the app's words out.
	if types := h.eventTypes(); len(types) != 0 {
		t.Fatalf("a refused read published %v", types)
	}
	// A refused write is announced, with app_error rather than the app's
	// words, because an event travels further than the member.
	own.run["ACME_CREATE_AN_ISSUE"] = func(context.Context, json.RawMessage) (json.RawMessage, error) {
		return nil, &integration.AppError{Message: "the title is taken"}
	}
	run, err = h.service.RunTool(ctx, memberToken, "org_1", RunToolInput{
		Tool: "ACME_CREATE_AN_ISSUE", Arguments: json.RawMessage(`{"owner":"acme","title":"y"}`),
		IdempotencyKey: "issue-2026-09-29-3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Error != "the title is taken" {
		t.Fatalf("the reason is %q", run.Action.Error)
	}
	var failed appsv1.ActionFailed
	decodeLast(t, h.store, appsv1.ActionFailedDetailType, &failed)
	if failed.Error != "app_error" {
		t.Fatalf("the event carries %q, want app_error", failed.Error)
	}
	if strings.Contains(h.allEvents(), "the title is taken") {
		t.Fatal("the app's own words reached an event")
	}
}

// The app's own kinds of failure become apps' own reasons, so a caller reads
// one set of words whichever broker ran the tool.
func TestRunToolMapsAnIntegrationsOwnErrors(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		failure error
		reason  string
		is      error
	}{
		{"a refused credential", integration.ErrNeedsReconnect, "needs_reconnect", domain.ErrNeedsReconnect},
		{"something that is not there", integration.ErrNotFound, "not_found", domain.ErrNotFound},
		{"a rate limit", &integration.RateLimited{RetryAfter: 30}, "rate_limited", domain.ErrRateLimited},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			h, own := newToolHarness(t)
			h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
			own.run["ACME_LIST_ISSUES"] = func(context.Context, json.RawMessage) (json.RawMessage, error) {
				return nil, test.failure
			}
			run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
				Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`),
			})
			if !errors.Is(err, test.is) {
				t.Fatalf("RunTool = %v, want %v", err, test.is)
			}
			if run.Action.Status != domain.ActionFailed || run.Action.Error != test.reason {
				t.Fatalf("the action is %+v, want failed with %q", run.Action, test.reason)
			}
		})
	}
}

// A caller that may only read cannot run a tool the manifest says changes
// something, whatever the app would have said.
func TestRunToolOnlyReadsRefusesAnOwnWrite(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	_, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
		Tool: "ACME_CREATE_AN_ISSUE", Arguments: json.RawMessage(`{"owner":"acme","title":"x"}`),
		IdempotencyKey: "issue-2026-09-29-4", OnlyReads: true,
	})
	if !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("RunTool = %v, want not read only", err)
	}
	if len(own.allCalls()) != 0 {
		t.Fatalf("the tool ran: %+v", own.allCalls())
	}
}

// An agent may run a member's own tools, as it may their Composio ones, and
// the run is recorded as the agent's.
func TestRunToolOverAnOryxaConnectionActsForTheMember(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	run, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
		Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.MemberID != "user-1" || !run.Action.Agent {
		t.Fatalf("the action is %+v", run.Action)
	}
	// Another member's connection is not this agent's to act through.
	h.oryxaConnection("apc_other", "user-2", "acme", domain.ConnectionActive, integration.KindAPIToken)
	if _, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
		Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`), ConnectionID: "apc_other",
	}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's connection = %v, want not found", err)
	}
}

// A credential that cannot be had has run nothing, so nothing is recorded.
func TestRunToolRecordsNothingWhenTheSessionCannotBeBuilt(t *testing.T) {
	t.Parallel()
	h, own := newToolHarness(t)
	h.oryxaConnection("apc_oryxa", "user-1", "acme", domain.ConnectionActive, integration.KindAPIToken)
	h.signin.sessionErr = integration.ErrNeedsReconnect
	_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: "ACME_LIST_ISSUES", Arguments: json.RawMessage(`{"owner":"acme"}`),
	})
	if !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("RunTool = %v, want needs reconnect", err)
	}
	if len(own.allCalls()) != 0 {
		t.Fatal("the tool ran without a session")
	}
	h.store.mu.Lock()
	actions := len(h.store.actions)
	h.store.mu.Unlock()
	if actions != 0 {
		t.Fatalf("%d runs were recorded", actions)
	}
}

// The registry is consulted before Composio, so a toolkit's own tool is
// found without a Composio call at all, and a tool it does not have falls
// back to Composio's catalog.
func TestRoutePrefersTheRegistryOverComposio(t *testing.T) {
	t.Parallel()
	h, _ := newToolHarness(t)
	ctx := context.Background()

	route, err := h.service.routeTool(ctx, "ACME_LIST_ISSUES")
	if err != nil {
		t.Fatal(err)
	}
	if !route.hasOwn || route.toolkit != "acme" || !route.readOnly {
		t.Fatalf("route = %+v", route)
	}
	if h.composio.toolReads != 0 {
		t.Fatalf("composio was asked for the tool %d times", h.composio.toolReads)
	}
	route, err = h.service.routeTool(ctx, "ACME_CREATE_AN_ISSUE")
	if err != nil {
		t.Fatal(err)
	}
	if !route.hasOwn || route.readOnly {
		t.Fatalf("a write's route = %+v", route)
	}
	h.composio.tools["NOTION_QUERY"] = composio.Tool{Slug: "NOTION_QUERY", Toolkit: "notion"}
	route, err = h.service.routeTool(ctx, "NOTION_QUERY")
	if err != nil {
		t.Fatal(err)
	}
	if route.hasOwn || route.toolkit != "notion" {
		t.Fatalf("a composio tool's route = %+v", route)
	}
	if _, err := h.service.routeTool(ctx, "ACME_NO_SUCH_TOOL"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("a slug neither knows = %v, want not found", err)
	}
}

// A read tool's session is signin's, and it refuses to send anything else:
// signin is what enforces it, and the route asks for it by passing the
// tool's own read_posts.
func TestReadToolViewsCarryTheToolsOwnMark(t *testing.T) {
	t.Parallel()
	specs := []integration.ToolSpec{{Name: "ACME_LIST_ISSUES", ReadOnly: true, ReadPOSTs: []string{"/search"}}}
	views := ownToolViews("acme", specs, "", 0)
	if len(views) != 1 || !views[0].ReadOnly || views[0].Destructive {
		t.Fatalf("views = %+v", views)
	}
	if got := toolkitsOf(map[string]ownTool{
		"ACME_LIST_ISSUES": {Toolkit: "acme"}, "ACME_CREATE_AN_ISSUE": {Toolkit: "acme"},
		"SLACK_POST_MESSAGE": {Toolkit: "slack"},
	}); !slices.Equal(got, []string{"acme", "slack"}) {
		t.Fatalf("toolkits = %v", got)
	}
	// A limit of zero means every tool, as the search's own does.
	if len(ownToolViews("acme", specs, "", 0)) != 1 {
		t.Fatal("a limit of zero dropped everything")
	}
}

// toolkitsOf is the index's toolkit listing, over an index a test built
// rather than a registry.
func toolkitsOf(index map[string]ownTool) []string {
	toolkits := make([]string, 0, len(index))
	for _, tool := range index {
		toolkits = append(toolkits, tool.Toolkit)
	}
	return slices.Compact(slices.Sorted(slices.Values(toolkits)))
}
