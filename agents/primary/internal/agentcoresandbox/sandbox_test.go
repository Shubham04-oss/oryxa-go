package agentcoresandbox

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"
)

type invocation struct {
	identifier string
	sessionID  string
	request    Request
}

type fakeRuntime struct {
	mu          sync.Mutex
	starts      []string
	invocations []invocation
	stops       []string
	results     []types.CodeInterpreterResult
	startErr    error
	invokeErr   error
	stopErr     error
}

func (f *fakeRuntime) start(_ context.Context, identifier, name string, _ int32) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.startErr != nil {
		return "", f.startErr
	}
	f.starts = append(f.starts, identifier+":"+name)
	return "sandbox-" + string(rune('0'+len(f.starts))), nil
}

func (f *fakeRuntime) invoke(_ context.Context, identifier, sessionID string, request Request) ([]types.CodeInterpreterResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.invocations = append(f.invocations, invocation{identifier: identifier, sessionID: sessionID, request: request})
	return f.results, f.invokeErr
}

func (f *fakeRuntime) stop(_ context.Context, _ string, sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.stops = append(f.stops, sessionID)
	return f.stopErr
}

func TestManagerReusesOneSandboxPerConversation(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	api := &fakeRuntime{results: []types.CodeInterpreterResult{{
		Content: []types.ContentBlock{{Type: types.ContentBlockTypeText, Text: aws.String("4")}},
		StructuredContent: &types.ToolResultStructuredContent{
			Stdout:        aws.String("4\n"),
			ExitCode:      aws.Int32(0),
			ExecutionTime: aws.Float64(12),
		},
	}}}
	manager, err := newManager(api, Options{CodeInterpreterIdentifier: "ci-test", SessionTimeout: time.Hour, now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	first, err := manager.Invoke(t.Context(), "krude\x00member\x00conversation", Request{Action: ExecuteCode, Language: "python", Code: "print(2 + 2)"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := manager.Invoke(t.Context(), "krude\x00member\x00conversation", Request{Action: ListFiles})
	if err != nil {
		t.Fatal(err)
	}

	if len(api.starts) != 1 || len(api.invocations) != 2 {
		t.Fatalf("starts = %v, invocations = %+v", api.starts, api.invocations)
	}
	if api.invocations[0].sessionID != api.invocations[1].sessionID {
		t.Fatalf("one conversation used two sandboxes: %+v", api.invocations)
	}
	if !first.SessionRecreated || second.SessionRecreated {
		t.Fatalf("session recreation flags = %v, %v", first.SessionRecreated, second.SessionRecreated)
	}
	if first.Stdout != "4\n" || len(first.Content) != 1 || first.Content[0].Text != "4" || first.ExitCode == nil || *first.ExitCode != 0 {
		t.Fatalf("result = %+v", first)
	}
}

func TestManagerIsolatesConversationsAndRecreatesBeforeExpiry(t *testing.T) {
	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	api := &fakeRuntime{}
	manager, err := newManager(api, Options{SessionTimeout: time.Minute, now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Action: ExecuteCommand, Command: "pwd"}
	if _, err := manager.Invoke(t.Context(), "one", request); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Invoke(t.Context(), "two", request); err != nil {
		t.Fatal(err)
	}
	now = now.Add(55 * time.Second)
	if _, err := manager.Invoke(t.Context(), "one", request); err != nil {
		t.Fatal(err)
	}

	if len(api.starts) != 3 {
		t.Fatalf("starts = %v", api.starts)
	}
	if api.invocations[0].sessionID == api.invocations[1].sessionID || api.invocations[0].sessionID == api.invocations[2].sessionID {
		t.Fatalf("sessions were not isolated/recreated: %+v", api.invocations)
	}
}

func TestManagerResetAndCloseStopSessions(t *testing.T) {
	api := &fakeRuntime{}
	manager, err := newManager(api, Options{})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Action: ExecuteCommand, Command: "pwd"}
	if _, err := manager.Invoke(t.Context(), "one", request); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Invoke(t.Context(), "two", request); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reset(t.Context(), "one"); err != nil {
		t.Fatal(err)
	}
	if err := manager.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.stops, []string{"sandbox-1", "sandbox-2"}) {
		t.Fatalf("stops = %v", api.stops)
	}
	if _, err := manager.Invoke(t.Context(), "three", request); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Invoke() after Close error = %v", err)
	}
}

func TestManagerSurfacesLifecycleErrors(t *testing.T) {
	api := &fakeRuntime{startErr: errors.New("no credentials")}
	manager, err := newManager(api, Options{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Invoke(t.Context(), "one", Request{Action: ExecuteCommand, Command: "pwd"})
	if err == nil || !strings.Contains(err.Error(), "start AgentCore sandbox: no credentials") {
		t.Fatalf("Invoke() error = %v", err)
	}
}

func TestRequestValidationAndArgumentMapping(t *testing.T) {
	tests := []struct {
		name    string
		request Request
		wantErr bool
	}{
		{name: "python", request: Request{Action: ExecuteCode, Language: "python", Code: "print(1)"}},
		{name: "bad language", request: Request{Action: ExecuteCode, Language: "ruby", Code: "puts 1"}, wantErr: true},
		{name: "code too large", request: Request{Action: ExecuteCode, Language: "python", Code: strings.Repeat("x", maxCodeInputBytes+1)}, wantErr: true},
		{name: "empty command", request: Request{Action: ExecuteCommand}, wantErr: true},
		{name: "list working directory", request: Request{Action: ListFiles}},
		{name: "null directory", request: Request{Action: ListFiles, DirectoryPath: "tmp\x00bad"}, wantErr: true},
		{name: "empty paths", request: Request{Action: ReadFiles}, wantErr: true},
		{name: "blank path", request: Request{Action: RemoveFiles, Paths: []string{" "}}, wantErr: true},
		{name: "write", request: Request{Action: WriteFiles, Files: []File{{Path: "notes.txt", Text: "hello"}}}},
		{name: "unknown", request: Request{Action: "launchMissiles"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.request.validate()
			if (err != nil) != test.wantErr {
				t.Fatalf("validate() error = %v", err)
			}
		})
	}

	write := Request{Action: WriteFiles, Files: []File{{Path: "notes.txt", Text: "hello"}}}.arguments()
	if len(write.Content) != 1 || aws.ToString(write.Content[0].Path) != "notes.txt" || aws.ToString(write.Content[0].Text) != "hello" {
		t.Fatalf("write arguments = %+v", write)
	}
	code := Request{Action: ExecuteCode, Language: "typescript", Code: "console.log(1)", ClearContext: true}.arguments()
	if code.Language != types.ProgrammingLanguageTypescript || !aws.ToBool(code.ClearContext) {
		t.Fatalf("code arguments = %+v", code)
	}
}

func TestAggregateBoundsModelOutput(t *testing.T) {
	result := aggregate([]types.CodeInterpreterResult{{
		Content:           []types.ContentBlock{{Type: types.ContentBlockTypeText, Text: aws.String(strings.Repeat("x", maxModelOutputBytes+10))}},
		StructuredContent: &types.ToolResultStructuredContent{Stdout: aws.String("not included")},
	}}, maxModelOutputBytes)
	if !result.OutputTruncated || len(result.Content) != 1 || len(result.Content[0].Text) != maxModelOutputBytes || result.Stdout != "" {
		t.Fatalf("bounded result = text:%d stdout:%q truncated:%v", len(result.Content[0].Text), result.Stdout, result.OutputTruncated)
	}
}

func TestAggregateCarriesAFileUpToTheArtifactBudget(t *testing.T) {
	deck := make([]byte, 3*1024*1024) // a deck with images, far past the model's budget
	result := aggregate([]types.CodeInterpreterResult{{
		Content: []types.ContentBlock{{Type: types.ContentBlockTypeEmbeddedResource, Resource: &types.ResourceContent{Blob: deck}}},
	}}, maxArtifactBytes)
	if result.OutputTruncated || len(result.Content) != 1 || base64.StdEncoding.DecodedLen(len(result.Content[0].ResourceDataBase64)) < len(deck) {
		t.Fatalf("a 3 MiB file was cut: truncated=%v", result.OutputTruncated)
	}
}

func TestNewManagerValidatesOptions(t *testing.T) {
	if _, err := newManager(nil, Options{}); err == nil {
		t.Fatal("newManager(nil) error = nil")
	}
	for _, timeout := range []time.Duration{time.Second, 8*time.Hour + time.Second, time.Minute + time.Millisecond} {
		if _, err := newManager(&fakeRuntime{}, Options{SessionTimeout: timeout}); err == nil {
			t.Fatalf("newManager() accepted timeout %s", timeout)
		}
	}
}
