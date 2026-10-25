package rooms

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// TestSeatsNamedTogetherRunTogether: "@a @b" starts both turns at once. Each
// waits here for the other to start, so a relay that ran them one after the
// other would leave the first waiting for a seat that cannot start until it
// is done.
func TestSeatsNamedTogetherRunTogether(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	a := relayAgentSeat("a", "user-a", "agent-a")
	b := relayAgentSeat("b", "user-b", "agent-b")

	var arrived sync.WaitGroup
	arrived.Add(2)
	both := make(chan struct{})
	go func() { arrived.Wait(); close(both) }()
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		arrived.Done()
		select {
		case <-both:
			return "on it, from " + seat.Handle, nil
		case <-time.After(2 * time.Second):
			return "", fmt.Errorf("%s waited for a seat that never started", seat.Handle)
		}
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, a, b}}, relayMessage("room-1", bo, "@a @b can you both look?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[1].Body != "on it, from a" || results[2].Body != "on it, from b" {
		t.Fatalf("got %+v, want the post, then a's answer, then b's", results)
	}
}

// TestASlowSeatDoesNotCostTheNextItsTurn is the bug that was reported: in
// "@a @b", b never answered when a's turn took longer than the relay's whole
// budget, which was only looked at between turns. Here each takes longer than
// a Post's whole budget, and both answer.
func TestASlowSeatDoesNotCostTheNextItsTurn(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	a := relayAgentSeat("a", "user-a", "agent-a")
	b := relayAgentSeat("b", "user-b", "agent-b")
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		time.Sleep(100 * time.Millisecond)
		return "here is " + seat.Handle, nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker, Budget: 20 * time.Millisecond, TurnBudget: 5 * time.Second}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, a, b}}, relayMessage("room-1", bo, "@a @b what do you think?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[1].Body != "here is a" || results[2].Body != "here is b" {
		t.Fatalf("got %+v, want both seats' answers", results)
	}
}

// TestAnswersAreStoredInTheOrderTheSeatsWereNamed: however the turns finish.
// Here b is done before a can finish, and a is still stored first.
func TestAnswersAreStoredInTheOrderTheSeatsWereNamed(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	a := relayAgentSeat("a", "user-a", "agent-a")
	b := relayAgentSeat("b", "user-b", "agent-b")
	bDone := make(chan struct{})
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "b" {
			close(bDone)
			return "b, quickly", nil
		}
		select {
		case <-bDone:
			return "a, eventually", nil
		case <-time.After(2 * time.Second):
			return "", errors.New("a waited for a seat that never started")
		}
	}}
	store := &relayFakeStore{}
	relay := &Relay{Store: store, Speaker: speaker}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, a, b}}, relayMessage("room-1", bo, "@a @b thoughts?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 || results[1].AuthorHandle != "a" || results[2].AuthorHandle != "b" {
		t.Fatalf("got %+v, want a's answer stored before b's", results)
	}
	for index, message := range store.messages {
		if message.Seq != int64(index+1) || message.Body != results[index].Body {
			t.Errorf("stored message %d = %+v, want what Post returned, in the same order", index, message)
		}
	}
}

