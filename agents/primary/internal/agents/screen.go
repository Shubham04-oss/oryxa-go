package agents

import (
	"encoding/json"
	"regexp"
	"strings"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
)

// ScreenStateKey carries a member-supplied snapshot for one run. It is ADK
// temporary state: a tool reads it while the run goes on, and ADK drops it
// before it stores the run's event or the session's state, so a snapshot never
// outlives its run. Without the prefix it would sit in sessions.state, and in
// the event's actions, for as long as the thread lives.
const ScreenStateKey = session.KeyPrefixTemp + "current_screen_context"

type ScreenItem struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	Name string `json:"name"`
}

type ScreenContext struct {
	View         string       `json:"view"`
	Selection    *ScreenItem  `json:"selection,omitempty"`
	VisibleItems []ScreenItem `json:"visible_items"`
}

var screenViewPattern = regexp.MustCompile(`^(home|team|agents)/[a-z-]{1,60}$`)

// The bounds of a snapshot's hints, in characters (runes) except the count.
const (
	maxScreenItems = 12
	maxKindRunes   = 40
	maxIDRunes     = 120
	maxNameRunes   = 120
)

// valid reports whether the hint is one an agent may be shown: a kind and an
// id to name it by, a name if any, each within its bounds and none holding a
// line break, which could pass as the start of an instruction.
func (item ScreenItem) valid() bool {
	return item.Kind != "" && utf8.RuneCountInString(item.Kind) <= maxKindRunes &&
		item.ID != "" && utf8.RuneCountInString(item.ID) <= maxIDRunes &&
		utf8.RuneCountInString(item.Name) <= maxNameRunes &&
		!strings.ContainsAny(item.Kind, "\r\n") && !strings.ContainsAny(item.ID, "\r\n") && !strings.ContainsAny(item.Name, "\r\n")
}

// Valid reports whether the whole snapshot is within bounds. Client-provided
// screen hints never authorize a read or mutation: every resource tool must
// still check the authenticated member.
func (screen ScreenContext) Valid() bool {
	if !screenViewPattern.MatchString(screen.View) || len(screen.VisibleItems) > maxScreenItems {
		return false
	}
	if screen.Selection != nil && !screen.Selection.valid() {
		return false
	}
	for _, item := range screen.VisibleItems {
		if !item.valid() {
			return false
		}
	}
	return true
}

// Sanitize keeps what is usable of a snapshot the browser sent, and counts
// what it leaves out. It never fails: a snapshot only points the agent at
// things on the page, so a hint that breaks a rule (an item with no kind or
// id, an oversized one, one with a line break, one past the twelfth, a
// selection that is none of these) is dropped alone, and the rest still
// reach the run. A view that names no Oryxa page drops the whole snapshot,
// and Sanitize returns nil. The result is always Valid, and screen is not
// changed.
func (screen *ScreenContext) Sanitize() (*ScreenContext, int) {
	if screen == nil {
		return nil, 0
	}
	hints := len(screen.VisibleItems)
	if screen.Selection != nil {
		hints++
	}
	if !screenViewPattern.MatchString(screen.View) {
		// Everything on it went with the view, and the view itself was one.
		return nil, hints + 1
	}
	cleaned := &ScreenContext{View: screen.View, VisibleItems: []ScreenItem{}}
	if screen.Selection != nil && screen.Selection.valid() {
		selection := *screen.Selection
		cleaned.Selection = &selection
	}
	for _, item := range screen.VisibleItems {
		if item.valid() && len(cleaned.VisibleItems) < maxScreenItems {
			cleaned.VisibleItems = append(cleaned.VisibleItems, item)
		}
	}
	kept := len(cleaned.VisibleItems)
	if cleaned.Selection != nil {
		kept++
	}
	return cleaned, hints - kept
}

// EncodedScreenContext is placed in the run's state delta. An absent context
// becomes an empty value, so no earlier page can linger in a session service
// that keeps temporary state past its run.
func EncodedScreenContext(screen *ScreenContext) string {
	if screen == nil {
		return ""
	}
	encoded, _ := json.Marshal(screen)
	return string(encoded)
}

type currentScreenResult struct {
	Available    bool         `json:"available"`
	View         string       `json:"view,omitempty"`
	Selection    *ScreenItem  `json:"selection,omitempty"`
	VisibleItems []ScreenItem `json:"visible_items,omitempty"`
}

// currentScreenTool describes the Oryxa view at the time the member sent the
// run. The browser supplies these hints; the tool does not read arbitrary
// pixels, other windows, or hidden page content.
func currentScreenTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "get_current_screen",
		Description: "Returns the member's current Oryxa view, selected item, and up to twelve visible items captured when they sent this request. Use when they refer to 'this', 'here', or what is on their screen. These are navigation hints, not proof of access; use the appropriate member-scoped tool to read an item.",
	}, func(ctx agent.Context, _ struct{}) (currentScreenResult, error) {
		value, err := ctx.ReadonlyState().Get(ScreenStateKey)
		if err != nil {
			return currentScreenResult{}, nil
		}
		encoded, ok := value.(string)
		if !ok || encoded == "" {
			return currentScreenResult{}, nil
		}
		var screen ScreenContext
		if json.Unmarshal([]byte(encoded), &screen) != nil || !screen.Valid() {
			return currentScreenResult{}, nil
		}
		return currentScreenResult{Available: true, View: screen.View, Selection: screen.Selection, VisibleItems: screen.VisibleItems}, nil
	})
}
