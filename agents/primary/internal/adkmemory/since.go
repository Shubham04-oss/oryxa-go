package adkmemory

import (
	"iter"
	"time"

	"google.golang.org/adk/v2/session"
)

// Since returns a view of conversation holding only what happened after
// mark, for handing one turn to memory rather than the whole thread again.
//
// Memory Bank consolidates what it is given with what it already holds, so
// re-sending a thread from the beginning after every run is not wrong, but
// it re-reads the same turns with a model each time and costs more the
// longer the conversation gets. Its own session source takes a start time
// for exactly this reason; a conversation sent as its contents has to be cut
// down here instead.
//
// A zero mark keeps everything. The view is a snapshot: what is appended to
// conversation afterwards is not in it.
func Since(conversation session.Session, mark time.Time) session.Session {
	if conversation == nil {
		return nil
	}
	var kept []*session.Event
	for event := range conversation.Events().All() {
		if event == nil {
			continue
		}
		if !mark.IsZero() && !event.Timestamp.After(mark) {
			continue
		}
		kept = append(kept, event)
	}
	return &sinceSession{Session: conversation, events: kept}
}

// sinceSession is a conversation with a shorter history. Everything else
// about it — who it belongs to, its state, when it was last touched — is the
// conversation's own.
type sinceSession struct {
	session.Session
	events []*session.Event
}

func (s *sinceSession) Events() session.Events { return sinceEvents(s.events) }

// sinceEvents is the kept events as [session.Events].
type sinceEvents []*session.Event

func (e sinceEvents) Len() int { return len(e) }

func (e sinceEvents) At(i int) *session.Event {
	if i < 0 || i >= len(e) {
		return nil
	}
	return e[i]
}

func (e sinceEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for _, event := range e {
			if !yield(event) {
				return
			}
		}
	}
}
