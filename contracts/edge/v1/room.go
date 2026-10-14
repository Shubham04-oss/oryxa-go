// Package v1 holds the live-layer contracts between the Go agents on AWS and
// the oryxa-edge Worker on Cloudflare: room naming, the edge token, the
// sandbox and browser APIs, the room log and the R2 layout. The plan is in
// docs/design/live-layer/01-contracts.md; the TypeScript twin of these types
// is edge/src/contracts, and both sides test against the JSON fixtures in
// testdata, so a change on one side that the other doesn't make fails CI.
//
// Field names are camelCase on the wire, as the plan pins them.
package v1

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// RoomKind is the kind of place work happens in.
type RoomKind string

const (
	// RoomThread is a member's chat with their agent; its id is the thread id.
	RoomThread RoomKind = "thread"
	// RoomTeamspace is a teamspace room; its id is the room id.
	RoomTeamspace RoomKind = "teamspace"
	// RoomDM is a direct message; its id is the DM id.
	RoomDM RoomKind = "dm"
	// RoomSide is a side conversation; its id is the side conversation id.
	RoomSide RoomKind = "side"
)

// Valid reports whether kind is one the edge knows.
func (kind RoomKind) Valid() bool {
	switch kind {
	case RoomThread, RoomTeamspace, RoomDM, RoomSide:
		return true
	}
	return false
}

// Room names one room.
type Room struct {
	Kind RoomKind `json:"kind"`
	ID   string   `json:"id"`
}

// idPattern bounds every identifier that becomes part of a Durable Object
// name, a URL path or an R2 key, so none can smuggle a separator in.
var idPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// ErrInvalidName means an identifier can't be used in a name, path or key.
var ErrInvalidName = errors.New("invalid live-layer identifier")

// ValidID reports whether id may appear in a DO name, path or R2 key.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// Validate reports whether room names a known kind and a usable id.
func (room Room) Validate() error {
	if !room.Kind.Valid() {
		return fmt.Errorf("%w: room kind %q", ErrInvalidName, room.Kind)
	}
	if !ValidID(room.ID) {
		return fmt.Errorf("%w: room id", ErrInvalidName)
	}
	return nil
}

// SharedSandbox is the room's shared sandbox: what a conversation's
// sandbox_* tools use today.
const SharedSandbox = "shared"

// PrivateSandbox is the name of member's private sandbox in a room. Only a
// token for that member with private set reaches it.
func PrivateSandbox(memberID string) string { return "private-" + memberID }

// ValidSandboxName reports whether name is "shared", "private-{member}" or a
// run id.
func ValidSandboxName(name string) bool {
	if name == SharedSandbox {
		return true
	}
	if member, ok := strings.CutPrefix(name, "private-"); ok {
		return ValidID(member)
	}
	return ValidID(name)
}

// RoomDOName is the RoomDO's name: org/{orgId}/room/{kind}/{id}.
func RoomDOName(orgID string, room Room) (string, error) {
	if !ValidID(orgID) {
		return "", fmt.Errorf("%w: organization id", ErrInvalidName)
	}
	if err := room.Validate(); err != nil {
		return "", err
	}
	return "org/" + orgID + "/room/" + string(room.Kind) + "/" + room.ID, nil
}

// SandboxDOName is org/{orgId}/room/{kind}/{id}/sandbox/{name}.
func SandboxDOName(orgID string, room Room, name string) (string, error) {
	base, err := RoomDOName(orgID, room)
	if err != nil {
		return "", err
	}
	if !ValidSandboxName(name) {
		return "", fmt.Errorf("%w: sandbox name", ErrInvalidName)
	}
	return base + "/sandbox/" + name, nil
}

// BrowserDOName is org/{orgId}/room/{kind}/{id}/browser/{sessionId}.
func BrowserDOName(orgID string, room Room, sessionID string) (string, error) {
	base, err := RoomDOName(orgID, room)
	if err != nil {
		return "", err
	}
	if !ValidID(sessionID) {
		return "", fmt.Errorf("%w: browser session id", ErrInvalidName)
	}
	return base + "/browser/" + sessionID, nil
}

// VoiceDOName is org/{orgId}/room/{kind}/{id}/voice.
func VoiceDOName(orgID string, room Room) (string, error) {
	base, err := RoomDOName(orgID, room)
	if err != nil {
		return "", err
	}
	return base + "/voice", nil
}

// RoomPath is the edge API path of a room: /v1/rooms/{kind}/{id}. The
// organization is never in the path; it comes from the token.
func RoomPath(room Room) (string, error) {
	if err := room.Validate(); err != nil {
		return "", err
	}
	return "/v1/rooms/" + string(room.Kind) + "/" + room.ID, nil
}

// SandboxPath is /v1/rooms/{kind}/{id}/sandbox/{name}.
func SandboxPath(room Room, name string) (string, error) {
	base, err := RoomPath(room)
	if err != nil {
		return "", err
	}
	if !ValidSandboxName(name) {
		return "", fmt.Errorf("%w: sandbox name", ErrInvalidName)
	}
	return base + "/sandbox/" + name, nil
}

// BrowserPath is /v1/rooms/{kind}/{id}/browser/{sessionId}.
func BrowserPath(room Room, sessionID string) (string, error) {
	base, err := RoomPath(room)
	if err != nil {
		return "", err
	}
	if !ValidID(sessionID) {
		return "", fmt.Errorf("%w: browser session id", ErrInvalidName)
	}
	return base + "/browser/" + sessionID, nil
}
