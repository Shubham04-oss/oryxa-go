package organizationagents

import (
	"context"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/teamspaces"
)

// Relevance decides whether a shared publication belongs in the
// organization's own context. A working group publishes the way people talk:
// a decision, then three messages agreeing with it, then a note about the
// coffee machine. Each publication otherwise costs a synthesis run and a new
// revision of what everyone reads, so the chatter is set aside here — it
// stays in the teamspace's ledger, where the people who wrote it can see it,
// and never reaches the organization agent.
type Relevance interface {
	// OrganizationRelevant answers whether entry should advance the shared
	// synthesis, and why not when it should not.
	OrganizationRelevant(ctx context.Context, entry teamspaces.Entry) (bool, string, error)
}

// substantialUpdateRunes is how much an update has to say, when it carries no
// specifics of its own, before it is treated as more than acknowledgement.
const substantialUpdateRunes = 140

// ByShape reads the publication itself: what it was published as, and whether
// it says anything a reader could act on. It asks nothing of a model, so it
// costs nothing and cannot fail.
//
// A decision, result, handoff or artifact is always organization context:
// publishing one is the deliberate act the teamspace exists for. An update is
// the kind people reach for when they are talking, so it counts only when it
// carries a figure — a date, a version, an amount — or says enough to stand
// on its own. One loose digit is not a figure: "+1 to that" is agreement.
type ByShape struct{}

var _ Relevance = ByShape{}

// OrganizationRelevant implements [Relevance].
func (ByShape) OrganizationRelevant(_ context.Context, entry teamspaces.Entry) (bool, string, error) {
	if entry.Kind != teamspaces.EntryUpdate {
		return true, "", nil
	}
	said := strings.TrimSpace(entry.Summary)
	if carriesFigure(said) || utf8.RuneCountInString(said) >= substantialUpdateRunes {
		return true, "", nil
	}
	return false, "a short update with nothing specific in it: kept in the teamspace, left out of organization context", nil
}

// carriesFigure reports whether said contains a number worth reading: two
// digits together, which covers dates, times, versions, counts and amounts,
// while leaving "+1" and "v2" as the asides they usually are.
func carriesFigure(said string) bool {
	digits := 0
	for _, symbol := range said {
		if unicode.IsDigit(symbol) {
			digits++
			if digits == 2 {
				return true
			}
			continue
		}
		digits = 0
	}
	return false
}
