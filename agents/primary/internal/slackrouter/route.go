package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

// appMention is the one Slack event the router turns into an ask.
const appMention = "app_mention"

// mention routes a message that mentioned the bot: who asks, who is asked,
// what the policy says, and then either a refusal to the asker or the ask.
//
// The facts the policy reads are read fresh here, for this ask: the asker and
// each owner from their Slack profile and auth's members, and the channel from
// apps as Slack has it now, so an administrator's mark that was removed, or a
// channel that was shared, a minute ago is not remembered.
func (router *Router) mention(ctx context.Context, job Job, inbound Inbound) error {
	mention := inbound.Mention
	switch {
	case mention == nil:
		return Permanent(errors.New("a message delivery carries no message"))
	case mention.EventType != "" && mention.EventType != appMention:
		return Permanent(fmt.Errorf("Slack event %q is acknowledged and dropped", mention.EventType))
	case mention.UserID == "":
		return Permanent(errors.New("the message has no human sender"))
	case mention.ChannelID == "" || mention.TS == "":
		return Permanent(errors.New("the message does not say where it was written"))
	case mention.BotUserID == "":
		return Permanent(errors.New("the message does not say which Slack user is the bot"))
	case mention.UserID == mention.BotUserID:
		return Permanent(errors.New("the message was written by the bot"))
	}

	// The people named are everyone mentioned but the bot, which the message
	// mentions to address it, and the asker, who is not asking themself.
	named := slices.DeleteFunc(mentionedUsers(mention.Text), func(id string) bool {
		return id == mention.BotUserID || id == mention.UserID
	})
	// A mention that names too many is refused whatever they are, so they are
	// not looked up.
	lookUp := []string{mention.UserID}
	if len(named) <= consentpolicy.MaxOwners {
		lookUp = append(lookUp, named...)
	}
	profiles, err := router.profiles(ctx, job, lookUp)
	if err != nil {
		return err
	}
	asker, err := router.resolver.resolve(ctx, job.OrganizationID, job.TeamID, mention.UserID, profiles)
	if err != nil {
		return err
	}
	if asker.Bot {
		// Another app mentioned this one. Answering it would start a
		// conversation between two programs.
		return Permanent(errors.New("the message was written by a bot"))
	}

	channel, err := router.channel(ctx, job, mention)
	if err != nil {
		return err
	}
	// Who is named is counted once the people are known: two Slack users who
	// are the same member, or a Slack user who is the asker under another
	// account, are one person and not a second owner.
	owners, ownersNamed := make([]resolved, 0, len(named)), len(named)
	if len(named) <= consentpolicy.MaxOwners {
		seen := map[string]bool{}
		if asker.Member {
			seen[asker.Person.UserID] = true
		}
		for _, id := range named {
			owner, err := router.resolver.resolve(ctx, job.OrganizationID, job.TeamID, id, profiles)
			if err != nil {
				return err
			}
			if owner.Member {
				if seen[owner.Person.UserID] {
					continue
				}
				seen[owner.Person.UserID] = true
			}
			owners = append(owners, owner)
		}
		ownersNamed = len(owners)
	}
	openAsks := 0
	if asker.Member {
		if openAsks, err = router.deps.Asks.CountOpenAsks(ctx, job.OrganizationID, asker.Person.UserID); err != nil {
			return fmt.Errorf("count the asker's open asks: %w", err)
		}
	}

	facts := consentpolicy.Facts{
		AskerMember: asker.Member, OwnersNamed: ownersNamed, Channel: channel, OpenAsks: openAsks,
	}
	// What refuses every owner alike refuses the ask. Asked of an owner who is
	// a member, the policy can only refuse for a reason that is not about the
	// owner.
	gate := facts
	gate.OwnerMember = true
	if decision := consentpolicy.Decide(gate); decision.Outcome == consentpolicy.Deny {
		return router.refuseAsk(ctx, job, mention, decision.Reason)
	}

	var asked []AskOwner
	var skipped []resolved
	for _, owner := range owners {
		ownerFacts := facts
		ownerFacts.OwnerMember = owner.Member
		decision := consentpolicy.Decide(ownerFacts)
		if decision.Outcome == consentpolicy.NeedsConsent {
			// Only the chart could lift this to a bypass, so only now is it read.
			outranks, err := router.outranks(ctx, job, asker.Person, owner.Person)
			if err != nil {
				return err
			}
			ownerFacts.Outranks = outranks
			decision = consentpolicy.Decide(ownerFacts)
		}
		switch {
		case decision.Outcome == consentpolicy.Deny && decision.Reason == consentpolicy.OwnerNotMember:
			// One person left out does not refuse the rest. The asker is told
			// who was not asked.
			skipped = append(skipped, owner)
		case decision.Outcome == consentpolicy.Deny:
			return router.refuseAsk(ctx, job, mention, decision.Reason)
		case decision.Outcome == consentpolicy.Allow:
			bypass := decision.Bypass
			asked = append(asked, AskOwner{Person: owner.Person, Bypass: &bypass})
		default:
			asked = append(asked, AskOwner{Person: owner.Person})
		}
	}
	if len(asked) == 0 {
		// Everyone named is left out, and telling the asker so is the whole
		// outcome of the job.
		return router.tellNotAsked(ctx, job, mention, skipped)
	}

	question := cleanQuestion(mention.Text, mention.BotUserID, namesOf(profiles))
	if question == "" {
		return Permanent(errors.New("the message holds no question"))
	}
	ask, err := router.deps.Asks.CreateAsk(ctx, AskInput{
		OrganizationID: job.OrganizationID,
		Asker:          asker.Person,
		Origin: Origin{
			TeamID: job.TeamID, ChannelID: mention.ChannelID, ThreadTS: threadOf(mention),
			TriggerTS: mention.TS, AskerSlackUserID: mention.UserID,
		},
		Question: question,
		Owners:   asked,
	})
	if err != nil {
		return fmt.Errorf("file the ask: %w", err)
	}
	router.recordAsk(ctx, job, ask)
	router.logger.InfoContext(ctx, "a mention became an ask",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", ask.ID,
		"owners", len(asked), "bypassed", countBypassed(asked), "skipped", len(skipped))
	if !router.deps.SkipAck {
		router.acknowledge(ctx, job, mention, ask, asked)
	}
	if len(skipped) > 0 {
		// The ask is made and is the job's outcome: a person who could not be
		// told who was left out has the ask all the same.
		if err := router.tellNotAsked(ctx, job, mention, skipped); err != nil {
			router.logger.WarnContext(ctx, "the asker was not told who was left out; the ask stands",
				"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", ask.ID, "error", err)
		}
	}
	return nil
}

