package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
)

var nonSlugCharacters = regexp.MustCompile(`[^a-z0-9]+`)

type Service struct {
	store                 Store
	invitationAcceptBase  string
	emailVerificationBase string
	passwordResetBase     string
	now                   func() time.Time
}

type Config struct {
	InvitationAcceptURL  string
	EmailVerificationURL string
	PasswordResetURL     string
}

type FederatedIdentity struct {
	Subject     string
	Email       string
	DisplayName string
	AvatarURL   string
	Provider    string
}

type CreateOrganizationInput struct {
	Name string
	Slug string
}

type UpdateOrganizationInput struct {
	Name string
	Slug string
}

type InviteMemberInput struct {
	Email string
	Role  domain.Role
}

func New(store Store, invitationAcceptBase string) *Service {
	return NewWithConfig(store, Config{InvitationAcceptURL: invitationAcceptBase})
}

func NewWithConfig(store Store, config Config) *Service {
	return &Service{
		store:                 store,
		invitationAcceptBase:  strings.TrimRight(config.InvitationAcceptURL, "/"),
		emailVerificationBase: strings.TrimRight(config.EmailVerificationURL, "/"),
		passwordResetBase:     strings.TrimRight(config.PasswordResetURL, "/"),
		now:                   time.Now,
	}
}

func (service *Service) SaveLoginFlow(ctx context.Context, flow domain.LoginFlow) error {
	return service.store.SaveLoginFlow(ctx, flow)
}

func (service *Service) ConsumeLoginFlow(ctx context.Context, stateHash string) (domain.LoginFlow, error) {
	return service.store.ConsumeLoginFlow(ctx, stateHash)
}