// TestASeatThatCannotAnswerSaysSoInTheRoom: a seat asked to answer that
// cannot gives the room one line naming it, with a plain reason and nothing of
// what went wrong, in reply to the message that asked it, while the seat next
// to it answers as usual.
func TestASeatThatCannotAnswerSaysSoInTheRoom(t *testing.T) {
	cases := []struct {
		name  string
		speak func(ctx context.Context) (string, error)
		want  string
	}{
		{
			"its turn failed",
			func(context.Context) (string, error) {
				return "", errors.New("vertex: 429 quota exceeded for project acme-prod")
			},
			"mira couldn't answer right now.",
		},
		{
			"it said nothing at all",
			func(context.Context) (string, error) { return " \n", nil },
			"mira couldn't answer right now.",
		},
		{
			"its turn panicked",
			func(context.Context) (string, error) { panic("boom: acme-prod credentials") },
			"mira couldn't answer right now.",
		},
		{
			"its thread waits for its owner",
			func(context.Context) (string, error) {
				return "", fmt.Errorf("start the turn: %w", threads.ErrConfirmationPending)
			},
			"mira is waiting on its owner's confirmation, so it can't answer yet.",
		},
		{
			"its thread stays busy with another message",
			func(context.Context) (string, error) { return "", threads.ErrRunInProgress },
			"mira is busy answering something else, so it couldn't answer this.",
		},
		{
			"it runs out of its turn's budget",
			func(ctx context.Context) (string, error) { <-ctx.Done(); return "", ctx.Err() },
			"mira is taking too long to answer, so the room stopped waiting.",
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			bo := relayPersonSeat("bo", "user-bo", true)
			mira := relayAgentSeat("mira", "user-mira", "agt_mira")
			cy := relayAgentSeat("cy", "user-cy", "agt_cy")
			store := &relayFakeStore{}
			speaker := &turnsScriptedSpeaker{turns: map[string]func(context.Context) (string, error){
				"mira": test.speak,
				"cy":   func(context.Context) (string, error) { return "Northwind was agreed at 4.50.", nil },
			}}
			relay := &Relay{Store: store, Speaker: speaker, TurnBudget: 100 * time.Millisecond}

			results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, mira, cy}}, relayMessage("room-1", bo, "@mira @cy what did we agree?"), Verdict{})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 3 {
				t.Fatalf("got %d messages, want 3 (the post, mira's line, cy's answer): %+v", len(results), results)
			}
			line := results[1]
			if line.Body != test.want {
				t.Errorf("mira's line = %q, want %q", line.Body, test.want)
			}
			if line.AuthorHandle != "mira" || line.AuthorKind != SeatAgent || line.AuthorUserID != "user-mira" || line.AuthorAgentID != "agt_mira" {
				t.Errorf("the line is by %+v, want mira's seat", line)
			}
			if line.InReplyToSeq != results[0].Seq || len(line.AddressedTo) != 0 {
				t.Errorf("the line answers seq %d addressing %v, want it a reply to the post that names nobody", line.InReplyToSeq, line.AddressedTo)
			}
			for _, leaked := range []string{"429", "quota", "acme-prod", "boom", "credentials", "context"} {
				if strings.Contains(line.Body, leaked) {
					t.Errorf("the line %q says %q of what went wrong", line.Body, leaked)
				}
			}
			if results[2].AuthorHandle != "cy" || results[2].Body != "Northwind was agreed at 4.50." {
				t.Errorf("cy's answer = %+v, want it heard all the same", results[2])
			}
			if len(store.messages) != 3 || store.messages[1].Body != test.want {
				t.Errorf("stored %+v, want the line stored like any other message", store.messages)
			}
		})
	}
}

// turnsScriptedSpeaker gives each seat the turn it is scripted, and counts
// how many turns it started.
type turnsScriptedSpeaker struct {
	turns map[string]func(context.Context) (string, error)
	mu    sync.Mutex
	asked map[string]int
}

func (speaker *turnsScriptedSpeaker) Speak(ctx context.Context, seat Seat, _ Situation, _ []Message, _ Summons) (string, error) {
	speaker.mu.Lock()
	if speaker.asked == nil {
		speaker.asked = map[string]int{}
	}
	speaker.asked[seat.Handle]++
	speaker.mu.Unlock()
	return speaker.turns[seat.Handle](ctx)
}

func (speaker *turnsScriptedSpeaker) timesAsked(handle string) int {
	speaker.mu.Lock()
	defer speaker.mu.Unlock()
	return speaker.asked[handle]
}

// TestTheGuardReadsWhatASeatSaysWhenItCannotAnswer: nothing an agent says in
// a room skips the guard, its lines saying it has no answer included. A line
// the guard could not read is still said as it is, since it holds nothing to
// find, and is not turned into a claim that an answer is being held.
func TestTheGuardReadsWhatASeatSaysWhenItCannotAnswer(t *testing.T) {
	const line = "mira couldn't answer right now."
	bo := relayPersonSeat("bo", "user-bo", true)
	mira := relayAgentSeat("mira", "user-mira", "agt_mira")
	broken := func(context.Context) (string, error) { return "", errors.New("down") }

	for name, verdict := range map[string]answerguard.Verdict{
		"the guard finds nothing": {State: answerguard.StateClear, Texts: []string{line}},
		"the guard cannot check":  {State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: []string{line}},
	} {
		t.Run(name, func(t *testing.T) {
			guard := &guardFinding{found: map[string]answerguard.Verdict{line: verdict}}
			relay := &Relay{
				Store:   &relayFakeStore{},
				Speaker: &turnsScriptedSpeaker{turns: map[string]func(context.Context) (string, error){"mira": broken}},
				Guard:   guard,
			}
			results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, mira}}, relayMessage("room-1", bo, "@mira?"), Verdict{})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 || results[1].Body != line {
				t.Fatalf("got %+v, want mira's line as it is", results)
			}
			if !slices.Contains(guard.read, line) {
				t.Errorf("the guard read %q, want it to read the line", guard.read)
			}
		})
	}
}