// channel reads the conversation the ask was made in, now, and says what the
// policy needs of it.
func (router *Router) channel(ctx context.Context, job Job, mention *Mention) (consentpolicy.Channel, error) {
	info, err := router.deps.Slack.Channel(ctx, job.OrganizationID, mention.ChannelID)
	switch {
	case errors.Is(err, ErrUnreadable):
		// The bot has no scope for a group message, which is what Slack will
		// not describe to it: it is refused as one.
		return consentpolicy.Channel{GroupDM: true}, nil
	case errors.Is(err, ErrGone):
		// A channel Slack does not know cannot be told from a shared one, and
		// the answer would go into it.
		return consentpolicy.Channel{}, Permanent(fmt.Errorf("the channel is not known to Slack: %w", err))
	case err != nil:
		return consentpolicy.Channel{}, fmt.Errorf("read the channel: %w", err)
	}
	return consentpolicy.Channel{
		GroupDM:      info.MPIM || info.IM || mention.ChannelType == "mpim" || mention.ChannelType == "im",
		Shared:       info.Shared || mention.SharedExternally,
		Private:      info.Private,
		HighPriority: info.HighPriority,
	}, nil
}

// outranks asks the organization chart whether the asker is above the owner.
// A chart that could not be read is tried again, since a bypass is worth a
// short wait; on the last attempt the ask goes ahead without one, needing the
// owner's consent, which is never the unsafe way to be wrong.
func (router *Router) outranks(ctx context.Context, job Job, asker, owner Person) (bool, error) {
	if router.deps.Authority == nil {
		return false, nil
	}
	outranks, err := router.deps.Authority.Outranks(ctx, job.OrganizationID, asker, owner)
	if err == nil {
		return outranks, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if job.Attempts+1 < maxAttempts {
		return false, fmt.Errorf("read the organization chart: %w", err)
	}
	router.logger.WarnContext(ctx, "the organization chart could not be read on the last attempt; the ask needs the owner's consent",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "error", err)
	return false, nil
}

// refuseAsk tells the asker why the ask as a whole is refused.
func (router *Router) refuseAsk(ctx context.Context, job Job, mention *Mention, reason consentpolicy.Reason) error {
	params := RefusalParams{Reason: reason}
	if reason == consentpolicy.NotMember {
		params.OrganizationName = router.organizationName(ctx, job.OrganizationID)
	}
	return router.refuse(ctx, job, mention, "", params)
}

// tellNotAsked tells the asker, once for each, who was named and is not in
// Oryxa, and so was not asked.
func (router *Router) tellNotAsked(ctx context.Context, job Job, mention *Mention, skipped []resolved) error {
	var failures []error
	for _, person := range skipped {
		params := RefusalParams{Reason: consentpolicy.OwnerNotMember, UserID: person.SlackUserID}
		if err := router.refuse(ctx, job, mention, person.SlackUserID, params); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// refuse tells the asker, and only the asker, why not. apps writes the words
// from the reason. person tells one refusal about one person from another: an
// ask is refused once, and each person left out is told of once.
func (router *Router) refuse(ctx context.Context, job Job, mention *Mention, person string, params RefusalParams) error {
	key := "slackrouter:" + job.InboundID + ":" + TemplateRefusal
	if person != "" {
		key += ":" + person
	}
	err := router.deps.Slack.Post(ctx, Post{
		OrganizationID: job.OrganizationID,
		IdempotencyKey: key,
		Target:         Target{ChannelID: mention.ChannelID, ThreadTS: threadOf(mention), UserID: mention.UserID},
		Template:       TemplateRefusal,
		Params:         params,
	})
	if err != nil {
		return fmt.Errorf("tell the asker why not: %w", err)
	}
	router.logger.InfoContext(ctx, "a mention was refused",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "reason", string(params.Reason))
	return nil
}

// organizationName reads the name a refusal to a non-member gives their
// organization. It is decoration: without it the message says "this
// organization".
func (router *Router) organizationName(ctx context.Context, organizationID string) string {
	name, err := router.deps.Members.OrganizationName(ctx, organizationID)
	if err != nil {
		router.logger.WarnContext(ctx, "could not read the organization's name for a refusal",
			"organization_id", organizationID, "error", err)
		return ""
	}
	return name
}

// acknowledge tells the asker, and only the asker, who was asked. The ask is
// already made and is the job's outcome, so a message that cannot be sent
// leaves the ask standing: the owners are told by their own notice, and the
// asker only misses this line.
func (router *Router) acknowledge(ctx context.Context, job Job, mention *Mention, ask Ask, asked []AskOwner) {
	params := AskAckParams{Owners: make([]AskedPerson, 0, len(asked))}
	for _, owner := range asked {
		params.Owners = append(params.Owners, AskedPerson{UserID: owner.SlackUserID})
	}
	if router.deps.OpenURL != nil {
		params.OpenURL = router.deps.OpenURL(ask.ID)
	}
	err := router.deps.Slack.Post(ctx, Post{
		OrganizationID: job.OrganizationID,
		IdempotencyKey: "slackrouter:" + job.InboundID + ":" + TemplateAskAck,
		Target:         Target{ChannelID: mention.ChannelID, ThreadTS: threadOf(mention), UserID: mention.UserID},
		Template:       TemplateAskAck,
		Params:         params,
	})
	if err != nil {
		router.logger.WarnContext(ctx, "the asker was not told who was asked; the ask stands",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", ask.ID, "error", err)
	}
}

// recordAsk remembers that the job made its ask. Failing to is only a wider
// window for a retry to find the ask counted against its own asker.
func (router *Router) recordAsk(ctx context.Context, job Job, ask Ask) {
	if router.deps.Asked == nil {
		return
	}
	if err := router.deps.Asked.MarkAsked(ctx, job.InboundID, ask.ID); err != nil {
		router.logger.WarnContext(ctx, "could not record the ask a mention made",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", ask.ID, "error", err)
	}
}

// threadOf is the thread a mention's replies belong in: the one it is in, or
// the mention itself when it started one.
func threadOf(mention *Mention) string {
	if mention.ThreadTS != "" {
		return mention.ThreadTS
	}
	return mention.TS
}

// namesOf says what to call each user of the profiles.
func namesOf(profiles map[string]Profile) map[string]string {
	names := make(map[string]string, len(profiles))
	for id, profile := range profiles {
		names[id] = nameOf(profile)
	}
	return names
}

func countBypassed(owners []AskOwner) int {
	count := 0
	for _, owner := range owners {
		if owner.Bypass != nil {
			count++
		}
	}
	return count
}