func (service *Service) CompleteLogin(ctx context.Context, identity FederatedIdentity) (domain.User, bool, error) {
	identity.Subject = strings.TrimSpace(identity.Subject)
	identity.Email = normalizeEmail(identity.Email)
	if identity.Subject == "" || !validEmail(identity.Email) {
		return domain.User{}, false, fmt.Errorf("valid subject and email are required: %w", domain.ErrInvalid)
	}
	if identity.Provider == "" {
		identity.Provider = "Google"
	}

	now := service.now().UTC()
	user := domain.User{
		ID:          identity.Subject,
		Email:       identity.Email,
		DisplayName: strings.TrimSpace(identity.DisplayName),
		AvatarURL:   strings.TrimSpace(identity.AvatarURL),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	registered, err := newEvent(autheventsv1.UserRegisteredDetailType, autheventsv1.UserRegistered{
		UserID:       user.ID,
		Email:        user.Email,
		DisplayName:  user.DisplayName,
		RegisteredAt: now,
	}, now)
	if err != nil {
		return domain.User{}, false, err
	}
	signedIn, err := newEvent(autheventsv1.UserSignedInDetailType, autheventsv1.UserSignedIn{
		UserID:     user.ID,
		Provider:   identity.Provider,
		SignedInAt: now,
	}, now)
	if err != nil {
		return domain.User{}, false, err
	}
	created, err := service.store.UpsertFederatedUser(ctx, user, registered, signedIn)
	if err != nil {
		return domain.User{}, false, err
	}
	return user, created, nil
}

func (service *Service) SignOut(ctx context.Context, userID string) error {
	now := service.now().UTC()
	event, err := newEvent(autheventsv1.UserSignedOutDetailType, autheventsv1.UserSignedOut{
		UserID:      userID,
		SignedOutAt: now,
	}, now)
	if err != nil {
		return err
	}
	return service.store.EnqueueEvent(ctx, event)
}

func (service *Service) RequestEmailVerification(ctx context.Context, userID, email string) error {
	return service.issueAction(ctx, domain.ActionEmailVerification, userID, email)
}

func (service *Service) RequestPasswordReset(ctx context.Context, userID, email string) error {
	return service.issueAction(ctx, domain.ActionPasswordReset, userID, email)
}

func (service *Service) EmailVerification(ctx context.Context, token string) (domain.ActionToken, error) {
	return service.actionToken(ctx, domain.ActionEmailVerification, token)
}

func (service *Service) PasswordReset(ctx context.Context, token string) (domain.ActionToken, error) {
	return service.actionToken(ctx, domain.ActionPasswordReset, token)
}

func (service *Service) CompleteEmailVerification(ctx context.Context, token string, action domain.ActionToken) error {
	return service.completeAction(ctx, domain.ActionEmailVerification, token, action)
}

func (service *Service) CompletePasswordReset(ctx context.Context, token string, action domain.ActionToken) error {
	return service.completeAction(ctx, domain.ActionPasswordReset, token, action)
}

func (service *Service) ReleaseEmailVerification(ctx context.Context, token string) error {
	return service.releaseAction(ctx, domain.ActionEmailVerification, token)
}

func (service *Service) ReleasePasswordReset(ctx context.Context, token string) error {
	return service.releaseAction(ctx, domain.ActionPasswordReset, token)
}

func (service *Service) issueAction(ctx context.Context, purpose domain.ActionPurpose, userID, email string) error {
	userID = strings.TrimSpace(userID)
	email = normalizeEmail(email)
	if userID == "" || !validEmail(email) {
		return fmt.Errorf("valid user and email are required: %w", domain.ErrInvalid)
	}
	token, err := randomToken(32)
	if err != nil {
		return fmt.Errorf("create action token: %w", err)
	}
	actionID, err := randomID("act_", 12)
	if err != nil {
		return fmt.Errorf("create action id: %w", err)
	}
	now := service.now().UTC()
	expiresAt := now.Add(24 * time.Hour)
	baseURL := service.emailVerificationBase
	if purpose == domain.ActionPasswordReset {
		expiresAt = now.Add(30 * time.Minute)
		baseURL = service.passwordResetBase
	}
	if baseURL == "" {
		return errors.New("action URL is not configured")
	}
	action := domain.ActionToken{
		ID:        actionID,
		UserID:    userID,
		Email:     email,
		Purpose:   purpose,
		TokenHash: hashToken(token),
		Status:    domain.ActionPending,
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	link := tokenURL(baseURL, token)
	var event domain.Event
	if purpose == domain.ActionEmailVerification {
		event, err = newEvent(autheventsv1.EmailVerificationDeliveryRequestedDetailType, autheventsv1.EmailVerificationDeliveryRequested{
			RequestID:       action.ID,
			UserID:          userID,
			Email:           email,
			VerificationURL: link,
			ExpiresAt:       expiresAt,
		}, now)
	} else {
		event, err = newEvent(autheventsv1.PasswordResetDeliveryRequestedDetailType, autheventsv1.PasswordResetDeliveryRequested{
			RequestID: action.ID,
			UserID:    userID,
			Email:     email,
			ResetURL:  link,
			ExpiresAt: expiresAt,
		}, now)
	}
	if err != nil {
		return err
	}
	return service.store.CreateActionToken(ctx, action, event)
}

func (service *Service) actionToken(ctx context.Context, purpose domain.ActionPurpose, token string) (domain.ActionToken, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return domain.ActionToken{}, fmt.Errorf("action token is required: %w", domain.ErrInvalid)
	}
	action, err := service.store.ClaimActionToken(ctx, purpose, hashToken(token), service.now().UTC())
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ActionToken{}, domain.ErrUnauthorized
	}
	return action, err
}

func (service *Service) releaseAction(ctx context.Context, purpose domain.ActionPurpose, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return nil
	}
	return service.store.ReleaseActionToken(ctx, purpose, hashToken(token))
}

func (service *Service) completeAction(ctx context.Context, purpose domain.ActionPurpose, token string, action domain.ActionToken) error {
	now := service.now().UTC()
	var event domain.Event
	var err error
	if purpose == domain.ActionEmailVerification {
		event, err = newEvent(autheventsv1.EmailVerifiedDetailType, autheventsv1.EmailVerified{
			UserID:     action.UserID,
			Email:      action.Email,
			VerifiedAt: now,
		}, now)
	} else {
		event, err = newEvent(autheventsv1.PasswordResetDetailType, autheventsv1.PasswordReset{
			UserID:  action.UserID,
			Email:   action.Email,
			ResetAt: now,
		}, now)
	}
	if err != nil {
		return err
	}
	return service.store.CompleteActionToken(ctx, purpose, hashToken(strings.TrimSpace(token)), now, event)
}

