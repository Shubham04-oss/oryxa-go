package slackbot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Linking a member's Slack account (discussion #195). Matching a Slack user to
// a member by their confirmed email trusts whoever administers the Slack
// workspace, who can set any account's email. A link trusts nobody's word: the
// member, signed in to Oryxa, signs in to Slack as themself with Sign in with
// Slack (OpenID Connect), in the same browser, and the pair Slack proves is
// kept. Changing an email moves nothing.
//
// Confirmed against Slack's documentation on 2026-10-10:
//   - https://docs.slack.dev/authentication/sign-in-with-slack/: the
//     authorize URL https://slack.com/openid/connect/authorize, with
//     response_type=code, scope openid, client_id, state, nonce,
//     redirect_uri and team; openid.connect.token takes code, client_id,
//     client_secret and redirect_uri and answers access_token and id_token;
//     Sign in with Slack's scopes are asked for in a flow of their own.
//   - https://docs.slack.dev/reference/methods/openid.connect.userInfo: POST
//     with the token as a bearer header, answering ok,
//     "https://slack.com/user_id" and "https://slack.com/team_id".

const (
	// LinkCallbackPath is where Slack sends the member's browser back after
	// Sign in with Slack. It is a redirect URL of the Slack app's.
	LinkCallbackPath = "/v1/apps/slack/link/callback"
	// openIDAuthorizeURL is Sign in with Slack's page.
	openIDAuthorizeURL = "https://slack.com/openid/connect/authorize"
)

// ErrLinkTaken means the Slack account a member signed in as is linked to
// another member of the organization: only that member, unlinking first,
// frees it.
var ErrLinkTaken = fmt.Errorf("that Slack account is linked to another member: %w", domain.ErrConflict)

// Link is a member's Slack account, as they proved it by signing in to Slack
// while signed in to Oryxa: a workspace's user, and the member they are. A
// workspace's user is one member's, and a member has one user in a workspace.
type Link struct {
	TeamID         string
	SlackUserID    string
	OrganizationID string
	MemberID       string
	LinkedAt       time.Time
}

// LinkFlow is a link in progress: a member was sent to Sign in with Slack and
// has not come back. Only the state's hash is kept, as for an install.
type LinkFlow struct {
	StateHash      string
	OrganizationID string
	MemberID       string
	// TeamID is the workspace the organization had installed when the flow
	// began: the one the member must sign in to.
	TeamID     string
	ReturnTo   string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
}

// LinkRedirectURI is the redirect every link names, registered with the Slack
// app beside the install's.
func (s *Service) LinkRedirectURI() string { return s.config.PublicBaseURL + LinkCallbackPath }

// LinkView is a member's own Slack link, as they see it.
type LinkView struct {
	Linked      bool       `json:"linked"`
	TeamID      string     `json:"team_id,omitempty"`
	SlackUserID string     `json:"slack_user_id,omitempty"`
	LinkedAt    *time.Time `json:"linked_at,omitempty"`
}

// LinkStart is where to send the member to link their Slack account: Sign in
// with Slack, with a state good for one use, for FlowLifetime.
type LinkStart struct {
	SignInURL string    `json:"sign_in_url"`
	ExpiresAt time.Time `json:"expires_at"`
}

// member answers the caller when they are a member of the organization in
// person, and not an agent: only a member links their own Slack account.
func (s *Service) member(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	caller, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	if caller.Agent {
		return domain.OrgMembership{}, fmt.Errorf("an agent cannot link a member's Slack account: %w", domain.ErrForbidden)
	}
	return caller, nil
}

// LinkStatus answers the caller's own link in the organization's workspace.
func (s *Service) LinkStatus(ctx context.Context, credentials authclient.Credentials, organizationID string) (LinkView, error) {
	caller, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return LinkView{}, err
	}
	link, err := s.store.LinkForMember(ctx, organizationID, caller.UserID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return LinkView{}, nil
	case err != nil:
		return LinkView{}, err
	}
	linkedAt := link.LinkedAt
	return LinkView{Linked: true, TeamID: link.TeamID, SlackUserID: link.SlackUserID, LinkedAt: &linkedAt}, nil
}

