package agents

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScreenContextBoundsAndEncoding(t *testing.T) {
	screen := ScreenContext{
		View:         "team/teamspace",
		Selection:    &ScreenItem{Kind: "teamspace", ID: "space_1", Name: "Launch"},
		VisibleItems: []ScreenItem{{Kind: "person", ID: "user_1", Name: "Ada"}},
	}
	if !screen.Valid() {
		t.Fatal("a bounded Oryxa view should be valid")
	}
	var decoded ScreenContext
	if err := json.Unmarshal([]byte(EncodedScreenContext(&screen)), &decoded); err != nil || decoded.View != screen.View {
		t.Fatalf("encoded screen context did not round trip: %+v, %v", decoded, err)
	}
	if EncodedScreenContext(nil) != "" {
		t.Fatal("a run without screen context must clear the prior snapshot")
	}
	for _, invalid := range []ScreenContext{
		{View: "other/window"},
		{View: screen.View, VisibleItems: make([]ScreenItem, 13)},
		{View: screen.View, Selection: &ScreenItem{Kind: "person", ID: strings.Repeat("x", 121)}},
		{View: screen.View, VisibleItems: []ScreenItem{{Kind: "person", ID: "user_1", Name: "A\nB"}}},
	} {
		if invalid.Valid() {
			t.Fatalf("accepted invalid screen context: %+v", invalid)
		}
	}
}
