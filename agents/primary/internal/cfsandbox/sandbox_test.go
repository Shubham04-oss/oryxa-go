package cfsandbox

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcoresandbox"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// ---- a fake edge ----

type issuer struct{ claims []edgev1.TokenClaims }

func (i *issuer) EdgeToken(_ context.Context, claims edgev1.TokenClaims) (string, error) {
	i.claims = append(i.claims, claims)
	return "token", nil
}

type seenRequest struct {
	Method, Path, Query string
	Body                map[string]any
}

func fakeEdge(t *testing.T, answer func(seenRequest) (int, any)) (*Executor, *issuer, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := seenRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery}
		_ = json.NewDecoder(r.Body).Decode(&request.Body)
		seen = append(seen, request)
		status, body := answer(request)
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(server.Close)
	tokens := &issuer{}
	client, err := edgeclient.New(edgeclient.Config{BaseURL: server.URL, Tokens: tokens, Attempts: 1})
	if err != nil {
		t.Fatal(err)
	}
	executor, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	return executor, tokens, &seen
}

var target = edgeclient.Target{Org: "org_1", Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "sess_1"}, Member: "usr_1", Agent: "agt_1", Run: "inv_1"}

func TestExecuteCodeCallsTheRoomSandboxAndKeepsAgentCoresShape(t *testing.T) {
	executor, tokens, seen := fakeEdge(t, func(seenRequest) (int, any) {
		return 200, edgev1.ExecResult{
			Stdout: "2\n", ExitCode: 0, DurationMs: 412, SessionRecreated: true,
			Results: []edgev1.ExecOutput{{Type: "image", MIMEType: "image/png", Data: "iVBORw==", Name: "chart.png"}},
		}
	})
	result, err := executor.Invoke(context.Background(), target, Request{Action: ExecuteCode, Language: "python", Code: "print(2)"})
	if err != nil {
		t.Fatal(err)
	}
	request := (*seen)[0]
	if request.Method != "POST" || request.Path != "/v1/rooms/thread/sess_1/sandbox/shared/exec/code" {
		t.Errorf("request = %s %s", request.Method, request.Path)
	}
	if request.Body["language"] != "python" || request.Body["code"] != "print(2)" || request.Body["timeoutMs"] != float64(120000) {
		t.Errorf("body = %v", request.Body)
	}
	claims := tokens.claims[0]
	if claims.Org != "org_1" || claims.Member != "usr_1" || claims.Run != "inv_1" || !claims.HasScope(edgev1.ScopeSandboxExec) || !claims.HasScope(edgev1.ScopeSandboxFiles) {
		t.Errorf("claims = %+v", claims)
	}

	encoded, _ := json.Marshal(result)
	want := `{"is_error":false,"content":[{"type":"text","text":"2\n"},{"type":"image","data_base64":"iVBORw==","name":"chart.png","mime_type":"image/png"}],"stdout":"2\n","exit_code":0,"execution_time_ms":412,"session_recreated":true}`
	if string(encoded) != want {
		t.Errorf("result\n got %s\nwant %s", encoded, want)
	}
}

func TestAFailedOrTimedOutRunIsAnError(t *testing.T) {
	executor, _, _ := fakeEdge(t, func(request seenRequest) (int, any) {
		if strings.Contains(request.Body["command"].(string), "sleep") {
			return 200, edgev1.ExecResult{ExitCode: -1, TimedOut: true, Stdout: "partial", DurationMs: 120000}
		}
		return 200, edgev1.ExecResult{ExitCode: 2, Stderr: "no such file"}
	})
	failed, err := executor.Invoke(context.Background(), target, Request{Action: ExecuteCommand, Command: "cat nope"})
	if err != nil || !failed.IsError || *failed.ExitCode != 2 {
		t.Fatalf("failed = %+v, %v", failed, err)
	}
	timedOut, err := executor.Invoke(context.Background(), target, Request{Action: ExecuteCommand, Command: "sleep 999"})
	if err != nil || !timedOut.IsError || timedOut.TaskStatus != "TIMED_OUT" || !strings.Contains(timedOut.Stderr, "limit") {
		t.Fatalf("timed out = %+v, %v", timedOut, err)
	}
}

