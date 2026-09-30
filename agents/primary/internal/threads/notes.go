package threads

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/adksession"
	"github.com/Shubham04-oss/oryxa-go/internal/cleantext"
)

// A note is a line Oryxa writes in a member's own agent chat without a model:
// "Asked Raya's agent in #launch · answered". It is how a member who asked a
// question in Slack hears, in Oryxa, how it went. Their agent is not woken to
// say it (that would be a model call spent on words the thread in Slack
// already holds), and the words are the caller's own, chosen from a small set
// of states and the names the request already carries.
//
// A note lives in the member's Requests thread, one for each organization,
// which is made the first time something is noted in it. It is an event with no
// content and only metadata, so it appears in the thread's history (as a
// request item) and never in what a model is shown or what Krude remembers:
// ADK builds a model's context from events that carry content, and the memory
// writer reads the same.

// RequestsTitle is the title of a member's Requests thread.
const RequestsTitle = "Requests"

// AuthorOryxa is who a note is from: not the member and not their agent.
const AuthorOryxa = "oryxa"

// The states a note says a request is in. They are a closed set so that a
// client can pick a mark for each.
const (
	// NoteAsked says the member asked and the request waits for its owner.
	NoteAsked = "asked"
	// NoteAnswered says the owner's agent answered.
	NoteAnswered = "answered"
	// NoteHeld says an answer is held for its owner's review.
	NoteHeld = "held"
	// NoteDeclined says the owner did not share, or withheld the answer.
	NoteDeclined = "declined"
	// NoteClosed says the request ended without an answer: it expired, or the
	// work could not be done.
	NoteClosed = "closed"
)

const (
	// noteMetadataKey is where a note keeps what it says, in its event's custom
	// metadata.
	noteMetadataKey = "oryxa.request_note"
	// noteInvocation stands in for the run id every ADK event carries: a note
	// is not the work of a run.
	noteInvocation = "oryxa-note"
	// maxNoteRunes bounds the line a note says.
	maxNoteRunes = 400
	// maxNoteAttempts bounds how often a note is appended again after ADK
	// refuses it. What it refuses is nearly always a write to the thread by
	// someone else a moment earlier, which a fresh read of the thread absorbs.
	maxNoteAttempts = 4
)

// Note is one line about a request, for a member's Requests thread.
type Note struct {
	// RequestID is the request the line is about. The client shows its card
	// from it, live, so a line never has to be right about how the request
	// stands now.
	RequestID string
	// State is how the request stood when the line was written: one of the
	// Note constants.
	State string
	// Text is the line itself. It should carry no more than the member already
	// knows: who they asked, and where.
	Text string
}

// noteBody is a note as its event keeps it.
type noteBody struct {
	RequestID string `json:"request_id"`
	State     string `json:"state"`
	Text      string `json:"text"`
}

func validNoteState(state string) bool {
	switch state {
	case NoteAsked, NoteAnswered, NoteHeld, NoteDeclined, NoteClosed:
		return true
	}
	return false
}

func (note Note) validate() error {
	switch {
	case strings.TrimSpace(note.RequestID) == "" || utf8.RuneCountInString(note.RequestID) > 128 || !cleantext.Valid(note.RequestID):
		return fmt.Errorf("%w: a note names the request it is about, in at most 128 characters", ErrInvalid)
	case !validNoteState(note.State):
		return fmt.Errorf("%w: a note's state is asked, answered, held, declined or closed", ErrInvalid)
	case strings.TrimSpace(note.Text) == "" || utf8.RuneCountInString(note.Text) > maxNoteRunes || !cleantext.Valid(note.Text):
		return fmt.Errorf("%w: a note says one line of at most %d characters", ErrInvalid, maxNoteRunes)
	}
	return nil
}

// RequestsThreadID is the id of the member's Requests thread in an
// organization. It is fixed so that two notes written at once find the same
// thread, and one per organization since a member's threads are their own in
// each.
func RequestsThreadID(organizationID string) string {
	return "requests-" + organizationID
}

