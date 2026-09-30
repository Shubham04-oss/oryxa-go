package slackbot

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
)

const (
	// FlowLifetime is how long an admin has to finish the install Slack's
	// consent page starts, from the link's being made.
	FlowLifetime = 10 * time.Minute
	// authorizeURL is where the Add to Slack link goes: Slack's OAuth v2
	// consent page.
	authorizeURL = "https://slack.com/oauth/v2/authorize"
	// completeTimeout bounds finishing an install once its state is spent.
	completeTimeout = 30 * time.Second
)

// RedirectURI is the OAuth redirect registered with the Slack app, and the one
// every install names.
func (s *Service) RedirectURI() string { return s.config.PublicBaseURL + InstallCallbackPath }

// orgAdmin answers the caller when they are an admin of the organization
// themselves, and not an agent acting for one: only an admin installs the
// Slack app for an organization or says which of its channels are high
// priority.
func (s *Service) orgAdmin(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, fmt.Errorf("an organization is required: %w", domain.ErrInvalid)
	}
	caller, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, err
	}
	switch {
	case caller.Agent:
		return domain.OrgMembership{}, fmt.Errorf("an agent cannot manage an organization's Slack install: %w", domain.ErrForbidden)
	case !caller.Role.IsOrgAdmin():
		return domain.OrgMembership{}, fmt.Errorf("only an organization's admins manage its Slack install: %w", domain.ErrForbidden)
	}
	return caller, nil
}

// TeamView is a Slack workspace.
type TeamView struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// InstallView is an organization's Slack install as its admins see it, with a
// link to install it, or to install it again.
type InstallView struct {
	// Status is not_installed, active, or ended.
	Status    string    `json:"status"`
	Team      *TeamView `json:"team,omitempty"`
	BotUserID string    `json:"bot_user_id,omitempty"`
	// Scopes are what the install was granted.
	Scopes      []string   `json:"scopes,omitempty"`
	InstalledAt *time.Time `json:"installed_at,omitempty"`
	EndedAt     *time.Time `json:"ended_at,omitempty"`
	// EndReason is why an ended install ended, as the events say.
	EndReason string `json:"end_reason,omitempty"`
	// AddToSlackURL sends the workspace's admin to Slack's consent page. It
	// carries a state that is good for one use, for FlowLifetime.
	AddToSlackURL       string    `json:"add_to_slack_url"`
	AddToSlackExpiresAt time.Time `json:"add_to_slack_expires_at"`
}

// The statuses an InstallView has.
const (
	StatusNotInstalled = "not_installed"
	StatusActive       = "active"
	StatusEnded        = "ended"
)

// Install answers an organization's Slack install, and makes the link to
// install it. Only an admin asks. The link's state binds the install to the
// organization and to the admin who asked, and works once.
func (s *Service) Install(ctx context.Context, credentials authclient.Credentials, organizationID, returnTo string) (InstallView, error) {
	caller, err := s.orgAdmin(ctx, credentials, organizationID)
	if err != nil {
		return InstallView{}, err
	}
	returnTo = strings.TrimSpace(returnTo)
	if returnTo != "" && (s.config.ReturnAllowed == nil || !s.config.ReturnAllowed(returnTo)) {
		return InstallView{}, fmt.Errorf("return_to is not an allowed page: %w", domain.ErrInvalid)
	}
	view := InstallView{Status: StatusNotInstalled}
	install, err := s.store.InstallForOrganization(ctx, organizationID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
	case err != nil:
		return InstallView{}, err
	default:
		view.Status = StatusActive
		if !install.Active() {
			view.Status = StatusEnded
		}
		view.Team = &TeamView{ID: install.TeamID, Name: install.TeamName}
		view.BotUserID, view.Scopes = install.BotUserID, slices.Clone(install.Scopes)
		installedAt := install.InstalledAt
		view.InstalledAt, view.EndedAt, view.EndReason = &installedAt, install.EndedAt, install.EndReason
	}
	state, hash, err := newState(s.random)
	if err != nil {
		return InstallView{}, err
	}
	now := s.now().UTC()
	flow := InstallFlow{
		StateHash: hash, OrganizationID: organizationID, MemberID: caller.UserID, ReturnTo: returnTo,
		CreatedAt: now, ExpiresAt: now.Add(FlowLifetime),
	}
	if err := s.store.InsertInstallFlow(ctx, flow); err != nil {
		return InstallView{}, err
	}
	view.AddToSlackURL, view.AddToSlackExpiresAt = s.authorizeURL(state), flow.ExpiresAt
	return view, nil
}