func TestFilesMapToAgentCoresBlocks(t *testing.T) {
	size := int64(9)
	executor, _, seen := fakeEdge(t, func(request seenRequest) (int, any) {
		switch request.Path {
		case "/v1/rooms/thread/sess_1/sandbox/shared/files":
			return 200, edgev1.ListFilesResponse{Path: "/workspace", Entries: []edgev1.FileEntry{{Name: "main.py", Path: "/workspace/main.py", Type: "file", Size: &size}}}
		case "/v1/rooms/thread/sess_1/sandbox/shared/files/read":
			return 200, edgev1.ReadFilesResponse{Files: []edgev1.FileContent{
				{Path: "/workspace/main.py", Encoding: "utf8", Content: "print(1)\n", MIMEType: "text/x-python", Size: 9},
				{Path: "/workspace/logo.png", Encoding: "base64", Content: "iVBORw==", MIMEType: "image/png", Size: 4},
				{Path: "/workspace/nope", Error: "ENOENT: no such file"},
			}}
		case "/v1/rooms/thread/sess_1/sandbox/shared/files/write":
			return 200, edgev1.WriteFilesResponse{Written: []edgev1.WrittenFile{{Path: "/workspace/a.txt", Size: 1}}}
		}
		return 404, edgev1.ErrorBody{Error: edgev1.ErrorDetail{Code: edgev1.CodeNotFound, Message: "no"}}
	})
	listed, err := executor.Invoke(context.Background(), target, Request{Action: ListFiles, DirectoryPath: "/workspace"})
	if err != nil || listed.Content[0].Type != "resource_link" || listed.Content[0].URI != "file:///workspace/main.py" || *listed.Content[0].Size != 9 {
		t.Fatalf("listed = %+v, %v", listed, err)
	}
	if (*seen)[0].Method != "GET" || (*seen)[0].Query != "path=%2Fworkspace" {
		t.Errorf("list request = %+v", (*seen)[0])
	}
	read, err := executor.Invoke(context.Background(), target, Request{Action: ReadFiles, Paths: []string{"main.py", "logo.png", "nope"}})
	if err != nil {
		t.Fatal(err)
	}
	if read.Content[0].ResourceText != "print(1)\n" || read.Content[1].ResourceDataBase64 != "iVBORw==" || !read.IsError || !strings.Contains(read.Content[2].Text, "ENOENT") {
		t.Errorf("read = %+v", read)
	}
	written, err := executor.Invoke(context.Background(), target, Request{Action: WriteFiles, Files: []File{{Path: "a.txt", Text: "a"}}})
	if err != nil || !strings.Contains(written.Content[0].Text, "/workspace/a.txt") {
		t.Errorf("written = %+v, %v", written, err)
	}
}

func TestEdgeRefusalsReachTheModelInPlainWords(t *testing.T) {
	executor, _, _ := fakeEdge(t, func(seenRequest) (int, any) {
		return 429, edgev1.ErrorBody{Error: edgev1.ErrorDetail{Code: edgev1.CodeLimit, Message: "this room already runs 4 sandboxes"}}
	})
	_, err := executor.Invoke(context.Background(), target, Request{Action: ExecuteCommand, Command: "true"})
	if err == nil || !strings.Contains(err.Error(), "as many sandboxes as it may") {
		t.Fatalf("err = %v", err)
	}
}

