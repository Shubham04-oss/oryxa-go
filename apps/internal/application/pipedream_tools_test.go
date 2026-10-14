package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// fakePipedream answers actions per app and runs them with run; every other
// method of the port panics, since nothing here should reach it.
type fakePipedream struct {
	Pipedream
	actions   map[string][]PipedreamComponent
	listCalls int
	runs      []PipedreamRunRequest
	run       func(PipedreamRunRequest) (PipedreamRunResult, error)
}

func (fake *fakePipedream) ListActions(_ context.Context, app string) ([]PipedreamComponent, error) {
	fake.listCalls++
	return fake.actions[app], nil
}

func (fake *fakePipedream) RunAction(_ context.Context, request PipedreamRunRequest) (PipedreamRunResult, error) {
	fake.runs = append(fake.runs, request)
	if fake.run != nil {
		return fake.run(request)
	}
	return PipedreamRunResult{Return: json.RawMessage(`{"id":"m_1"}`), Exports: json.RawMessage(`{"$summary":"sent"}`)}, nil
}

func hint(value bool) *bool { return &value }

const gmailProps = `[
	{"name":"gmail","type":"app","app":"gmail"},
	{"name":"note","type":"alert","content":"read me"},
	{"name":"to","type":"string[]","label":"To","description":"Recipients"},
	{"name":"subject","type":"string","label":"Subject"},
	{"name":"maxResults","type":"integer","optional":true},
	{"name":"html","type":"boolean","optional":true},
	{"name":"headers","type":"object","optional":true},
	{"name":"label","type":"string","optional":true,"remoteOptions":true}
]`

func gmailActions() []PipedreamComponent {
	return []PipedreamComponent{
		{Key: "gmail-send-email", Name: "Send Email", Description: "Send an email", Props: json.RawMessage(gmailProps), DestructiveHint: hint(false)},
		{Key: "gmail-find-email", Name: "Find Email", Description: "Find emails", ReadOnlyHint: hint(true),
			Props: json.RawMessage(`[{"name":"gmail","type":"app","app":"gmail"},{"name":"q","type":"string"}]`)},
		{Key: "gmail-archive-email", Name: "Archive Email", ReadOnlyHint: hint(false), DestructiveHint: hint(true)},
		{Key: "gmail-delete-email", Name: "Delete Email", ReadOnlyHint: hint(true)},
		{Key: "gmail-add-label", Name: "Add Label", Props: json.RawMessage(`[{"name":"label","type":"string","remoteOptions":true}]`)},
		{Key: "gmail-reloading", Name: "Reloading", Props: json.RawMessage(`[{"name":"kind","type":"string","reloadProps":true}]`)},
	}
}

// newPipedreamHarness is a harness whose Pipedream has Gmail's actions and
// which denies gmail-delete-email, with user-1 connected to Gmail through
// Pipedream.
func newPipedreamHarness(t *testing.T) (harness, *fakePipedream) {
	t.Helper()
	h := newHarness(t)
	fake := &fakePipedream{actions: map[string][]PipedreamComponent{"gmail": gmailActions()}}
	h.service.pipedream = fake
	h.service.pipedreamDeny = map[string]bool{"gmail-delete-email": true}
	connection := h.activeConnection("apc_pd", "user-1", "gmail", "")
	connection.Broker, connection.PipedreamAccountID = domain.BrokerPipedream, "apn_1"
	h.store.connections[connection.ID] = connection
	return h, fake
}

func TestPipedreamSlugRoundTripsAndRefusesWhatCannotMap(t *testing.T) {
	t.Parallel()
	for key, want := range map[string]string{
		"gmail-send-email":             "PD_GMAIL_SEND_EMAIL",
		"google_sheets-add-single-row": "PD_GOOGLE_SHEETS_ADD_SINGLE_ROW",
		"x-1":                          "PD_X_1",
	} {
		slug, ok := pipedreamSlug(key)
		if !ok || slug != want {
			t.Fatalf("pipedreamSlug(%q) = %q, %v; want %q", key, slug, ok, want)
		}
		if _, err := domain.CheckTool(slug); err != nil {
			t.Fatalf("%s fails the slug rule: %v", slug, err)
		}
	}
	for _, key := range []string{"gmail send", "gmail.send", "gmäil-send", strings.Repeat("a", 126)} {
		if slug, ok := pipedreamSlug(key); ok {
			t.Fatalf("pipedreamSlug(%q) = %q, want refused", key, slug)
		}
	}
	if _, ok := pipedreamSlug(strings.Repeat("a", 125)); !ok {
		t.Fatal("a key that fits exactly was refused")
	}
}

