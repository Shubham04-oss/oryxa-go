package rooms

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// Errors a caller can act on.
var (
	ErrNotFound  = errors.New("room not found")
	ErrForbidden = errors.New("room is not available to this member")
	ErrInvalid   = errors.New("invalid room request")
	// ErrPaused refuses saying anything in a room whose teamspace is paused.
	// What was said there stays readable to the team.
	ErrPaused = errors.New("this room is paused")
)

// presentWithin is how recently somebody must have read a room to count as
// being in it. Long enough to survive a reload and a slow network, short
// enough that an agent stands in for somebody who has actually walked away.
const presentWithin = 90 * time.Second

// recentMessages is how much of the scrollback anybody — an agent taking a
// turn, or the decider — is shown alongside the brief. The brief is what
// stands in for everything older; this is only the immediate thread of talk.
const recentMessages = 12

// maxBody is the longest message a room accepts, in runes. A room is for
// talking; a document belongs in the ledger or in files.
const maxBody = 4_000

// Spaces is the teamspace a room is. teamspaces.Service implements it: it
// resolves who may read the room from the team's live membership, every time,
// so somebody removed from the team stops being in the room at once.
type Spaces interface {
	Get(ctx context.Context, actor teamspaces.Actor, id string) (teamspaces.Space, error)
}

// Ledger is where a room's conclusions go. A decision or a commitment said in
// a room is published to the teamspace's own ledger, which is what the
// organization agent reads — so a room contributes to organization context by
// concluding something, not by anybody remembering to publish.
type Ledger interface {
	PublishEntry(ctx context.Context, actor teamspaces.Actor, teamspaceID string, input teamspaces.PublishEntryInput) (teamspaces.Entry, error)
}

// Config is what a room service runs with.
type Config struct {
	Store     Store
	Spaces    Spaces
	Ledger    Ledger
	Decider   Decider
	Compactor Compactor
	Relay     *Relay
	Logger    *slog.Logger
}

// Service is a teamspace read and written as a conversation.
type Service struct {
	store     Store
	spaces    Spaces
	ledger    Ledger
	decider   Decider
	compactor Compactor
	relay     *Relay
	logger    *slog.Logger
}

// New returns the room service. Everything but the ledger and the compactor
// is required: a room without a ledger still works, it simply concludes
// nothing durable, and a room without a compactor keeps a brief that never
// changes.
func New(config Config) (*Service, error) {
	switch {
	case config.Store == nil:
		return nil, errors.New("rooms: a store is required")
	case config.Spaces == nil:
		return nil, errors.New("rooms: the teamspaces a room hangs off are required")
	case config.Decider == nil:
		return nil, errors.New("rooms: a decider is required")
	case config.Relay == nil:
		return nil, errors.New("rooms: a relay is required")
	}
	logger := config.Logger
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		store: config.Store, spaces: config.Spaces, ledger: config.Ledger,
		decider: config.Decider, compactor: config.Compactor, relay: config.Relay,
		logger: logger,
	}, nil
}

// Actor is who is acting in a room: always a person, never an agent. An agent
// speaks in a room because the relay ran it, never because it called this API.
type Actor struct {
	OrganizationID string
	UserID         string
	AccessToken    string
}