func TestInvalidRequestsNeverReachTheEdge(t *testing.T) {
	executor, _, seen := fakeEdge(t, func(seenRequest) (int, any) { return 200, map[string]any{} })
	for name, request := range map[string]Request{
		"no code":        {Action: ExecuteCode, Language: "python"},
		"bad language":   {Action: ExecuteCode, Language: "ruby", Code: "1"},
		"huge command":   {Action: ExecuteCommand, Command: strings.Repeat("x", maxCommandBytes+1)},
		"no paths":       {Action: ReadFiles},
		"null in path":   {Action: RemoveFiles, Paths: []string{"a\x00b"}},
		"too many files": {Action: WriteFiles, Files: make([]File, maxFilesPerCall+1)},
	} {
		if _, err := executor.Invoke(context.Background(), target, request); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("%d invalid requests reached the edge", len(*seen))
	}
}

// ---- the tools ----

type mapState map[string]any

func (state mapState) Get(key string) (any, error) {
	if value, ok := state[key]; ok {
		return value, nil
	}
	return nil, session.ErrStateKeyNotExist
}

func (state mapState) All() iter.Seq2[string, any] {
	return func(yield func(string, any) bool) {
		for key, value := range state {
			if !yield(key, value) {
				return
			}
		}
	}
}

type toolContext struct {
	agent.Context
	state     mapState
	artifacts agent.Artifacts
}

// The embedded agent.Context is nil; these keep the context.Context part
// working.
func (toolContext) Deadline() (time.Time, bool)            { return time.Time{}, false }
func (toolContext) Done() <-chan struct{}                  { return nil }
func (toolContext) Err() error                             { return nil }
func (toolContext) Value(any) any                          { return nil }
func (toolContext) AppName() string                        { return "krude" }
func (toolContext) UserID() string                         { return "usr_1" }
func (toolContext) SessionID() string                      { return "sess_1" }
func (toolContext) InvocationID() string                   { return "inv_1" }
func (toolContext) AgentName() string                      { return "krude" }
func (c toolContext) ReadonlyState() session.ReadonlyState { return c.state }
func (toolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil
}
func (c toolContext) Artifacts() agent.Artifacts { return c.artifacts }

type artifacts struct {
	name string
	part *genai.Part
}

func (a *artifacts) Save(_ context.Context, name string, part *genai.Part) (*artifact.SaveResponse, error) {
	a.name, a.part = name, part
	return &artifact.SaveResponse{Version: 3}, nil
}
func (*artifacts) List(context.Context) (*artifact.ListResponse, error) { return nil, nil }
func (*artifacts) Load(context.Context, string) (*artifact.LoadResponse, error) {
	return nil, nil
}
func (*artifacts) LoadVersion(context.Context, string, int) (*artifact.LoadResponse, error) {
	return nil, nil
}

type runnable interface {
	tool.Tool
	Run(agent.Context, any) (map[string]any, error)
}

type declared interface {
	Declaration() *genai.FunctionDeclaration
}

// The tools are agentcoresandbox's: names, order, confirmations and
// argument schemas, so switching backends changes nothing the model sees
// but the wording. (Deleted with agentcoresandbox at cutover, A11.)
func TestToolsMatchAgentCores(t *testing.T) {
	ours, err := NewToolset(&Executor{})
	if err != nil {
		t.Fatal(err)
	}
	theirs, err := agentcoresandbox.NewToolset(noopAgentCore{})
	if err != nil {
		t.Fatal(err)
	}
	ourTools, _ := ours.Tools(nil)
	theirTools, _ := theirs.Tools(nil)
	if len(ourTools) != len(theirTools) {
		t.Fatalf("%d tools, AgentCore has %d", len(ourTools), len(theirTools))
	}
	for index := range ourTools {
		our, their := ourTools[index].(declared).Declaration(), theirTools[index].(declared).Declaration()
		if our.Name != their.Name {
			t.Errorf("tool %d is %s, AgentCore's is %s", index, our.Name, their.Name)
		}
		if !reflect.DeepEqual(shape(t, our.ParametersJsonSchema), shape(t, their.ParametersJsonSchema)) || !reflect.DeepEqual(shape(t, our.Parameters), shape(t, their.Parameters)) {
			t.Errorf("%s: arguments differ from AgentCore's", our.Name)
		}
		if ourTools[index].IsLongRunning() != theirTools[index].IsLongRunning() {
			t.Errorf("%s: long-running differs", our.Name)
		}
		if strings.Contains(our.Description, "AgentCore") || strings.Contains(our.Description, "AWS") {
			t.Errorf("%s still names AgentCore", our.Name)
		}
	}
}

