package application

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// readTool and writeTool are the two tools every run test needs: one Composio
// marks read-only, one it does not.
func (h harness) readTool() composio.Tool {
	tool := composio.Tool{
		Slug: "GITHUB_LIST_ISSUES", Name: "List issues", Toolkit: "github", Version: "20260901",
		Tags: []string{composio.HintReadOnly, "important"}, InputParameters: json.RawMessage(`{"type":"object"}`),
	}
	h.composio.tools[tool.Slug] = tool
	return tool
}

func (h harness) writeTool() composio.Tool {
	tool := composio.Tool{
		Slug: "GITHUB_CREATE_AN_ISSUE", Name: "Create an issue", Toolkit: "github", Version: "20260901",
		Tags: []string{composio.HintDestructive},
	}
	h.composio.tools[tool.Slug] = tool
	return tool
}

func TestSearchToolsAnswersFeaturedToolsWithoutAQuery(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	read := h.readTool()
	h.writeTool()

	views, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].Tool != read.Slug {
		t.Fatalf("views = %+v", views)
	}
	if !views[0].ReadOnly || views[0].Destructive {
		t.Fatalf("marks = %+v", views[0])
	}

	// A search looks through everything the app has, and says which tools
	// change things.
	views, err = h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "create", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 || views[0].ReadOnly || !views[0].Destructive {
		t.Fatalf("views = %+v", views)
	}
}

func TestSearchToolsListsWhatAnAppHasWhenItFeaturesNothing(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.writeTool() // not featured

	views, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "github", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(views) != 1 {
		t.Fatalf("views = %+v", views)
	}
}

func TestSearchToolsChecksTheCaller(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	if _, err := h.service.SearchTools(context.Background(), memberToken, "org_2", "github", "", 0); !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("another organization = %v, want forbidden", err)
	}
	if _, err := h.service.SearchTools(context.Background(), memberToken, "org_1", "NOT A SLUG", "", 0); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("bad toolkit = %v, want invalid", err)
	}
}

func TestRunToolRunsAReadAsTheCallersConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.readTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, Arguments: json.RawMessage(`{"repo":"oryxa"}`), OnlyReads: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionSucceeded || string(run.Data) != `{"ok":true}` {
		t.Fatalf("run = %+v", run)
	}
	if run.Action.LogID != "log_1" || !run.Action.ReadOnly {
		t.Fatalf("action = %+v", run.Action)
	}
	if len(h.composio.executed) != 1 {
		t.Fatalf("executed = %d", len(h.composio.executed))
	}
	request := h.composio.executed[0]
	if request.ConnectedAccountID != "ca_1" || request.UserID != "oryxa.org_1.user-1" {
		t.Fatalf("request ran as the wrong account: %+v", request)
	}
	if request.Version != tool.Version || string(request.Arguments) != `{"repo":"oryxa"}` {
		t.Fatalf("request = %+v", request)
	}
	// A read keeps no answer and announces nothing.
	if run.Action.Result != nil {
		t.Fatalf("a read kept its answer: %s", run.Action.Result)
	}
	if len(h.store.events) != 0 {
		t.Fatalf("a read published %d events", len(h.store.events))
	}
}

func TestRunToolRefusesAWriteToACallerThatOnlyReads(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, IdempotencyKey: "key-12345678", OnlyReads: true,
	})
	if !errors.Is(err, domain.ErrNotReadOnly) {
		t.Fatalf("error = %v, want not read-only", err)
	}
	if len(h.composio.executed) != 0 {
		t.Fatal("the tool ran anyway")
	}
}

func TestRunToolNeedsAnIdempotencyKeyForAWrite(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")

	for name, key := range map[string]string{"none": "", "too short": "short"} {
		t.Run(name, func(t *testing.T) {
			_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: tool.Slug, IdempotencyKey: key})
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("error = %v, want invalid", err)
			}
		})
	}
	if len(h.composio.executed) != 0 {
		t.Fatal("the tool ran anyway")
	}
}

func TestRunToolAnnouncesAWriteAndCanReplayIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	input := RunToolInput{Tool: tool.Slug, Arguments: json.RawMessage(`{"title":"bug"}`), IdempotencyKey: "issue-2026-09-17"}

	run, err := h.service.RunTool(context.Background(), agentToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionSucceeded || run.Replayed {
		t.Fatalf("run = %+v", run)
	}
	events := h.store.published(appsv1.ActionCompletedDetailType)
	if len(events) != 1 {
		t.Fatalf("completed events = %d", len(events))
	}
	if !strings.Contains(string(events[0].Data), `"agent":true`) ||
		!strings.Contains(string(events[0].Data), `"tool":"`+tool.Slug+`"`) {
		t.Fatalf("event data = %s", events[0].Data)
	}

	// Asked again under the same key, it answers the first run rather than
	// creating a second issue.
	replayed, err := h.service.RunTool(context.Background(), agentToken, "org_1", input)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Action.ID != run.Action.ID || string(replayed.Data) != `{"ok":true}` {
		t.Fatalf("replay = %+v", replayed)
	}
	if len(h.composio.executed) != 1 {
		t.Fatalf("the tool ran %d times", len(h.composio.executed))
	}
	if len(h.store.published(appsv1.ActionCompletedDetailType)) != 1 {
		t.Fatal("the replay published a second event")
	}

	// The same key for another tool is a mistake, not a replay.
	h.readTool()
	_, err = h.service.RunTool(context.Background(), agentToken, "org_1", RunToolInput{
		Tool: "GITHUB_LIST_ISSUES", IdempotencyKey: input.IdempotencyKey,
	})
	if err != nil {
		// A read needs no key, so the key is ignored rather than refused.
		t.Fatalf("a read with a used key = %v", err)
	}
}