// Post says something in a room as the member, and returns what the room
// added: their message first, then whatever the agents that answered said.
//
// The judgment happens once, here, and is then used three ways: the relay
// decides who answers with it, a conclusion is published to the ledger with
// it, and the brief is rewritten when it says the room has moved on. Asking
// once is not an optimisation — it is what keeps those three from disagreeing
// about the same message.
func (service *Service) Post(ctx context.Context, actor Actor, roomID, body string) ([]Message, error) {
	body = strings.TrimSpace(body)
	if body == "" {
		return nil, fmt.Errorf("%w: a message needs something in it", ErrInvalid)
	}
	if len([]rune(body)) > maxBody {
		return nil, fmt.Errorf("%w: a message is at most %d characters", ErrInvalid, maxBody)
	}
	space, err := service.space(ctx, actor, roomID)
	if err != nil {
		return nil, err
	}
	if space.Status != teamspaces.StatusActive {
		return nil, ErrPaused
	}
	// Saying something is the plainest evidence there is that somebody is in
	// the room, and it is recorded before the seating is read so the author is
	// present in their own message's situation: an agent must never answer
	// over the person it speaks for while that person is doing the talking.
	if err := service.store.Watch(ctx, space.ID, actor.UserID, time.Now().UTC()); err != nil {
		service.logger.WarnContext(ctx, "record that a member is talking in a room", "room_id", space.ID, "error", err)
	}
	situation, seats, err := service.situation(ctx, space)
	if err != nil {
		return nil, err
	}
	author, ok := personSeat(seats, actor.UserID)
	if !ok {
		return nil, ErrForbidden
	}
	message := Message{
		RoomID: space.ID, AuthorHandle: author.Handle, AuthorKind: SeatPerson,
		AuthorUserID: actor.UserID, Body: body, CreatedAt: time.Now().UTC(),
	}

	verdict, err := service.decider.Decide(ctx, situation, message)
	if err != nil {
		// A room that stops working because a judgment failed would be worse
		// than a room where nobody answers for one message.
		service.logger.WarnContext(ctx, "room decision failed; the message still lands", "room_id", space.ID, "error", err)
		verdict = Verdict{Why: "the decision failed; only what was addressed answers"}
	}

	// One line per message, because this is the judgment everything else in
	// the room follows and it is the first thing worth reading when a room
	// answers too much or too little.
	service.logger.InfoContext(ctx, "room judged a message",
		"room_id", space.ID, "author", author.Handle,
		"answer", verdict.Answer, "speaker", verdict.Speaker, "keep", verdict.Keep, "kind", verdict.Kind,
		"compact", verdict.Compact, "certainty", verdict.Certainty, "why", verdict.Why, "model", verdict.Model,
		"seats", handles(seats))

	// What a seat's turn cannot answer from the room it files with its own
	// member, for the member who posted this.
	said, err := service.relay.Post(askingFor(ctx, actor, space), situation, message, verdict)
	if err != nil {
		return nil, err
	}
	if len(said) == 0 {
		return nil, errors.New("rooms: the relay recorded nothing, not even the message")
	}
	posted := said[0]

	if verdict.Keep && service.ledger != nil {
		service.publish(ctx, actor, space, posted, verdict)
	}
	// A conclusion is new state by definition: a brief that does not mention
	// what the room just decided is already out of date, whatever the decider
	// thought of the brief a moment ago.
	service.compact(ctx, situation, space.ID, verdict.Compact || verdict.Keep, said[len(said)-1].Seq)
	return said, nil
}

