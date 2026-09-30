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
// The one doubt that does not is the screen of the asker's words, which fails
// closed: an ask nobody screened, or whose screen could not finish, is refused
// (the zero Screen is Unscreened), and so is a bypass.
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

	// The refusals below come from the screen of the asker's words, and from
	// what the screen needs. None of them quotes the words, and none says which
	// question of the screen was tripped more than its own name does: apps'
	// copy for injection and screen_failed should stay a plain no. A request
	// for a credential is not among them: it is asked, as a request for a
	// secret (see Screen).

	// ScreenFailed is an ask whose words could not be screened: no Jev is
	// configured, Jev did not answer in time, failed, or answered something
	// that makes no sense. Nobody is asked. The asker is told it was not their
	// doing and to try again in a moment.
	ScreenFailed Reason = "screen_failed"
	// NotWork is an ask the screen found plainly not to be work: chat, a joke,
	// a personal errand. The asker is told Oryxa passes on work questions.
	NotWork Reason = "not_work"
	// Injection is an ask the screen found to be aimed at the agents (an
	// instruction to ignore rules, to reveal, to skip approval) and not at a
	// colleague. The asker is told no more than that it cannot be passed on.
	Injection Reason = "injection"
	// Implausible is an ask the screen found plainly out of place for the asker,
	// given their role, teams and Slack title.
	Implausible Reason = "implausible"
	// NoGrant is an asker who has not given Oryxa's agent standing access, so
	// the directory cannot be read as them to see whom the question fits. The
	// asker is told to give the agent access in Settings and ask again.
	NoGrant Reason = "no_grant"
	// OwnerDepartment is a named person whose department plainly does not fit
	// the question. Like OwnerNotMember it leaves that one person out and does
	// not refuse the rest; the asker is told who was not asked, and that the
	// question looks like one for another team.
	OwnerDepartment Reason = "owner_department"
)

// AboutOneOwner reports whether the refusal is about one named person: it
// leaves that person out of the ask, and the rest are still asked. Every other
// refusal is of the ask as a whole.
func (reason Reason) AboutOneOwner() bool {
	return reason == OwnerNotMember || reason == OwnerDepartment
}

// BypassReason is why an ask went ahead without the owner's consent. It is
// recorded with the decision (decided_by {bypass, reason}), shown to the owner
// in the notice that tells them, and audited, so each value is a short code
// that names the rule and no more. They are the codes the owner's notice is
// rendered from.
type BypassReason string

const (
	// PriorityChannel is a private, unshared channel an administrator marked
	// high priority.
	PriorityChannel BypassReason = "priority_channel"
	// OrgChart is an asker above the owner in the organization chart.
	OrgChart BypassReason = "org_chart"
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

// AnswerFor says where the answer to an ask that was bypassed for reason goes.
// It is what Decide answers, so whoever posts the answer, working from the
// reason recorded with the decision, reaches the same place the router said:
// the thread for a priority channel, and for every other reason, including
// one this does not know, the asker's direct message.
func AnswerFor(reason BypassReason) AnswerTo {
	if reason == PriorityChannel {
		return InThread
	}
	return InDM
}

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

// Screen is what the screen found in the words of an ask, for Decide to read
// like any other fact. The screen is one Jev read of the words against who
// asked: is it work, is it aimed at the agents, is it plausible for this
// asker, does it ask for a credential. Reading Jev's probabilities into one of
// these values is the screen's own business; the policy only knows that an ask
// goes ahead when, and only when, the screen cleared it.
//
// Asking for a credential is not a finding that refuses: an ask for a secret
// goes to its owners like any other, filed as an ask for a secret, whose answer
// its owner shares as a one-time view in Oryxa and never in Slack. It is the
// router's to file it so, from the screen's result, and it changes nothing
// here: the same policy decides who may be asked for a secret and how.
type Screen int

const (
	// Unscreened is the zero Screen, and refuses: the words were never
	// screened, or the screen could not finish (no Jev, an error, a timeout, an
	// answer that made no sense). A screen that did not say an ask is clear has
	// not cleared it, and the same goes for a value this package has not heard
	// of.
	Unscreened Screen = iota
	// Cleared is the only Screen an ask goes ahead on.
	Cleared
	// FoundNotWork, FoundInjection and FoundImplausible are what the screen
	// found, and each refuses with the reason of the same name.
	FoundNotWork
	FoundInjection
	FoundImplausible
)

func (screen Screen) String() string {
	switch screen {
	case Cleared:
		return "cleared"
	case FoundNotWork:
		return string(NotWork)
	case FoundInjection:
		return string(Injection)
	case FoundImplausible:
		return string(Implausible)
	default:
		return "unscreened"
	}
}

// refusal is the reason an ask this Screen did not clear is refused for.
func (screen Screen) refusal() Reason {
	switch screen {
	case FoundNotWork:
		return NotWork
	case FoundInjection:
		return Injection
	case FoundImplausible:
		return Implausible
	default:
		return ScreenFailed
	}
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
	// Screen is what the screen found in the asker's words. It is the same for
	// every owner of one ask, and the zero value refuses.
	Screen Screen
	// OwnerMisfit is whether the screen judged this owner's department plainly
	// the wrong one for the question. It is false whenever the owner's
	// department is not known, or the screen was not sure: a doubt about
	// whether to ask a person resolves toward asking them.
	OwnerMisfit bool
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
//  7. words the screen did not clear, which includes words nobody screened;
//  8. an owner whose department plainly does not fit the question;
//
// and then the two ways to go ahead without consent: the channel, then
// authority. Anything else needs the owner's consent. A bypass is no way
// round any of the eight: an ask the screen refuses is refused whoever asks,
// in whatever channel, and an owner whose department does not fit is not asked
// on the authority of the asker either.
//
// The refusals through step 5 depend on nothing about the owner, so they
// are the same for every owner of one ask, and so does step 7. Steps 6 and 8
// are the ones that refuse one owner and not the rest. Step 6 comes before the
// screen because the router does not screen the words of an ask that names
// nobody who can be asked: an ask to people who are not in Oryxa is answered
// by saying so, whatever it says.
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
	case facts.Screen != Cleared:
		return deny(facts.Screen.refusal())
	case facts.OwnerMisfit:
		return deny(OwnerDepartment)
	case facts.Channel.bypasses():
		return Decision{Outcome: Allow, Bypass: Bypass{Reason: PriorityChannel, AnswerTo: AnswerFor(PriorityChannel)}}
	case facts.Outranks:
		return Decision{Outcome: Allow, Bypass: Bypass{Reason: OrgChart, AnswerTo: AnswerFor(OrgChart)}}
	}
	return Decision{Outcome: NeedsConsent}
}

func deny(reason Reason) Decision { return Decision{Outcome: Deny, Reason: reason} }