func TestRunToolWillNotRunAgainWhileARunIsInFlight(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	// An action left running, as a process that died mid-run leaves it.
	h.store.actions["apa_stuck"] = domain.Action{
		ID: "apa_stuck", OrganizationID: "org_1", MemberID: "user-1", ConnectionID: "apc_1",
		Toolkit: "github", Tool: tool.Slug, IdempotencyKey: "issue-2026-09-17", Status: domain.ActionRunning,
	}

	_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, IdempotencyKey: "issue-2026-09-17",
	})
	if !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("error = %v, want conflict", err)
	}
	if len(h.composio.executed) != 0 {
		t.Fatal("the tool ran while an earlier run was in flight")
	}
}

func TestRunToolReportsWhatTheAppRefused(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.result = composio.ExecuteResult{Successful: false, Error: "Validation failed: title is required", LogID: "log_2"}

	// A tool that ran and failed at the app is an answer, not an error: the
	// caller needs the app's reason to try again.
	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, IdempotencyKey: "issue-2026-09-17",
	})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.Status != domain.ActionFailed || run.Action.Error != "Validation failed: title is required" {
		t.Fatalf("action = %+v", run.Action)
	}
	events := h.store.published(appsv1.ActionFailedDetailType)
	if len(events) != 1 {
		t.Fatalf("failed events = %d", len(events))
	}
	// The app's own words stay out of the event, which travels further.
	if !strings.Contains(string(events[0].Data), `"error":"app_error"`) {
		t.Fatalf("event data = %s", events[0].Data)
	}
}

func TestRunToolRecordsARunComposioCouldNotMake(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.executeErr = composio.ErrRateLimited

	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, IdempotencyKey: "issue-2026-09-17",
	})
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("error = %v, want rate limited", err)
	}
	if run.Action.Status != domain.ActionFailed || run.Action.Error != "rate_limited" {
		t.Fatalf("action = %+v", run.Action)
	}
	events := h.store.published(appsv1.ActionFailedDetailType)
	if len(events) != 1 || !strings.Contains(string(events[0].Data), `"error":"rate_limited"`) {
		t.Fatalf("failed events = %+v", events)
	}
}

func TestRunToolPicksTheCallersOwnConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.readTool()
	mine := h.activeConnection("apc_1", "user-1", "github", "ca_1")
	theirs := h.activeConnection("apc_2", "user-2", "github", "ca_2")

	// Another member's connection to the same app is not a candidate.
	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: tool.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if run.Action.ConnectionID != mine.ID {
		t.Fatalf("ran with %q", run.Action.ConnectionID)
	}
	// Nor can it be named outright, even by an org admin.
	_, err = h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{Tool: tool.Slug, ConnectionID: theirs.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("another member's connection = %v, want not found", err)
	}
	_, err = h.service.RunTool(context.Background(), adminToken, "org_1", RunToolInput{Tool: tool.Slug, ConnectionID: theirs.ID})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an admin acting as a member = %v, want not found", err)
	}
}

func TestMemberAndOwnedAgentsShareTheSameConnection(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.readTool()
	connection := h.activeConnection("apc_gmail", "user-1", "github", "ca_shared")

	for _, credentials := range []authclient.Credentials{memberToken, agentToken, specialistToken} {
		run, err := h.service.RunTool(context.Background(), credentials, "org_1", RunToolInput{Tool: tool.Slug})
		if err != nil {
			t.Fatal(err)
		}
		if run.Action.ConnectionID != connection.ID {
			t.Fatalf("caller %q used connection %q, want shared %q", credentials.Authorization, run.Action.ConnectionID, connection.ID)
		}
	}
	if len(h.composio.executed) != 3 {
		t.Fatalf("executions=%d", len(h.composio.executed))
	}
	for _, request := range h.composio.executed {
		if request.ConnectedAccountID != "ca_shared" || request.UserID != "oryxa.org_1.user-1" {
			t.Fatalf("provider request=%+v", request)
		}
	}
}

