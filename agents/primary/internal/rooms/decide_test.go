package rooms

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/jev"
)

// ---- fixtures ----

func decidePersonSeat(handle, userID string, present bool) Seat {
	return Seat{Handle: handle, Kind: SeatPerson, UserID: userID, DisplayName: handle, Present: present}
}

func decideAgentSeat(handle, userID, agentID, role string) Seat {
	return Seat{
		Handle: handle, Kind: SeatAgent, UserID: userID, AgentID: agentID, AgentName: handle,
		DisplayName: handle, Role: role,
	}
}

func decideMessage(body string, seq, throughSeq int64) (Situation, Message) {
	return Situation{Brief: Brief{ThroughSeq: throughSeq}}, Message{Body: body, Seq: seq}
}

// ---------------------------------------------------------------------
// Rules
// ---------------------------------------------------------------------

func TestRulesDecide(t *testing.T) {
	bob := decidePersonSeat("bob", "user-bob", false)
	sales := decideAgentSeat("ada", "user-ada", "agent-ada", "sales")
	salesOwnerPresent := decidePersonSeat("ada-person", "user-ada", true)
	salesOwnerAway := decidePersonSeat("ada-person", "user-ada", false)
	// ownSales is Ada's own agent, which speaks for her; sales above is a
	// specialist she contributed, which speaks for nobody.
	ownSales := sales
	ownSales.SpeaksFor = "user-ada"
	pricing := decideAgentSeat("pricing", "user-ada", "agent-pricing", "pricing")

	cases := []struct {
		name    string
		seats   []Seat
		body    string
		seq     int64
		through int64
		every   int
		want    Verdict
	}{
		{
			name:  "bare thanks is never answered",
			seats: []Seat{bob, sales},
			body:  "thanks!",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered"},
		},
		{
			name:  "emoji-only is never answered",
			seats: []Seat{bob, sales},
			body:  " 🙏🙌 ",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered"},
		},
		{
			name:  "+1 is never answered",
			seats: []Seat{bob, sales},
			body:  "+1",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered"},
		},
		{
			name:  "a question with a free agent seat is answered by that seat",
			seats: []Seat{bob, sales},
			body:  "does anyone have the sales deck?",
			want:  Verdict{Answer: true, Speaker: "ada", Certainty: 1, Model: rulesModel, Why: "asks something, and an agent seat is free to take it"},
		},
		{
			name:  "an away person's own agent is free to take a question",
			seats: []Seat{bob, salesOwnerAway, ownSales},
			body:  "does anyone have the sales deck?",
			want:  Verdict{Answer: true, Speaker: "ada", Certainty: 1, Model: rulesModel, Why: "asks something, and an agent seat is free to take it"},
		},
		{
			name:  "a contributed specialist is preferred to an away person's own agent",
			seats: []Seat{bob, salesOwnerAway, ownSales, pricing},
			body:  "what do we charge for this?",
			want:  Verdict{Answer: true, Speaker: "pricing", Certainty: 1, Model: rulesModel, Why: "asks something, and an agent seat is free to take it"},
		},
		{
			name:  "a contributed specialist is free while the person who contributed it is present",
			seats: []Seat{salesOwnerPresent, ownSales, pricing},
			body:  "what do we charge for this?",
			want:  Verdict{Answer: true, Speaker: "pricing", Certainty: 1, Model: rulesModel, Why: "asks something, and an agent seat is free to take it"},
		},
		{
			name:  "a question with no agent seat at all goes unanswered",
			seats: []Seat{bob},
			body:  "does anyone have the sales deck?",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "asks something, but no agent seat is free"},
		},
		{
			name:  "a question is unanswered while the only agent's own person is present",
			seats: []Seat{salesOwnerPresent, ownSales},
			body:  "does anyone have the sales deck?",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "asks something, but no agent seat is free"},
		},
		{
			name:  "a plain statement is not a question",
			seats: []Seat{bob, sales},
			body:  "the sales deck is in the drive folder",
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "not a question"},
		},
		{
			name: "a plain decision cue is kept without needing a question mark",
			body: "we've decided to ship Monday",
			want: Verdict{Certainty: 1, Model: rulesModel, Why: "not a question", Keep: true, Kind: "decision"},
		},
		{
			name: "a plain commitment cue is kept",
			body: "I'll have the deck ready by Thursday.",
			want: Verdict{Certainty: 1, Model: rulesModel, Why: "not a question", Keep: true, Kind: "commitment"},
		},
		{
			name:  "compact backstop fires once N messages have piled up",
			body:  "ok",
			seq:   10,
			every: 10,
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered", Compact: true},
		},
		{
			name:  "compact backstop does not fire early",
			body:  "ok",
			seq:   9,
			every: 10,
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered"},
		},
		{
			name:  "compact backstop disabled at zero",
			body:  "ok",
			seq:   1000,
			every: 0,
			want:  Verdict{Certainty: 1, Model: rulesModel, Why: "acknowledgement: never answered"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			room, message := decideMessage(tc.body, tc.seq, tc.through)
			room.Seats = tc.seats
			got, err := (Rules{CompactEvery: tc.every}).Decide(context.Background(), room, message)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("Decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Jev: a fake System One server that answers with crafted distributions.
// ---------------------------------------------------------------------

// fakeSystemOne answers every call with the same scripted status and body,
// and records every request it was sent, so a test can both craft what Jev
// "said" and check what it was actually asked.
type fakeSystemOne struct {
	mu       sync.Mutex
	status   int // 0 means 200
	body     string
	requests []map[string]any
}

func (fake *fakeSystemOne) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	raw, _ := io.ReadAll(r.Body)
	var request map[string]any
	_ = json.Unmarshal(raw, &request)
	fake.requests = append(fake.requests, request)
	status := fake.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, fake.body)
}

func (fake *fakeSystemOne) calls() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.requests)
}

