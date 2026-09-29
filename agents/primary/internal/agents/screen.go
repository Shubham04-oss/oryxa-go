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

// Valid bounds client-provided screen hints. They never authorize a read or
// mutation: every resource tool must still check the authenticated member.
func (screen ScreenContext) Valid() bool {
	if !screenViewPattern.MatchString(screen.View) || len(screen.VisibleItems) > 12 {
		return false
	}
	validItem := func(item ScreenItem) bool {
		return item.Kind != "" && utf8.RuneCountInString(item.Kind) <= 40 && item.ID != "" && utf8.RuneCountInString(item.ID) <= 120 && utf8.RuneCountInString(item.Name) <= 120 &&
			!strings.ContainsAny(item.Kind, "\r\n") && !strings.ContainsAny(item.ID, "\r\n") && !strings.ContainsAny(item.Name, "\r\n")
	}
	if screen.Selection != nil && !validItem(*screen.Selection) {
		return false
	}
	for _, item := range screen.VisibleItems {
		if !validItem(item) {
			return false
		}
	}
	return true
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
