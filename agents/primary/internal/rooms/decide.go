package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/internal/jev"
)

// Rules and Jev are the two [Decider]s rooms.go describes: Rules, a floor
// with no model and no network; Jev, the production decider, which asks
// TypeSafe's System One model and falls back to Rules when it cannot
// answer.

// ---------------------------------------------------------------------
// Rules: the floor.
// ---------------------------------------------------------------------

// Rules is a [Decider] with no model, no key and no network: cheap,
// deterministic checks that keep a room minimally honest when Jev is not
// configured or has failed. It is a floor, not a judgment — it answers less
// than Jev would and remembers less than Jev would, and it says so wherever
// that trade shows. Every field it can be wrong about is written to fail
// toward quiet: an ambiguous message is never answered, and an ambiguous
// decision is never kept.
type Rules struct {
	// CompactEvery backstops the brief: once this many messages have
	// arrived since the brief's ThroughSeq, Compact fires regardless of
	// what any of them said. Zero or negative disables the backstop, which
	// then depends entirely on Jev (or on nothing, if Jev is also absent).
	CompactEvery int
}

var _ Decider = Rules{}

// rulesModel names Rules as the [Verdict.Model] that judged, the same way a
// model name would, so a change in behaviour is never mistaken for one of
// Jev's.
const rulesModel = "rules"

// Decide implements [Decider]. See the [Rules] doc comment: this is a floor,
// so every branch is a rule a person could restate, not a probability.
func (r Rules) Decide(_ context.Context, room Situation, message Message) (Verdict, error) {
	body := strings.TrimSpace(message.Body)
	verdict := Verdict{Certainty: 1, Model: rulesModel}

	switch {
	case isAcknowledgement(body):
		verdict.Why = "acknowledgement: never answered"
	case strings.Contains(body, "?") && freeAgentSeat(room.Seats):
		verdict.Answer = true
		verdict.Why = "asks something, and an agent seat is free to take it"
	case strings.Contains(body, "?"):
		verdict.Why = "asks something, but no agent seat is free"
	default:
		verdict.Why = "not a question"
	}

	// Rules leaves Speaker empty even when Answer is true: choosing which of
	// several seats should take a message is a judgment, and a rule that
	// guessed at it would not be honest about being a rule.

	if kind, ok := ruleKeep(body); ok {
		verdict.Keep, verdict.Kind = true, kind
	}

	if r.CompactEvery > 0 && message.Seq-room.Brief.ThroughSeq >= int64(r.CompactEvery) {
		verdict.Compact = true
	}
	return verdict, nil
}

// ackPhrases are bare acknowledgements: the entire message, trimmed of space
// and light punctuation and lowercased, matches one of these exactly. A
// substring match would risk swallowing "thanks, but can someone also..." —
// a rule this blunt would rather miss an acknowledgement than eat a request.
var ackPhrases = map[string]bool{
	"thanks": true, "thank you": true, "thankyou": true, "ty": true, "thx": true,
	"+1": true, "-1": true, "ok": true, "okay": true, "cool": true, "nice": true,
	"got it": true, "sounds good": true, "noted": true, "ack": true, "acknowledged": true,
	"np": true, "perfect": true, "awesome": true, "great": true,
}

// isAcknowledgement reports whether body is nothing but an acknowledgement:
// one of [ackPhrases], or a run of symbols and emoji with no letter or digit
// in it — a thumbs-up, a wave, a string of exclamation marks.
func isAcknowledgement(body string) bool {
	trimmed := strings.ToLower(strings.Trim(strings.TrimSpace(body), "!.~ "))
	if trimmed == "" {
		return false
	}
	return ackPhrases[trimmed] || symbolOnly(trimmed)
}

