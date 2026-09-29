// Package consentpolicy decides, from facts alone, what may happen with one
// Slack ask to one owner: it may go ahead without the owner's say (a bypass),
// it needs the owner's consent, or it is refused.
//
// The decision is code, not stored policy. Nothing here reads a database, a
// clock or the network: the router reads the facts fresh, once for each ask,
// and passes them in, so the same facts always give the same answer and every
// branch can be tabled in a test. It is shaped like a Cedar request so it can
// move to internal/authz when that lands; until then it holds no policies an
// administrator could write, so a policy that fails to evaluate and silently
// stops applying cannot arise.
//
// Every doubt resolves toward asking. The zero Decision is a refusal, and a
// bypass needs every one of its conditions to hold at the moment of the ask.
package consentpolicy

const (
	// MaxOpenAsks is how many asks one asker may have open at once. A
	// mention that would make one more is refused: at most this many are
	// ever waiting on other people.
	MaxOpenAsks = 5
	// MaxOwners is how many people one mention may name. It keeps one
	// message from sending a notice to a whole channel.
	MaxOwners = 5
)

// Reason is why an ask was refused. The values are the reason codes the
// Slack refusal message is rendered from, so they are part of the contract
// with apps and never change once shipped.
type Reason string

const (
	// NotMember is an asker who is not a full member of the workspace that
	// matches a member of the organization: a guest, a bot, a deactivated
	// account, someone from another workspace, or an address nobody in the
	// organization uses.
	NotMember Reason = "not_member"
	// OwnerNotMember is a named person who is not such a member either.
	// One of several named people does not refuse the ask: the rest are
	// asked and the asker is told who was left out.
	OwnerNotMember Reason = "owner_not_member"
	// GroupDM is a mention in a group message, which Oryxa does not answer.
	GroupDM Reason = "group_dm"
	// ExternalChannel is a mention in a channel shared with another
	// organization, where the answer would reach people outside it.
	ExternalChannel Reason = "external_channel"
	// NoOwner is a mention that names nobody to ask.
	NoOwner Reason = "no_owner"
	// TooManyAsks is an asker who already has MaxOpenAsks asks open, or a
	// mention that names more than MaxOwners people.
	TooManyAsks Reason = "too_many_asks"
)

// BypassReason is why an ask went ahead without the owner's consent. It is
// recorded with the decision (decided_by {bypass, reason}) and shown to the
// owner, so each value says something a person can read back.
type BypassReason string

const (
	// HighPriorityChannel is a private, unshared channel an administrator
	// marked high priority.
	HighPriorityChannel BypassReason = "high_priority_channel"
	// AskerOutranks is an asker above the owner in the organization chart.
	AskerOutranks BypassReason = "outranks"
)

// AnswerTo is where a bypassed ask's answer goes.
type AnswerTo string

const (
	// InThread is the ask's own Slack thread: only for the marked private
	// channel, whose members the administrator chose to trust with it.
	InThread AnswerTo = "thread"
	// InDM is the asker's direct message with the bot, with a note in the
	// thread that it was answered privately. Everywhere authority alone
	// approved the ask: the owner never agreed to share the answer with
	// whoever else is in the channel.
	InDM AnswerTo = "dm"
)

// Outcome is what may happen with an ask.
type Outcome int

const (
	// Deny is the zero Outcome: an ask nothing has approved is refused.
	Deny Outcome = iota
	// NeedsConsent asks the owner, who decides.
	NeedsConsent
	// Allow lets the ask go ahead without the owner's say: a bypass.
	Allow
)

func (outcome Outcome) String() string {
	switch outcome {
	case NeedsConsent:
		return "needs_consent"
	case Allow:
		return "allow"
	default:
		return "deny"
	}
}

// Bypass is what an Allow is recorded and answered as.
type Bypass struct {
	Reason   BypassReason
	AnswerTo AnswerTo
}

// Decision is the answer for one ask to one owner.
type Decision struct {
	Outcome Outcome
	// Reason is why a Deny denies.
	Reason Reason
	// Bypass says how an Allow came about. It is empty for the others.
	Bypass Bypass
}

// Channel is what the router read of the Slack channel the ask was made in,
// fresh for this ask. A channel read a minute ago is not a channel: an
// administrator can unmark it, and anyone can share it, between two asks.
type Channel struct {
	// GroupDM is a group message between several people.
	GroupDM bool
	// Shared is a channel shared with another organization (Slack Connect),
	// whether Slack said so in the delivery or the channel read did.
	Shared bool
	// Private is a private channel.
	Private bool
	// HighPriority is the administrator's mark, kept in Oryxa.
	HighPriority bool
}

// bypasses reports whether the channel alone lets an ask go ahead: private,
// unshared and marked. Each condition is checked here as well as by the
// refusals before it, so a change to their order cannot open it.
func (channel Channel) bypasses() bool {
	return channel.Private && channel.HighPriority && !channel.Shared && !channel.GroupDM
}

// Facts is everything Decide reads: one ask to one owner.
type Facts struct {
	// AskerMember is whether the asker is a full member of the workspace
	// that matches a member of the organization.
	AskerMember bool
	// OwnersNamed is how many people the mention names to ask, members or
	// not, without the bot and without the asker.
	OwnersNamed int
	// OwnerMember is the same of this owner.
	OwnerMember bool
	Channel     Channel
	// OpenAsks is how many asks the asker has open now.
	OpenAsks int
	// Outranks is whether the asker is above this owner in the organization
	// chart. It is false whenever the chart cannot say, and in particular
	// for an asker who has given agents no standing grant to read it.
	Outranks bool
}

// Decide answers what may happen with one ask to one owner. The checks run
// in a fixed order, each refusing before anything could allow:
//
//  1. an asker who is not a member;
//  2. a group message;
//  3. a shared channel, which is never a bypass, whoever asks;
//  4. a mention that names nobody, or too many;
//  5. an asker with too many asks open;
//  6. an owner who is not a member;
//
// and then the two ways to go ahead without consent: the channel, then
// authority. Anything else needs the owner's consent.
//
// The refusals through step 5 depend on nothing about the owner, so they
// are the same for every owner of one ask. Step 6 is the only one that
// refuses one owner and not the rest.
func Decide(facts Facts) Decision {
	switch {
	case !facts.AskerMember:
		return deny(NotMember)
	case facts.Channel.GroupDM:
		return deny(GroupDM)
	case facts.Channel.Shared:
		return deny(ExternalChannel)
	case facts.OwnersNamed < 1:
		return deny(NoOwner)
	case facts.OwnersNamed > MaxOwners, facts.OpenAsks >= MaxOpenAsks:
		return deny(TooManyAsks)
	case !facts.OwnerMember:
		return deny(OwnerNotMember)
	case facts.Channel.bypasses():
		return Decision{Outcome: Allow, Bypass: Bypass{Reason: HighPriorityChannel, AnswerTo: InThread}}
	case facts.Outranks:
		return Decision{Outcome: Allow, Bypass: Bypass{Reason: AskerOutranks, AnswerTo: InDM}}
	}
	return Decision{Outcome: NeedsConsent}
}

func deny(reason Reason) Decision { return Decision{Outcome: Deny, Reason: reason} }