// StartLink makes the caller's link to Sign in with Slack, for the workspace
// their organization has installed. The state binds it to the organization,
// the member and the workspace, and works once.
func (s *Service) StartLink(ctx context.Context, credentials authclient.Credentials, organizationID, returnTo string) (LinkStart, error) {
	caller, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return LinkStart{}, err
	}
	returnTo = strings.TrimSpace(returnTo)
	if returnTo != "" && (s.config.ReturnAllowed == nil || !s.config.ReturnAllowed(returnTo)) {
		return LinkStart{}, fmt.Errorf("return_to is not an allowed page: %w", domain.ErrInvalid)
	}
	install, err := s.activeInstall(ctx, organizationID)
	if err != nil {
		return LinkStart{}, err
	}
	state, hash, err := newState(s.random)
	if err != nil {
		return LinkStart{}, err
	}
	nonce, _, err := newState(s.random)
	if err != nil {
		return LinkStart{}, err
	}
	now := s.now().UTC()
	flow := LinkFlow{
		StateHash: hash, OrganizationID: organizationID, MemberID: caller.UserID, TeamID: install.TeamID,
		ReturnTo: returnTo, CreatedAt: now, ExpiresAt: now.Add(FlowLifetime),
	}
	if err := s.store.InsertLinkFlow(ctx, flow); err != nil {
		return LinkStart{}, err
	}
	query := url.Values{
		"response_type": {"code"},
		"scope":         {"openid"},
		"client_id":     {s.config.App.ClientID},
		"state":         {state},
		"nonce":         {nonce},
		"redirect_uri":  {s.LinkRedirectURI()},
		// Signs a member who is signed in to that workspace straight in, and
		// says which one to the rest.
		"team": {install.TeamID},
	}
	return LinkStart{SignInURL: openIDAuthorizeURL + "?" + query.Encode(), ExpiresAt: flow.ExpiresAt}, nil
}

// Why a link did not finish, beside the install's FailureState,
// FailureDenied, FailureUnavailable and FailureSlackRefused: the member signed
// in to another workspace than the organization's, or as a Slack account
// another member has linked.
const (
	FailureWrongTeam = "wrong_team"
	FailureTaken     = "taken"
	// FailureSession is a sign-in that came back to a browser not signed in to
	// Oryxa as the member who began the link.
	FailureSession = "session"
)

// LinkCompletion is a link that finished.
type LinkCompletion struct {
	OrganizationID string
	MemberID       string
	TeamID         string
	SlackUserID    string
	// ReturnTo is the page to send the browser back to, checked again now, or
	// "".
	ReturnTo string
}

// CompleteLink finishes the link a callback's state names: it spends the
// state, exchanges the code (openid.connect.token), asks Slack who signed in
// (openid.connect.userInfo), and keeps the pair when it is a user of the
// workspace the flow was for, and of the organization's install still. The
// failures are InstallFailures, and name the page to go back to once the state
// is found. credentials are the browser's own Oryxa session, which must be the
// member who began the link: else anyone could send a coworker their own
// sign-in URL and have the coworker's Slack account linked to them.
func (s *Service) CompleteLink(ctx context.Context, credentials authclient.Credentials, callback InstallCallback) (LinkCompletion, error) {
	if callback.State == "" {
		return LinkCompletion{}, failure(FailureState, "", fmt.Errorf("the link carries no state: %w", domain.ErrInvalid))
	}
	flow, err := s.store.ConsumeLinkFlow(ctx, hashState(callback.State), s.now().UTC())
	if errors.Is(err, domain.ErrNotFound) {
		return LinkCompletion{}, failure(FailureState, "", fmt.Errorf("the link is unknown, used or expired: %w", domain.ErrInvalid))
	}
	if err != nil {
		return LinkCompletion{}, err
	}
	returnTo := ""
	if flow.ReturnTo != "" && s.config.ReturnAllowed != nil && s.config.ReturnAllowed(flow.ReturnTo) {
		returnTo = flow.ReturnTo
	}
	caller, err := s.member(ctx, credentials, flow.OrganizationID)
	switch {
	case errors.Is(err, domain.ErrUnavailable):
		return LinkCompletion{}, failure(FailureUnavailable, returnTo, err)
	case err != nil || caller.UserID != flow.MemberID:
		s.logger.WarnContext(ctx, "a Slack link was refused: the browser is not signed in as the member who began it",
			"organization_id", flow.OrganizationID, "member_id", flow.MemberID)
		return LinkCompletion{}, failure(FailureSession, returnTo, fmt.Errorf("sign in to Oryxa as the member who began the link: %w", domain.ErrForbidden))
	}
	switch {
	case callback.Error != "":
		if callback.Error == "access_denied" {
			return LinkCompletion{}, failure(FailureDenied, returnTo, fmt.Errorf("signing in to Slack was declined: %w", domain.ErrInvalid))
		}
		return LinkCompletion{}, failure(FailureSlackRefused, returnTo, fmt.Errorf("slack ended the sign-in with %q: %w", truncate(callback.Error, 64), domain.ErrInvalid))
	case callback.Code == "":
		return LinkCompletion{}, failure(FailureSlackRefused, returnTo, fmt.Errorf("slack brought no code: %w", domain.ErrInvalid))
	}

	// The state is spent: the link finishes even if the browser goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancel()
	token, err := s.api.OpenIDToken(ctx, s.config.App.ClientID, s.config.App.ClientSecret, callback.Code, s.LinkRedirectURI())
	if err != nil {
		return LinkCompletion{}, s.exchangeFailed(ctx, returnTo, err)
	}
	user, err := s.api.OpenIDUser(ctx, token)
	if err != nil {
		return LinkCompletion{}, s.exchangeFailed(ctx, returnTo, err)
	}
	if !teamIDPattern.MatchString(user.TeamID) || !userIDPattern.MatchString(user.UserID) {
		return LinkCompletion{}, failure(FailureSlackRefused, returnTo, fmt.Errorf("slack named no user of a workspace: %w", domain.ErrInvalid))
	}
	install, err := s.activeInstall(ctx, flow.OrganizationID)
	switch {
	case err != nil:
		return LinkCompletion{}, failure(FailureUnavailable, returnTo, err)
	case user.TeamID != flow.TeamID || user.TeamID != install.TeamID:
		s.logger.WarnContext(ctx, "a Slack link was refused: the member signed in to another workspace",
			"organization_id", flow.OrganizationID, "member_id", flow.MemberID, "team_id", truncate(user.TeamID, 32))
		return LinkCompletion{}, failure(FailureWrongTeam, returnTo, fmt.Errorf("that Slack account is not of the organization's workspace: %w", domain.ErrInvalid))
	}
	link := Link{
		TeamID: user.TeamID, SlackUserID: user.UserID, OrganizationID: flow.OrganizationID, MemberID: flow.MemberID,
		LinkedAt: s.now().UTC(),
	}
	if err := s.store.SaveLink(ctx, link); err != nil {
		if errors.Is(err, ErrLinkTaken) {
			s.logger.WarnContext(ctx, "a Slack link was refused: the Slack account is linked to another member",
				"organization_id", flow.OrganizationID, "member_id", flow.MemberID, "team_id", link.TeamID)
			return LinkCompletion{}, failure(FailureTaken, returnTo, err)
		}
		return LinkCompletion{}, failure(FailureUnavailable, returnTo, err)
	}
	s.logger.InfoContext(ctx, "a member linked their Slack account",
		"organization_id", link.OrganizationID, "member_id", link.MemberID, "team_id", link.TeamID)
	return LinkCompletion{
		OrganizationID: link.OrganizationID, MemberID: link.MemberID, TeamID: link.TeamID, SlackUserID: link.SlackUserID, ReturnTo: returnTo,
	}, nil
}