func TestPipedreamActionsTranslateTheirPropsAndMarks(t *testing.T) {
	t.Parallel()
	h, fake := newPipedreamHarness(t)
	actions, err := h.service.pipedreamActionsFor(context.Background(), "gmail")
	if err != nil {
		t.Fatal(err)
	}
	bySlug := map[string]pipedreamAction{}
	for _, action := range actions {
		bySlug[action.slug] = action
	}
	// Denied, reloading and remote-options-required actions are not offered.
	for _, slug := range []string{"PD_GMAIL_DELETE_EMAIL", "PD_GMAIL_RELOADING", "PD_GMAIL_ADD_LABEL"} {
		if _, ok := bySlug[slug]; ok {
			t.Fatalf("%s was offered", slug)
		}
	}
	if len(actions) != 3 {
		t.Fatalf("actions = %d, want 3", len(actions))
	}
	// Read only on an explicit true only; false and missing are writes.
	if !bySlug["PD_GMAIL_FIND_EMAIL"].readOnly || bySlug["PD_GMAIL_SEND_EMAIL"].readOnly || bySlug["PD_GMAIL_ARCHIVE_EMAIL"].readOnly {
		t.Fatalf("read-only marks = %+v", bySlug)
	}
	if !bySlug["PD_GMAIL_ARCHIVE_EMAIL"].destructive || bySlug["PD_GMAIL_SEND_EMAIL"].destructive {
		t.Fatal("destructive marks are wrong")
	}

	send := bySlug["PD_GMAIL_SEND_EMAIL"]
	if send.appProp != "gmail" {
		t.Fatalf("app prop = %q", send.appProp)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	if err := json.Unmarshal(send.schema, &schema); err != nil {
		t.Fatal(err)
	}
	wantTypes := map[string]string{"to": "array", "subject": "string", "maxResults": "integer", "html": "boolean", "headers": "object", "label": "string"}
	if len(schema.Properties) != len(wantTypes) {
		t.Fatalf("properties = %v", schema.Properties)
	}
	for name, kind := range wantTypes {
		if schema.Properties[name]["type"] != kind {
			t.Fatalf("%s type = %v, want %s", name, schema.Properties[name]["type"], kind)
		}
	}
	if items, _ := schema.Properties["to"]["items"].(map[string]any); items["type"] != "string" {
		t.Fatalf("to items = %v", schema.Properties["to"])
	}
	if schema.Properties["to"]["description"] != "Recipients" || schema.Properties["subject"]["description"] != "Subject" {
		t.Fatalf("descriptions = %v", schema.Properties)
	}
	if fmt.Sprint(schema.Required) != "[to subject]" {
		t.Fatalf("required = %v", schema.Required)
	}

	// The listing is cached.
	if _, err := h.service.pipedreamActionsFor(context.Background(), "gmail"); err != nil || fake.listCalls != 1 {
		t.Fatalf("listed %d times, err %v", fake.listCalls, err)
	}
}

func TestSearchToolsListsAPipedreamConnectionsActionsUnderTheOffer(t *testing.T) {
	t.Parallel()
	h, _ := newPipedreamHarness(t)
	views, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "gmail", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 3 || views[0].Toolkit != "gmail" || !strings.HasPrefix(views[0].Tool, "PD_GMAIL_") {
		t.Fatalf("views = %+v", views)
	}
	views, err = h.service.SearchTools(context.Background(), memberToken, "org_1", "gmail", "find", 0)
	if err != nil || len(views) != 1 || views[0].Tool != "PD_GMAIL_FIND_EMAIL" || !views[0].ReadOnly {
		t.Fatalf("search = %+v, %v", views, err)
	}
	if h.composio.searches != 0 {
		t.Fatal("Composio was asked for a Pipedream app's tools")
	}

	offer := []string{"github"}
	h.service.policy = &fakePolicySource{settings: policy.Settings{Offer: &offer}}
	if _, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "gmail", "", 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("an app the organization does not offer = %v, want forbidden", err)
	}
	if _, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: "PD_GMAIL_FIND_EMAIL"}); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("running an app the organization does not offer = %v, want forbidden", err)
	}
}

