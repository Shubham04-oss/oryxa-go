package guardpolicy

import (
	"context"
	"errors"
	"fmt"
)

// ErrInvalid is what a refused setting wraps: a level, a relevance mode or an
// id this package cannot use. It is the caller's mistake, to be told so, where
// every other error is a failure.
var ErrInvalid = errors.New("invalid answer guard setting")

// Level is how strictly the answer guard reads an answer.
type Level string

const (
	// LevelSecrets masks the kinds that are always masked (passwords, keys,
	// and card, bank and government numbers) and leaves whatever else the
	// guard finds as it was written.
	LevelSecrets Level = "secrets"
	// LevelStrict also holds what the guardrail only detects: email addresses,
	// phone numbers and addresses.
	LevelStrict Level = "strict"
)

// Valid reports whether level is one of the two levels. No choice, "", is
// not a level.
func (level Level) Valid() bool { return level == LevelSecrets || level == LevelStrict }

// reading is level as it is read where it decides something: itself when it
// is a level, and strict when it is anything else, so that a value this code
// cannot make sense of never loosens what applies.
func (level Level) reading() Level {
	if level == LevelSecrets {
		return LevelSecrets
	}
	return LevelStrict
}

// stricter is the stricter of two levels, each read as reading says.
func stricter(a, b Level) Level {
	if a.reading() == LevelSecrets && b.reading() == LevelSecrets {
		return LevelSecrets
	}
	return LevelStrict
}

// ParseLevel reads a level as a client or a policy spells it: exactly
// "secrets" or "strict".
func ParseLevel(text string) (Level, error) {
	if level := Level(text); level.Valid() {
		return level, nil
	}
	return "", fmt.Errorf("%w: a level is %q or %q", ErrInvalid, LevelSecrets, LevelStrict)
}

// Origin says whose answer is being read.
type Origin string

const (
	// OriginOwner is an answer the member started themselves.
	OriginOwner Origin = "owner"
	// OriginPeer is an answer another member reads.
	OriginPeer Origin = "peer"
)

// DefaultLevel is the level that applies to origin when a member has chosen
// none: secrets for an answer the member started themselves, strict for one
// another member reads. An origin this code does not know is read as peer.
func DefaultLevel(origin Origin) Level {
	if origin == OriginOwner {
		return LevelSecrets
	}
	return LevelStrict
}

// RelevanceMode is how the relevance check treats an answer that does not
// answer what was asked.
type RelevanceMode string

const (
	// RelevanceHold holds such an answer for review.
	RelevanceHold RelevanceMode = "hold"
	// RelevanceShadow runs the check and records what it found, but never
	// holds an answer.
	RelevanceShadow RelevanceMode = "shadow"
	// RelevanceOff does not run the check.
	RelevanceOff RelevanceMode = "off"
)

// Valid reports whether mode is one of the three modes. No choice, "", is not
// a mode.
func (mode RelevanceMode) Valid() bool {
	return mode == RelevanceHold || mode == RelevanceShadow || mode == RelevanceOff
}

// rank orders the modes by how much they do: off, then shadow, then hold.
func (mode RelevanceMode) rank() int {
	switch mode {
	case RelevanceHold:
		return 2
	case RelevanceShadow:
		return 1
	default:
		return 0
	}
}

// ParseRelevanceMode reads a mode as a policy or a deployment spells it:
// exactly "hold", "shadow" or "off".
func ParseRelevanceMode(text string) (RelevanceMode, error) {
	if mode := RelevanceMode(text); mode.Valid() {
		return mode, nil
	}
	return "", fmt.Errorf("%w: a relevance mode is %q, %q or %q", ErrInvalid, RelevanceHold, RelevanceShadow, RelevanceOff)
}

// OrgPolicy is an organization's answer guard settings, from its workspace
// policy. The zero value says nothing: no minimum, and no relevance mode of
// its own.
type OrgPolicy struct {
	// MinimumOwner and MinimumPeer are the least strict levels the
	// organization lets a member use, for an answer they started themselves
	// and for one another member reads. Empty is no minimum, which is secrets:
	// the lowest level there is.
	MinimumOwner, MinimumPeer Level
	// Relevance is how the organization has the relevance check treat an
	// answer. Empty is no choice of its own: hold, unless the deployment's Cap
	// says less. A mode this code does not know is read as empty.
	Relevance RelevanceMode
	// Regulated says the organization runs the regulated preset. It is only
	// carried here, to whoever reads Effective or View: what a regulated
	// organization is owed that another is not is theirs to say.
	Regulated bool
}

// OrgPolicies reads an organization's OrgPolicy. The workspace policy is what
// implements it, through a small adapter; a deployment without the workspace
// service uses NoOrgPolicies.
type OrgPolicies interface {
	Get(ctx context.Context, organizationID string) (OrgPolicy, error)
}

