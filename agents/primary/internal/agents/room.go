package agents

import (
	"context"
	_ "embed"
	"errors"
	"strings"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/memberauth"
)

//go:embed toolboxes/room.md
var roomInstructions string

// RoomToolboxName names the toolbox a turn in a room starts with.
const RoomToolboxName = "room"

// RoomAsker is who a turn in a room files what it cannot answer for: the
// member whose message started the exchange, through their own agent in the
// room, which is the teamspace the request is made in. The room sets it on
// the turn's context (WithRoomAsker) from the session that member posted in;
// the model never names it, and it is never written to the thread.
type RoomAsker struct {
	RoomID         string
	OrganizationID string
	UserID         string
	AgentID        string
	AgentName      string
	AgentKind      string
}

type roomAskerKey struct{}

// roomAsking is a RoomAsker with the token of the session they posted in.
type roomAsking struct {
	asker RoomAsker
	token string
}

// WithRoomAsker is ctx for a turn in a room that asker's message started,
// with their own access token from the session they posted in: a request the
// turn files is made in that session, as one they made there themself would
// be. The token files that request and nothing else; it never acts for the
// member whose agent is taking the turn.
func WithRoomAsker(ctx context.Context, asker RoomAsker, token string) context.Context {
	return context.WithValue(ctx, roomAskerKey{}, roomAsking{asker: asker, token: token})
}

// RoomAskerFrom is who WithRoomAsker says a turn in a room files for, if
// anybody.
func RoomAskerFrom(ctx context.Context) (RoomAsker, bool) {
	asking, ok := ctx.Value(roomAskerKey{}).(roomAsking)
	return asking.asker, ok
}

// RoomConfig is what the room toolbox files requests with.
type RoomConfig struct {
	Service InteractionService
}

// RoomAskInput is the question a turn in a room files with its member.
type RoomAskInput struct {
	Purpose        string   `json:"purpose" jsonschema:"Why this needs your member: what the room asked that only they have."`
	Task           string   `json:"task" jsonschema:"The question, bounded and in your own words, as your member will read it. Do not put instructions for bypassing their decision here."`
	RequestedTools []string `json:"requested_tools,omitempty" jsonschema:"What answering needs of your member: apps (their connected apps, read only) and knowledge (the organization's knowledge search). Naming one does not grant it; your member's approval says which the work may use."`
}

// RoomAsked is what a turn learns of the request it filed: which it is, and
// who decides it, "owner" being the member themself, in Oryxa.
type RoomAsked struct {
	RequestID string `json:"request_id"`
	Status    string `json:"status"`
	Approval  string `json:"approval"`
}

// RoomToolbox is a turn in a room's one way past the room: it files a
// question the room cannot answer with the member whose agent is taking the
// turn, as a request from the member who asked, made in the room's teamspace
// through the request ledger, which holds it for that member unless they
// let the agent decide for itself there. It reads, decides and answers none
// of the member's requests.
func RoomToolbox(config RoomConfig) (Toolbox, error) {
	if config.Service == nil {
		return Toolbox{}, errors.New("agents: RoomToolbox requires a service")
	}
	tools := roomTools{service: config.Service}
	ask, err := functiontool.New(functiontool.Config{
		Name:        "room_ask_owner",
		Description: "Files a question you cannot answer from the room with your member, as a request from the person whose message started this exchange. Your member decides it in Oryxa, and the answer reaches that person once they have. It answers nothing itself.",
	}, tools.askOwner)
	if err != nil {
		return Toolbox{}, err
	}
	return Toolbox{
		Name: RoomToolboxName, Description: "Asking your member, from a room, for what only they have.",
		Instructions: roomInstructions,
		Toolset:      fixedTools{name: RoomToolboxName, tools: []tool.Tool{ask}},
	}, nil
}

type roomTools struct{ service InteractionService }

// askOwner files input with the member whose agent takes the turn, as a
// request from the member who asked, in the room's teamspace. Who asked, and
// where, come from the room (WithRoomAsker), never from the model; the agent
// asked is the one taking the turn.
func (tools roomTools) askOwner(ctx agent.Context, input RoomAskInput) (RoomAsked, error) {
	asking, ok := ctx.Value(roomAskerKey{}).(roomAsking)
	if !ok {
		return RoomAsked{}, errors.New("this run is not a turn in a room that somebody's message started, so it files nothing")
	}
	member, err := memberauth.MemberFromSession(ctx)
	if err != nil {
		return RoomAsked{}, interactionError(err)
	}
	target := stateText(ctx.ReadonlyState(), AgentIDKey, "")
	switch {
	case target == "":
		return RoomAsked{}, errors.New("this conversation has no persistent agent identity to be asked")
	case asking.asker.OrganizationID != member.OrganizationID:
		return RoomAsked{}, errors.New("the room is in another organization than this conversation, so nothing is filed")
	case asking.asker.UserID == member.UserID:
		return RoomAsked{}, errors.New("your own member started this exchange: they ask you for what they hold in a conversation of their own with you, not in a room")
	}
	requester := InteractionActor{
		OrganizationID: member.OrganizationID, UserID: asking.asker.UserID,
		AgentID: asking.asker.AgentID, AgentName: asking.asker.AgentName, AgentKind: asking.asker.AgentKind,
		Session: SessionAgent,
	}
	idempotencyKey := strings.TrimSpace(ctx.InvocationID() + ":" + ctx.FunctionCallID())
	created, err := tools.service.Create(ctx, requester, asking.token, idempotencyKey, InteractionCreateInput{
		TargetAgentID: target, TeamspaceID: asking.asker.RoomID,
		Purpose: input.Purpose, Task: input.Task, RequestedTools: input.RequestedTools,
	})
	if err != nil {
		return RoomAsked{}, interactionError(err)
	}
	return RoomAsked{RequestID: created.ID, Status: created.Status, Approval: created.Approval}, nil
}
