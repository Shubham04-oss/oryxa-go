package agentcorebrowser

import (
	"context"
	"slices"
	"strings"
	"testing"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/artifact"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolconfirmation"
	"google.golang.org/genai"
)

type fakeController struct {
	key      string
	request  Request
	resetKey string
	result   Result
}

func (f *fakeController) Invoke(_ context.Context, key string, request Request) (Result, error) {
	f.key = key
	f.request = request
	return f.result, nil
}

func (f *fakeController) Reset(_ context.Context, key string) error {
	f.resetKey = key
	return nil
}

type fakeToolContext struct {
	agent.Context
	artifacts agent.Artifacts
	confirmed bool
}

func (f fakeToolContext) AppName() string   { return "krude" }
func (f fakeToolContext) UserID() string    { return "member" }
func (f fakeToolContext) SessionID() string { return "conversation" }
func (f fakeToolContext) Artifacts() agent.Artifacts {
	return f.artifacts
}
func (fakeToolContext) Actions() *session.EventActions        { return &session.EventActions{} }
func (fakeToolContext) RequestConfirmation(string, any) error { return nil }
func (f fakeToolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation {
	if f.confirmed {
		return &toolconfirmation.ToolConfirmation{Confirmed: true}
	}
	return nil
}

type runnableTool interface {
	tool.Tool
	Run(agent.Context, any) (map[string]any, error)
}

func TestToolsetExposesBasicBrowserTools(t *testing.T) {
	toolset, err := NewToolset(&fakeController{})
	if err != nil {
		t.Fatal(err)
	}
	tools, err := toolset.Tools(nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, browserTool := range tools {
		names = append(names, browserTool.Name())
	}
	want := []string{
		"browser_navigate",
		"browser_snapshot",
		"browser_click",
		"browser_fill",
		"browser_screenshot",
		"browser_reset",
	}
	if !slices.Equal(names, want) {
		t.Fatalf("tool names = %v", names)
	}
	for _, index := range []int{2, 3, 5} {
		if !strings.Contains(tools[index].Description(), "confirm") {
			t.Fatalf("%s does not describe confirmation", tools[index].Name())
		}
	}
}

func TestNavigateToolRoutesADKConversation(t *testing.T) {
	controller := &fakeController{result: Result{Page: PageSnapshot{Title: "Example"}, SessionRecreated: true}}
	browserTool, err := navigateTool(controller)
	if err != nil {
		t.Fatal(err)
	}
	runnable := browserTool.(runnableTool)
	result, err := runnable.Run(fakeToolContext{}, map[string]any{"url": "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	if controller.key != "krude\x00member\x00conversation" || controller.request.Action != Navigate || controller.request.URL != "https://example.test" {
		t.Fatalf("key = %q, request = %+v", controller.key, controller.request)
	}
	if result["session_recreated"] != true {
		t.Fatalf("result = %#v", result)
	}
}

type fakeArtifacts struct {
	name string
	part *genai.Part
}

func (f *fakeArtifacts) Save(_ context.Context, name string, part *genai.Part) (*artifact.SaveResponse, error) {
	f.name = name
	f.part = part
	return &artifact.SaveResponse{Version: 3}, nil
}

func (*fakeArtifacts) List(context.Context) (*artifact.ListResponse, error) {
	return &artifact.ListResponse{}, nil
}

func (*fakeArtifacts) Load(context.Context, string) (*artifact.LoadResponse, error) {
	return nil, nil
}

func (*fakeArtifacts) LoadVersion(context.Context, string, int) (*artifact.LoadResponse, error) {
	return nil, nil
}

func TestScreenshotToolStoresPNGAsArtifact(t *testing.T) {
	controller := &fakeController{result: Result{
		ScreenshotPNG:  []byte("png-data"),
		ViewportWidth:  1440,
		ViewportHeight: 900,
	}}
	store := &fakeArtifacts{}
	browserTool, err := screenshotTool(controller)
	if err != nil {
		t.Fatal(err)
	}
	result, err := browserTool.(runnableTool).Run(fakeToolContext{artifacts: store}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(store.name, "browser-screenshot-") || store.part == nil || store.part.InlineData == nil || store.part.InlineData.MIMEType != "image/png" {
		t.Fatalf("saved name = %q, part = %+v", store.name, store.part)
	}
	if result["artifact_name"] != store.name || result["artifact_version"] != float64(3) {
		t.Fatalf("result = %#v", result)
	}
}

func TestClickRequiresConfirmationBeforeControllerCall(t *testing.T) {
	controller := &fakeController{}
	browserTool, err := clickTool(controller)
	if err != nil {
		t.Fatal(err)
	}
	_, err = browserTool.(runnableTool).Run(fakeToolContext{}, map[string]any{"selector": "#buy"})
	if err == nil || !strings.Contains(err.Error(), "requires confirmation") {
		t.Fatalf("click error = %v", err)
	}
	if controller.request.Action != "" {
		t.Fatalf("controller was invoked before confirmation: %+v", controller.request)
	}
}

func TestNewToolsetRequiresController(t *testing.T) {
	if _, err := NewToolset(nil); err == nil {
		t.Fatal("NewToolset(nil) error = nil")
	}
}