// NoOrgPolicies is the policy source of a deployment without the workspace
// service: no organization says anything, so every one gets the defaults. It
// is the explicit choice, and reports nothing; a nil OrgPolicies gets the same
// defaults but is logged, since it is more likely a mistake than a choice.
type NoOrgPolicies struct{}

// Get reports that organizationID says nothing.
func (NoOrgPolicies) Get(context.Context, string) (OrgPolicy, error) { return OrgPolicy{}, nil }

// Cap is what a deployment allows of the relevance check, whatever an
// organization's policy asks: a ceiling, set while the check is being proven,
// so that it holds nobody's answers before it has earned that.
type Cap struct {
	// Relevance is the most the check may do. Empty is no ceiling, and so is
	// hold, since hold is the most there is. Shadow lets the check run and be
	// recorded but never hold an answer; off stops it. A mode this code does
	// not know is read as shadow.
	Relevance RelevanceMode
	// HoldOrgs are the organizations, by id, the ceiling does not apply to.
	HoldOrgs map[string]bool
}

// Choices are what a member chose for each origin. Empty is no choice: the
// default applies.
type Choices struct {
	Owner, Peer Level
}

// Effective is what applies to a member's answers.
type Effective struct {
	// Owner is the level for an answer the member started themselves, and
	// Peer the level for one another member reads.
	Owner, Peer Level
	// Relevance is how the relevance check treats an answer, once the
	// deployment's Cap has been applied.
	Relevance RelevanceMode
	// Regulated is OrgPolicy.Regulated.
	Regulated bool
}

// For is the level that applies to origin. An origin this code does not know
// is read as peer.
func (effective Effective) For(origin Origin) Level {
	if origin == OriginOwner {
		return effective.Owner
	}
	return effective.Peer
}

// OriginView is how one origin's level came to be.
type OriginView struct {
	// Chosen is what the member chose, or "" when they have not.
	Chosen Level
	// Default is what applies when they have not.
	Default Level
	// Floor is the organization's minimum: the level the choice or the default
	// cannot go below.
	Floor Level
	// Effective is what applies: the stricter of Floor and the choice, or the
	// default when there is none. A Chosen below Floor is kept, and Floor
	// wins here.
	Effective Level
}

// View is everything a member is told about their settings.
type View struct {
	Owner, Peer OriginView
	// Relevance is how the relevance check treats an answer, once the
	// deployment's Cap has been applied.
	Relevance RelevanceMode
	// Regulated is OrgPolicy.Regulated.
	Regulated bool
}

// Effective is the View reduced to what applies.
func (view View) Effective() Effective {
	return Effective{Owner: view.Owner.Effective, Peer: view.Peer.Effective, Relevance: view.Relevance, Regulated: view.Regulated}
}

// Resolve is the rule this package exists for. For each origin what applies is
// the stricter of the organization's minimum (secrets when it has none) and
// the member's choice (the origin's default when they made none). The relevance
// mode is the organization's (hold when it has none), held to the ceiling in
// deployment unless organizationID is one the ceiling does not apply to.
//
// A level or a mode this code does not know is never allowed to loosen
// anything: an unknown minimum or choice is read as strict, an unknown
// relevance mode as no choice of the organization's own, and an unknown
// ceiling as shadow.
func Resolve(organizationID string, org OrgPolicy, chosen Choices, deployment Cap) View {
	return View{
		Owner:     resolveOrigin(OriginOwner, org.MinimumOwner, chosen.Owner),
		Peer:      resolveOrigin(OriginPeer, org.MinimumPeer, chosen.Peer),
		Relevance: resolveRelevance(organizationID, org.Relevance, deployment),
		Regulated: org.Regulated,
	}
}

func resolveOrigin(origin Origin, minimum, chosen Level) OriginView {
	floor := LevelSecrets
	if minimum != "" {
		floor = minimum.reading()
	}
	view := OriginView{Default: DefaultLevel(origin), Floor: floor}
	base := view.Default
	if chosen != "" {
		view.Chosen = chosen.reading()
		base = view.Chosen
	}
	view.Effective = stricter(floor, base)
	return view
}

func resolveRelevance(organizationID string, asked RelevanceMode, deployment Cap) RelevanceMode {
	mode := asked
	if !mode.Valid() {
		mode = RelevanceHold
	}
	if deployment.Relevance == "" || deployment.HoldOrgs[organizationID] {
		return mode
	}
	ceiling := deployment.Relevance
	if !ceiling.Valid() {
		ceiling = RelevanceShadow
	}
	if mode.rank() > ceiling.rank() {
		return ceiling
	}
	return mode
}