// TestAPendingConfirmationDoesNotMuteASeatForGood: a seat whose thread is
// waiting on its owner's confirmation cannot take a turn. Every message that
// names it is told so, rather than met with silence, and it answers again
// once its owner has answered.
func TestAPendingConfirmationDoesNotMuteASeatForGood(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	mira := relayAgentSeat("mira", "user-mira", "agt_mira")
	var waiting atomic.Bool
	waiting.Store(true)
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		if waiting.Load() {
			return "", threads.ErrConfirmationPending
		}
		return "Northwind was agreed at 4.50.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}
	room := Situation{Seats: []Seat{bo, mira}}
	const waitingLine = "mira is waiting on its owner's confirmation, so it can't answer yet."

	for attempt := 1; attempt <= 2; attempt++ {
		results, err := relay.Post(context.Background(), room, relayMessage("room-1", bo, "@mira what did we agree?"), Verdict{})
		if err != nil {
			t.Fatal(err)
		}
		if len(results) != 2 || results[1].Body != waitingLine {
			t.Fatalf("while waiting, post %d: got %+v, want mira's line saying so", attempt, results)
		}
	}

	waiting.Store(false)
	results, err := relay.Post(context.Background(), room, relayMessage("room-1", bo, "@mira and now?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].Body != "Northwind was agreed at 4.50." {
		t.Fatalf("once answered, got %+v, want mira answering again", results)
	}
}

// TestASeatBusyWithAnotherMessageIsAskedAgain: two people asking the same
// seat at once is not a failure to report. The seat's thread is free again
// within its turn, so the second is asked again until it is.
func TestASeatBusyWithAnotherMessageIsAskedAgain(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	mira := relayAgentSeat("mira", "user-mira", "agt_mira")
	var calls atomic.Int32
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		if calls.Add(1) <= 2 {
			return "", threads.ErrRunInProgress
		}
		return "Free now: Northwind was agreed at 4.50.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker, TurnBudget: 500 * time.Millisecond}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, mira}}, relayMessage("room-1", bo, "@mira what did we agree?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].Body != "Free now: Northwind was agreed at 4.50." {
		t.Fatalf("got %+v, want mira's answer once her thread was free", results)
	}
	if got := speaker.callCount(); got != 3 {
		t.Errorf("Speak called %d times, want 3 (busy twice, then the answer)", got)
	}
}

// TestSeatsThatPassDoNotStopASeatNamedAfterThem: the relay ends after two
// passes in a row, but a seat that has already run has its answer heard.
func TestSeatsThatPassDoNotStopASeatNamedAfterThem(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	seats := []Seat{bo, relayAgentSeat("a", "user-a", "agent-a"), relayAgentSeat("b", "user-b", "agent-b"), relayAgentSeat("c", "user-c", "agent-c")}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "c" {
			return "I know this one.", nil
		}
		return Pass, nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}

	results, err := relay.Post(context.Background(), Situation{Seats: seats}, relayMessage("room-1", bo, "@a @b @c anybody?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].AuthorHandle != "c" {
		t.Fatalf("got %+v, want c's answer after a and b passed", results)
	}
}

// TestWhatASeatsAnswerCausesOnlyStartsWhileAWholeTurnFits: the seats a person
// named always run, but the seat their answer goes on to name only gets a
// turn when a whole turn's budget still fits in the Post's. Otherwise the
// chain ends there, without a line: nobody asked that seat.
func TestWhatASeatsAnswerCausesOnlyStartsWhileAWholeTurnFits(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	a := relayAgentSeat("a", "user-a", "agent-a")
	b := relayAgentSeat("b", "user-b", "agent-b")
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		if seat.Handle == "a" {
			return "@b, over to you.", nil
		}
		return "b here.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker, Budget: 20 * time.Millisecond, TurnBudget: 5 * time.Second}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, a, b}}, relayMessage("room-1", bo, "@a please start"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].AuthorHandle != "a" {
		t.Fatalf("got %+v, want a's answer and nothing after it", results)
	}
	if got := speaker.callCount(); got != 1 {
		t.Errorf("Speak called %d times, want only a's", got)
	}
}