func (service *Service) CurrentUser(ctx context.Context, userID string) (domain.User, []domain.Organization, error) {
	user, err := service.store.UserByID(ctx, userID)
	if err != nil {
		return domain.User{}, nil, err
	}
	organizations, err := service.store.OrganizationsForUser(ctx, userID)
	if err != nil {
		return domain.User{}, nil, err
	}
	return user, organizations, nil
}

func (service *Service) CreateOrganization(ctx context.Context, actorID string, input CreateOrganizationInput) (domain.Organization, error) {
	name, slug, err := organizationFields(input.Name, input.Slug)
	if err != nil {
		return domain.Organization{}, err
	}
	user, err := service.store.UserByID(ctx, actorID)
	if err != nil {
		return domain.Organization{}, err
	}

	organizationID, err := randomID("org_", 12)
	if err != nil {
		return domain.Organization{}, err
	}
	now := service.now().UTC()
	organization := domain.Organization{
		ID:        organizationID,
		Name:      name,
		Slug:      slug,
		CreatedBy: actorID,
		CreatedAt: now,
		UpdatedAt: now,
	}
	owner := domain.Membership{
		OrganizationID: organization.ID,
		UserID:         actorID,
		Email:          user.Email,
		DisplayName:    user.DisplayName,
		Role:           domain.RoleOwner,
		JoinedAt:       now,
	}
	created, err := newEvent(autheventsv1.OrganizationCreatedDetailType, autheventsv1.OrganizationCreated{
		OrganizationID: organization.ID,
		Name:           organization.Name,
		Slug:           organization.Slug,
		CreatedBy:      actorID,
		CreatedAt:      now,
	}, now)
	if err != nil {
		return domain.Organization{}, err
	}
	joined, err := newEvent(autheventsv1.MemberJoinedDetailType, autheventsv1.MemberJoined{
		OrganizationID: organization.ID,
		UserID:         actorID,
		Email:          user.Email,
		Role:           string(domain.RoleOwner),
		JoinedAt:       now,
	}, now)
	if err != nil {
		return domain.Organization{}, err
	}
	if err := service.store.CreateOrganization(ctx, organization, owner, []domain.Event{created, joined}); err != nil {
		return domain.Organization{}, err
	}
	return organization, nil
}

func (service *Service) UpdateOrganization(ctx context.Context, actorID, organizationID string, input UpdateOrganizationInput) (domain.Organization, error) {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return domain.Organization{}, concealMembershipError(err)
	}
	if actor.Role != domain.RoleOwner {
		return domain.Organization{}, domain.ErrForbidden
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return domain.Organization{}, err
	}
	name, slug, err := organizationFields(input.Name, input.Slug)
	if err != nil {
		return domain.Organization{}, err
	}
	if organization.Name == name && organization.Slug == slug {
		return organization, nil
	}
	now := service.now().UTC()
	organization.Name = name
	organization.Slug = slug
	organization.UpdatedAt = now
	event, err := newEvent(autheventsv1.OrganizationUpdatedDetailType, autheventsv1.OrganizationUpdated{
		OrganizationID: organization.ID,
		Name:           organization.Name,
		Slug:           organization.Slug,
		UpdatedBy:      actorID,
		UpdatedAt:      now,
	}, now)
	if err != nil {
		return domain.Organization{}, err
	}
	if err := service.store.UpdateOrganization(ctx, organization, event); err != nil {
		return domain.Organization{}, err
	}
	return organization, nil
}

func (service *Service) DeleteOrganization(ctx context.Context, actorID, organizationID string) error {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return concealMembershipError(err)
	}
	if actor.Role != domain.RoleOwner {
		return domain.ErrForbidden
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	deleted, err := newEvent(autheventsv1.OrganizationDeletedDetailType, autheventsv1.OrganizationDeleted{
		OrganizationID: organization.ID,
		Name:           organization.Name,
		Slug:           organization.Slug,
		DeletedBy:      actorID,
		DeletedAt:      now,
	}, now)
	if err != nil {
		return err
	}
	// Deleting an organization ends every membership in it, so each member
	// gets the same removal event as if they had been removed one by one.
	removed := func(member domain.Membership) (domain.Event, error) {
		return newEvent(autheventsv1.MemberRemovedDetailType, autheventsv1.MemberRemoved{
			OrganizationID:   organization.ID,
			OrganizationName: organization.Name,
			UserID:           member.UserID,
			Email:            member.Email,
			PreviousRole:     string(member.Role),
			Reason:           autheventsv1.MemberRemovedReasonOrganizationDeleted,
			RemovedBy:        actorID,
			RemovedAt:        now,
		}, now)
	}
	return service.store.DeleteOrganization(ctx, organizationID, deleted, removed)
}

