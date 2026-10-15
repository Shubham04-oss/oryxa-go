package agentcoresandbox

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

type fakeExecutor struct {
	key       string
	request   Request
	resetKey  string
	result    Result
	resultSet bool
}

func (f *fakeExecutor) Invoke(_ context.Context, key string, request Request) (Result, error) {
	f.key = key
	f.request = request
	if f.resultSet {
		return f.result, nil
	}
	return Result{Stdout: "done"}, nil
}

func (f *fakeExecutor) Reset(_ context.Context, key string) error {
	f.resetKey = key
	return nil
}

type fakeToolContext struct {
	agent.Context
	artifacts agent.Artifacts
}

func (fakeToolContext) AppName() string   { return "krude" }
func (fakeToolContext) UserID() string    { return "member" }
func (fakeToolContext) SessionID() string { return "conversation" }
func (fakeToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil
}
func (f fakeToolContext) Artifacts() agent.Artifacts {
	return f.artifacts
}

type fakeArtifacts struct {
	name string
	part *genai.Part
}

func (f *fakeArtifacts) Save(_ context.Context, name string, part *genai.Part) (*artifact.SaveResponse, error) {
	f.name = name
	f.part = part
	return &artifact.SaveResponse{Version: 4}, nil
}

func (*fakeArtifacts) List(context.Context) (*artifact.ListResponse, error) { return nil, nil }
func (*fakeArtifacts) Load(context.Context, string) (*artifact.LoadResponse, error) {
	return nil, nil
}
func (*fakeArtifacts) LoadVersion(context.Context, string, int) (*artifact.LoadResponse, error) {
	return nil, nil
}

type runnableTool interface {
	tool.Tool
	Run(agent.Context, any) (map[string]any, error)
}

func TestToolsetExposesTheBasicSandboxTools(t *testing.T) {
	toolset, err := NewToolset(&fakeExecutor{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := toolset.Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, sandboxTool := range tools {
		names = append(names, sandboxTool.Name())
	}
	want := []string{
		"sandbox_execute_code",
		"sandbox_execute_command",
		"sandbox_list_files",
		"sandbox_read_files",
		"sandbox_write_files",
		"sandbox_remove_files",
		"sandbox_reset",
		"sandbox_save_artifact",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("tool names = %v", names)
	}
	if !strings.Contains(tools[5].Description(), "confirmation") {
		t.Fatal("sandbox_remove_files does not describe confirmation")
	}
}

func TestExecuteCodeToolRoutesTheADKConversation(t *testing.T) {
	executor := &fakeExecutor{}
	sandboxTool, err := executeCodeTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	runnable, ok := sandboxTool.(runnableTool)
	if !ok {
		t.Fatalf("tool type %T is not runnable", sandboxTool)
	}
	result, err := runnable.Run(fakeToolContext{}, map[string]any{"code": "print(1)"})
	if err != nil {
		t.Fatal(err)
	}
	if executor.key != "krude\x00member\x00conversation" || executor.request.Action != ExecuteCode || executor.request.Language != "python" || executor.request.Code != "print(1)" {
		t.Fatalf("key = %q, request = %+v", executor.key, executor.request)
	}
	if result["stdout"] != "done" {
		t.Fatalf("result = %#v", result)
	}
}

func TestNewToolsetRequiresExecutor(t *testing.T) {
	if _, err := NewToolset(nil); err == nil {
		t.Fatal("NewToolset(nil) error = nil")
	}
}

func TestSaveArtifactToolMovesBinaryContentWithoutTheModel(t *testing.T) {
	raw := []byte{0x50, 0x4b, 0x03, 0x04, 0x00, 0x01, 0x02, 0x03} // stands in for real file bytes
	// AgentCore answers ReadFiles as one "resource" block, payload in
	// ResourceDataBase64 and Name left empty — confirmed against a real
	// sandbox; see sandboxFileBytes. It is picked up by being the result's
	// only block, the same way real calls are.
	executor := &fakeExecutor{resultSet: true, result: Result{Content: []Content{{
		Type: "resource", MIMEType: "application/vnd.openxmlformats-officedocument.presentationml.presentation",
		ResourceDataBase64: base64.StdEncoding.EncodeToString(raw), ResourceURI: "file:///report.pptx",
	}}}}
	store := &fakeArtifacts{}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	result, err := sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: store}, map[string]any{"path": "/tmp/report.pptx"})
	if err != nil {
		t.Fatal(err)
	}
	if store.name != "report.pptx" {
		t.Fatalf("artifact name = %q, want report.pptx", store.name)
	}
	if store.part == nil || store.part.InlineData == nil || string(store.part.InlineData.Data) != string(raw) {
		t.Fatalf("saved part = %+v, want the raw sandbox bytes", store.part)
	}
	if store.part.InlineData.MIMEType != "application/vnd.openxmlformats-officedocument.presentationml.presentation" {
		t.Fatalf("mime type = %q", store.part.InlineData.MIMEType)
	}
	if result["artifact_name"] != "report.pptx" || result["artifact_version"] != float64(4) || result["size_bytes"] != float64(len(raw)) {
		t.Fatalf("result = %#v", result)
	}
	if executor.request.MaxOutputBytes != maxArtifactBytes {
		t.Fatalf("read budget = %d, want the artifact budget: the bytes never reach the model", executor.request.MaxOutputBytes)
	}
	if executor.request.Action != ReadFiles || !slices.Equal(executor.request.Paths, []string{"/tmp/report.pptx"}) {
		t.Fatalf("request = %+v", executor.request)
	}
}

