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
	// owner. The screen is left to the end of this: it is a call to Jev, and
	// its verdict is the one fact not yet known, so the ask is taken as cleared
	// here and asked again below, with what the screen said.
	gate := facts
	gate.OwnerMember, gate.Screen = true, consentpolicy.Cleared
	if decision := consentpolicy.Decide(gate); decision.Outcome == consentpolicy.Deny {
		return router.refuseAsk(ctx, job, mention, decision.Reason)
	}
	if !slices.ContainsFunc(owners, func(owner resolved) bool { return owner.Member }) {
		// Nobody who can be asked is named. The words are not screened, and
		// telling the asker who was left out is the whole outcome of the job.
		left := make([]leftOut, 0, len(owners))
		for _, owner := range owners {
			left = append(left, leftOut{person: owner, reason: consentpolicy.OwnerNotMember})
		}
		return router.tellNotAsked(ctx, job, mention, left)
	}

	question := cleanQuestion(mention.Text, mention.BotUserID, namesOf(profiles))
	if question == "" {
		return Permanent(errors.New("the message holds no question"))
	}
	input := ScreenInput{
		Question: question,
		Asker:    ScreenAsker{OrgRole: asker.Role, Title: asker.Title},
	}
	// The screen is given the teams of the asker and of each owner, as the
	// asker's own agent would read them. An asker who has given agents no
	// standing access cannot be read as, and is told to (no_grant).
	if reason, err := router.departments(ctx, job, asker, owners, &input); err != nil {
		return err
	} else if reason != "" {
		return router.refuseAsk(ctx, job, mention, reason)
	}
	screened, err := router.screen(ctx, job, input)
	if err != nil {
		return err
	}
	facts.Screen, gate.Screen = screened.Verdict, screened.Verdict
	if decision := consentpolicy.Decide(gate); decision.Outcome == consentpolicy.Deny {
		return router.refuseAsk(ctx, job, mention, decision.Reason)
	}

	var asked []AskOwner
	var skipped []leftOut
	for _, owner := range owners {
		ownerFacts := facts
		ownerFacts.OwnerMember = owner.Member
		ownerFacts.OwnerMisfit = owner.Member && screened.Misfits[owner.Person.UserID]
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
		case decision.Outcome == consentpolicy.Deny && decision.Reason.AboutOneOwner():
			// One person left out does not refuse the rest. The asker is told
			// who was not asked, and why.
			skipped = append(skipped, leftOut{person: owner, reason: decision.Reason})
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

	// Words that ask for a credential are asked like any other, of the same
	// owners under the same policy, and filed as an ask for a secret: the
	// owner shares it as a one-time view in Oryxa, and Slack is told only that
	// it was answered privately.
	kind := AskQuestion
	if screened.AsksForSecret {
		kind = AskSecret
	}
	ask, err := router.deps.Asks.CreateAsk(ctx, AskInput{
		OrganizationID: job.OrganizationID,
		Asker:          asker.Person,
		Origin: Origin{
			TeamID: job.TeamID, ChannelID: mention.ChannelID, ThreadTS: threadOf(mention),
			TriggerTS: mention.TS, AskerSlackUserID: mention.UserID,
		},
		Question: question,
		Kind:     kind,
		Owners:   asked,
	})
	if err != nil {
		return fmt.Errorf("file the ask: %w", err)
	}
	router.recordAsk(ctx, job, ask)
	router.logger.InfoContext(ctx, "a mention became an ask",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "ask_id", ask.ID, "kind", kind,
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

// departments reads, as the asker, the teams of the asker and of each owner
// who can be asked, and puts what it finds into what the screen will judge: the
// asker's teams, and each owner whose department is known. An owner the asker
// can see on no team is not put in, and is asked: a department that is not
// listed is one that is not known, and a doubt about whether to ask a person
// resolves toward asking them.
//
// It answers a reason when the ask as a whole is refused for what it found:
// NoGrant for an asker who has given agents no standing access. The directory
// is another service's, so a read that fails is tried again by the job, and on
// the last attempt the ask is refused as one the screen could not finish, never
// asked without the check: the screen fails closed.
//
// Without a Directory nothing is read; without a Screen nothing is either,
// since the ask is refused before anything it would show.
func (router *Router) departments(ctx context.Context, job Job, asker resolved, owners []resolved, input *ScreenInput) (consentpolicy.Reason, error) {
	if router.deps.Directory == nil || router.deps.Screen == nil {
		return "", nil
	}
	ids := []string{asker.Person.UserID}
	for _, owner := range owners {
		if owner.Member {
			ids = append(ids, owner.Person.UserID)
		}
	}
	teams, err := router.deps.Directory.Teams(ctx, job.OrganizationID, asker.Person, ids)
	switch {
	case errors.Is(err, ErrNoGrant):
		router.logger.InfoContext(ctx, "the asker has given agents no standing access, so the directory cannot be read as them",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID)
		return consentpolicy.NoGrant, nil
	case err != nil && ctx.Err() != nil:
		return "", ctx.Err()
	case err != nil && job.Attempts+1 < maxAttempts:
		return "", fmt.Errorf("read the organization directory as the asker: %w", err)
	case err != nil:
		router.logger.WarnContext(ctx, "the directory could not be read on the last attempt, so the ask is refused",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "error", err)
		return consentpolicy.ScreenFailed, nil
	}
	input.Asker.Teams = teamNames(teams[asker.Person.UserID])
	for _, owner := range owners {
		if !owner.Member {
			continue
		}
		if names := teamNames(teams[owner.Person.UserID]); len(names) > 0 {
			input.Owners = append(input.Owners, ScreenOwner{UserID: owner.Person.UserID, Name: owner.Name, Title: owner.Title, Teams: names})
		}
	}
	router.logger.InfoContext(ctx, "the teams of an ask's people were read as the asker",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID,
		"asker_teams", len(input.Asker.Teams), "owners_with_teams", len(input.Owners))
	return "", nil
}

// teamNames cleans the names of teams, which a team's own administrators
// wrote, and leaves out what is empty once cleaned.
func teamNames(names []string) []string {
	var cleaned []string
	for _, name := range names {
		if name = cleanTeam(name); name != "" {
			cleaned = append(cleaned, name)
		}
	}
	return cleaned
}

// screen has the words of an ask judged and says what was found. It never
// fails the ask for the screen's own sake: a screen that could not finish, or
// was never configured, is a verdict that refuses (the zero one), and the
// caller's policy turns it into the refusal, so nobody is asked. The one thing
// it does return an error for is the job's own context ending, which is a job
// interrupted and not an ask refused.
func (router *Router) screen(ctx context.Context, job Job, input ScreenInput) (ScreenResult, error) {
	if router.deps.Screen == nil {
		router.logger.WarnContext(ctx, "no screen is configured, so a mention is refused",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID)
		return ScreenResult{}, nil
	}
	judged, err := router.deps.Screen.Judge(ctx, input)
	if err != nil {
		if ctx.Err() != nil {
			return ScreenResult{}, ctx.Err()
		}
		// What a screen says of its failure should not quote the words, and if
		// some layer of it ever does, the words are not carried on.
		router.logger.WarnContext(ctx, "the screen could not judge a mention, so it is refused",
			"inbound_id", job.InboundID, "organization_id", job.OrganizationID,
			"error", scrub(err, input.Question, "[message]"))
		return ScreenResult{}, nil
	}
	router.logger.InfoContext(ctx, "a mention was screened",
		"inbound_id", job.InboundID, "organization_id", job.OrganizationID, "verdict", judged.Verdict.String(),
		"secret", judged.AsksForSecret, "work", judged.Scores.Work, "injection", judged.Scores.Injection, "plausible", judged.Scores.Plausible,
		"credential", judged.Scores.Credential, "owners_judged", len(input.Owners), "owners_misfit", len(judged.Misfits),
		"model", judged.Model)
	return judged, nil
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

// leftOut is a named person who is not asked, and why: one of the refusals
// about one owner, which leaves that person out and refuses nobody else.
type leftOut struct {
	person resolved
	reason consentpolicy.Reason
}

// tellNotAsked tells the asker, once for each, who was named and not asked:
// someone who is not in Oryxa, or whose department plainly does not fit what
// was asked. apps writes the words from the reason.
func (router *Router) tellNotAsked(ctx context.Context, job Job, mention *Mention, left []leftOut) error {
	var failures []error
	for _, one := range left {
		params := RefusalParams{Reason: one.reason, UserID: one.person.SlackUserID}
		if err := router.refuse(ctx, job, mention, one.person.SlackUserID, params); err != nil {
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