// authorizeURL is Slack's consent page for the app, asking for the bot scopes
// and no user scope, and naming the one redirect the install comes back to.
func (s *Service) authorizeURL(state string) string {
	query := url.Values{
		"client_id":    {s.config.App.ClientID},
		"scope":        {strings.Join(BotScopes, ",")},
		"redirect_uri": {s.RedirectURI()},
		"state":        {state},
	}
	return authorizeURL + "?" + query.Encode()
}

// newState makes an install's state: 32 random bytes, of which only the
// SHA-256 is kept.
func newState(random io.Reader) (string, string, error) {
	raw := make([]byte, 32)
	if _, err := io.ReadFull(random, raw); err != nil {
		return "", "", fmt.Errorf("generate an install state: %w", err)
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	return state, hashState(state), nil
}

func hashState(state string) string {
	sum := sha256.Sum256([]byte(state))
	return hex.EncodeToString(sum[:])
}

// Why an install did not finish: InstallFailure.Reason, which the browser is
// told when it can be sent back.
const (
	FailureState        = "state"
	FailureDenied       = "denied"
	FailureEnterprise   = "enterprise"
	FailureRotation     = "rotation"
	FailureScopes       = "scopes"
	FailureTeamBound    = "team_bound"
	FailureOrgBound     = "organization_bound"
	FailureUnavailable  = "unavailable"
	FailureSlackRefused = "slack_refused"
)

// InstallFailure is an install that did not finish, and why. Its error is one
// of the sentinels a caller maps to a status. ReturnTo is where to send the
// browser once the state was found.
type InstallFailure struct {
	Reason   string
	ReturnTo string
	err      error
}

func (f *InstallFailure) Error() string { return f.err.Error() }
func (f *InstallFailure) Unwrap() error { return f.err }

func failure(reason, returnTo string, err error) *InstallFailure {
	return &InstallFailure{Reason: reason, ReturnTo: returnTo, err: err}
}

// InstallCallback is what Slack's redirect brought the browser back with.
type InstallCallback struct {
	State string
	// Code is the authorization code, and Error Slack's error parameter
	// (access_denied) when it sent one instead.
	Code  string
	Error string
}

// InstallCompletion is an install that finished.
type InstallCompletion struct {
	OrganizationID string
	TeamID         string
	TeamName       string
	// ReturnTo is the page to send the browser back to, checked again now, or
	// "".
	ReturnTo string
}

// CompleteInstall finishes the install a callback's state names: it spends the
// state, exchanges the code, and checks what Slack issued before keeping
// it. It refuses an Enterprise Grid install, a token that rotates and an
// install that was not granted every scope, and confirms the bot with
// auth.test. The bot token is sealed under the workspace's team id.
//
// Once the state is found, the failure names the page to send the browser back
// to. Nothing an install fails on revokes the token Slack issued: it may be
// the token another organization's install of the same workspace holds.
func (s *Service) CompleteInstall(ctx context.Context, callback InstallCallback) (InstallCompletion, error) {
	if callback.State == "" {
		return InstallCompletion{}, failure(FailureState, "", fmt.Errorf("the install link carries no state: %w", domain.ErrInvalid))
	}
	flow, err := s.store.ConsumeInstallFlow(ctx, hashState(callback.State), s.now().UTC())
	if errors.Is(err, domain.ErrNotFound) {
		return InstallCompletion{}, failure(FailureState, "", fmt.Errorf("the install link is unknown, used or expired: %w", domain.ErrInvalid))
	}
	if err != nil {
		return InstallCompletion{}, err
	}
	// Checked again: a page allowed ten minutes ago may not be now.
	returnTo := ""
	if flow.ReturnTo != "" && s.config.ReturnAllowed != nil && s.config.ReturnAllowed(flow.ReturnTo) {
		returnTo = flow.ReturnTo
	}
	switch {
	case callback.Error != "":
		if callback.Error == "access_denied" {
			return InstallCompletion{}, failure(FailureDenied, returnTo, fmt.Errorf("the install was declined at Slack: %w", domain.ErrInvalid))
		}
		return InstallCompletion{}, failure(FailureSlackRefused, returnTo, fmt.Errorf("slack ended the install with %q: %w", truncate(callback.Error, 64), domain.ErrInvalid))
	case callback.Code == "":
		return InstallCompletion{}, failure(FailureSlackRefused, returnTo, fmt.Errorf("slack brought no code: %w", domain.ErrInvalid))
	}

	// The state is spent, so this is the install's one chance: it finishes
	// even if the browser goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), completeTimeout)
	defer cancel()
	access, err := s.api.OAuthAccess(ctx, s.config.App.ClientID, s.config.App.ClientSecret, callback.Code, s.RedirectURI())
	if err != nil {
		return InstallCompletion{}, s.exchangeFailed(ctx, returnTo, err)
	}
	if problem := checkAccess(access); problem != nil {
		problem.ReturnTo = returnTo
		s.logger.WarnContext(ctx, "a Slack install was refused", "reason", problem.Reason, "team_id", truncate(access.TeamID, 32))
		return InstallCompletion{}, problem
	}
	test, err := s.api.AuthTest(ctx, access.AccessToken)
	if err != nil {
		return InstallCompletion{}, s.exchangeFailed(ctx, returnTo, err)
	}
	if test.TeamID != access.TeamID || test.UserID != access.BotUserID || test.Enterprise {
		return InstallCompletion{}, failure(FailureSlackRefused, returnTo,
			fmt.Errorf("slack's token belongs to another workspace or bot than its install said: %w", domain.ErrInvalid))
	}

	sealed, err := s.sealer.Seal(ctx, tokenBinding(access.TeamID), []byte(access.AccessToken.Reveal()))
	if err != nil {
		return InstallCompletion{}, failure(FailureUnavailable, returnTo, fmt.Errorf("%w: seal the Slack bot token: %v", domain.ErrUnavailable, err))
	}
	now := s.now().UTC()
	install := Install{
		TeamID: access.TeamID, OrganizationID: flow.OrganizationID, Status: appsv1.SlackInstallActive, TeamName: truncate(access.TeamName, 200),
		BotUserID: access.BotUserID, AppID: access.AppID, Scopes: access.Scopes, Sealed: &sealed, InstalledBy: flow.MemberID,
		InstalledAt: now, UpdatedAt: now,
	}
	err = s.store.SaveInstall(ctx, install, func(reinstalled bool) (domain.Event, error) {
		reason := appsv1.SlackInstalled
		if reinstalled {
			reason = appsv1.SlackReinstalled
		}
		return s.event(appsv1.SlackInstallChangedDetailType, appsv1.SlackInstallChanged{
			OrganizationID: install.OrganizationID, TeamID: install.TeamID, State: appsv1.SlackInstallActive, Reason: reason,
			ActorID: install.InstalledBy, ChangedAt: now,
		}, now)
	})
	switch {
	case errors.Is(err, ErrTeamBound):
		s.logger.WarnContext(ctx, "a Slack install was refused: the workspace belongs to another organization", "team_id", install.TeamID, "organization_id", install.OrganizationID)
		return InstallCompletion{}, failure(FailureTeamBound, returnTo, err)
	case errors.Is(err, ErrOrganizationBound):
		s.logger.WarnContext(ctx, "a Slack install was refused: the organization has another workspace", "team_id", install.TeamID, "organization_id", install.OrganizationID)
		return InstallCompletion{}, failure(FailureOrgBound, returnTo, err)
	case err != nil:
		return InstallCompletion{}, err
	}
	s.tokens.forget(install.TeamID)
	s.logger.InfoContext(ctx, "slack installed", "organization_id", install.OrganizationID, "team_id", install.TeamID, "member_id", install.InstalledBy)
	return InstallCompletion{OrganizationID: install.OrganizationID, TeamID: install.TeamID, TeamName: install.TeamName, ReturnTo: returnTo}, nil
}

// exchangeFailed is a call to Slack during an install that did not work.
func (s *Service) exchangeFailed(ctx context.Context, returnTo string, err error) *InstallFailure {
	var refused *Error
	if errors.As(err, &refused) {
		s.logger.WarnContext(ctx, "slack refused an install", "method", refused.Method, "error", refused.Code)
		return failure(FailureSlackRefused, returnTo, fmt.Errorf("slack refused the install (%s): %w", refused.Code, domain.ErrInvalid))
	}
	s.logger.WarnContext(ctx, "an install could not reach Slack", "error", err)
	return failure(FailureUnavailable, returnTo, fmt.Errorf("%w: slack did not answer the install", domain.ErrUnavailable))
}

// checkAccess holds what Slack issued to what v1 serves: a bot token for a
// workspace, granted every scope, that does not expire.
func checkAccess(access OAuthAccess) *InstallFailure {
	switch {
	case access.Enterprise:
		return failure(FailureEnterprise, "", fmt.Errorf("Enterprise Grid installs are not supported yet: %w", domain.ErrNotConnectable))
	case access.Rotating:
		return failure(FailureRotation, "", fmt.Errorf("the Slack app has token rotation on, which is not supported: %w", domain.ErrNotConnectable))
	case access.TokenType != "bot" || !strings.HasPrefix(access.AccessToken.Reveal(), "xoxb-") ||
		!teamIDPattern.MatchString(access.TeamID) || !userIDPattern.MatchString(access.BotUserID):
		return failure(FailureSlackRefused, "", fmt.Errorf("slack did not issue a bot token for a workspace: %w", domain.ErrInvalid))
	}
	var missing []string
	for _, scope := range BotScopes {
		if !slices.Contains(access.Scopes, scope) {
			missing = append(missing, scope)
		}
	}
	if len(missing) > 0 {
		return failure(FailureScopes, "", fmt.Errorf("the install was not granted %s: %w", strings.Join(missing, ", "), domain.ErrNotConnectable))
	}
	return nil
}

// Uninstall disconnects an organization's Slack: the app is removed from the
// workspace, best effort, and the install ends whether or not Slack
// confirms. Only an admin does it.
func (s *Service) Uninstall(ctx context.Context, credentials authclient.Credentials, organizationID string) error {
	caller, err := s.orgAdmin(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	install, err := s.store.InstallForOrganization(ctx, organizationID)
	switch {
	case errors.Is(err, domain.ErrNotFound) || (err == nil && !install.Active()):
		return fmt.Errorf("Slack is not installed for this organization: %w", domain.ErrNotFound)
	case err != nil:
		return err
	}
	if token, err := s.botToken(ctx, install); err != nil {
		s.logger.WarnContext(ctx, "the Slack bot token could not be opened to uninstall the app", "team_id", install.TeamID, "error", err)
	} else if err := s.api.Uninstall(ctx, token, s.config.App.ClientID, s.config.App.ClientSecret); err != nil {
		// Its own consequences are Slack's: the install ends here regardless.
		s.logger.WarnContext(ctx, "slack did not uninstall the app", "team_id", install.TeamID, "error", err)
	}
	now := s.now().UTC()
	_, ended, err := s.store.EndInstall(ctx, install.TeamID, appsv1.SlackRemovedByAdmin, now, func(ended Install) (domain.Event, error) {
		return s.event(appsv1.SlackInstallChangedDetailType, appsv1.SlackInstallChanged{
			OrganizationID: ended.OrganizationID, TeamID: ended.TeamID, State: appsv1.SlackInstallEnded,
			Reason: appsv1.SlackRemovedByAdmin, ActorID: caller.UserID, ChangedAt: now,
		}, now)
	})
	if err != nil {
		return err
	}
	s.tokens.forget(install.TeamID)
	if ended {
		s.logger.InfoContext(ctx, "slack disconnected", "organization_id", install.OrganizationID, "team_id", install.TeamID, "member_id", caller.UserID)
	}
	return nil
}
