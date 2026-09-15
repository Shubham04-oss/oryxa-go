package agentcoresandbox

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
)

type fakeExecutor struct {
	key      string
	request  Request
	resetKey string
}

func (f *fakeExecutor) Invoke(_ context.Context, key string, request Request) (Result, error) {
	f.key = key
	f.request = request
	return Result{Stdout: "done"}, nil
}

func (f *fakeExecutor) Reset(_ context.Context, key string) error {
	f.resetKey = key
	return nil
}

type fakeToolContext struct {
	agent.Context
}

func (fakeToolContext) AppName() string   { return "krude" }
func (fakeToolContext) UserID() string    { return "member" }
func (fakeToolContext) SessionID() string { return "conversation" }
func (fakeToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	return nil
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
