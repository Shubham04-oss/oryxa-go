package v1

import (
	"fmt"
	"sort"
	"strings"
)

// The membership API (A18): Go is the source of truth for who is in a room,
// and sets it on the edge with PUT /v1/rooms/{kind}/{id}/members and a
// room:members token. A member not in the list is refused, whatever their
// web session says; a member removed is refused even when their agent's
// token names them, until Go adds them again.

// MembersRequest is PUT …/members: either Members, a full replace, or Add
// and Remove, a change. Members and Add/Remove can't be mixed.
type MembersRequest struct {
	Members []string `json:"members,omitempty"`
	Add     []string `json:"add,omitempty"`
	Remove  []string `json:"remove,omitempty"`
}

// MembersResponse answers PUT and GET …/members with the room's members
// after the change, sorted.
type MembersResponse struct {
	Members []string `json:"members"`
}

// MaxRoomMembers bounds one room's member list.
const MaxRoomMembers = 1000

// DirectRoomID is the edge room id of the direct message between two
// members: their ids, sorted, joined by "_". Member ids are UUIDs, which
// have no "_", so ParseDirectRoomID can split it again.
func DirectRoomID(memberA, memberB string) (string, error) {
	members, err := pair(memberA, memberB)
	if err != nil {
		return "", err
	}
	return checkedID(members[0] + "_" + members[1])
}

// SideRoomID is the edge room id of a side conversation in a teamspace:
// the teamspace's id, then the two members' ids sorted, joined by "_". The
// members are the last two parts, so a teamspace id's own "_" (tsp_…) stays
// unambiguous.
func SideRoomID(teamspaceID, memberA, memberB string) (string, error) {
	members, err := pair(memberA, memberB)
	if err != nil {
		return "", err
	}
	if teamspaceID == "" || !ValidID(teamspaceID) {
		return "", fmt.Errorf("%w: teamspace id", ErrInvalidName)
	}
	return checkedID(teamspaceID + "_" + members[0] + "_" + members[1])
}

// ParseDirectRoomID returns a direct message room's two members.
func ParseDirectRoomID(id string) ([2]string, error) {
	parts := strings.Split(id, "_")
	if len(parts) != 2 {
		return [2]string{}, fmt.Errorf("%w: not a direct message room id", ErrInvalidName)
	}
	return checkedPair(id, "", parts[0], parts[1])
}

// ParseSideRoomID returns a side conversation room's teamspace and two
// members.
func ParseSideRoomID(id string) (string, [2]string, error) {
	parts := strings.Split(id, "_")
	if len(parts) < 3 {
		return "", [2]string{}, fmt.Errorf("%w: not a side conversation room id", ErrInvalidName)
	}
	teamspace := strings.Join(parts[:len(parts)-2], "_")
	members, err := checkedPair(id, teamspace, parts[len(parts)-2], parts[len(parts)-1])
	return teamspace, members, err
}

func checkedPair(id, teamspace, a, b string) ([2]string, error) {
	members, err := pair(a, b)
	if err != nil {
		return [2]string{}, err
	}
	// Only the canonical form: sorted, so one conversation has one room.
	want, _ := DirectRoomID(a, b)
	if teamspace != "" {
		want, _ = SideRoomID(teamspace, a, b)
	}
	if want != id {
		return [2]string{}, fmt.Errorf("%w: room id is not in canonical order", ErrInvalidName)
	}
	return members, nil
}

func pair(a, b string) ([2]string, error) {
	if a == "" || b == "" || a == b || strings.Contains(a, "_") || strings.Contains(b, "_") || !ValidID(a) || !ValidID(b) {
		return [2]string{}, fmt.Errorf("%w: a conversation needs two distinct member ids without _", ErrInvalidName)
	}
	members := []string{a, b}
	sort.Strings(members)
	return [2]string{members[0], members[1]}, nil
}

func checkedID(id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%w: room id too long", ErrInvalidName)
	}
	return id, nil
}