// Read returns what was said after seq, oldest first, and who is in the room
// to hear it. The seating comes back with every read because it is not static:
// it says who is here right now, and it carries the handles people are
// addressed by — which a reader has to know before they can name anybody.
func (service *Service) Read(ctx context.Context, actor Actor, roomID string, afterSeq int64, limit int) ([]Message, []Seat, error) {
	space, err := service.space(ctx, actor, roomID)
	if err != nil {
		return nil, nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	// Recorded before the seating is read, so a reader is in their own answer.
	if err := service.store.Watch(ctx, space.ID, actor.UserID, time.Now().UTC()); err != nil {
		service.logger.WarnContext(ctx, "record that a member is reading a room", "room_id", space.ID, "error", err)
	}
	messages, err := service.store.Messages(ctx, space.ID, afterSeq, limit)
	if err != nil {
		return nil, nil, err
	}
	watching, err := service.store.Watching(ctx, space.ID, time.Now().UTC().Add(-presentWithin))
	if err != nil {
		return nil, nil, err
	}
	return messages, seatsOf(space, watching), nil
}

// Catchup is what somebody who has been away is handed: what the room amounts
// to, and what they have not seen.
func (service *Service) Catchup(ctx context.Context, actor Actor, roomID string, afterSeq int64) (Brief, []Message, error) {
	space, err := service.space(ctx, actor, roomID)
	if err != nil {
		return Brief{}, nil, err
	}
	if err := service.store.Watch(ctx, space.ID, actor.UserID, time.Now().UTC()); err != nil {
		service.logger.WarnContext(ctx, "record that a member is reading a room", "room_id", space.ID, "error", err)
	}
	return Catchup(ctx, service.store, space.ID, afterSeq)
}

// MarkRead says the member has seen the room up to seq, and returns how far
// they have now read. Reading the room (Read, Catchup) never moves this: a
// client says what was on screen, since a fetch is not a reading. A paused
// room can still be read, so it can still be marked read.
func (service *Service) MarkRead(ctx context.Context, actor Actor, roomID string, seq int64) (int64, error) {
	if seq < 0 {
		return 0, fmt.Errorf("%w: seq must be a message sequence", ErrInvalid)
	}
	space, err := service.space(ctx, actor, roomID)
	if err != nil {
		return 0, err
	}
	return service.store.MarkRead(ctx, space.ID, actor.UserID, seq)
}

// previewRunes is the most of a message a teamspace list shows.
const previewRunes = 140

// Activity fills in, on each space the actor is a member of, how much of its
// room they have not read and the last thing said there. spaces must be what
// teamspaces.Service showed this actor, since membership is taken from them:
// a manager who is not on a team sees its teamspace but none of its room, so
// theirs are left as they were. One read covers every room.
func (service *Service) Activity(ctx context.Context, actor Actor, spaces []teamspaces.Space) ([]teamspaces.Space, error) {
	roomIDs := make([]string, 0, len(spaces))
	for _, space := range spaces {
		if space.IsMember {
			roomIDs = append(roomIDs, space.ID)
		}
	}
	if len(roomIDs) == 0 {
		return spaces, nil
	}
	activity, err := service.store.Activity(ctx, actor.OrganizationID, actor.UserID, roomIDs)
	if err != nil {
		return nil, err
	}
	for index := range spaces {
		found, ok := activity[spaces[index].ID]
		if !ok || !spaces[index].IsMember {
			continue
		}
		spaces[index].LastReadSeq = &found.LastReadSeq
		spaces[index].UnreadCount = &found.Unread
		if found.Last != nil {
			spaces[index].LastMessage = lastMessage(spaces[index], *found.Last)
		}
	}
	return spaces, nil
}

// lastMessage is message as a teamspace list shows it, its author named as
// the teamspace names them now.
func lastMessage(space teamspaces.Space, message Message) *teamspaces.LastMessage {
	last := &teamspaces.LastMessage{
		Seq: message.Seq, Kind: string(message.Kind), AuthorKind: string(message.AuthorKind), AuthorID: message.AuthorUserID,
		AuthorHandle: message.AuthorHandle, AuthorName: message.AuthorHandle,
		Preview: preview(message.Body), CreatedAt: message.CreatedAt,
	}
	if message.AuthorKind == SeatAgent {
		last.AuthorID = message.AuthorAgentID
	}
	if name := authorName(space, message); name != "" {
		last.AuthorName = name
	}
	return last
}

// authorName is who said message, by the name they have in space: a
// teammate, their own agent, a contributed specialist, or the organization
// agent. Empty for somebody who no longer has a seat there.
func authorName(space teamspaces.Space, message Message) string {
	if message.AuthorKind != SeatAgent {
		for _, whisperer := range space.Whisperers {
			if whisperer.UserID == message.AuthorUserID {
				return whisperer.UserDisplayName
			}
		}
		return ""
	}
	for _, whisperer := range space.Whisperers {
		if whisperer.AgentID == message.AuthorAgentID {
			return whisperer.AgentName
		}
	}
	for _, contribution := range space.Contributions {
		if contribution.AgentID == message.AuthorAgentID {
			return contribution.Name
		}
	}
	if space.OrganizationAgent.ID != "" && space.OrganizationAgent.ID == message.AuthorAgentID {
		return space.OrganizationAgent.Name
	}
	return ""
}

// preview is the start of body on one line, cut at a word where it can be
// and marked as cut, in at most previewRunes characters.
func preview(body string) string {
	line := strings.Join(strings.Fields(body), " ")
	runes := []rune(line)
	if len(runes) <= previewRunes {
		return line
	}
	// The word break is looked for in runes, as the length is: a byte
	// offset would cut Hindi or emoji to a fraction of the length.
	cut := runes[:previewRunes-1]
	for index := len(cut) - 1; index > previewRunes/2; index-- {
		if cut[index] == ' ' {
			cut = cut[:index]
			break
		}
	}
	return strings.TrimRight(string(cut), " ,;:-") + "…"
}

// space resolves the teamspace and refuses anybody the team does not include.
// An organization admin who is not on the team may manage its teamspace, and
// teamspaces.Service lets them read it to do so, but what the team says in
// its room is the team's: they read none of it and say nothing there. A
// paused teamspace's room is still resolved, for its team to read; Post is
// what refuses it.
func (service *Service) space(ctx context.Context, actor Actor, roomID string) (teamspaces.Space, error) {
	space, err := service.spaces.Get(ctx, teamspaces.Actor{
		OrganizationID: actor.OrganizationID, UserID: actor.UserID, AccessToken: actor.AccessToken,
	}, strings.TrimSpace(roomID))
	switch {
	case errors.Is(err, teamspaces.ErrNotFound):
		return teamspaces.Space{}, ErrNotFound
	case errors.Is(err, teamspaces.ErrForbidden):
		return teamspaces.Space{}, ErrForbidden
	case err != nil:
		return teamspaces.Space{}, err
	case !space.IsMember:
		return teamspaces.Space{}, fmt.Errorf("%w: only a member of its team is in a room", ErrForbidden)
	}
	return space, nil
}

// situation gathers what anybody judging or answering in this room is shown.
func (service *Service) situation(ctx context.Context, space teamspaces.Space) (Situation, []Seat, error) {
	brief, err := service.store.Brief(ctx, space.ID)
	if err != nil {
		return Situation{}, nil, err
	}
	recent, err := service.store.Recent(ctx, space.ID, recentMessages)
	if err != nil {
		return Situation{}, nil, err
	}
	watching, err := service.store.Watching(ctx, space.ID, time.Now().UTC().Add(-presentWithin))
	if err != nil {
		return Situation{}, nil, err
	}
	seats := seatsOf(space, watching)
	return Situation{
		RoomID: space.ID, OrganizationID: space.OrganizationID,
		Name: space.Name, Purpose: space.Description, Seats: seats, Brief: brief, Recent: recent,
	}, seats, nil
}

// publish lifts a conclusion into the teamspace's ledger, where the
// organization agent will read it. A room that decides something has
// published it by deciding it.
func (service *Service) publish(ctx context.Context, actor Actor, space teamspaces.Space, message Message, verdict Verdict) {
	author := whispererAgent(space, actor.UserID)
	if author == "" {
		// Somebody with no agent of their own said it. The ledger records
		// what an agent published, so there is nothing to attribute it to.
		return
	}
	kind := teamspaces.EntryUpdate
	switch verdict.Kind {
	case "decision":
		kind = teamspaces.EntryDecision
	case "commitment", "fact":
		kind = teamspaces.EntryResult
	}
	if _, err := service.ledger.PublishEntry(ctx, teamspaces.Actor{
		OrganizationID: actor.OrganizationID, UserID: actor.UserID, AccessToken: actor.AccessToken,
	}, space.ID, teamspaces.PublishEntryInput{
		AuthorAgentID: author, Kind: kind,
		Title:   firstLine(message.Body),
		Summary: message.Body,
	}); err != nil {
		service.logger.WarnContext(ctx, "publish a room's conclusion to its ledger",
			"room_id", space.ID, "seq", message.Seq, "error", err)
	}
}

// compact rewrites the brief when the room has moved past it. It runs after
// the member already has their answer, and a failure only means the brief is
// older than it could be.
func (service *Service) compact(ctx context.Context, situation Situation, roomID string, decided bool, latestSeq int64) {
	if service.compactor == nil || !Due(situation.Brief, latestSeq, decided) {
		return
	}
	since, err := service.store.Messages(ctx, roomID, situation.Brief.ThroughSeq, recentMessages*4)
	if err != nil || len(since) == 0 {
		return
	}
	brief, err := service.compactor.Compact(ctx, situation, since)
	if err != nil {
		service.logger.WarnContext(ctx, "compact a room", "room_id", roomID, "error", err)
		return
	}
	brief.RoomID = roomID
	if _, err := service.store.SaveBrief(ctx, brief, situation.Brief.Revision); err != nil {
		service.logger.WarnContext(ctx, "save a room's brief", "room_id", roomID, "error", err)
	}
}

// seatsOf turns a teamspace into the room's seating: everybody on the team,
// the agent that speaks for each of them, and the specialists their owners
// deliberately contributed.
func seatsOf(space teamspaces.Space, watching []string) []Seat {
	present := make(map[string]bool, len(watching))
	for _, userID := range watching {
		present[userID] = true
	}
	taken := map[string]bool{}
	seats := make([]Seat, 0, len(space.Whisperers)*2+len(space.Contributions))
	for _, whisperer := range space.Whisperers {
		seats = append(seats, Seat{
			Handle: handle(whisperer.UserDisplayName, taken), Kind: SeatPerson,
			UserID: whisperer.UserID, DisplayName: whisperer.UserDisplayName,
			Present: present[whisperer.UserID],
		})
		seats = append(seats, Seat{
			Handle: handle(whisperer.AgentName, taken), Kind: SeatAgent,
			UserID: whisperer.UserID, AgentID: whisperer.AgentID, AgentName: whisperer.AgentName,
			DisplayName: whisperer.AgentName, SpeaksFor: whisperer.UserID,
			Role: whisperer.UserDisplayName + "'s own agent",
		})
	}
	for _, contribution := range space.Contributions {
		if contribution.Status != "active" {
			continue
		}
		seats = append(seats, Seat{
			Handle: handle(contribution.Name, taken), Kind: SeatAgent,
			UserID: contribution.OwnerUserID, AgentID: contribution.AgentID, AgentName: contribution.Name,
			DisplayName: contribution.Name, Role: contribution.Role,
		})
	}
	return seats
}

// handle makes an addressable name, and never the same one twice: two seats
// sharing a handle would make two speakers indistinguishable to everybody,
// including the agents reading the room.
func handle(name string, taken map[string]bool) string {
	var cleaned strings.Builder
	for _, symbol := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case symbol >= 'a' && symbol <= 'z', symbol >= '0' && symbol <= '9':
			cleaned.WriteRune(symbol)
		case symbol == ' ' || symbol == '-' || symbol == '_':
			if cleaned.Len() > 0 && !strings.HasSuffix(cleaned.String(), "-") {
				cleaned.WriteRune('-')
			}
		}
	}
	base := strings.Trim(cleaned.String(), "-")
	if base == "" {
		base = "someone"
	}
	candidate := base
	for suffix := 2; taken[candidate]; suffix++ {
		candidate = fmt.Sprintf("%s-%d", base, suffix)
	}
	taken[candidate] = true
	return candidate
}

