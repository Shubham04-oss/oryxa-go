package slackrouter

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
)

// resolved is what became of one Slack user.
type resolved struct {
	// SlackUserID is who this is, whether or not they are a member.
	SlackUserID string
	// Member says the user is a full member of the workspace and matches a
	// member of the organization. Person is set only then.
	Member bool
	Person Person
	// Name is what to call them, whether or not they are a member.
	Name string
	// Why says, for a log and never a person, why a user is not a member.
	Why string
	// Bot is a user that is a bot: never answered, so no reply is sent.
	Bot bool
}

// resolver matches Slack users to members.
//
// A Slack user is a member of the organization when all of these hold, read
// fresh from their profile every time and never from the match kept last time:
//
//   - they belong to the installed workspace: not a guest, not a Slack
//     Connect user from another workspace, not a bot, not deactivated;
//   - Slack says they control their address; an address they typed and never
//     confirmed proves nothing about who they are;
//   - that address, without regard to case, is the sign-in address of a
//     member of the organization the workspace is installed for.
//
// The match is kept in slack_identities for whoever must go from a member to
// their Slack user, and dropped as soon as it stops holding. Nothing is
// authorized from what was kept.
type resolver struct {
	members    Members
	identities Identities
	logger     *slog.Logger
}

// resolve matches one user of the profiles apps returned. A user apps did not
// return is one Slack does not know. The error is only for what could not be
// read or kept: a person who is not a member is a result.
func (resolver *resolver) resolve(ctx context.Context, organizationID, teamID, slackUserID string, profiles map[string]Profile) (resolved, error) {
	profile, known := profiles[slackUserID]
	if !known {
		resolver.forget(ctx, teamID, slackUserID)
		return resolved{SlackUserID: slackUserID, Name: unknownName, Why: "Slack does not know that user"}, nil
	}
	name := nameOf(profile)
	if why := notAFullMember(profile, teamID); why != "" {
		resolver.forget(ctx, teamID, slackUserID)
		return resolved{SlackUserID: slackUserID, Name: name, Why: why, Bot: profile.Bot}, nil
	}
	email := strings.ToLower(strings.TrimSpace(profile.Email))
	member, err := resolver.members.ByEmail(ctx, organizationID, email)
	if errors.Is(err, ErrNotMember) {
		resolver.forget(ctx, teamID, slackUserID)
		return resolved{SlackUserID: slackUserID, Name: name, Why: "no member of the organization has that address"}, nil
	}
	if err != nil {
		return resolved{}, fmt.Errorf("match a Slack user to a member: %w", err)
	}
	if name == unknownName {
		name = cleanName(member.DisplayName)
	}
	person := Person{
		UserID: member.UserID, Email: strings.ToLower(strings.TrimSpace(member.Email)),
		Name: name, SlackUserID: slackUserID,
	}
	if err := resolver.identities.SaveIdentity(ctx, Identity{
		OrganizationID: organizationID, TeamID: teamID, SlackUserID: slackUserID,
		MemberUserID: member.UserID, Email: person.Email, DisplayName: name,
	}); err != nil {
		return resolved{}, fmt.Errorf("keep a Slack user's match to a member: %w", err)
	}
	return resolved{SlackUserID: slackUserID, Member: true, Person: person, Name: name}, nil
}

// forget drops a match that no longer holds. It is housekeeping, and nothing
// is authorized from the match it drops, so a failure to do it is logged and
// does not stop what the person asked.
func (resolver *resolver) forget(ctx context.Context, teamID, slackUserID string) {
	if err := resolver.identities.DeleteIdentity(ctx, teamID, slackUserID); err != nil {
		resolver.logger.WarnContext(ctx, "drop a Slack match that no longer holds", "team_id", teamID, "error", err)
	}
}

// notAFullMember says why a profile is not a full member of the workspace
// the bot is installed in, or "" when it is.
func notAFullMember(profile Profile, teamID string) string {
	switch {
	case profile.Bot:
		return "a bot"
	case profile.Deleted:
		return "a deactivated account"
	case profile.Guest:
		return "a guest"
	case profile.Stranger:
		return "a user from another workspace"
	case profile.TeamID != teamID:
		return "not in the installed workspace"
	case strings.TrimSpace(profile.Email) == "":
		return "no email address"
	case !profile.EmailConfirmed:
		return "an email address Slack has not confirmed"
	}
	return ""
}