func (service *Service) Organizations(ctx context.Context, actorID string) ([]domain.Organization, error) {
	return service.store.OrganizationsForUser(ctx, actorID)
}

func (service *Service) Organization(ctx context.Context, actorID, organizationID string) (domain.Organization, domain.Membership, error) {
	membership, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return domain.Organization{}, domain.Membership{}, concealMembershipError(err)
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return domain.Organization{}, domain.Membership{}, err
	}
	return organization, membership, nil
}

func (service *Service) Members(ctx context.Context, actorID, organizationID string) ([]domain.Membership, error) {
	if _, err := service.store.Membership(ctx, organizationID, actorID); err != nil {
		return nil, concealMembershipError(err)
	}
	return service.store.Members(ctx, organizationID)
}

func (service *Service) InviteMember(ctx context.Context, actorID, organizationID string, input InviteMemberInput) (domain.Invitation, string, error) {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return domain.Invitation{}, "", concealMembershipError(err)
	}
	if !actor.Role.CanManageMembers() {
		return domain.Invitation{}, "", domain.ErrForbidden
	}
	email := normalizeEmail(input.Email)
	if !validEmail(email) {
		return domain.Invitation{}, "", fmt.Errorf("valid invitation email is required: %w", domain.ErrInvalid)
	}
	if input.Role != domain.RoleAdmin && input.Role != domain.RoleMember {
		return domain.Invitation{}, "", fmt.Errorf("invitation role must be admin or member: %w", domain.ErrInvalid)
	}
	if actor.Role == domain.RoleAdmin && input.Role != domain.RoleMember {
		return domain.Invitation{}, "", domain.ErrForbidden
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return domain.Invitation{}, "", err
	}

	invitationID, err := randomID("inv_", 12)
	if err != nil {
		return domain.Invitation{}, "", err
	}
	token, err := randomToken(32)
	if err != nil {
		return domain.Invitation{}, "", err
	}
	now := service.now().UTC()
	invitation := domain.Invitation{
		ID:             invitationID,
		OrganizationID: organizationID,
		Email:          email,
		Role:           input.Role,
		TokenHash:      hashToken(token),
		InvitedBy:      actorID,
		Status:         domain.InvitationPending,
		ExpiresAt:      now.Add(7 * 24 * time.Hour),
		CreatedAt:      now,
	}
	acceptURL := service.invitationAcceptBase + "?token=" + url.QueryEscape(token)
	event, err := newEvent(autheventsv1.MemberInvitedDetailType, autheventsv1.MemberInvited{
		InvitationID:     invitation.ID,
		OrganizationID:   organizationID,
		OrganizationName: organization.Name,
		Email:            invitation.Email,
		Role:             string(invitation.Role),
		InvitedBy:        actorID,
		AcceptURL:        acceptURL,
		ExpiresAt:        invitation.ExpiresAt,
	}, now)
	if err != nil {
		return domain.Invitation{}, "", err
	}
	if err := service.store.CreateInvitation(ctx, invitation, event); err != nil {
		return domain.Invitation{}, "", err
	}
	return invitation, token, nil
}

func (service *Service) Invitations(ctx context.Context, actorID, organizationID string) ([]domain.Invitation, error) {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return nil, concealMembershipError(err)
	}
	if !actor.Role.CanManageMembers() {
		return nil, domain.ErrForbidden
	}
	return service.store.Invitations(ctx, organizationID)
}

func (service *Service) RevokeInvitation(ctx context.Context, actorID, organizationID, invitationID string) error {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return concealMembershipError(err)
	}
	if !actor.Role.CanManageMembers() {
		return domain.ErrForbidden
	}
	invitation, err := service.store.InvitationByID(ctx, organizationID, invitationID)
	if err != nil {
		return err
	}
	if actor.Role == domain.RoleAdmin && invitation.Role != domain.RoleMember {
		return domain.ErrForbidden
	}
	if invitation.Status != domain.InvitationPending {
		return domain.ErrConflict
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	event, err := newEvent(autheventsv1.MemberInvitationRevokedDetailType, autheventsv1.MemberInvitationRevoked{
		InvitationID:     invitation.ID,
		OrganizationID:   invitation.OrganizationID,
		OrganizationName: organization.Name,
		Email:            invitation.Email,
		Role:             string(invitation.Role),
		RevokedBy:        actorID,
		RevokedAt:        now,
	}, now)
	if err != nil {
		return err
	}
	return service.store.RevokeInvitation(ctx, organizationID, invitationID, event)
}