// Unlink forgets the caller's own link. One they never made is no error.
func (s *Service) Unlink(ctx context.Context, credentials authclient.Credentials, organizationID string) error {
	caller, err := s.member(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	removed, err := s.store.DeleteLink(ctx, organizationID, caller.UserID)
	if err != nil {
		return err
	}
	if removed {
		s.logger.InfoContext(ctx, "a member unlinked their Slack account", "organization_id", organizationID, "member_id", caller.UserID)
	}
	return nil
}

// linkedMembers is who each of the workspace's users is linked to, for the
// organization the install serves, and nobody for a link made for another.
func (s *Service) linkedMembers(ctx context.Context, install Install, userIDs []string) (map[string]string, error) {
	links, err := s.store.LinksForSlackUsers(ctx, install.TeamID, userIDs)
	if err != nil {
		return nil, err
	}
	members := make(map[string]string, len(links))
	for _, link := range links {
		if link.OrganizationID == install.OrganizationID {
			members[link.SlackUserID] = link.MemberID
		}
	}
	return members, nil
}

// OpenIDUser is who signed in with Slack: a user of a workspace.
type OpenIDUser struct {
	UserID string
	TeamID string
}

// OpenIDToken exchanges a Sign in with Slack code for its access token
// (openid.connect.token). The client's credentials go in the form, as the
// method documents.
func (c *Client) OpenIDToken(ctx context.Context, clientID string, clientSecret secrets.Secret, code, redirectURI string) (secrets.Secret, error) {
	body, err := c.postForm(ctx, "openid.connect.token", "", url.Values{
		"code": {code}, "redirect_uri": {redirectURI}, "client_id": {clientID}, "client_secret": {clientSecret.Reveal()},
	}, "", "")
	if err != nil {
		return "", err
	}
	var decoded struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.AccessToken == "" {
		return "", fmt.Errorf("%w: slack openid.connect.token answered no access token", domain.ErrUnavailable)
	}
	return secrets.Secret(decoded.AccessToken), nil
}

// OpenIDUser asks Slack who a Sign in with Slack token is
// (openid.connect.userInfo).
func (c *Client) OpenIDUser(ctx context.Context, token secrets.Secret) (OpenIDUser, error) {
	body, err := c.postForm(ctx, "openid.connect.userInfo", token, url.Values{}, "", "")
	if err != nil {
		return OpenIDUser{}, err
	}
	var decoded struct {
		UserID string `json:"https://slack.com/user_id"`
		TeamID string `json:"https://slack.com/team_id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		return OpenIDUser{}, fmt.Errorf("%w: slack openid.connect.userInfo answered something unreadable", domain.ErrUnavailable)
	}
	return OpenIDUser{UserID: decoded.UserID, TeamID: decoded.TeamID}, nil
}