func (fake *fakeSystemOne) lastRequest() map[string]any {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return fake.requests[len(fake.requests)-1]
}

// choiceAnswer and noulAnswer render one raw answer for the fake server, in
// the shape TypeSafe's own API sends.
func choiceAnswer(choice string, confidence float64, probabilities map[string]float64) map[string]any {
	return map[string]any{"type": "choice", "choice": choice, "confidence": confidence, "probabilities": probabilities}
}

func noulAnswer(value float64) map[string]any {
	return map[string]any{"type": "noul", "noul": value}
}

// systemOneBody renders a full System One response body from raw answers,
// keyed the same way [qAnswer], [qSpeaker], [qKeep] and [qCompact] are.
func systemOneBody(t *testing.T, model string, answers map[string]any) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]any{
		"model": model, "answers": answers, "usage": map[string]int{"input_tokens": 250},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// testJevDecider points a Jev decider at a fake System One server answering
// with body, falling back to Rules exactly as production wiring would.
func testJevDecider(t *testing.T, status int, body string) Jev {
	t.Helper()
	server := httptest.NewServer(&fakeSystemOne{status: status, body: body})
	t.Cleanup(server.Close)
	client, err := jev.New(jev.Config{APIKey: "ts_test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	return Jev{Ask: client.Ask, Fallback: Rules{CompactEvery: 20}}
}

const testModel = "jev-test-1"

func TestJevDecide(t *testing.T) {
	sales := decideAgentSeat("ada", "user-ada", "agent-ada", "sales")
	support := decideAgentSeat("zack", "user-zack", "agent-zack", "support")
	bob := decidePersonSeat("bob", "user-bob", true)

	noOpinion := choiceAnswer("nothing", 0.9, map[string]float64{"decision": 0.03, "commitment": 0.03, "fact": 0.04, "nothing": 0.9})
	quiet := noulAnswer(0.05)

	cases := []struct {
		name    string
		seats   []Seat
		body    string
		answer  map[string]any
		speaker map[string]any
		keep    map[string]any
		compact map[string]any
		want    Verdict
	}{
		{
			name:    "routing picks the sales-role seat for a sales request",
			seats:   []Seat{bob, sales, support},
			body:    "Can someone send over the sales deck for the Acme call?",
			answer:  choiceAnswer("needs_an_agent", 0.88, map[string]float64{"needs_an_agent": 0.88, "between_people": 0.07, "small_talk": 0.05}),
			speaker: choiceAnswer("ada", 0.81, map[string]float64{"ada": 0.81, "zack": 0.14, "nobody": 0.05}),
			keep:    noOpinion,
			compact: quiet,
			want: Verdict{
				Answer: true, Speaker: "ada", Certainty: 0.88, Model: testModel,
				Why: "answer=needs_an_agent (0.88)",
			},
		},
		{
			name:    "low confidence on whether to answer at all means nobody answers",
			seats:   []Seat{bob, sales, support},
			body:    "can someone take a look at this?",
			answer:  choiceAnswer("needs_an_agent", 0.40, map[string]float64{"needs_an_agent": 0.40, "between_people": 0.35, "small_talk": 0.25}),
			speaker: choiceAnswer("ada", 0.5, map[string]float64{"ada": 0.5, "zack": 0.3, "nobody": 0.2}),
			keep:    noOpinion,
			compact: quiet,
			want: Verdict{
				Certainty: 0.40, Model: testModel,
				Why: "answer=needs_an_agent (0.40)",
			},
		},
		{
			name:    "a routed seat unknown to this room is discarded",
			seats:   []Seat{bob, sales},
			body:    "does anyone know the status on this?",
			answer:  choiceAnswer("needs_an_agent", 0.9, map[string]float64{"needs_an_agent": 0.9, "between_people": 0.06, "small_talk": 0.04}),
			speaker: choiceAnswer("stale-seat-from-a-deleted-agent", 0.7, map[string]float64{"stale-seat-from-a-deleted-agent": 0.7, "nobody": 0.3}),
			keep:    noOpinion,
			compact: quiet,
			want: Verdict{
				Answer: true, Certainty: 0.9, Model: testModel,
				Why: "answer=needs_an_agent (0.90); routed seat discarded",
			},
		},
		{
			name:    "acknowledgement-shaped small talk is not answered",
			seats:   []Seat{bob, sales},
			body:    "thanks!",
			answer:  choiceAnswer("small_talk", 0.95, map[string]float64{"needs_an_agent": 0.02, "between_people": 0.03, "small_talk": 0.95}),
			speaker: choiceAnswer("nobody", 0.9, map[string]float64{"nobody": 0.9, "ada": 0.1}),
			keep:    noOpinion,
			compact: quiet,
			want: Verdict{
				Certainty: 0.95, Model: testModel,
				Why: "answer=small_talk (0.95)",
			},
		},
		{
			name:    "no agent seat in the room means speaker is never asked or set",
			seats:   []Seat{bob},
			body:    "does anybody know the wifi password?",
			answer:  choiceAnswer("needs_an_agent", 0.7, map[string]float64{"needs_an_agent": 0.7, "between_people": 0.2, "small_talk": 0.1}),
			keep:    noOpinion,
			compact: quiet,
			want: Verdict{
				Answer: true, Certainty: 0.7, Model: testModel,
				Why: "answer=needs_an_agent (0.70)",
			},
		},
		{
			name:    "a plain decision is kept as a decision",
			seats:   []Seat{bob, sales},
			body:    "Let's go with the S3 default, decided.",
			answer:  choiceAnswer("between_people", 0.7, map[string]float64{"needs_an_agent": 0.1, "between_people": 0.7, "small_talk": 0.2}),
			speaker: choiceAnswer("nobody", 0.9, map[string]float64{"nobody": 0.9, "ada": 0.1}),
			keep:    choiceAnswer("decision", 0.83, map[string]float64{"decision": 0.83, "commitment": 0.05, "fact": 0.02, "nothing": 0.10}),
			compact: quiet,
			want: Verdict{
				Certainty: 0.7, Model: testModel, Keep: true, Kind: "decision",
				Why: "answer=between_people (0.70)",
			},
		},
		{
			name:    "a low confidence memory verdict is not kept",
			seats:   []Seat{bob, sales},
			body:    "maybe we go with the S3 default?",
			answer:  choiceAnswer("between_people", 0.6, map[string]float64{"needs_an_agent": 0.2, "between_people": 0.6, "small_talk": 0.2}),
			speaker: choiceAnswer("nobody", 0.9, map[string]float64{"nobody": 0.9, "ada": 0.1}),
			keep:    choiceAnswer("decision", 0.55, map[string]float64{"decision": 0.55, "commitment": 0.05, "fact": 0.05, "nothing": 0.35}),
			compact: quiet,
			want: Verdict{
				Certainty: 0.6, Model: testModel,
				Why: "answer=between_people (0.60)",
			},
		},
		{
			name:    "the brief is judged stale and compaction fires",
			seats:   []Seat{bob, sales},
			body:    "ok let's also move the launch to next week",
			answer:  choiceAnswer("small_talk", 0.6, map[string]float64{"needs_an_agent": 0.2, "between_people": 0.2, "small_talk": 0.6}),
			speaker: choiceAnswer("nobody", 0.9, map[string]float64{"nobody": 0.9, "ada": 0.1}),
			keep:    noOpinion,
			compact: noulAnswer(0.7),
			want: Verdict{
				Certainty: 0.6, Model: testModel, Compact: true,
				Why: "answer=small_talk (0.60)",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			answers := map[string]any{qAnswer: tc.answer, qKeep: tc.keep, qCompact: tc.compact}
			if tc.speaker != nil {
				answers[qSpeaker] = tc.speaker
			}
			decider := testJevDecider(t, 0, systemOneBody(t, testModel, answers))
			room := Situation{Name: "launch-room", Seats: tc.seats}
			message := Message{Body: tc.body}

			got, err := decider.Decide(context.Background(), room, message)
			if err != nil {
				t.Fatalf("Decide() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("Decide() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestJevSpeakerQuestionOffersEachSeatsOwnRole checks the request Jev is
// actually sent, not just the verdict drawn from a canned answer: the
// speaker question's options are the room's agent seats, described by their
// own role and who they speak for, plus nobody — and nothing internal (a
// user or agent id) leaks into it.
func TestJevSpeakerQuestionOffersEachSeatsOwnRole(t *testing.T) {
	sales := decideAgentSeat("ada", "user-ada-secret", "agent-ada-secret", "sales")
	bob := decidePersonSeat("bob", "user-bob", true)
	answers := map[string]any{
		qAnswer:  choiceAnswer("needs_an_agent", 0.9, map[string]float64{"needs_an_agent": 0.9, "between_people": 0.06, "small_talk": 0.04}),
		qSpeaker: choiceAnswer("ada", 0.8, map[string]float64{"ada": 0.8, "nobody": 0.2}),
		qKeep:    choiceAnswer("nothing", 0.9, map[string]float64{"decision": 0.02, "commitment": 0.02, "fact": 0.06, "nothing": 0.9}),
		qCompact: noulAnswer(0.1),
	}
	fake := &fakeSystemOne{body: systemOneBody(t, testModel, answers)}
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	client, err := jev.New(jev.Config{APIKey: "ts_test", BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	decider := Jev{Ask: client.Ask, Fallback: Rules{}}
	room := Situation{Name: "launch-room", Seats: []Seat{bob, sales}}
	message := Message{Body: "can someone send the sales deck?"}

	if _, err := decider.Decide(context.Background(), room, message); err != nil {
		t.Fatalf("Decide() error = %v", err)
	}

	raw, err := json.Marshal(fake.lastRequest())
	if err != nil {
		t.Fatal(err)
	}
	sent := string(raw)
	if !strings.Contains(sent, `"role":"sales"`) || !strings.Contains(sent, `"speaks_for":"ada"`) {
		t.Fatalf("request sent = %s, want the sales seat's role and who it speaks for", sent)
	}
	if strings.Contains(sent, "user-ada-secret") || strings.Contains(sent, "agent-ada-secret") {
		t.Fatalf("request sent = %s, an internal id leaked into what Jev is shown", sent)
	}
}

// ---------------------------------------------------------------------
// Falling back
// ---------------------------------------------------------------------

func TestJevFallsBackToRulesOnAPIFailure(t *testing.T) {
	decider := testJevDecider(t, http.StatusInternalServerError, "")
	sales := decideAgentSeat("ada", "user-ada", "agent-ada", "sales")
	room := Situation{Seats: []Seat{decidePersonSeat("bob", "user-bob", false), sales}}
	message := Message{Body: "does anyone have the sales deck?", Seq: 5}

	got, err := decider.Decide(context.Background(), room, message)
	if err != nil {
		t.Fatalf("Decide() error = %v, want the fallback to answer instead", err)
	}
	want, err := decider.Fallback.Decide(context.Background(), room, message)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("Decide() = %+v, want exactly what Rules would have said: %+v", got, want)
	}
	if got.Model != rulesModel {
		t.Fatalf("Decide().Model = %q, want %q: a failed call must not claim to be Jev's own", got.Model, rulesModel)
	}
}

func TestJevWithNoKeyConfiguredFallsBackWithoutCallingOut(t *testing.T) {
	decider := Jev{Fallback: Rules{}}
	room := Situation{Seats: []Seat{decideAgentSeat("ada", "user-ada", "agent-ada", "sales")}}
	message := Message{Body: "does anyone have the sales deck?"}

	got, err := decider.Decide(context.Background(), room, message)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	want, _ := (Rules{}).Decide(context.Background(), room, message)
	if got != want {
		t.Fatalf("Decide() = %+v, want %+v", got, want)
	}
}

func TestJevWithNoFallbackReportsItsFailure(t *testing.T) {
	decider := Jev{}
	if _, err := decider.Decide(context.Background(), Situation{}, Message{Body: "hello"}); err == nil {
		t.Fatal("Decide() error = nil for an unconfigured Jev with no fallback")
	}
}

// ---------------------------------------------------------------------
// Live: TYPESAFE_API_KEY only.
// ---------------------------------------------------------------------

// TestLiveJevAsksTheRealQuestions asks the production Jev decider — the
// same four questions [Jev.Decide] builds in production — about three
// realistic messages, when TYPESAFE_API_KEY is set. It does not assert on
// what Jev decided, only that it decided something from among the options
// it was actually offered: the model can improve or drift without breaking
// this test, since only this file's own contract with it is being checked.
func TestLiveJevAsksTheRealQuestions(t *testing.T) {
	key := os.Getenv("TYPESAFE_API_KEY")
	if key == "" {
		t.Skip("TYPESAFE_API_KEY names the account to check Jev with")
	}
	client, err := jev.New(jev.Config{APIKey: key, BaseURL: os.Getenv("TYPESAFE_BASE_URL")})
	if err != nil {
		t.Fatal(err)
	}
	decider := Jev{Ask: client.Ask, Fallback: Rules{}}

	sales := decideAgentSeat("ada", "user-ada", "agent-ada", "sales")
	support := decideAgentSeat("zack", "user-zack", "agent-zack", "support")
	bob := decidePersonSeat("bob", "user-bob", true)
	room := Situation{
		Name: "launch-room", Purpose: "coordinating the Acme launch",
		Seats: []Seat{bob, sales, support},
		Brief: Brief{Summary: "The team is finishing the Acme launch checklist before Friday."},
	}

	messages := []string{
		"Can someone send over the sales deck for the Acme call tomorrow?",
		"Sounds good, see you at 3!",
		"Let's go with the S3 Vectors default for the RAG index — that's settled.",
	}
	validKind := map[string]bool{"": true, "decision": true, "commitment": true, "fact": true}
	for _, body := range messages {
		verdict, err := decider.Decide(context.Background(), room, Message{Body: body})
		if err != nil {
			t.Errorf("%q: Decide() error = %v", body, err)
			continue
		}
		t.Logf("%-70s answer=%-5t speaker=%-6q keep=%-5t kind=%-10q compact=%-5t certainty=%.2f model=%s why=%q",
			body, verdict.Answer, verdict.Speaker, verdict.Keep, verdict.Kind, verdict.Compact, verdict.Certainty, verdict.Model, verdict.Why)
		if verdict.Model == rulesModel {
			t.Errorf("%q: fell back to Rules; want a live Jev answer", body)
		}
		if !validKind[verdict.Kind] {
			t.Errorf("%q: kind = %q, want one of the offered options", body, verdict.Kind)
		}
		if verdict.Speaker != "" && !knownAgentSeat([]Seat{sales, support}, verdict.Speaker) {
			t.Errorf("%q: speaker = %q, want one of this room's own agent seats", body, verdict.Speaker)
		}
	}
}

// The author of a message is doing the talking, so their own agent is never
// the one Rules picks to answer it, even before their presence is recorded.
func TestRulesNeverPicksTheAuthorsOwnAgent(t *testing.T) {
	ada := decidePersonSeat("ada", "user-ada", false)
	adaAgent := decideAgentSeat("krude", "user-ada", "agent-ada", "")
	adaAgent.SpeaksFor = "user-ada"
	room := Situation{Seats: []Seat{ada, adaAgent}}
	message := Message{Body: "does anyone have the deck?", AuthorHandle: "ada", AuthorUserID: "user-ada"}

	got, err := (Rules{}).Decide(context.Background(), room, message)
	if err != nil {
		t.Fatalf("Decide() error = %v", err)
	}
	if got.Answer || got.Speaker != "" {
		t.Fatalf("Decide() = %+v, want no answer: the only agent speaks for the author", got)
	}
}
