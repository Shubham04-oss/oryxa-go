package cfbrowser

import (
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"net/http/httptest"
	"net/netip"
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

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agentcorebrowser"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

type issuer struct{ claims []edgev1.TokenClaims }

func (i *issuer) EdgeToken(_ context.Context, claims edgev1.TokenClaims) (string, error) {
	i.claims = append(i.claims, claims)
	return "token", nil
}

// resolver answers every name with the addresses in answers.
type resolver map[string][]netip.Addr

func (r resolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	return r[host], nil
}

type seenRequest struct {
	Path string
	Body map[string]any
}

func fakeEdge(t *testing.T, answer func(seenRequest) (int, any)) (*Controller, *issuer, *[]seenRequest) {
	t.Helper()
	var seen []seenRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := seenRequest{Path: r.URL.Path}
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
	controller, err := New(client)
	if err != nil {
		t.Fatal(err)
	}
	controller.resolver = resolver{
		"example.com":   {netip.MustParseAddr("93.184.215.14")},
		"intranet.test": {netip.MustParseAddr("10.1.2.3")},
	}
	return controller, tokens, &seen
}

var target = edgeclient.Target{Org: "org_1", Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "sess_1"}, Member: "usr_1", Agent: "agt_1", Run: "inv_1"}

var page = edgev1.PageResponse{Page: edgev1.PageSnapshot{
	Title: "Example", URL: "https://example.com/", Text: "Hello", TextTruncated: true,
	Elements: []edgev1.Element{{Selector: "a:nth-of-type(1)", Tag: "a", Text: "More", Href: "https://example.com/more"}},
}, SessionRecreated: true}

func TestNavigateUsesTheMembersBrowserAndKeepsAgentCoresShape(t *testing.T) {
	controller, tokens, seen := fakeEdge(t, func(seenRequest) (int, any) { return 200, page })
	result, err := controller.Invoke(context.Background(), target, Request{Action: Navigate, URL: " https://example.com/ "})
	if err != nil {
		t.Fatal(err)
	}
	if (*seen)[0].Path != "/v1/rooms/thread/sess_1/browser/usr_1/navigate" || (*seen)[0].Body["url"] != "https://example.com/" {
		t.Errorf("request = %+v", (*seen)[0])
	}
	if claims := tokens.claims[0]; !claims.HasScope(edgev1.ScopeBrowserDrive) || len(claims.Scopes) != 1 || claims.Member != "usr_1" {
		t.Errorf("claims = %+v", claims)
	}
	encoded, _ := json.Marshal(result.Page)
	want := `{"title":"Example","url":"https://example.com/","text":"Hello","elements":[{"selector":"a:nth-of-type(1)","tag":"a","text":"More","href":"https://example.com/more"}],"text_truncated":true}`
	if string(encoded) != want || !result.SessionRecreated {
		t.Errorf("page\n got %s\nwant %s", encoded, want)
	}
}

func TestNavigationToNonPublicHostsNeverReachesTheEdge(t *testing.T) {
	controller, _, seen := fakeEdge(t, func(seenRequest) (int, any) { return 200, page })
	for _, address := range []string{
		"http://localhost:3000/", "http://127.0.0.1/", "http://169.254.169.254/latest/", "http://[::1]/",
		"http://intranet.test/", "http://2130706433/", "http://0x7f.1/", "file:///etc/passwd",
		"https://user:pass@example.com/", "http://unresolvable.test/",
	} {
		if _, err := controller.Invoke(context.Background(), target, Request{Action: Navigate, URL: address}); err == nil {
			t.Errorf("%s: allowed", address)
		}
	}
	if len(*seen) != 0 {
		t.Errorf("%d refused navigations reached the edge", len(*seen))
	}
}

func TestAMembersTakeoverReadsAsWaitForThem(t *testing.T) {
	controller, _, _ := fakeEdge(t, func(seenRequest) (int, any) {
		return 409, edgev1.ErrorBody{Error: edgev1.ErrorDetail{Code: edgev1.CodeConflict, Message: "a member has control"}}
	})
	_, err := controller.Invoke(context.Background(), target, Request{Action: Click, Selector: "#go"})
	if err == nil || !strings.Contains(err.Error(), "hand it back") {
		t.Fatalf("err = %v", err)
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

func (toolContext) Deadline() (time.Time, bool)                          { return time.Time{}, false }
func (toolContext) Done() <-chan struct{}                                { return nil }
func (toolContext) Err() error                                           { return nil }
func (toolContext) Value(any) any                                        { return nil }
func (toolContext) AppName() string                                      { return "krude" }
func (toolContext) UserID() string                                       { return "usr_1" }
func (toolContext) SessionID() string                                    { return "sess_1" }
func (toolContext) InvocationID() string                                 { return "inv_1" }
func (toolContext) AgentName() string                                    { return "krude" }
func (c toolContext) ReadonlyState() session.ReadonlyState               { return c.state }
func (c toolContext) Artifacts() agent.Artifacts                         { return c.artifacts }
func (toolContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

type artifacts struct {
	name string
	part *genai.Part
}

func (a *artifacts) Save(_ context.Context, name string, part *genai.Part) (*artifact.SaveResponse, error) {
	a.name, a.part = name, part
	return &artifact.SaveResponse{Version: 2}, nil
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

type noopAgentCore struct{}

func (noopAgentCore) Invoke(context.Context, string, agentcorebrowser.Request) (agentcorebrowser.Result, error) {
	return agentcorebrowser.Result{}, nil
}
func (noopAgentCore) Reset(context.Context, string) error { return nil }

// The tools are agentcorebrowser's: names, order, confirmations and
// argument shapes. (Deleted with agentcorebrowser at cutover, A11.)
func TestToolsMatchAgentCores(t *testing.T) {
	ours, _ := NewToolset(&Controller{})
	theirs, _ := agentcorebrowser.NewToolset(noopAgentCore{})
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
		if !reflect.DeepEqual(shape(t, our.ParametersJsonSchema), shape(t, their.ParametersJsonSchema)) {
			t.Errorf("%s: arguments differ from AgentCore's", our.Name)
		}
		if strings.Contains(our.Description, "AgentCore") || strings.Contains(our.Description, "AWS") {
			t.Errorf("%s still names AgentCore", our.Name)
		}
	}
}

func shape(t *testing.T, schema any) any {
	t.Helper()
	encoded, _ := json.Marshal(schema)
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

func TestScreenshotToolKeepsTheImageAsAnArtifact(t *testing.T) {
	controller, _, _ := fakeEdge(t, func(seenRequest) (int, any) {
		return 200, edgev1.ScreenshotResponse{Key: "runs/x.png", MIMEType: "image/png", Width: 1440, Height: 900, Data: "iVBORw=="}
	})
	toolset, _ := NewToolset(controller)
	list, _ := toolset.Tools(nil)
	var screenshot runnable
	for _, each := range list {
		if each.Name() == "browser_screenshot" {
			screenshot = each.(runnable)
		}
	}
	saved := &artifacts{}
	result, err := screenshot.Run(toolContext{state: mapState{"organization_id": "org_1"}, artifacts: saved}, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(saved.name, "browser-screenshot-") || saved.part.InlineData.MIMEType != "image/png" || len(saved.part.InlineData.Data) != 4 {
		t.Errorf("saved %s", saved.name)
	}
	if result["viewport_width"] != float64(1440) && result["viewport_width"] != int32(1440) {
		t.Errorf("result = %v", result)
	}
}
