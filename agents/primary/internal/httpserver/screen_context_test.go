package httpserver

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// A screen snapshot only points an agent at things the member had open. One
// hint that breaks a rule must cost the run that hint, never the member's
// message: the run starts, with the hints that are usable.
func TestAScreenHintThatBreaksARuleIsDroppedAndTheRunGoesOn(t *testing.T) {
	tooMany := make([]string, 13)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf(`{"kind":"artifact","id":"file_%d.md","name":"File %d"}`, i, i)
	}
	notes := primaryagents.ScreenItem{Kind: "artifact", ID: "notes.md", Name: "Notes"}
	launch := primaryagents.ScreenItem{Kind: "teamspace", ID: "space_1", Name: "Launch"}

	for name, test := range map[string]struct {
		screen  string
		want    *primaryagents.ScreenContext
		dropped int
	}{
		"every hint is fine": {
			`{"view":"team/teamspace","selection":{"kind":"teamspace","id":"space_1","name":"Launch"},"visible_items":[{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", Selection: &launch, VisibleItems: []primaryagents.ScreenItem{notes}}, 0,
		},
		"an item with a line break in its name": {
			`{"view":"team/teamspace","visible_items":[{"kind":"artifact","id":"notes.md","name":"Notes"},{"kind":"person","id":"user_1","name":"A\nB"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", VisibleItems: []primaryagents.ScreenItem{notes}}, 1,
		},
		"an item with a carriage return in its id": {
			`{"view":"team/teamspace","visible_items":[{"kind":"artifact","id":"a\rb","name":"Notes"},{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", VisibleItems: []primaryagents.ScreenItem{notes}}, 1,
		},
		"items with no kind or no id": {
			`{"view":"team/teamspace","visible_items":[{"kind":"","id":"x","name":"No kind"},{"kind":"artifact","id":"","name":"No id"},{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", VisibleItems: []primaryagents.ScreenItem{notes}}, 2,
		},
		"an oversized item": {
			`{"view":"team/teamspace","visible_items":[{"kind":"artifact","id":"` + strings.Repeat("x", 121) + `","name":"Long"},{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", VisibleItems: []primaryagents.ScreenItem{notes}}, 1,
		},
		"a selection that breaks a rule, beside good items": {
			`{"view":"team/teamspace","selection":{"kind":"","id":"space_1","name":"Launch"},"visible_items":[{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			&primaryagents.ScreenContext{View: "team/teamspace", VisibleItems: []primaryagents.ScreenItem{notes}}, 1,
		},
		"more than twelve items": {
			`{"view":"agents/overview","visible_items":[` + strings.Join(tooMany, ",") + `]}`,
			&primaryagents.ScreenContext{View: "agents/overview", VisibleItems: func() []primaryagents.ScreenItem {
				items := make([]primaryagents.ScreenItem, 12)
				for i := range items {
					items[i] = primaryagents.ScreenItem{Kind: "artifact", ID: fmt.Sprintf("file_%d.md", i), Name: fmt.Sprintf("File %d", i)}
				}
				return items
			}()}, 1,
		},
		// A view that names no Oryxa page leaves nothing to point at: the
		// snapshot goes, and the message still runs.
		"a view that is no Oryxa page": {
			`{"view":"other/window","selection":{"kind":"teamspace","id":"space_1","name":"Launch"},"visible_items":[{"kind":"artifact","id":"notes.md","name":"Notes"}]}`,
			nil, 3,
		},
	} {
		for route, path := range map[string]string{
			"a thread": "/v1/organizations/org_1/threads/thread_1/runs",
			"an agent": "/v1/organizations/org_1/agents/agt_sales/runs",
		} {
			t.Run(name+" on "+route, func(t *testing.T) {
				var logs bytes.Buffer
				service := &fakeThreads{run: &fakeRun{}}
				managed := &fakeManagedAgents{
					definitions: []managedagents.Definition{{ID: "agt_sales", Kind: managedagents.KindSpecialist, Name: "Sales", Status: managedagents.StatusActive}},
					run:         &fakeRun{},
				}
				server := New(member(), service, Config{
					AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Agents: managed,
				}, slog.New(slog.NewTextHandler(&logs, nil)))

				response := serve(server, http.MethodPost, path, `{"message":"What is this?","screen_context":`+test.screen+`}`, bearer)
				if response.Code != http.StatusOK {
					t.Fatalf("run = %d %s, want it started", response.Code, response.Body.String())
				}
				var got threads.RunInput
				if strings.Contains(path, "/agents/") {
					got = managed.lastInput
				} else {
					if len(service.inputs) != 1 {
						t.Fatalf("the run was started %d times, want once", len(service.inputs))
					}
					got = service.inputs[0]
				}
				if got.Message != "What is this?" {
					t.Fatalf("the run's message = %q", got.Message)
				}
				if !reflect.DeepEqual(got.ScreenContext, test.want) {
					t.Fatalf("the run's screen = %+v, want %+v", got.ScreenContext, test.want)
				}
				logged := logs.String()
				if test.dropped == 0 {
					if strings.Contains(logged, "screen hints") {
						t.Fatalf("logged %q although every hint was fine", logged)
					}
					return
				}
				if want := fmt.Sprintf("count=%d", test.dropped); !strings.Contains(logged, "left out screen hints") || !strings.Contains(logged, want) {
					t.Fatalf("log = %q, want it to say %q hints were left out", logged, want)
				}
				// What the member had open is theirs: the count is all the log gets.
				for _, private := range []string{"Launch", "Notes", "notes.md", "space_1", "user_1", "No kind", "No id", "File "} {
					if strings.Contains(logged, private) {
						t.Fatalf("the log carries %q of the member's screen: %q", private, logged)
					}
				}
			})
		}
	}
}
