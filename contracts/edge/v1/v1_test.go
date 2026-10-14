package v1

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fixtureTypes maps every shared fixture to the type it holds. The edge's
// contract test (edge/src/contracts/contracts.test.ts) maps the same files
// to its schemas, so a field one side adds and the other doesn't fails here
// or there.
var fixtureTypes = map[string]func() any{
	"token.claims.json":                  func() any { return new(TokenClaims) },
	"sandbox.exec_code.request.json":     func() any { return new(ExecCodeRequest) },
	"sandbox.exec_command.request.json":  func() any { return new(ExecCommandRequest) },
	"sandbox.exec.result.json":           func() any { return new(ExecResult) },
	"sandbox.exec.timeout.result.json":   func() any { return new(ExecResult) },
	"sandbox.files.list.response.json":   func() any { return new(ListFilesResponse) },
	"sandbox.files.read.request.json":    func() any { return new(ReadFilesRequest) },
	"sandbox.files.read.response.json":   func() any { return new(ReadFilesResponse) },
	"sandbox.files.write.request.json":   func() any { return new(WriteFilesRequest) },
	"sandbox.files.write.response.json":  func() any { return new(WriteFilesResponse) },
	"sandbox.files.remove.request.json":  func() any { return new(RemoveFilesRequest) },
	"sandbox.files.remove.response.json": func() any { return new(RemoveFilesResponse) },
	"sandbox.reset.response.json":        func() any { return new(ResetResponse) },
	"sandbox.artifact.request.json":      func() any { return new(ArtifactRequest) },
	"sandbox.artifact.response.json":     func() any { return new(ArtifactResponse) },
	"sandbox.preview.request.json":       func() any { return new(PreviewRequest) },
	"sandbox.terminal.request.json":      func() any { return new(TerminalRequest) },
	"sandbox.snapshot.response.json":     func() any { return new(SnapshotResponse) },
	"signed_url.json":                    func() any { return new(SignedURL) },
	"browser.navigate.request.json":      func() any { return new(NavigateRequest) },
	"browser.click.request.json":         func() any { return new(ClickRequest) },
	"browser.fill.request.json":          func() any { return new(FillRequest) },
	"browser.page.response.json":         func() any { return new(PageResponse) },
	"browser.screenshot.response.json":   func() any { return new(ScreenshotResponse) },
	"browser.live.request.json":          func() any { return new(LiveRequest) },
	"browser.handoff.request.json":       func() any { return new(HandoffRequest) },
	"browser.handoff.response.json":      func() any { return new(HandoffResponse) },
	"browser.handoff.state.json":         func() any { return new(HandoffState) },
	"browser.resume.request.json":        func() any { return new(ResumeRequest) },
	"browser.resume.response.json":       func() any { return new(ResumeResponse) },
	"event.json":                         func() any { return new(Event) },
	"event.room.json":                    func() any { return new(Event) },
	"log.append.request.json":            func() any { return new(AppendEventRequest) },
	"log.response.json":                  func() any { return new(LogResponse) },
	"children.response.json":             func() any { return new(ChildrenResponse) },
	"members.replace.request.json":       func() any { return new(MembersRequest) },
	"members.change.request.json":        func() any { return new(MembersRequest) },
	"members.response.json":              func() any { return new(MembersResponse) },
	"error.json":                         func() any { return new(ErrorBody) },
	"error.retryable.json":               func() any { return new(ErrorBody) },
}

// Each fixture decodes with no unknown field and encodes back to the same
// JSON, so Go neither drops nor invents a field the edge sends or expects.
func TestFixturesRoundTrip(t *testing.T) {
	for name, newValue := range fixtureTypes {
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			value := newValue()
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(value); err != nil {
				t.Fatalf("decode: %v", err)
			}
			encoded, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			var want, got any
			if err := json.Unmarshal(raw, &want); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(encoded, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(want, got) {
				t.Fatalf("round trip changed the fixture\nwant %s\ngot  %s", bytes.TrimSpace(raw), encoded)
			}
		})
	}
}

// A fixture nobody maps tests nothing, on either side.
func TestEveryFixtureIsMapped(t *testing.T) {
	entries, err := os.ReadDir("testdata")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") && fixtureTypes[entry.Name()] == nil {
			t.Errorf("testdata/%s has no type in fixtureTypes", entry.Name())
		}
	}
}