func personSeat(seats []Seat, userID string) (Seat, bool) {
	for _, seat := range seats {
		if seat.Kind == SeatPerson && seat.UserID == userID {
			return seat, true
		}
	}
	return Seat{}, false
}

// askingFor is ctx saying who asks, for a seat's turn that files what it
// cannot answer from the room with its own member (agents.WithRoomAsker):
// the member who posted, through their own agent in the room, with the token
// of the session they posted in, as a request they made there themself
// would be.
func askingFor(ctx context.Context, actor Actor, space teamspaces.Space) context.Context {
	for _, whisperer := range space.Whisperers {
		if whisperer.UserID == actor.UserID {
			return agents.WithRoomAsker(ctx, agents.RoomAsker{
				RoomID: space.ID, OrganizationID: actor.OrganizationID, UserID: actor.UserID,
				// A whisperer is always its member's primary agent.
				AgentID: whisperer.AgentID, AgentName: whisperer.AgentName, AgentKind: string(managedagents.KindPrimary),
			}, actor.AccessToken)
		}
	}
	return ctx
}

// whispererAgent is the agent that speaks for a member in this space.
func whispererAgent(space teamspaces.Space, userID string) string {
	for _, whisperer := range space.Whisperers {
		if whisperer.UserID == userID {
			return whisperer.AgentID
		}
	}
	return ""
}

