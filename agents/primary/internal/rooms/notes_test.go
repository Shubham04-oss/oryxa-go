package rooms

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// A parley's notes land once per call, as one message a client can draw as
// a card and anybody reading the room as text can still read.
func TestAParleysNotesArePostedOncePerCall(t *testing.T) {
	store := newServiceFakeStore()
	service := aService(t, store, &serviceFakeDecider{}, nil)
	notes := ParleyNotes{
		CallID: "call_1", DurationMs: 754_000,
		Decisions: []string{"  Ship on the 14th ", ""},
		Actions:   []ParleyAction{{Text: "Send the  price list", OwnerUserID: "user-bo"}, {Text: "Book the venue"}},
	}
	posted, created, err := service.PostParleyNotes(context.Background(), "org_1", "tsp_room", notes)
	if err != nil || !created {
		t.Fatalf("post notes = %+v, %v, %v", posted, created, err)
	}
	if posted.Kind != KindParleyNotes || posted.AuthorKind != SeatAgent {
		t.Fatalf("posted = %+v", posted)
	}
	want := "Parley notes (13 min)\nDecided:\n- Ship on the 14th\nActions:\n- Send the price list\n- Book the venue"
	if posted.Body != want {
		t.Errorf("body = %q, want %q", posted.Body, want)
	}
	var data ParleyNotes
	if err := json.Unmarshal(posted.Data, &data); err != nil || data.CallID != "call_1" || len(data.Decisions) != 1 ||
		len(data.Actions) != 2 || data.Actions[0].OwnerUserID != "user-bo" || data.DurationMs != 754_000 {
		t.Fatalf("data = %s, %v", posted.Data, err)
	}

	again, created, err := service.PostParleyNotes(context.Background(), "org_1", "tsp_room", notes)
	if err != nil || created || again.Seq != posted.Seq || len(store.messages) != 1 {
		t.Fatalf("the same call's notes again = %+v, %v, %v; %d messages", again, created, err, len(store.messages))
	}

	spaces, err := service.Activity(context.Background(), Actor{OrganizationID: "org_1", UserID: "user-ada"}, []teamspaces.Space{aRoom()})
	if err != nil || spaces[0].LastMessage == nil || spaces[0].LastMessage.Kind != "parley_notes" || *spaces[0].UnreadCount != 1 {
		t.Fatalf("a teamspace whose last message is a parley's notes = %+v, %v", spaces[0].LastMessage, err)
	}
}

func TestParleyNotesAreRefusedWhatTheyCannotHold(t *testing.T) {
	service := aService(t, newServiceFakeStore(), &serviceFakeDecider{}, nil)
	for name, notes := range map[string]ParleyNotes{
		"no call":                   {},
		"a call id with a /":        {CallID: "call/1"},
		"a negative length":         {CallID: "call_1", DurationMs: -1},
		"too many decisions":        {CallID: "call_1", Decisions: make([]string, 51)},
		"a decision too long":       {CallID: "call_1", Decisions: []string{strings.Repeat("a", 501)}},
		"an owner that isn't an id": {CallID: "call_1", Actions: []ParleyAction{{Text: "Do it", OwnerUserID: "bo@example.com"}}},
	} {
		if name == "too many decisions" {
			for index := range notes.Decisions {
				notes.Decisions[index] = "decided"
			}
		}
		if _, _, err := service.PostParleyNotes(context.Background(), "org_1", "tsp_room", notes); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
}

func TestACallWithNothingDecidedStillLeavesNotes(t *testing.T) {
	if got := notesBody(ParleyNotes{CallID: "call_1", DurationMs: 20_000}); got != "Parley notes (under a minute)\nNothing was decided and nobody took anything away." {
		t.Errorf("body = %q", got)
	}
}