func TestNames(t *testing.T) {
	room := Room{Kind: RoomThread, ID: "thr_1"}
	for _, test := range []struct {
		name string
		got  func() (string, error)
		want string
	}{
		{"room", func() (string, error) { return RoomDOName("org_1", room) }, "org/org_1/room/thread/thr_1"},
		{"shared sandbox", func() (string, error) { return SandboxDOName("org_1", room, SharedSandbox) }, "org/org_1/room/thread/thr_1/sandbox/shared"},
		{"private sandbox", func() (string, error) { return SandboxDOName("org_1", room, PrivateSandbox("usr_1")) }, "org/org_1/room/thread/thr_1/sandbox/private-usr_1"},
		{"run sandbox", func() (string, error) { return SandboxDOName("org_1", room, "run_9") }, "org/org_1/room/thread/thr_1/sandbox/run_9"},
		{"browser", func() (string, error) { return BrowserDOName("org_1", room, "default") }, "org/org_1/room/thread/thr_1/browser/default"},
		{"voice", func() (string, error) { return VoiceDOName("org_1", room) }, "org/org_1/room/thread/thr_1/voice"},
		{"sandbox path", func() (string, error) { return SandboxPath(room, SharedSandbox) }, "/v1/rooms/thread/thr_1/sandbox/shared"},
		{"browser path", func() (string, error) { return BrowserPath(room, "default") }, "/v1/rooms/thread/thr_1/browser/default"},
		{"run prefix", func() (string, error) { return RunPrefix("org_1", room, "run_1") }, "runs/org_1/thread/thr_1/run_1/"},
		{"room events prefix", func() (string, error) { return RunPrefix("org_1", room, RoomRun) }, "runs/org_1/thread/thr_1/_room/"},
	} {
		got, err := test.got()
		if err != nil || got != test.want {
			t.Errorf("%s = %q, %v; want %q", test.name, got, err, test.want)
		}
	}
}

// Nothing that could break out of its segment of a name, path or key is
// accepted.
func TestNamesRefuseSeparators(t *testing.T) {
	good := Room{Kind: RoomThread, ID: "thr_1"}
	for name, call := range map[string]func() error{
		"slash in org":       func() error { _, err := RoomDOName("org/2", good); return err },
		"dots in room":       func() error { _, err := RoomDOName("org_1", Room{Kind: RoomThread, ID: ".."}); return err },
		"unknown kind":       func() error { _, err := RoomDOName("org_1", Room{Kind: "lobby", ID: "x"}); return err },
		"empty room":         func() error { _, err := RoomDOName("org_1", Room{Kind: RoomDM}); return err },
		"slash in sandbox":   func() error { _, err := SandboxDOName("org_1", good, "shared/../x"); return err },
		"empty private":      func() error { _, err := SandboxDOName("org_1", good, "private-"); return err },
		"slash in browser":   func() error { _, err := BrowserPath(good, "a/b"); return err },
		"unicode in run":     func() error { _, err := RunPrefix("org_1", good, "rün"); return err },
		"too long":           func() error { _, err := RoomDOName(strings.Repeat("a", 129), good); return err },
		"newline in room id": func() error { _, err := RoomPath(Room{Kind: RoomSide, ID: "a\nb"}); return err },
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestHasScope(t *testing.T) {
	claims := TokenClaims{Scopes: []Scope{ScopeSandboxExec}}
	if !claims.HasScope(ScopeSandboxExec) || claims.HasScope(ScopeBrowserDrive) {
		t.Fatal("HasScope is wrong")
	}
}

func TestConversationRoomIDs(t *testing.T) {
	a, b := "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f", "0a1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"
	direct, err := DirectRoomID(a, b)
	if err != nil || direct != b+"_"+a {
		t.Fatalf("DirectRoomID = %q, %v", direct, err)
	}
	if again, _ := DirectRoomID(b, a); again != direct {
		t.Error("the order of the members changed the room")
	}
	if members, err := ParseDirectRoomID(direct); err != nil || members != [2]string{b, a} {
		t.Errorf("ParseDirectRoomID = %v, %v", members, err)
	}
	side, err := SideRoomID("tsp_Ab-c_d9", a, b)
	if err != nil || side != "tsp_Ab-c_d9_"+b+"_"+a {
		t.Fatalf("SideRoomID = %q, %v", side, err)
	}
	if teamspace, members, err := ParseSideRoomID(side); err != nil || teamspace != "tsp_Ab-c_d9" || members != [2]string{b, a} {
		t.Errorf("ParseSideRoomID = %q %v %v", teamspace, members, err)
	}
	for name, bad := range map[string]func() error{
		"same member":     func() error { _, err := DirectRoomID(a, a); return err },
		"underscore":      func() error { _, err := DirectRoomID("usr_1", b); return err },
		"unsorted direct": func() error { _, err := ParseDirectRoomID(a + "_" + b); return err },
		"three parts":     func() error { _, err := ParseDirectRoomID("x_y_z"); return err },
		"unsorted side":   func() error { _, _, err := ParseSideRoomID("tsp_1_" + a + "_" + b); return err },
		"no teamspace":    func() error { _, err := SideRoomID("", a, b); return err },
	} {
		if bad() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
