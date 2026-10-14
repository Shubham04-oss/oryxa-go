package v1

import "fmt"

// Actor types in the room log.
const (
	ActorMember = "member"
	ActorAgent  = "agent"
	ActorSystem = "system"
)

// Actor is who did something. OnBehalfOf is the member an agent acted for.
type Actor struct {
	Type       string `json:"type"`
	ID         string `json:"id"`
	OnBehalfOf string `json:"onBehalfOf,omitempty"`
}

// Cost is what one event cost.
type Cost struct {
	Unit   string  `json:"unit"`
	Amount float64 `json:"amount"`
	Model  string  `json:"model,omitempty"`
}

// Event is one entry in a room's append-only log (contract §8). Run is null
// for events outside a run. Private events are visible only to their
// OnBehalfOf member.
type Event struct {
	Seq        int64          `json:"seq"`
	At         string         `json:"at"`
	Org        string         `json:"org"`
	Room       Room           `json:"room"`
	Actor      Actor          `json:"actor"`
	Run        *string        `json:"run"`
	Type       string         `json:"type"`
	Summary    string         `json:"summary"`
	Data       map[string]any `json:"data"`
	DurationMs int64          `json:"durationMs"`
	Cost       *Cost          `json:"cost,omitempty"`
	Private    bool           `json:"private"`
}

// Event types this baseline writes.
const (
	EventRoomCreated        = "room.created"
	EventSandboxStarted     = "sandbox.started"
	EventSandboxExec        = "sandbox.exec"
	EventSandboxFileChanged = "sandbox.file.changed"
	EventSandboxReset       = "sandbox.reset"
	EventSandboxArtifact    = "sandbox.artifact"
	EventSandboxPreview     = "sandbox.preview"
	EventSandboxTerminal    = "sandbox.terminal"
	EventSandboxSnapshot    = "sandbox.snapshot"
	EventSandboxStopped     = "sandbox.stopped"
	EventBrowserStarted     = "browser.started"
	EventBrowserNavigate    = "browser.navigate"
	EventBrowserSnapshot    = "browser.snapshot"
	EventBrowserClick       = "browser.click"
	EventBrowserFill        = "browser.fill"
	EventBrowserScreenshot  = "browser.screenshot"
	EventBrowserHandoff     = "browser.handoff"
	EventBrowserResume      = "browser.resume"
	EventBrowserReset       = "browser.reset"
	EventBrowserStopped     = "browser.stopped"
	EventAccessDenied       = "access.denied"
)

// AppendEventRequest is POST /v1/rooms/{kind}/{id}/log: Go records an event
// of its own (a run starting or ending, say). The edge fills seq, at, org and
// room, and takes the actor from the token.
type AppendEventRequest struct {
	Type       string         `json:"type"`
	Summary    string         `json:"summary"`
	Data       map[string]any `json:"data,omitempty"`
	DurationMs int64          `json:"durationMs,omitempty"`
	Cost       *Cost          `json:"cost,omitempty"`
}

// LogResponse answers GET /v1/rooms/{kind}/{id}/log?after={seq}&run={run}.
// Next is the seq to ask after; More says there is more to read.
type LogResponse struct {
	Events []Event `json:"events"`
	Next   int64   `json:"next"`
	More   bool    `json:"more"`
}

// Child kinds a room runs.
const (
	ChildSandbox = "sandbox"
	ChildBrowser = "browser"
)

// Child is one sandbox or browser a room runs (A3).
type Child struct {
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Status     string `json:"status"`
	Private    bool   `json:"private,omitempty"`
	Member     string `json:"member,omitempty"`
	CreatedAt  string `json:"createdAt"`
	LastUsedAt string `json:"lastUsedAt"`
}

// ChildrenResponse answers GET /v1/rooms/{kind}/{id}/children.
type ChildrenResponse struct {
	Children []Child `json:"children"`
}

// RunPrefix is a run's R2 prefix (contract §7):
// runs/{orgId}/{roomKind}/{roomId}/{runId}/. Events outside a run go under
// the run id "_room".
func RunPrefix(orgID string, room Room, runID string) (string, error) {
	if !ValidID(orgID) {
		return "", fmt.Errorf("%w: organization id", ErrInvalidName)
	}
	if err := room.Validate(); err != nil {
		return "", err
	}
	if runID != RoomRun && !ValidID(runID) {
		return "", fmt.Errorf("%w: run id", ErrInvalidName)
	}
	return "runs/" + orgID + "/" + string(room.Kind) + "/" + room.ID + "/" + runID + "/", nil
}

// RoomRun is the run id R2 keys use for room events that belong to no run.
const RoomRun = "_room"