// noteEventID names the event that holds a note: the same request in the same
// state is one event, so writing a note again is finding it there.
func noteEventID(requestID, state string) string {
	sum := sha256.Sum256([]byte(requestID + "\x00" + state))
	return "note-" + hex.EncodeToString(sum[:16])
}

// Note appends a line about a request to the member's Requests thread,
// making the thread when they have none. It calls no model, starts no run and
// leaves the thread as it is for the next one.
//
// A request in a given state has one note: writing it again, as a retried
// caller does, changes nothing. A member who deleted the thread gets a new one
// with this note in it. Anything that fails is the caller's to try again.
func (service *Service) Note(ctx context.Context, member Member, note Note) error {
	if strings.TrimSpace(member.UserID) == "" || strings.TrimSpace(member.OrganizationID) == "" {
		return fmt.Errorf("%w: a note is written for a member of an organization", ErrInvalid)
	}
	note.Text = strings.Join(strings.Fields(note.Text), " ")
	if err := note.validate(); err != nil {
		return err
	}
	eventID := noteEventID(note.RequestID, note.State)
	var last error
	for attempt := range maxNoteAttempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 20 * time.Millisecond):
			}
		}
		stored, err := service.requestsThread(ctx, member)
		if err != nil {
			last = err
			continue
		}
		if hasEvent(stored, eventID) {
			return nil
		}
		event := session.NewEvent(ctx, noteInvocation)
		event.ID = eventID
		event.Author = AuthorOryxa
		event.CustomMetadata = map[string]any{noteMetadataKey: map[string]any{
			"request_id": note.RequestID, "state": note.State, "text": note.Text,
		}}
		if err := service.sessions.AppendEvent(ctx, stored, event); err != nil {
			// Appended by the attempt that failed after storing it, or by another
			// caller writing the same note: the note is there either way.
			if again, getErr := service.thread(ctx, member, RequestsThreadID(member.OrganizationID)); getErr == nil && hasEvent(again, eventID) {
				return nil
			}
			last = fmt.Errorf("append a note: %w", err)
			continue
		}
		return nil
	}
	return last
}

// requestsThread reads the member's Requests thread, and makes it when there is
// none. Two callers making it at once settle on one: the id is fixed, so the
// second's creation is refused and it reads the first's.
func (service *Service) requestsThread(ctx context.Context, member Member) (session.Session, error) {
	id := RequestsThreadID(member.OrganizationID)
	stored, err := service.thread(ctx, member, id)
	if err == nil {
		return stored, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return nil, err
	}
	state := map[string]any{
		adksession.StateOrganizationID: member.OrganizationID,
		stateCreatedAt:                 time.Now().UTC().Format(time.RFC3339Nano),
		stateOrigin:                    OriginChat,
		stateTitle:                     RequestsTitle,
	}
	for key, value := range service.initialState {
		state[key] = value
	}
	for key, value := range memberAgentState(member) {
		state[key] = value
	}
	created, createErr := service.sessions.Create(ctx, &session.CreateRequest{
		AppName: service.appName, UserID: member.UserID, SessionID: id, State: state,
	})
	if createErr == nil && created != nil && created.Session != nil {
		return created.Session, nil
	}
	if stored, err := service.thread(ctx, member, id); err == nil {
		return stored, nil
	}
	if createErr == nil {
		createErr = errors.New("the session service returned no session")
	}
	return nil, fmt.Errorf("create the requests thread: %w", createErr)
}

// hasEvent reports whether the session holds the event.
func hasEvent(stored session.Session, id string) bool {
	events := stored.Events()
	if events == nil {
		return false
	}
	for event := range events.All() {
		if event != nil && event.ID == id {
			return true
		}
	}
	return false
}

// noteOf reads the note an event holds, if it holds one.
func noteOf(event *session.Event) (noteBody, bool) {
	raw, ok := event.CustomMetadata[noteMetadataKey]
	if !ok {
		return noteBody{}, false
	}
	var body noteBody
	if !decodeInto(raw, &body) || body.RequestID == "" || !validNoteState(body.State) {
		return noteBody{}, false
	}
	return body, true
}