// TestAToolRunNamesTheAgentThatRanIt: a specialist's run is recorded, and
// announced, as that specialist's, not merely as "an agent".
func TestAToolRunNamesTheAgentThatRanIt(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.readTool()
	h.activeConnection("apc_gmail", "user-1", "github", "ca_shared")

	run, err := h.service.RunTool(context.Background(), specialistToken, "org_1", RunToolInput{Tool: tool.Slug})
	if err != nil {
		t.Fatal(err)
	}
	if !run.Action.Agent || run.Action.AgentID != "agt_writer" || run.Action.AgentRole != "specialist" {
		t.Fatalf("the action is %+v, want it to name agt_writer, a specialist", run.Action)
	}
	if recorded := h.store.actions[run.Action.ID]; recorded.AgentID != "agt_writer" || recorded.AgentRole != "specialist" {
		t.Fatalf("the stored action is %+v", recorded)
	}
}

func TestRunToolSaysWhenTheAppIsNotUsable(t *testing.T) {
	t.Parallel()
	for name, testCase := range map[string]struct {
		prepare func(harness) string
		want    error
	}{
		"nothing connected": {
			prepare: func(harness) string { return "" },
			want:    domain.ErrNotConnected,
		},
		"the connection expired": {
			prepare: func(h harness) string {
				connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
				connection.Status = domain.ConnectionExpired
				h.store.connections[connection.ID] = connection
				return ""
			},
			want: domain.ErrNeedsReconnect,
		},
		"the named connection expired": {
			prepare: func(h harness) string {
				connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
				connection.Status = domain.ConnectionExpired
				h.store.connections[connection.ID] = connection
				return connection.ID
			},
			want: domain.ErrNeedsReconnect,
		},
		"the connection was removed": {
			prepare: func(h harness) string {
				connection := h.activeConnection("apc_1", "user-1", "github", "ca_1")
				connection.Status = domain.ConnectionRemoved
				h.store.connections[connection.ID] = connection
				return connection.ID
			},
			want: domain.ErrNotConnected,
		},
		"two connections could be meant": {
			prepare: func(h harness) string {
				h.activeConnection("apc_1", "user-1", "github", "ca_1")
				h.activeConnection("apc_2", "user-1", "github", "ca_2")
				return ""
			},
			want: domain.ErrChooseConnection,
		},
		"the named connection is to another app": {
			prepare: func(h harness) string {
				return h.activeConnection("apc_1", "user-1", "slack", "ca_1").ID
			},
			want: domain.ErrInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t)
			tool := h.readTool()
			connectionID := testCase.prepare(h)

			_, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
				Tool: tool.Slug, ConnectionID: connectionID,
			})
			if !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
			if len(h.composio.executed) != 0 {
				t.Fatal("the tool ran anyway")
			}
		})
	}
}

func TestRunToolRefusesAToolItCannotTrust(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	h.composio.tools["GITHUB_OLD_TOOL"] = composio.Tool{Slug: "GITHUB_OLD_TOOL", Toolkit: "github", Deprecated: true}

	for name, testCase := range map[string]struct {
		input RunToolInput
		want  error
	}{
		"a tool that does not exist": {input: RunToolInput{Tool: "GITHUB_NO_SUCH_TOOL"}, want: domain.ErrNotFound},
		"a deprecated tool":          {input: RunToolInput{Tool: "GITHUB_OLD_TOOL"}, want: domain.ErrNotFound},
		"a slug that is not one":     {input: RunToolInput{Tool: "not a tool"}, want: domain.ErrInvalid},
		"arguments that are not an object": {
			input: RunToolInput{Tool: "GITHUB_LIST_ISSUES", Arguments: json.RawMessage(`[1,2]`)},
			want:  domain.ErrInvalid,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.service.RunTool(context.Background(), memberToken, "org_1", testCase.input); !errors.Is(err, testCase.want) {
				t.Fatalf("error = %v, want %v", err, testCase.want)
			}
		})
	}
}

func TestRunToolKeepsNoAnswerLargerThanTheLimit(t *testing.T) {
	t.Parallel()
	h := newHarness(t)
	tool := h.writeTool()
	h.activeConnection("apc_1", "user-1", "github", "ca_1")
	big := json.RawMessage(`{"data":"` + strings.Repeat("x", domain.MaxResultBytes) + `"}`)
	h.composio.result = composio.ExecuteResult{Successful: true, Data: big}

	run, err := h.service.RunTool(context.Background(), memberToken, "org_1", RunToolInput{
		Tool: tool.Slug, IdempotencyKey: "issue-2026-09-17",
	})
	if err != nil {
		t.Fatal(err)
	}
	// The caller is answered in full; only the replay copy is dropped.
	if len(run.Data) != len(big) {
		t.Fatalf("data = %d bytes", len(run.Data))
	}
	if run.Action.Result != nil {
		t.Fatalf("kept %d bytes for a replay", len(run.Action.Result))
	}
}