func TestSaveArtifactToolMovesTextContent(t *testing.T) {
	executor := &fakeExecutor{resultSet: true, result: Result{Content: []Content{{
		Type: "resource", MIMEType: "text/plain", ResourceText: "hello from the sandbox", ResourceURI: "file:///notes.txt",
	}}}}
	store := &fakeArtifacts{}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: store}, map[string]any{"path": "notes.txt"}); err != nil {
		t.Fatal(err)
	}
	if store.part == nil || string(store.part.InlineData.Data) != "hello from the sandbox" {
		t.Fatalf("saved part = %+v", store.part)
	}
}

// TestSaveArtifactToolAlsoAcceptsTheDirectContentShape covers the Text and
// DataBase64 fields directly, the shape a non-ReadFiles content block (or a
// future AgentCore change) could still use.
func TestSaveArtifactToolAlsoAcceptsTheDirectContentShape(t *testing.T) {
	raw := []byte{0xff, 0xd8, 0xff, 0x00}
	executor := &fakeExecutor{resultSet: true, result: Result{Content: []Content{{
		Type: "data", MIMEType: "image/jpeg", DataBase64: base64.StdEncoding.EncodeToString(raw),
	}}}}
	store := &fakeArtifacts{}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: store}, map[string]any{"path": "photo.jpg"}); err != nil {
		t.Fatal(err)
	}
	if store.part == nil || string(store.part.InlineData.Data) != string(raw) {
		t.Fatalf("saved part = %+v, want the raw bytes", store.part)
	}
}

func TestSaveArtifactToolRefusesATruncatedResult(t *testing.T) {
	executor := &fakeExecutor{resultSet: true, result: Result{
		OutputTruncated: true,
		Content:         []Content{{Name: "big.bin", DataBase64: base64.StdEncoding.EncodeToString([]byte("partial"))}},
	}}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	store := &fakeArtifacts{}
	if _, err := sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: store}, map[string]any{"path": "big.bin"}); err == nil {
		t.Fatal("expected an error for a truncated read")
	}
	if store.name != "" {
		t.Fatalf("a truncated file must not be saved, got %q", store.name)
	}
}

func TestSaveArtifactToolRequiresAnArtifactService(t *testing.T) {
	executor := &fakeExecutor{resultSet: true, result: Result{Content: []Content{{Name: "notes.txt", Text: "hi"}}}}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sandboxTool.(runnableTool).Run(fakeToolContext{}, map[string]any{"path": "notes.txt"}); err == nil {
		t.Fatal("expected an error with no artifact service configured")
	}
}

func TestSaveArtifactToolNeverReturnsAnEmptyError(t *testing.T) {
	executor := &fakeExecutor{resultSet: true, result: Result{IsError: true}}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: &fakeArtifacts{}}, map[string]any{"path": "deck.pptx"})
	if err == nil || !strings.Contains(err.Error(), "deck.pptx") || !strings.Contains(err.Error(), "sandbox_list_files") {
		t.Fatalf("error = %v, want the path and something to try", err)
	}
}

func TestSaveArtifactToolSurfacesASandboxError(t *testing.T) {
	executor := &fakeExecutor{resultSet: true, result: Result{IsError: true, Stderr: "no such file"}}
	sandboxTool, err := saveArtifactTool(executor)
	if err != nil {
		t.Fatal(err)
	}
	_, err = sandboxTool.(runnableTool).Run(fakeToolContext{artifacts: &fakeArtifacts{}}, map[string]any{"path": "missing.txt"})
	if err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Fatalf("error = %v, want it to mention the sandbox's own message", err)
	}
}
