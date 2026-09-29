package agents

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxPinnedTeamspaceRunes bounds the id a run is pinned to. A teamspace's own
// id is far shorter; this only stops a caller sending something that is not
// one.
const maxPinnedTeamspaceRunes = 128

// A run can be pinned to one teamspace: a side conversation's own-agent run,
// whose prompt names the teamspace it is about, and whose tools must then
// work in that teamspace and no other. Without a pin the model names the
// teamspace, and a model that names another one, by mistake or because words
// somebody wrote in the prompt told it to, would file a request there, read
// its ledger or publish to it, as its member.
//
// The pin is part of one run and nothing else. Whoever starts the run sets it
// on the run's context (WithPinnedTeamspace); it is never written to the
// thread's state, so the next run of the thread has none, and the model
// neither reads it nor writes it: a tool that names a different teamspace is
// refused without saying which one the run is pinned to.
//
// The pin grants nothing. It only narrows what the model may name; the
// services under the tools still check the live seat and team membership of
// the member the run acts for, exactly as they do without it.

// pinnedTeamspaceKey is the context key of a run's pin.
type pinnedTeamspaceKey struct{}

// WithPinnedTeamspace is ctx for one run that works in the teamspace id and no
// other: interactions_create, and the teamspaces tools, act in it whatever
// teamspace the model names, or refuse. An empty id pins nothing. Callers
// validate it first (CleanTeamspacePin).
func WithPinnedTeamspace(ctx context.Context, id string) context.Context {
	id = strings.TrimSpace(id)
	if id == "" {
		return ctx
	}
	return context.WithValue(ctx, pinnedTeamspaceKey{}, id)
}

// PinnedTeamspace is the teamspace WithPinnedTeamspace pinned the run of ctx
// to, if any.
func PinnedTeamspace(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(pinnedTeamspaceKey{}).(string)
	return id, ok && id != ""
}

// CleanTeamspacePin is the teamspace id a caller asked to pin a run to, as
// the pin keeps it, or the reason it cannot be one: after trimming it holds
// something, no more than 128 characters, and no whitespace.
func CleanTeamspacePin(id string) (string, error) {
	id = strings.TrimSpace(id)
	switch {
	case id == "":
		return "", errors.New("teamspace_id must not be blank; leave it out to run without one")
	case utf8.RuneCountInString(id) > maxPinnedTeamspaceRunes:
		return "", errors.New("teamspace_id holds at most 128 characters")
	case strings.ContainsFunc(id, unicode.IsSpace):
		return "", errors.New("teamspace_id must not hold whitespace")
	}
	return id, nil
}

// errPinnedElsewhere is what a pinned run is told when a tool names another
// teamspace. It does not name the one the run is pinned to.
var errPinnedElsewhere = errors.New("this conversation is about one teamspace, so a tool call in it cannot act in another: leave teamspace_id out, or name the teamspace this conversation is about")

// pinnedTeamspaceFor is the teamspace a tool acts in, given the one the model
// named. An unpinned run acts in what it named, untouched, exactly as it
// always has. A pinned one acts in its pinned teamspace when the model left
// it out, and when it named that one, and refuses any other.
func pinnedTeamspaceFor(ctx context.Context, named string) (string, error) {
	pinned, ok := PinnedTeamspace(ctx)
	if !ok {
		return named, nil
	}
	switch strings.TrimSpace(named) {
	case "", pinned:
		return pinned, nil
	}
	return "", errPinnedElsewhere
}

// onlyPinned is spaces for an unpinned run, and for a pinned one just the
// teamspace it is pinned to, if it is among them.
func onlyPinned(ctx context.Context, spaces []Teamspace) []Teamspace {
	pinned, ok := PinnedTeamspace(ctx)
	if !ok {
		return spaces
	}
	kept := make([]Teamspace, 0, 1)
	for _, space := range spaces {
		if space.ID == pinned {
			kept = append(kept, space)
		}
	}
	return kept
}