// shape is a schema without its descriptions: the argument names, types and
// requirements the model must satisfy, which must not change.
func shape(t *testing.T, schema any) any {
	t.Helper()
	encoded, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	var strip func(any) any
	strip = func(value any) any {
		switch typed := value.(type) {
		case map[string]any:
			delete(typed, "description")
			for key, child := range typed {
				typed[key] = strip(child)
			}
		case []any:
			for index, child := range typed {
				typed[index] = strip(child)
			}
		}
		return value
	}
	return strip(decoded)
}

type noopAgentCore struct{}

func (noopAgentCore) Invoke(context.Context, string, agentcoresandbox.Request) (agentcoresandbox.Result, error) {
	return agentcoresandbox.Result{}, nil
}
func (noopAgentCore) Reset(context.Context, string) error { return nil }

func tools(t *testing.T, executor *Executor) map[string]runnable {
	t.Helper()
	toolset, err := NewToolset(executor)
	if err != nil {
		t.Fatal(err)
	}
	list, _ := toolset.Tools(nil)
	byName := map[string]runnable{}
	for _, each := range list {
		byName[each.Name()] = each.(runnable)
	}
	return byName
}

func TestToolsActInTheConversationsRoomForItsMember(t *testing.T) {
	executor, tokens, seen := fakeEdge(t, func(seenRequest) (int, any) { return 200, edgev1.ExecResult{Stdout: "ok"} })
	ctx := toolContext{state: mapState{"organization_id": "org_9", "managed_agent_id": "agt_7"}}
	if _, err := tools(t, executor)["sandbox_execute_command"].Run(ctx, map[string]any{"command": "ls"}); err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Path != "/v1/rooms/thread/sess_1/sandbox/shared/exec/command" {
		t.Errorf("path = %s", (*seen)[0].Path)
	}
	claims := tokens.claims[0]
	if claims.Org != "org_9" || claims.Member != "usr_1" || claims.Agent != "agt_7" || claims.Run != "inv_1" {
		t.Errorf("claims = %+v", claims)
	}
}

func TestToolsRefuseAConversationWithNoOrganization(t *testing.T) {
	executor, _, seen := fakeEdge(t, func(seenRequest) (int, any) { return 200, edgev1.ExecResult{} })
	_, err := tools(t, executor)["sandbox_execute_command"].Run(toolContext{state: mapState{}}, map[string]any{"command": "ls"})
	if err == nil || len(*seen) != 0 {
		t.Fatalf("err %v, %d calls", err, len(*seen))
	}
}

func TestSaveArtifactMovesTheFileWithoutTheModel(t *testing.T) {
	executor, _, _ := fakeEdge(t, func(seenRequest) (int, any) {
		return 200, edgev1.ReadFilesResponse{Files: []edgev1.FileContent{{Path: "/workspace/chart.png", Encoding: "base64", Content: base64.StdEncoding.EncodeToString([]byte{1, 2, 3}), MIMEType: "image/png", Size: 3}}}
	})
	saved := &artifacts{}
	ctx := toolContext{state: mapState{"organization_id": "org_1"}, artifacts: saved}
	result, err := tools(t, executor)["sandbox_save_artifact"].Run(ctx, map[string]any{"path": "/workspace/chart.png"})
	if err != nil {
		t.Fatal(err)
	}
	if saved.name != "chart.png" || saved.part.InlineData.MIMEType != "image/png" || !reflect.DeepEqual(saved.part.InlineData.Data, []byte{1, 2, 3}) {
		t.Errorf("saved %s %+v", saved.name, saved.part.InlineData)
	}
	if result["artifact_version"] != int64(3) && result["artifact_version"] != float64(3) {
		t.Errorf("result = %v", result)
	}
}