// AcceptInvitation joins the member to the organization whose emailed
// invitation link carries token.
func (service *Service) AcceptInvitation(ctx context.Context, actorID, token string) (domain.Membership, error) {
	user, err := service.store.UserByID(ctx, actorID)
	if err != nil {
		return domain.Membership{}, err
	}
	invitation, err := service.store.InvitationByTokenHash(ctx, hashToken(strings.TrimSpace(token)))
	if err != nil {
		return domain.Membership{}, err
	}
	if invitation.Status != domain.InvitationPending {
		return domain.Membership{}, domain.ErrConflict
	}
	if normalizeEmail(invitation.Email) != normalizeEmail(user.Email) {
		return domain.Membership{}, domain.ErrForbidden
	}
	return service.join(ctx, user, invitation.ID, invitation.OrganizationID, invitation.Role)
}

// ReceivedInvitations lists the pending invitations sent to the member's own
// email, in organizations they have not joined. Auth only signs in verified
// email addresses, so the member owns that address and needs no emailed link
// to see them: whoever signs up without following the link still finds the
// organization that invited them.
func (service *Service) ReceivedInvitations(ctx context.Context, actorID string) ([]domain.ReceivedInvitation, error) {
	user, err := service.store.UserByID(ctx, actorID)
	if err != nil {
		return nil, err
	}
	return service.store.ReceivedInvitations(ctx, user.ID, user.Email, service.now().UTC())
}

// AcceptReceivedInvitation joins the member to the organization of one of
// their ReceivedInvitations. Any other invitation is not found, so an ID
// alone tells nobody whether it exists.
func (service *Service) AcceptReceivedInvitation(ctx context.Context, actorID, invitationID string) (domain.Membership, error) {
	user, err := service.store.UserByID(ctx, actorID)
	if err != nil {
		return domain.Membership{}, err
	}
	invitations, err := service.store.ReceivedInvitations(ctx, user.ID, user.Email, service.now().UTC())
	if err != nil {
		return domain.Membership{}, err
	}
	for _, invitation := range invitations {
		if invitation.ID == invitationID {
			return service.join(ctx, user, invitation.ID, invitation.OrganizationID, invitation.Role)
		}
	}
	return domain.Membership{}, domain.ErrNotFound
}

func (service *Service) join(ctx context.Context, user domain.User, invitationID, organizationID string, role domain.Role) (domain.Membership, error) {
	now := service.now().UTC()
	membership := domain.Membership{
		OrganizationID: organizationID,
		UserID:         user.ID,
		Email:          user.Email,
		DisplayName:    user.DisplayName,
		Role:           role,
		JoinedAt:       now,
	}
	event, err := newEvent(autheventsv1.MemberJoinedDetailType, autheventsv1.MemberJoined{
		OrganizationID: membership.OrganizationID,
		UserID:         membership.UserID,
		Email:          membership.Email,
		Role:           string(membership.Role),
		JoinedAt:       now,
	}, now)
	if err != nil {
		return domain.Membership{}, err
	}
	if err := service.store.AcceptInvitation(ctx, invitationID, user, membership, event); err != nil {
		return domain.Membership{}, err
	}
	return membership, nil
}

func (service *Service) ChangeMemberRole(ctx context.Context, actorID, organizationID, userID string, replacement domain.Role) (domain.Membership, error) {
	if !replacement.Valid() {
		return domain.Membership{}, fmt.Errorf("role must be owner, admin, or member: %w", domain.ErrInvalid)
	}
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return domain.Membership{}, concealMembershipError(err)
	}
	if actor.Role != domain.RoleOwner {
		return domain.Membership{}, domain.ErrForbidden
	}
	target, err := service.store.Membership(ctx, organizationID, userID)
	if err != nil {
		return domain.Membership{}, err
	}
	if target.Role == replacement {
		return target, nil
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return domain.Membership{}, err
	}
	now := service.now().UTC()
	event, err := newEvent(autheventsv1.MemberRoleChangedDetailType, autheventsv1.MemberRoleChanged{
		OrganizationID:   organizationID,
		OrganizationName: organization.Name,
		UserID:           userID,
		Email:            target.Email,
		PreviousRole:     string(target.Role),
		Role:             string(replacement),
		ChangedBy:        actorID,
		ChangedAt:        now,
	}, now)
	if err != nil {
		return domain.Membership{}, err
	}
	if err := service.store.ChangeMemberRole(ctx, organizationID, userID, target.Role, replacement, event); err != nil {
		return domain.Membership{}, err
	}
	target.Role = replacement
	return target, nil
}