// symbolOnly reports whether body has no letter and no digit anywhere in it.
func symbolOnly(body string) bool {
	for _, r := range body {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// decisionCues and commitmentCues are literal, case-insensitive phrases.
// Rules only catches what is spelled out this plainly; it would rather miss
// a real decision than invent one nobody made, which is Jev's job to judge
// with more than a substring match.
var decisionCues = []string{
	"we've decided", "we have decided", "decided to", "let's go with",
	"we'll go with", "final decision", "it's settled", "the decision is",
}

var commitmentCues = []string{
	"i will ", "i'll ", "i promise", "we will ", "we'll ", "i commit to",
	"committing to", "i owe you", "you have my word",
}

// ruleKeep reports whether body plainly states a decision or a promise, and
// which: the two things [Rules] will ever keep. Anything subtler — a fact
// worth remembering, a decision phrased without one of these cues — is left
// for Jev; a floor that guessed at meaning would not be a floor.
func ruleKeep(body string) (kind string, ok bool) {
	lower := strings.ToLower(body)
	for _, cue := range decisionCues {
		if strings.Contains(lower, cue) {
			return "decision", true
		}
	}
	for _, cue := range commitmentCues {
		if strings.Contains(lower, cue) {
			return "commitment", true
		}
	}
	return "", false
}

// freeAgentSeat reports whether some agent seat's own person is not
// currently reading the room. A present person answers for themselves; an
// agent whose person is right here is not free to answer over them, which
// is the same fact [AgentSpeaker.Speak] already reads presentOwner for.
func freeAgentSeat(seats []Seat) bool {
	for _, seat := range seats {
		if seat.Kind == SeatAgent && !presentOwner(seats, seat.UserID) {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------
// Jev: the production decider.
// ---------------------------------------------------------------------

// Default confidence gates for [Jev]'s four judgments, and its default
// budget for one call. See the field comments on [Jev] for what is at stake
// in each, and the package's design notes for why each number is where it
// is.
const (
	DefaultAnswerConfidence = 0.55
	DefaultKeepConfidence   = 0.60
	DefaultCompactThreshold = 0.50
	DefaultAskTimeout       = 5 * time.Second
)

// Question IDs Jev is asked by. They are for this file's own code, never
// shown to the model, and never seen outside it.
const (
	qAnswer  = "answer"
	qSpeaker = "speaker"
	qKeep    = "keep"
	qCompact = "compact"

	// optionNobody is the Choice option meaning no seat here fits, both as
	// an offered option and as [Verdict.Speaker]'s zero value in spirit —
	// though the Verdict itself uses "" for that, never this string.
	optionNobody = "nobody"
)

// errJevNotConfigured means [Jev.Ask] is nil: there is nothing to call,
// typically because no TypeSafe key was configured.
var errJevNotConfigured = errors.New("rooms: Jev decider has no Ask function configured")

// Jev is the production [Decider]: one System One request per message,
// carrying the four judgments a room needs as independent questions over
// the same state. They are evaluated in parallel — Jev cannot see one
// answer while forming another — and combined into a [Verdict] here, in
// Go: the policy is this code, never the prompt Jev is given.
//
// A message's body reaches Jev only inside the request's state, exactly as
// [toSituationState] builds it. Instructions and Criteria hold nothing but
// this file's own fixed wording, so nothing anybody types in a room can
// change what Jev is being asked.
type Jev struct {
	// Ask calls TypeSafe's System One API. Production code sets this to a
	// (*jev.Client).Ask; nil means no key was configured, which Decide
	// treats exactly like a failed call.
	Ask func(ctx context.Context, request jev.Request) (jev.Result, error)
	// Fallback answers whenever Ask is nil, times out, errors, or answers
	// something this code cannot make sense of — so a room never stops
	// working because TypeSafe is unconfigured or down. Production code
	// sets this to a [Rules]. A nil Fallback turns any of those failures
	// into an error instead of a quiet degrade.
	Fallback Decider
	// Logger records why a call fell back, at debug level: falling back is
	// routine enough not to warrant a warning on its own, but silent,
	// permanent fallback (an expired key, say) is worth being able to find.
	// A discarded logger when nil.
	Logger *slog.Logger
	// Timeout bounds one call to Ask, [DefaultAskTimeout] when zero. A slow
	// System One is indistinguishable from a down one: either way, Fallback
	// should answer before the room's own caller notices.
	Timeout time.Duration
	// AnswerConfidence is the least confidence "needs_an_agent" must carry
	// for [Verdict.Answer] to be true; [DefaultAnswerConfidence] when zero.
	AnswerConfidence float64
	// KeepConfidence is the least confidence a non-"nothing" memory verdict
	// must carry for [Verdict.Keep] to be true; [DefaultKeepConfidence]
	// when zero.
	KeepConfidence float64
	// CompactThreshold is how likely the brief must be stale, from the
	// question that asks as a Noul, before [Verdict.Compact] fires;
	// [DefaultCompactThreshold] when zero.
	CompactThreshold float64
}

var _ Decider = Jev{}

// Decide implements [Decider].
func (j Jev) Decide(ctx context.Context, room Situation, message Message) (Verdict, error) {
	if j.Ask == nil {
		return j.fallback(ctx, room, message, errJevNotConfigured)
	}
	agentSeats := make([]Seat, 0, len(room.Seats))
	for _, seat := range room.Seats {
		if seat.Kind == SeatAgent {
			agentSeats = append(agentSeats, seat)
		}
	}
	request := jev.Request{
		State: toSituationState(room, message),
		Questions: map[string]jev.Question{
			qAnswer:  answerQuestion(),
			qKeep:    keepQuestion(),
			qCompact: compactQuestion(),
		},
	}
	if len(agentSeats) > 0 {
		// Nothing to route to without an agent seat in the room; asking
		// would only spend a question on a single forced option.
		request.Questions[qSpeaker] = speakerQuestion(agentSeats)
	}

	timeout := j.Timeout
	if timeout <= 0 {
		timeout = DefaultAskTimeout
	}
	asking, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result, err := j.Ask(asking, request)
	if err != nil {
		return j.fallback(ctx, room, message, err)
	}
	verdict, err := j.verdictFrom(result, agentSeats)
	if err != nil {
		return j.fallback(ctx, room, message, err)
	}
	return verdict, nil
}

// fallback answers through Fallback, logging why. cause is never returned
// to the caller when Fallback answers: a room's caller should not have to
// tell "Jev is down but Rules answered" apart from "Jev answered", since
// both mean the room keeps working.
func (j Jev) fallback(ctx context.Context, room Situation, message Message, cause error) (Verdict, error) {
	logger := j.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if j.Fallback == nil {
		return Verdict{}, fmt.Errorf("rooms: Jev decider failed with no fallback configured: %w", cause)
	}
	logger.DebugContext(ctx, "rooms: Jev call failed, falling back", "error", cause)
	return j.Fallback.Decide(ctx, room, message)
}

// verdictFrom applies this room's own policy to Jev's four answers. Every
// threshold and every discard here is a decision this code makes; Jev only
// ever hands back a distribution.
func (j Jev) verdictFrom(result jev.Result, agentSeats []Seat) (Verdict, error) {
	answer, ok := result.Answers[qAnswer]
	if !ok || answer.Type != jev.TypeChoice {
		return Verdict{}, errors.New("rooms: Jev did not answer whether an agent should answer")
	}
	keep, ok := result.Answers[qKeep]
	if !ok || keep.Type != jev.TypeChoice {
		return Verdict{}, errors.New("rooms: Jev did not answer what the message changed")
	}
	compact, ok := result.Answers[qCompact]
	if !ok || compact.Type != jev.TypeNoul {
		return Verdict{}, errors.New("rooms: Jev did not answer whether to compact")
	}

	verdict := Verdict{Certainty: answer.Confidence, Model: result.Model}

	// An unconfident "should anyone answer" means nobody answers, rather
	// than acting on whichever option happened to lead.
	answerConfidence := j.AnswerConfidence
	if answerConfidence <= 0 {
		answerConfidence = DefaultAnswerConfidence
	}
	verdict.Answer = answer.Choice == "needs_an_agent" && answer.Confidence >= answerConfidence
	verdict.Why = fmt.Sprintf("answer=%s (%.2f)", answer.Choice, answer.Confidence)

	if verdict.Answer {
		if speaker, ok := result.Answers[qSpeaker]; ok && speaker.Type == jev.TypeChoice {
			switch {
			case speaker.Choice == optionNobody:
				// Jev itself found no fitting seat; nothing to discard.
			case knownAgentSeat(agentSeats, speaker.Choice):
				verdict.Speaker = speaker.Choice
			default:
				// A routed seat that is not an agent seat in this room —
				// stale seating, or an answer that drifted from the
				// options it was given. Either way, discarded rather than
				// trusted.
				verdict.Why += "; routed seat discarded"
			}
		}
	}

	keepConfidence := j.KeepConfidence
	if keepConfidence <= 0 {
		keepConfidence = DefaultKeepConfidence
	}
	if keep.Choice != "nothing" && keep.Confidence >= keepConfidence {
		verdict.Keep, verdict.Kind = true, keep.Choice
	}

	compactThreshold := j.CompactThreshold
	if compactThreshold <= 0 {
		compactThreshold = DefaultCompactThreshold
	}
	verdict.Compact = compact.Noul >= compactThreshold

	return verdict, nil
}

// knownAgentSeat reports whether handle names one of this room's current
// agent seats.
func knownAgentSeat(agentSeats []Seat, handle string) bool {
	for _, seat := range agentSeats {
		if seat.Handle == handle {
			return true
		}
	}
	return false
}

// answerQuestion asks whether the new message is work an agent should pick
// up, people talking to each other, or small talk — the first thing every
// message needs decided, and the only one that gates whether an agent
// speaks at all.
func answerQuestion() jev.Question {
	return jev.Question{
		Type:         jev.TypeChoice,
		Instructions: "Should an agent answer the new message, or is it something else? Judge the new message; the rest of the state is only context for it.",
		Criteria: map[string]jev.Option{
			"needs_an_agent": {
				What:   "The new message asks for something an agent could do or knows: a question aimed at nobody in particular, a request, or work somebody should pick up.",
				NotFor: "A message two people in the room are already handling directly between themselves, or one with nothing in it to answer.",
				Examples: []string{
					"Can someone pull the Q3 numbers before the call?",
					"Does anyone have the latest pricing sheet?",
					"We still need someone to review the contract redlines.",
				},
			},
			"between_people": {
				What:   "People in the room talking directly to each other: replying, coordinating, agreeing, debating.",
				NotFor: "A question or request that nobody in the room has picked up yet, even if it reads casually.",
				Examples: []string{
					"Sounds good, see you at 3",
					"I'd use the Q2 numbers instead, they're cleaner",
					"Sure, sending it over now",
				},
			},
			"small_talk": {
				What:   "Greetings, banter, congratulations, or chat with no ask and no decision in it.",
				NotFor: "A real request or question, even a casually phrased one.",
				Examples: []string{
					"Good morning everyone",
					"Happy Friday team",
					"Congrats on the launch!",
				},
			},
		},
	}
}

// seatOption describes one agent seat as a routing option: its role in the
// member's own words, and who it speaks for, which is what routing a
// message to the right seat is actually decided on.
type seatOption struct {
	Role      string `json:"role"`
	SpeaksFor string `json:"speaks_for"`
}

// speakerQuestion asks which agent seat should answer, built fresh from
// this room's own seats every time: the options are exactly the agent seats
// present, by their role and who they speak for, plus nobody. Only called
// when at least one agent seat exists.
func speakerQuestion(agentSeats []Seat) jev.Question {
	criteria := map[string]any{
		optionNobody: "No seat here fits the new message: it is not any agent's stated role, or a person should answer it instead.",
	}
	for _, seat := range agentSeats {
		role := seat.Role
		if role == "" {
			role = "no stated role"
		}
		criteria[seat.Handle] = seatOption{Role: role, SpeaksFor: seat.DisplayName}
	}
	return jev.Question{
		Type:         jev.TypeChoice,
		Instructions: "Given the seats' own roles and who each speaks for, which one should answer the new message? Choose nobody if none fits — for instance, a request for a sales document is for the seat whose role is sales.",
		Criteria:     criteria,
	}
}

// keepQuestion asks whether the new message changed what the organization
// should remember, and as what kind.
func keepQuestion() jev.Question {
	return jev.Question{
		Type:         jev.TypeChoice,
		Instructions: "Did the new message change what the organization should remember later, and as what kind of thing?",
		Criteria: map[string]jev.Option{
			"decision": {
				What:   "The room settled on a choice among alternatives: what to do, use, ship, or go with.",
				NotFor: "One person's private plan, or a task somebody agreed to do (commitment).",
				Examples: []string{
					"We'll ship the redesign Monday, not the 15th",
					"Let's use Postgres instead of Mongo for this",
				},
			},
			"commitment": {
				What:   "Somebody promised to do something, with or without a deadline.",
				NotFor: "A decision the room reached together (decision).",
				Examples: []string{
					"I'll have the deck ready by Thursday",
					"I'll ping the vendor about the delay",
				},
			},
			"fact": {
				What:   "A lasting fact worth remembering later: a number, a status, a name — something true about the work rather than a choice or a promise.",
				NotFor: "A passing detail nobody will need to look up again.",
				Examples: []string{
					"The client's renewal date is March 3rd",
					"Prod is on Postgres 16 now",
				},
			},
			"nothing": {
				What: "Small talk, a still-open question, an acknowledgement, or anything else not worth writing down.",
				Examples: []string{
					"Sounds good",
					"Any update on this?",
				},
			},
		},
	}
}

// compactQuestion asks whether the room has outgrown its own brief.
func compactQuestion() jev.Question {
	return jev.Question{
		Type:         jev.TypeNoul,
		Instructions: "Has the room moved far enough since the brief below was written that the brief no longer describes where things now stand?",
		Criteria: map[string]string{
			"true":  "The brief's summary, open items, or owed items no longer match where the conversation now stands.",
			"false": "The brief still describes the room fairly, even if it is missing some small, recent detail.",
		},
	}
}

// ---------------------------------------------------------------------
// State sent to Jev: a bounded, named projection of a Situation and its new
// message. Internal bookkeeping — ids, sequence numbers, brief revisions —
// is left out, since none of it helps judge the message; only what a person
// reading the room would themselves use is here.
// ---------------------------------------------------------------------

const (
	// jevMaxRecent bounds how many recent messages are sent, defensively:
	// the room already hands Decide a short, bounded Situation.Recent, but
	// this file does not rely on that holding for every caller.
	jevMaxRecent = 12
	// jevMaxBodyRunes and jevMaxBriefRunes bound a single message body and
	// the brief's summary. Jev's own read of a document elsewhere in this
	// codebase budgets a few thousand runes for the same reason: accuracy
	// falls, not rises, once what it reads is mostly unrelated to the
	// judgment being asked.
	jevMaxBodyRunes  = 2_000
	jevMaxBriefRunes = 4_000
)

type situationState struct {
	Room    situationRoom      `json:"room"`
	Seats   []situationSeat    `json:"seats"`
	Brief   situationBrief     `json:"brief"`
	Recent  []situationMessage `json:"recent"`
	Message situationMessage   `json:"new_message"`
}

type situationRoom struct {
	Name    string `json:"name"`
	Purpose string `json:"purpose,omitempty"`
}

type situationSeat struct {
	Handle      string `json:"handle"`
	Kind        string `json:"kind"`
	Role        string `json:"role,omitempty"`
	DisplayName string `json:"display_name"`
	Present     bool   `json:"present"`
}

type situationBrief struct {
	Summary string   `json:"summary"`
	Decided []string `json:"decided,omitempty"`
	Open    []string `json:"open,omitempty"`
	Owed    []string `json:"owed,omitempty"`
}

type situationMessage struct {
	Author      string   `json:"author"`
	Kind        string   `json:"kind"`
	Body        string   `json:"body"`
	AddressedTo []string `json:"addressed_to,omitempty"`
}

// toSituationState projects room and message into what Jev is shown. The
// new message's body, and every recent message's, is data here: it can
// state a decision or ask a question, but it can never add an instruction,
// since Instructions and Criteria come only from this file's own questions.
func toSituationState(room Situation, message Message) situationState {
	recent := room.Recent
	if len(recent) > jevMaxRecent {
		recent = recent[len(recent)-jevMaxRecent:]
	}
	messages := make([]situationMessage, len(recent))
	for i, m := range recent {
		messages[i] = toSituationMessage(m)
	}
	return situationState{
		Room:  situationRoom{Name: room.Name, Purpose: room.Purpose},
		Seats: situationSeats(room.Seats),
		Brief: situationBrief{
			Summary: truncateRunes(room.Brief.Summary, jevMaxBriefRunes),
			Decided: room.Brief.Decided, Open: room.Brief.Open, Owed: room.Brief.Owed,
		},
		Recent:  messages,
		Message: toSituationMessage(message),
	}
}

func situationSeats(seats []Seat) []situationSeat {
	out := make([]situationSeat, len(seats))
	for i, seat := range seats {
		out[i] = situationSeat{
			Handle: seat.Handle, Kind: string(seat.Kind), Role: seat.Role,
			DisplayName: seat.DisplayName, Present: seat.Present,
		}
	}
	return out
}

func toSituationMessage(m Message) situationMessage {
	return situationMessage{
		Author: m.AuthorHandle, Kind: string(m.AuthorKind),
		Body: truncateRunes(m.Body, jevMaxBodyRunes), AddressedTo: m.AddressedTo,
	}
}

// truncateRunes bounds s to at most limit runes, cutting on a rune boundary
// so a multi-byte character is never split in half, and marking that it
// cut so Jev is not misled into reading a truncated body as the whole of
// it.
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + " […]"
}
