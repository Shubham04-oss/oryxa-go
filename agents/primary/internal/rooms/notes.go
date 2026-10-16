package rooms

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// The most parley notes may hold. A call worth more than this is better
// summarized than transcribed.
const (
	maxNoteItems = 50
	maxNoteRunes = 500
)

// ParleyNotes is what one parley in a teamspace's room left behind: the
// call, how long it ran, what was decided and who owes what. The edge's voice
// agent sends them when the call ends, and the organization agent posts them
// in the room as one KindParleyNotes message whose Data they are.
type ParleyNotes struct {
	CallID     string         `json:"call_id"`
	DurationMs int64          `json:"duration_ms"`
	Decisions  []string       `json:"decisions"`
	Actions    []ParleyAction `json:"actions"`
}

// ParleyAction is one thing somebody took away from a parley.
type ParleyAction struct {
	Text string `json:"text"`
	// OwnerUserID is who took it, as the call knew them; empty for nobody in
	// particular. It is the edge's word, not checked against the team, so a
	// client shows an owner it cannot seat as nobody.
	OwnerUserID string `json:"owner_user_id,omitempty"`
}

// PostParleyNotes posts notes in roomID, a teamspace of organizationID, as
// the organization agent, and returns the message. Notes for a call that
// already has them are not posted twice: the first message comes back, with
// created false. It answers to the edge, never to a member, so it checks no
// membership: the caller has already bound the organization and the room to
// the edge's token.
func (service *Service) PostParleyNotes(ctx context.Context, organizationID, roomID string, notes ParleyNotes) (Message, bool, error) {
	notes = cleanParleyNotes(notes)
	if err := validateParleyNotes(notes); err != nil {
		return Message{}, false, err
	}
	message, created, err := service.store.AppendNotes(ctx, organizationID, strings.TrimSpace(roomID), notes, notesBody(notes))
	if err != nil {
		return Message{}, false, err
	}
	if created {
		service.logger.InfoContext(ctx, "posted a parley's notes in its room",
			"room_id", message.RoomID, "seq", message.Seq, "decisions", len(notes.Decisions), "actions", len(notes.Actions))
	}
	return message, created, nil
}

func cleanParleyNotes(notes ParleyNotes) ParleyNotes {
	notes.CallID = strings.TrimSpace(notes.CallID)
	decisions := make([]string, 0, len(notes.Decisions))
	for _, decision := range notes.Decisions {
		if decision = oneLine(decision); decision != "" {
			decisions = append(decisions, decision)
		}
	}
	notes.Decisions = decisions
	actions := make([]ParleyAction, 0, len(notes.Actions))
	for _, action := range notes.Actions {
		action.Text = oneLine(action.Text)
		action.OwnerUserID = strings.TrimSpace(action.OwnerUserID)
		if action.Text != "" {
			actions = append(actions, action)
		}
	}
	notes.Actions = actions
	return notes
}

func validateParleyNotes(notes ParleyNotes) error {
	switch {
	case !edgev1.ValidID(notes.CallID):
		return fmt.Errorf("%w: call_id is required, in letters, digits, - and _", ErrInvalid)
	case notes.DurationMs < 0:
		return fmt.Errorf("%w: duration_ms must be zero or more", ErrInvalid)
	case len(notes.Decisions) > maxNoteItems:
		return fmt.Errorf("%w: decisions holds at most %d entries", ErrInvalid, maxNoteItems)
	case len(notes.Actions) > maxNoteItems:
		return fmt.Errorf("%w: actions holds at most %d entries", ErrInvalid, maxNoteItems)
	}
	for _, decision := range notes.Decisions {
		if utf8.RuneCountInString(decision) > maxNoteRunes {
			return fmt.Errorf("%w: each decision holds at most %d characters", ErrInvalid, maxNoteRunes)
		}
	}
	for _, action := range notes.Actions {
		if utf8.RuneCountInString(action.Text) > maxNoteRunes {
			return fmt.Errorf("%w: each action holds at most %d characters", ErrInvalid, maxNoteRunes)
		}
		if action.OwnerUserID != "" && !edgev1.ValidID(action.OwnerUserID) {
			return fmt.Errorf("%w: an action's owner_user_id is a user id", ErrInvalid)
		}
	}
	return nil
}

// notesBody is notes in words: what the room shows a reader that does not
// draw the notes card, and what an agent reading the room as text reads. It
// never exceeds what a room message may hold.
func notesBody(notes ParleyNotes) string {
	var body strings.Builder
	body.WriteString("Parley notes (" + callLength(time.Duration(notes.DurationMs)*time.Millisecond) + ")")
	if len(notes.Decisions) == 0 && len(notes.Actions) == 0 {
		body.WriteString("\nNothing was decided and nobody took anything away.")
	}
	if len(notes.Decisions) > 0 {
		body.WriteString("\nDecided:")
		for _, decision := range notes.Decisions {
			body.WriteString("\n- " + decision)
		}
	}
	if len(notes.Actions) > 0 {
		body.WriteString("\nActions:")
		for _, action := range notes.Actions {
			body.WriteString("\n- " + action.Text)
		}
	}
	text := body.String()
	if runes := []rune(text); len(runes) > maxBody {
		text = string(runes[:maxBody-1]) + "…"
	}
	return text
}

// callLength is how long a call ran, as a person would say it.
func callLength(length time.Duration) string {
	minutes := int(length.Round(time.Minute) / time.Minute)
	switch {
	case length < time.Minute:
		return "under a minute"
	case minutes == 1:
		return "1 min"
	default:
		return fmt.Sprintf("%d min", minutes)
	}
}

func oneLine(text string) string { return strings.Join(strings.Fields(text), " ") }