func (service *Service) RemoveMember(ctx context.Context, actorID, organizationID, userID string) error {
	actor, err := service.store.Membership(ctx, organizationID, actorID)
	if err != nil {
		return concealMembershipError(err)
	}
	target, err := service.store.Membership(ctx, organizationID, userID)
	if err != nil {
		return err
	}
	reason := autheventsv1.MemberRemovedReasonLeft
	if actorID != userID {
		if !actor.Role.CanManageMembers() {
			return domain.ErrForbidden
		}
		if actor.Role == domain.RoleAdmin && target.Role != domain.RoleMember {
			return domain.ErrForbidden
		}
		reason = autheventsv1.MemberRemovedReasonRemoved
	}
	organization, err := service.store.OrganizationByID(ctx, organizationID)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	event, err := newEvent(autheventsv1.MemberRemovedDetailType, autheventsv1.MemberRemoved{
		OrganizationID:   organizationID,
		OrganizationName: organization.Name,
		UserID:           userID,
		Email:            target.Email,
		PreviousRole:     string(target.Role),
		Reason:           reason,
		RemovedBy:        actorID,
		RemovedAt:        now,
	}, now)
	if err != nil {
		return err
	}
	return service.store.RemoveMember(ctx, organizationID, userID, target.Role, event)
}

// ApplyBillingEntitlements records the member limit billing set for an
// organization, which invitations and acceptances are then held to.
func (service *Service) ApplyBillingEntitlements(ctx context.Context, entitlements billingeventsv1.EntitlementsChanged) error {
	if entitlements.OrganizationID == "" || entitlements.Revision < 1 {
		return fmt.Errorf("entitlements need an organization and a revision: %w", domain.ErrInvalid)
	}
	if entitlements.MemberLimit != nil && *entitlements.MemberLimit < 1 {
		return fmt.Errorf("member limit must be at least 1: %w", domain.ErrInvalid)
	}
	return service.store.ApplyEntitlements(ctx, entitlements.OrganizationID, entitlements.Revision, entitlements.MemberLimit, entitlements.ChangedAt)
}

func newEvent(detailType string, data any, occurredAt time.Time) (domain.Event, error) {
	id, err := randomID("evt_", 16)
	if err != nil {
		return domain.Event{}, fmt.Errorf("create event id: %w", err)
	}
	detail, err := json.Marshal(data)
	if err != nil {
		return domain.Event{}, fmt.Errorf("encode %s: %w", detailType, err)
	}
	return domain.Event{ID: id, DetailType: detailType, OccurredAt: occurredAt, Data: detail}, nil
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

func randomToken(size int) (string, error) {
	return randomID("", size)
}

func hashToken(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func slugify(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = nonSlugCharacters.ReplaceAllString(value, "-")
	return strings.Trim(value, "-")
}

func organizationFields(rawName, rawSlug string) (string, string, error) {
	name := strings.TrimSpace(rawName)
	if name == "" || len(name) > 120 {
		return "", "", fmt.Errorf("organization name must contain 1-120 characters: %w", domain.ErrInvalid)
	}
	slug := slugify(rawSlug)
	if slug == "" {
		slug = slugify(name)
	}
	if len(slug) < 2 || len(slug) > 63 {
		return "", "", fmt.Errorf("organization slug must contain 2-63 characters: %w", domain.ErrInvalid)
	}
	return name, slug, nil
}

func tokenURL(base, token string) string {
	parsed, err := url.Parse(base)
	if err != nil {
		return base
	}
	query := parsed.Query()
	query.Set("token", token)
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func normalizeEmail(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func validEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Address == value
}

func concealMembershipError(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}