// TestASeatNamedPastTheTurnsAMessageWakesSaysSo: a person who names more
// agents than one message may wake gets a line from each that had no turn,
// not silence from it.
func TestASeatNamedPastTheTurnsAMessageWakesSaysSo(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	seats := []Seat{bo}
	var names []string
	for index := 1; index <= maxAgentTurns+2; index++ {
		handle := fmt.Sprintf("a%d", index)
		seats = append(seats, relayAgentSeat(handle, "user-"+handle, "agent-"+handle))
		names = append(names, "@"+handle)
	}
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		return seat.Handle + " here.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}

	results, err := relay.Post(context.Background(), Situation{Seats: seats}, relayMessage("room-1", bo, strings.Join(names, " ")+" all of you?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1+maxAgentTurns+2 {
		t.Fatalf("got %d messages, want the post, %d answers and 2 lines: %+v", len(results), maxAgentTurns, results)
	}
	for index := 1; index <= maxAgentTurns; index++ {
		if want := fmt.Sprintf("a%d here.", index); results[index].Body != want {
			t.Errorf("message %d = %q, want %q", index, results[index].Body, want)
		}
	}
	for index, handle := range []string{"a5", "a6"} {
		line := results[1+maxAgentTurns+index]
		if want := fmt.Sprintf("%s wasn't given a turn: one message wakes at most %d agents.", handle, maxAgentTurns); line.AuthorHandle != handle || line.Body != want {
			t.Errorf("line for %s = %+v, want %q", handle, line, want)
		}
	}
	if got := speaker.callCount(); got != maxAgentTurns {
		t.Errorf("Speak called %d times, want %d", got, maxAgentTurns)
	}
}

// TestASeatNamedTwiceAnswersOnce: naming an absent person and their own
// agent names one seat twice, and one seat answering twice at once would only
// collide with itself.
func TestASeatNamedTwiceAnswersOnce(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	dan := relayPersonSeat("dan", "user-dan", false)
	danAgent := relayAgentSeat("dan-agent", "user-dan", "agent-dan")
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		return "Ready.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}

	results, err := relay.Post(context.Background(), Situation{Seats: []Seat{bo, dan, danAgent}}, relayMessage("room-1", bo, "@dan @dan-agent is it ready?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || speaker.callCount() != 1 {
		t.Fatalf("got %+v after %d turns, want dan's agent answering once", results, speaker.callCount())
	}
}

// turnsLiveStore refuses what a request that has gone away would store.
type turnsLiveStore struct{ relayFakeStore }

func (store *turnsLiveStore) Append(ctx context.Context, message Message) (Message, error) {
	if err := ctx.Err(); err != nil {
		return Message{}, err
	}
	return store.relayFakeStore.Append(ctx, message)
}

// TestARequestThatHasGoneAwayStillLeavesTheLine: a seat's turn is often cut
// short because the request that wanted it is gone. The room is owed being
// told all the same, and nothing more is started for it.
func TestARequestThatHasGoneAwayStillLeavesTheLine(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	mira := relayAgentSeat("mira", "user-mira", "agt_mira")
	ctx, gone := context.WithCancel(context.Background())
	defer gone()
	speaker := &relayFakeSpeaker{reply: func(Seat, Situation, []Message) (string, error) {
		gone()
		return "", context.Canceled
	}}
	store := &turnsLiveStore{}
	relay := &Relay{Store: store, Speaker: speaker}

	results, err := relay.Post(ctx, Situation{Seats: []Seat{bo, mira}}, relayMessage("room-1", bo, "@mira what did we agree?"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || results[1].Body != "mira couldn't answer right now." {
		t.Fatalf("got %+v, want the post and mira's line", results)
	}
}

// TestNothingMoreStartsForARequestThatHasGoneAway: a's answer names b, but
// the request is gone by the time it is heard, so b is never asked.
func TestNothingMoreStartsForARequestThatHasGoneAway(t *testing.T) {
	bo := relayPersonSeat("bo", "user-bo", true)
	a := relayAgentSeat("a", "user-a", "agent-a")
	b := relayAgentSeat("b", "user-b", "agent-b")
	ctx, gone := context.WithCancel(context.Background())
	defer gone()
	speaker := &relayFakeSpeaker{reply: func(seat Seat, _ Situation, _ []Message) (string, error) {
		gone()
		return "@b, over to you.", nil
	}}
	relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker}

	results, err := relay.Post(ctx, Situation{Seats: []Seat{bo, a, b}}, relayMessage("room-1", bo, "@a please start"), Verdict{})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 || speaker.callCount() != 1 {
		t.Fatalf("got %+v after %d turns, want a's answer and no turn for b", results, speaker.callCount())
	}
}

// turnsFakeRuns starts a seat's run however start says.
type turnsFakeRuns struct {
	start func(ctx context.Context) (managedagents.StartedRun, error)
}

func (runs turnsFakeRuns) StartUnattendedRun(ctx context.Context, _ threads.Member, _, _ string, _ threads.RunInput, _ managedagents.UnattendedProfile) (managedagents.StartedRun, error) {
	return runs.start(ctx)
}

// turnsStuckRun says nothing and ends only when its context does.
type turnsStuckRun struct{}

func (turnsStuckRun) ID() string { return "run_stuck" }

func (turnsStuckRun) Execute(ctx context.Context, _ func(threads.Item) error) { <-ctx.Done() }

func (turnsStuckRun) Discard(context.Context, string) {}

// TestWhatTheRoomsOwnSpeakerRunsIntoIsSaidInTheRoom: the relay reads why a
// turn failed off the errors the real speaker returns, so it is checked
// against that speaker and not only against fakes of it.
func TestWhatTheRoomsOwnSpeakerRunsIntoIsSaidInTheRoom(t *testing.T) {
	cases := map[string]struct {
		start func(ctx context.Context) (managedagents.StartedRun, error)
		want  string
	}{
		"the seat's thread waits for its owner": {
			func(context.Context) (managedagents.StartedRun, error) {
				return managedagents.StartedRun{}, threads.ErrConfirmationPending
			},
			"mira is waiting on its owner's confirmation, so it can't answer yet.",
		},
		"the seat's thread is busy": {
			func(context.Context) (managedagents.StartedRun, error) {
				return managedagents.StartedRun{}, threads.ErrRunInProgress
			},
			"mira is busy answering something else, so it couldn't answer this.",
		},
		"the seat's run never finishes": {
			func(context.Context) (managedagents.StartedRun, error) {
				return managedagents.StartedRun{ThreadID: "thread_1", Run: turnsStuckRun{}}, nil
			},
			"mira is taking too long to answer, so the room stopped waiting.",
		},
		"the seat's agent cannot be started": {
			func(context.Context) (managedagents.StartedRun, error) {
				return managedagents.StartedRun{}, errors.New("build the agent for an unwatched run: no key for acme-prod")
			},
			"mira couldn't answer right now.",
		},
	}
	for name, test := range cases {
		t.Run(name, func(t *testing.T) {
			speaker, err := NewAgentSpeaker(turnsFakeRuns{start: test.start}, &speakerFakeThreads{}, 0)
			if err != nil {
				t.Fatal(err)
			}
			bo := relayPersonSeat("bo", "user-bo", true)
			mira := Seat{Handle: "mira", Kind: SeatAgent, UserID: "user-mira", AgentID: "agt_mira", SpeaksFor: "user-mira"}
			relay := &Relay{Store: &relayFakeStore{}, Speaker: speaker, TurnBudget: 100 * time.Millisecond}
			room := Situation{RoomID: "room-1", OrganizationID: "org_1", Name: "Launch Council", Seats: []Seat{bo, mira}}

			results, err := relay.Post(context.Background(), room, relayMessage("room-1", bo, "@mira what did we agree?"), Verdict{})
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 2 || results[1].Body != test.want {
				t.Fatalf("got %+v, want mira's line %q", results, test.want)
			}
		})
	}
}

// TestStatusLinesNameSeatsAsTheRoomShowsThem: a status line names a seat by
// its display name, not by a handle nobody chose ("@arlo-ujvx", T8). Only
// the away person keeps an @mention, which is what tells them.
func TestStatusLinesNameSeatsAsTheRoomShowsThem(t *testing.T) {
	arlo := Seat{Handle: "arlo-ujvx", Kind: SeatAgent, UserID: "user-ada", AgentID: "agt_ada", AgentName: "Arlo", DisplayName: "Arlo"}
	if got, want := unanswered(arlo, turn{err: errSaidNothing}), "Arlo couldn't answer right now."; got != want {
		t.Errorf("unanswered = %q, want %q", got, want)
	}
	if got, want := noTurn(arlo), fmt.Sprintf("Arlo wasn't given a turn: one message wakes at most %d agents.", maxAgentTurns); got != want {
		t.Errorf("noTurn = %q, want %q", got, want)
	}
	room := Situation{Seats: []Seat{{Handle: "dan-2", Kind: SeatPerson, UserID: "user-dan", DisplayName: "Dan Ruiz"}, arlo}}
	if got, want := passedFor(arlo, "dan-2", awayName(room, "dan-2")), "Dan Ruiz is away, and Arlo has nothing to add for them, so this waits for @dan-2."; got != want {
		t.Errorf("passedFor = %q, want %q", got, want)
	}
	// A seat without a display name, or an away person no longer seated,
	// falls back to the handle.
	if got := seatName(Seat{Handle: "krude"}); got != "krude" {
		t.Errorf("seatName = %q, want the handle", got)
	}
	if got := awayName(room, "gone"); got != "gone" {
		t.Errorf("awayName = %q, want the handle", got)
	}
}