// firstLine is a title made from what was said, since a room has no titles.
func firstLine(body string) string {
	line := strings.TrimSpace(body)
	if cut := strings.IndexByte(line, '\n'); cut > 0 {
		line = line[:cut]
	}
	// A full stop ends a sentence only when a space or the end of the line
	// follows it. "4.50" is a price, and a title cut inside one reads as a
	// mistake to everybody who sees it in the ledger.
	for at := 0; at < len(line); at++ {
		if line[at] != '.' && line[at] != '!' && line[at] != '?' {
			continue
		}
		if at > 0 && (at+1 == len(line) || line[at+1] == ' ') {
			line = line[:at]
			break
		}
	}
	if runes := []rune(strings.TrimSpace(line)); len(runes) > 80 {
		cut := strings.TrimSpace(string(runes[:80]))
		if space := strings.LastIndexByte(cut, ' '); space > 40 {
			cut = cut[:space]
		}
		return strings.TrimRight(cut, " ,;:-") + "…"
	}
	return strings.TrimSpace(line)
}

// handles is the room's seating as one line, for the log: who could have
// answered, in the form a message would have to name them.
func handles(seats []Seat) string {
	names := make([]string, 0, len(seats))
	for _, seat := range seats {
		names = append(names, string(seat.Kind)[0:1]+":@"+seat.Handle)
	}
	return strings.Join(names, " ")
}