func TestRunToolRunsAPipedreamReadAsTheMembersAccount(t *testing.T) {
	t.Parallel()
	h, fake := newPipedreamHarness(t)
	run, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
		Tool: "PD_GMAIL_FIND_EMAIL", Arguments: json.RawMessage(`{"q":"from:jane"}`), OnlyReads: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionSucceeded || string(run.Data) != `{"ret":{"id":"m_1"},"exports":{"$summary":"sent"}}` {
		t.Fatalf("run = %+v data %s", run, run.Data)
	}
	request := fake.runs[0]
	account, _ := request.ConfiguredProps["gmail"].(map[string]any)
	if request.ComponentKey != "gmail-find-email" || request.ExternalUserID != "oryxa.org_1.user-1" || account["authProvisionId"] != "apn_1" {
		t.Fatalf("request = %+v", request)
	}
	if string(request.ConfiguredProps["q"].(json.RawMessage)) != `"from:jane"` {
		t.Fatalf("q = %v", request.ConfiguredProps["q"])
	}

	// An agent may not name the account, nor a prop the action lacks.
	for _, arguments := range []string{`{"gmail":{"authProvisionId":"apn_other"}}`, `{"nope":1}`} {
		if _, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
			Tool: "PD_GMAIL_FIND_EMAIL", Arguments: json.RawMessage(arguments),
		}); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("arguments %s = %v, want invalid", arguments, err)
		}
	}
	// A write needs the member's confirmation and an idempotency key.
	if _, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{Tool: "PD_GMAIL_SEND_EMAIL", OnlyReads: true}); !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("a write to a reads-only caller = %v", err)
	}
	if _, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{Tool: "PD_GMAIL_SEND_EMAIL"}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("a write without a key = %v", err)
	}
}

func TestRunToolRefusesAPipedreamSlugTheMemberHasNotConnected(t *testing.T) {
	t.Parallel()
	h, fake := newPipedreamHarness(t)
	fake.actions["slack"] = []PipedreamComponent{{Key: "slack-send-message", ReadOnlyHint: hint(true)}}
	for _, input := range []struct {
		token string
		tool  string
	}{
		{otherToken.Authorization, "PD_GMAIL_FIND_EMAIL"},    // another member's app
		{memberToken.Authorization, "PD_SLACK_SEND_MESSAGE"}, // an app nobody connected
		{memberToken.Authorization, "PD_GMAIL_DELETE_EMAIL"}, // denied
		{memberToken.Authorization, "PD_GMAIL_NO_SUCH"},
	} {
		credentials := memberToken
		credentials.Authorization = input.token
		if _, err := h.service.RunTool(context.Background(), credentials, "org_1", RunToolInput{Tool: input.tool}); !errors.Is(err, domain.ErrNotFound) {
			t.Fatalf("%s as %s = %v, want not found", input.tool, input.token, err)
		}
	}
	if len(fake.runs) != 0 {
		t.Fatalf("ran %d actions", len(fake.runs))
	}
}

func TestRunToolReplaysAPipedreamWrite(t *testing.T) {
	t.Parallel()
	h, fake := newPipedreamHarness(t)
	input := RunToolInput{
		Tool: "PD_GMAIL_SEND_EMAIL", Arguments: json.RawMessage(`{"to":["a@b.c"],"subject":"hi"}`), IdempotencyKey: "send-2026-10-13",
	}
	first, err := h.service.RunTool(context.Background(), agentToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.service.RunTool(context.Background(), agentToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Action.ID != first.Action.ID || string(again.Data) != string(first.Data) {
		t.Fatalf("replay = %+v", again)
	}
	if len(fake.runs) != 1 || len(h.store.published(appsv1.ActionCompletedDetailType)) != 1 {
		t.Fatalf("runs = %d", len(fake.runs))
	}
}

func TestRunToolAttributesPipedreamFailures(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		err       error
		wantErr   error
		wantStore string
	}{
		"app error":         {&PipedreamActionError{Message: "Invalid To header", Origin: "upstream_api", UpstreamStatus: 400}, nil, "Invalid To header"},
		"app rate limiting": {&PipedreamActionError{Message: "slow down", Origin: "upstream_api", UpstreamStatus: 429}, domain.ErrRateLimited, "rate_limited"},
		"pipedream limit":   {fmt.Errorf("pipedream: %w", domain.ErrRateLimited), domain.ErrRateLimited, "rate_limited"},
		"pipedream down":    {errors.New("connection reset"), domain.ErrUnavailable, "unavailable"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h, fake := newPipedreamHarness(t)
			fake.run = func(PipedreamRunRequest) (PipedreamRunResult, error) { return PipedreamRunResult{}, test.err }
			run, err := h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
				Tool: "PD_GMAIL_SEND_EMAIL", IdempotencyKey: "send-2026-10-13",
			})
			if test.wantErr == nil && err != nil || test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("err = %v, want %v", err, test.wantErr)
			}
			if run.Action.Status != domain.ActionFailed || run.Action.Error != test.wantStore {
				t.Fatalf("action = %+v", run.Action)
			}
			failed := h.store.published(appsv1.ActionFailedDetailType)
			if len(failed) != 1 || strings.Contains(string(failed[0].Data), "Invalid To header") {
				t.Fatalf("failed events = %v", failed)
			}
		})
	}
}
