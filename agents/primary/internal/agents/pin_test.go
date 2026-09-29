package agents

import (
	"context"
	"strings"
	"testing"

	"google.golang.org/adk/v2/model"
	"google.golang.org/genai"
)

// callingOnce is a model that loads a toolbox, calls one tool in it with the
// arguments it is given, and then says what the tool answered, so a test can
// read the tool's answer as the model would.
func callingOnce(toolbox, toolName string, args map[string]any) *treeModel {
	return newTreeModel(func(_ string, request *model.LLMRequest) *genai.Content {
		switch {
		case functionResponse(request, toolName) != "":
			return genai.NewContentFromText("The tool answered: "+functionResponse(request, toolName), genai.RoleModel)
		case functionResponse(request, "load_tools") != "":
			return functionCalls(&genai.FunctionCall{ID: "call_" + toolName, Name: toolName, Args: args})
		default:
			return functionCalls(&genai.FunctionCall{ID: "load_" + toolbox, Name: "load_tools", Args: map[string]any{"names": []any{toolbox}}})
		}
	})
}

// unrelatedContextKey keys a context value that has nothing to do with a pin.
type unrelatedContextKey struct{}

// TestARunKnowsTheTeamspaceItIsPinnedToOnlyFromItsContext: the pin is on the
// context of the run it was set for, trimmed, and nowhere else; a blank one
// pins nothing, and what is derived from a pinned context is pinned too.
func TestARunKnowsTheTeamspaceItIsPinnedToOnlyFromItsContext(t *testing.T) {
	if id, ok := PinnedTeamspace(context.Background()); ok || id != "" {
		t.Fatalf("a run with no pin reads %q, %t", id, ok)
	}
	pinned := WithPinnedTeamspace(context.Background(), " tsp_launch ")
	if id, ok := PinnedTeamspace(pinned); !ok || id != "tsp_launch" {
		t.Fatalf("a pinned run reads %q, %t, want tsp_launch", id, ok)
	}
	if id, ok := PinnedTeamspace(context.WithValue(pinned, unrelatedContextKey{}, "another value")); !ok || id != "tsp_launch" {
		t.Fatalf("a context derived from a pinned one reads %q, %t, want it pinned too", id, ok)
	}
	for _, blank := range []string{"", "  ", "\t\n"} {
		if _, ok := PinnedTeamspace(WithPinnedTeamspace(context.Background(), blank)); ok {
			t.Errorf("a blank pin (%q) pinned the run", blank)
		}
	}
	if _, ok := PinnedTeamspace(WithPinnedTeamspace(pinned, "")); !ok {
		t.Error("pinning nothing unpinned a run that was pinned")
	}
}

// TestOnlyATeamspaceIdCanPinARun: a pin has something in it after trimming,
// at most 128 characters, and no whitespace, and is kept as trimmed.
func TestOnlyATeamspaceIdCanPinARun(t *testing.T) {
	for _, testCase := range []struct {
		name string
		id   string
		want string
		ok   bool
	}{
		{"a teamspace id", "tsp_u9Kq-3w", "tsp_u9Kq-3w", true},
		{"one with spaces around it", "  tsp_launch\t", "tsp_launch", true},
		{"one 128 characters long", strings.Repeat("t", 128), strings.Repeat("t", 128), true},
		{"one 128 characters long, each more than a byte", strings.Repeat("é", 128), strings.Repeat("é", 128), true},
		{"nothing", "", "", false},
		{"only spaces", " \t\n ", "", false},
		{"one 129 characters long", strings.Repeat("t", 129), "", false},
		{"one 129 characters long, each more than a byte", strings.Repeat("é", 129), "", false},
		{"a space inside", "tsp launch", "", false},
		{"a tab inside", "tsp\tlaunch", "", false},
		{"a newline inside", "tsp_launch\ntsp_other", "", false},
		{"a no-break space inside", "tsp launch", "", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := CleanTeamspacePin(testCase.id)
			if (err == nil) != testCase.ok || got != testCase.want {
				t.Fatalf("CleanTeamspacePin(%q) = %q, %v; want %q, ok=%t", testCase.id, got, err, testCase.want, testCase.ok)
			}
		})
	}
}

// TestAPinnedRunActsInItsTeamspaceOrRefuses: what a tool acts in when the
// model names a teamspace. Without a pin, exactly what it named, even blank or
// padded; with one, the pinned teamspace when the model left it out or named
// that one, and a refusal for any other, which does not say which one the run
// is pinned to.
func TestAPinnedRunActsInItsTeamspaceOrRefuses(t *testing.T) {
	unpinned := context.Background()
	pinned := WithPinnedTeamspace(context.Background(), "tsp_launch")
	for _, testCase := range []struct {
		name  string
		ctx   context.Context
		named string
		want  string
		ok    bool
	}{
		{"unpinned, none named", unpinned, "", "", true},
		{"unpinned, one named", unpinned, "tsp_other", "tsp_other", true},
		{"unpinned, one named with spaces, untouched", unpinned, "  tsp_other ", "  tsp_other ", true},
		{"pinned, none named", pinned, "", "tsp_launch", true},
		{"pinned, only spaces named", pinned, "  ", "tsp_launch", true},
		{"pinned, the pinned one named", pinned, "tsp_launch", "tsp_launch", true},
		{"pinned, the pinned one named with spaces", pinned, " tsp_launch\n", "tsp_launch", true},
		{"pinned, another named", pinned, "tsp_other", "", false},
		{"pinned, the pinned one named in another case", pinned, "TSP_LAUNCH", "", false},
		{"pinned, the pinned one named and more", pinned, "tsp_launch tsp_other", "", false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := pinnedTeamspaceFor(testCase.ctx, testCase.named)
			if (err == nil) != testCase.ok || got != testCase.want {
				t.Fatalf("pinnedTeamspaceFor(%q) = %q, %v; want %q, ok=%t", testCase.named, got, err, testCase.want, testCase.ok)
			}
			if err != nil && strings.Contains(err.Error(), "tsp_launch") {
				t.Errorf("the refusal tells the model which teamspace it is pinned to: %v", err)
			}
		})
	}
}

// TestOnlyThePinnedTeamspaceIsListed: a pinned run sees the one teamspace it is
// pinned to among those an agent may work in, and none when that one is not
// among them; an unpinned run sees them all.
func TestOnlyThePinnedTeamspaceIsListed(t *testing.T) {
	spaces := []Teamspace{{ID: "tsp_launch"}, {ID: "tsp_sales"}, {ID: "tsp_ops"}}
	if got := onlyPinned(context.Background(), spaces); len(got) != 3 {
		t.Fatalf("an unpinned run sees %+v, want all three", got)
	}
	if got := onlyPinned(WithPinnedTeamspace(context.Background(), "tsp_sales"), spaces); len(got) != 1 || got[0].ID != "tsp_sales" {
		t.Fatalf("a pinned run sees %+v, want tsp_sales alone", got)
	}
	got := onlyPinned(WithPinnedTeamspace(context.Background(), "tsp_elsewhere"), spaces)
	if got == nil || len(got) != 0 {
		t.Fatalf("a run pinned to a teamspace the agent cannot work in sees %#v, want an empty list that encodes as []", got)
	}
}
