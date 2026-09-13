package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	billingeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/billing/v1"
)

type fakeStore struct {
	Store
	user             domain.User
	actorMembership  domain.Membership
	targetMembership domain.Membership
	organization     domain.Organization
	members          []domain.Membership
	events           []domain.Event
	invitation       domain.Invitation
	deletedOrgID     string
	revokedInviteID  string
	action           domain.ActionToken
	completedAction  domain.ActionPurpose

	appliedEntitlements []string
}

func (fake *fakeStore) UpsertFederatedUser(_ context.Context, user domain.User, registered, signedIn domain.Event) (bool, error) {
	fake.user = user
	fake.events = append(fake.events, registered, signedIn)
	return true, nil
}

func (fake *fakeStore) UserByID(_ context.Context, _ string) (domain.User, error) {
	if fake.user.ID == "" {
		return domain.User{}, domain.ErrNotFound
	}
	return fake.user, nil
}

func (fake *fakeStore) CreateOrganization(_ context.Context, organization domain.Organization, _ domain.Membership, events []domain.Event) error {
	fake.organization = organization
	fake.events = append(fake.events, events...)
	return nil
}

func (fake *fakeStore) Membership(_ context.Context, _ string, userID string) (domain.Membership, error) {
	if fake.actorMembership.UserID == userID {
		return fake.actorMembership, nil
	}
	if fake.targetMembership.UserID == userID {
		return fake.targetMembership, nil
	}
	return domain.Membership{}, domain.ErrNotFound
}

func (fake *fakeStore) OrganizationByID(_ context.Context, _ string) (domain.Organization, error) {
	if fake.organization.ID == "" {
		return domain.Organization{}, domain.ErrNotFound
	}
	return fake.organization, nil
}

func (fake *fakeStore) CreateInvitation(_ context.Context, invitation domain.Invitation, event domain.Event) error {
	fake.invitation = invitation
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) UpdateOrganization(_ context.Context, organization domain.Organization, event domain.Event) error {
	fake.organization = organization
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) DeleteOrganization(_ context.Context, organizationID string, deleted domain.Event, removed func(domain.Membership) (domain.Event, error)) error {
	fake.deletedOrgID = organizationID
	fake.events = append(fake.events, deleted)
	for _, member := range fake.members {
		event, err := removed(member)
		if err != nil {
			return err
		}
		fake.events = append(fake.events, event)
	}
	return nil
}

func (fake *fakeStore) ChangeMemberRole(_ context.Context, _, _ string, _, _ domain.Role, event domain.Event) error {
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) RemoveMember(_ context.Context, _, _ string, _ domain.Role, event domain.Event) error {
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) ApplyEntitlements(_ context.Context, organizationID string, revision int64, memberLimit *int, _ time.Time) error {
	fake.appliedEntitlements = append(fake.appliedEntitlements, fmt.Sprintf("%s r%d limit=%v", organizationID, revision, describeLimit(memberLimit)))
	return nil
}

func describeLimit(limit *int) string {
	if limit == nil {
		return "none"
	}
	return fmt.Sprint(*limit)
}

func (fake *fakeStore) InvitationByID(_ context.Context, _, _ string) (domain.Invitation, error) {
	if fake.invitation.ID == "" {
		return domain.Invitation{}, domain.ErrNotFound
	}
	return fake.invitation, nil
}

func (fake *fakeStore) RevokeInvitation(_ context.Context, _, invitationID string, event domain.Event) error {
	fake.revokedInviteID = invitationID
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) CreateActionToken(_ context.Context, action domain.ActionToken, event domain.Event) error {
	fake.action = action
	fake.events = append(fake.events, event)
	return nil
}

func (fake *fakeStore) ClaimActionToken(_ context.Context, purpose domain.ActionPurpose, _ string, _ time.Time) (domain.ActionToken, error) {
	if fake.action.ID == "" || fake.action.Purpose != purpose {
		return domain.ActionToken{}, domain.ErrNotFound
	}
	return fake.action, nil
}

func (fake *fakeStore) ReleaseActionToken(context.Context, domain.ActionPurpose, string) error {
	return nil
}

func (fake *fakeStore) CompleteActionToken(_ context.Context, purpose domain.ActionPurpose, _ string, _ time.Time, event domain.Event) error {
	fake.completedAction = purpose
	fake.events = append(fake.events, event)
	return nil
}

func TestCompleteLoginCreatesDomainEvents(t *testing.T) {
	store := &fakeStore{}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	user, created, err := service.CompleteLogin(context.Background(), FederatedIdentity{
		Subject:     "google-subject",
		Email:       "PERSON@EXAMPLE.COM",
		DisplayName: "Person",
		Provider:    "Google",
	})
	if err != nil {
		t.Fatalf("CompleteLogin() error = %v", err)
	}
	if !created || user.Email != "person@example.com" {
		t.Fatalf("unexpected user: %+v, created=%v", user, created)
	}
	if len(store.events) != 2 || store.events[0].DetailType != autheventsv1.UserRegisteredDetailType || store.events[1].DetailType != autheventsv1.UserSignedInDetailType {
		t.Fatalf("unexpected events: %+v", store.events)
	}
}

func TestCreateOrganizationCreatesOwnerAndEvents(t *testing.T) {
	store := &fakeStore{user: domain.User{ID: "user-1", Email: "owner@example.com"}}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	organization, err := service.CreateOrganization(context.Background(), "user-1", CreateOrganizationInput{Name: "Acme Labs"})
	if err != nil {
		t.Fatalf("CreateOrganization() error = %v", err)
	}
	if organization.Slug != "acme-labs" {
		t.Fatalf("slug = %q, want acme-labs", organization.Slug)
	}
	if len(store.events) != 2 || store.events[0].DetailType != autheventsv1.OrganizationCreatedDetailType || store.events[1].DetailType != autheventsv1.MemberJoinedDetailType {
		t.Fatalf("unexpected events: %+v", store.events)
	}
}

func TestInviteMemberCreatesHashedTokenAndDomainEvent(t *testing.T) {
	store := &fakeStore{
		actorMembership: domain.Membership{OrganizationID: "org-1", UserID: "owner-1", Role: domain.RoleOwner},
		organization:    domain.Organization{ID: "org-1", Name: "Acme"},
	}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	invitation, token, err := service.InviteMember(context.Background(), "owner-1", "org-1", InviteMemberInput{
		Email: "MEMBER@EXAMPLE.COM",
		Role:  domain.RoleMember,
	})
	if err != nil {
		t.Fatalf("InviteMember() error = %v", err)
	}
	if token == "" || invitation.TokenHash == "" || strings.Contains(invitation.TokenHash, token) {
		t.Fatal("invitation token was not safely hashed")
	}
	if len(store.events) != 1 || store.events[0].DetailType != autheventsv1.MemberInvitedDetailType {
		t.Fatalf("unexpected events: %+v", store.events)
	}
	var event autheventsv1.MemberInvited
	if err := json.Unmarshal(store.events[0].Data, &event); err != nil {
		t.Fatalf("decode invitation event: %v", err)
	}
	if event.Email != "member@example.com" || !strings.Contains(event.AcceptURL, "token=") {
		t.Fatalf("unexpected invitation event: %+v", event)
	}
}

func TestOnlyOwnerCanChangeMemberRole(t *testing.T) {
	store := &fakeStore{
		actorMembership:  domain.Membership{OrganizationID: "org-1", UserID: "admin-1", Role: domain.RoleAdmin},
		targetMembership: domain.Membership{OrganizationID: "org-1", UserID: "member-1", Role: domain.RoleMember},
	}
	service := New(store, "https://app.example.com/invitations/accept")

	_, err := service.ChangeMemberRole(context.Background(), "admin-1", "org-1", "member-1", domain.RoleAdmin)
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("ChangeMemberRole() error = %v, want forbidden", err)
	}
}

func TestAdminCannotCreateAdminInvitation(t *testing.T) {
	store := &fakeStore{actorMembership: domain.Membership{UserID: "admin-1", Role: domain.RoleAdmin}}
	service := New(store, "https://app.example.com/invitations/accept")

	_, _, err := service.InviteMember(context.Background(), "admin-1", "org-1", InviteMemberInput{
		Email: "next-admin@example.com",
		Role:  domain.RoleAdmin,
	})
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("InviteMember() error = %v, want forbidden", err)
	}
}

func TestOwnerUpdatesAndDeletesOrganizationWithEvents(t *testing.T) {
	store := &fakeStore{
		actorMembership: domain.Membership{UserID: "owner-1", Role: domain.RoleOwner},
		organization:    domain.Organization{ID: "org-1", Name: "Before", Slug: "before"},
		members: []domain.Membership{
			{OrganizationID: "org-1", UserID: "owner-1", Email: "owner@example.com", Role: domain.RoleOwner},
			{OrganizationID: "org-1", UserID: "member-1", Email: "member@example.com", Role: domain.RoleMember},
		},
	}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	organization, err := service.UpdateOrganization(context.Background(), "owner-1", "org-1", UpdateOrganizationInput{Name: "After"})
	if err != nil {
		t.Fatalf("UpdateOrganization() error = %v", err)
	}
	if organization.Slug != "after" || store.events[0].DetailType != autheventsv1.OrganizationUpdatedDetailType {
		t.Fatalf("unexpected update: organization=%+v events=%+v", organization, store.events)
	}
	if err := service.DeleteOrganization(context.Background(), "owner-1", "org-1"); err != nil {
		t.Fatalf("DeleteOrganization() error = %v", err)
	}
	if store.deletedOrgID != "org-1" || store.events[1].DetailType != autheventsv1.OrganizationDeletedDetailType {
		t.Fatalf("unexpected delete: store=%+v", store)
	}

	removals := store.events[2:]
	if len(removals) != 2 {
		t.Fatalf("removal events = %d, want one per member", len(removals))
	}
	for index, event := range removals {
		var removed autheventsv1.MemberRemoved
		if event.DetailType != autheventsv1.MemberRemovedDetailType || json.Unmarshal(event.Data, &removed) != nil {
			t.Fatalf("unexpected removal event: %+v", event)
		}
		member := store.members[index]
		if removed.UserID != member.UserID || removed.Email != member.Email || removed.PreviousRole != string(member.Role) ||
			removed.OrganizationName != "After" || removed.Reason != autheventsv1.MemberRemovedReasonOrganizationDeleted || removed.RemovedBy != "owner-1" {
			t.Fatalf("unexpected removal: %+v", removed)
		}
	}
}

func TestRoleChangeEventNamesMemberAndOrganization(t *testing.T) {
	store := &fakeStore{
		actorMembership:  domain.Membership{OrganizationID: "org-1", UserID: "owner-1", Role: domain.RoleOwner},
		targetMembership: domain.Membership{OrganizationID: "org-1", UserID: "member-1", Email: "member@example.com", Role: domain.RoleMember},
		organization:     domain.Organization{ID: "org-1", Name: "Acme"},
	}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	if _, err := service.ChangeMemberRole(context.Background(), "owner-1", "org-1", "member-1", domain.RoleAdmin); err != nil {
		t.Fatalf("ChangeMemberRole() error = %v", err)
	}
	var changed autheventsv1.MemberRoleChanged
	if len(store.events) != 1 || json.Unmarshal(store.events[0].Data, &changed) != nil {
		t.Fatalf("unexpected events: %+v", store.events)
	}
	want := autheventsv1.MemberRoleChanged{
		OrganizationID: "org-1", OrganizationName: "Acme", UserID: "member-1", Email: "member@example.com",
		PreviousRole: "member", Role: "admin", ChangedBy: "owner-1", ChangedAt: fixedTime(),
	}
	if changed != want {
		t.Fatalf("role change event = %+v, want %+v", changed, want)
	}
}

func TestRemoveMemberEventSaysWhyMembershipEnded(t *testing.T) {
	tests := []struct {
		name    string
		actorID string
		reason  string
	}{
		{name: "removed by an admin", actorID: "admin-1", reason: autheventsv1.MemberRemovedReasonRemoved},
		{name: "left", actorID: "member-1", reason: autheventsv1.MemberRemovedReasonLeft},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			target := domain.Membership{OrganizationID: "org-1", UserID: "member-1", Email: "member@example.com", Role: domain.RoleMember}
			actor := domain.Membership{OrganizationID: "org-1", UserID: "admin-1", Role: domain.RoleAdmin}
			if test.actorID == target.UserID {
				actor = target
			}
			store := &fakeStore{actorMembership: actor, targetMembership: target, organization: domain.Organization{ID: "org-1", Name: "Acme"}}
			service := New(store, "https://app.example.com/invitations/accept")
			service.now = fixedTime

			if err := service.RemoveMember(context.Background(), test.actorID, "org-1", "member-1"); err != nil {
				t.Fatalf("RemoveMember() error = %v", err)
			}
			var removed autheventsv1.MemberRemoved
			if len(store.events) != 1 || json.Unmarshal(store.events[0].Data, &removed) != nil {
				t.Fatalf("unexpected events: %+v", store.events)
			}
			if removed.Reason != test.reason || removed.Email != "member@example.com" || removed.OrganizationName != "Acme" || removed.RemovedBy != test.actorID {
				t.Fatalf("removal event = %+v, want reason %q", removed, test.reason)
			}
		})
	}
}

func TestRevokedInvitationEventNamesOrganization(t *testing.T) {
	store := &fakeStore{
		actorMembership: domain.Membership{UserID: "owner-1", Role: domain.RoleOwner},
		organization:    domain.Organization{ID: "org-1", Name: "Acme"},
		invitation: domain.Invitation{
			ID: "inv-1", OrganizationID: "org-1", Email: "invitee@example.com", Role: domain.RoleMember, Status: domain.InvitationPending,
		},
	}
	service := New(store, "https://app.example.com/invitations/accept")
	service.now = fixedTime

	if err := service.RevokeInvitation(context.Background(), "owner-1", "org-1", "inv-1"); err != nil {
		t.Fatalf("RevokeInvitation() error = %v", err)
	}
	var revoked autheventsv1.MemberInvitationRevoked
	if len(store.events) != 1 || json.Unmarshal(store.events[0].Data, &revoked) != nil {
		t.Fatalf("unexpected events: %+v", store.events)
	}
	if revoked.OrganizationName != "Acme" || revoked.Email != "invitee@example.com" || revoked.RevokedBy != "owner-1" {
		t.Fatalf("revocation event = %+v", revoked)
	}
}

func TestAdminCannotRevokeAdminInvitation(t *testing.T) {
	store := &fakeStore{
		actorMembership: domain.Membership{UserID: "admin-1", Role: domain.RoleAdmin},
		invitation: domain.Invitation{
			ID: "inv-1", OrganizationID: "org-1", Role: domain.RoleAdmin, Status: domain.InvitationPending,
		},
	}
	service := New(store, "https://app.example.com/invitations/accept")

	err := service.RevokeInvitation(context.Background(), "admin-1", "org-1", "inv-1")
	if !errors.Is(err, domain.ErrForbidden) {
		t.Fatalf("RevokeInvitation() error = %v, want forbidden", err)
	}
}

func TestEmailVerificationDeliveryUsesHashedTokenAndAuthEvent(t *testing.T) {
	store := &fakeStore{}
	service := NewWithConfig(store, Config{EmailVerificationURL: "https://app.example.com/auth/verify-email"})
	service.now = fixedTime

	if err := service.RequestEmailVerification(context.Background(), "user-1", "PERSON@EXAMPLE.COM"); err != nil {
		t.Fatalf("RequestEmailVerification() error = %v", err)
	}
	if store.action.TokenHash == "" || store.action.Email != "person@example.com" {
		t.Fatalf("unexpected action token: %+v", store.action)
	}
	if len(store.events) != 1 || store.events[0].DetailType != autheventsv1.EmailVerificationDeliveryRequestedDetailType {
		t.Fatalf("unexpected events: %+v", store.events)
	}
	var delivery autheventsv1.EmailVerificationDeliveryRequested
	if err := json.Unmarshal(store.events[0].Data, &delivery); err != nil {
		t.Fatalf("decode delivery event: %v", err)
	}
	if delivery.VerificationURL == "" || !strings.Contains(delivery.VerificationURL, "token=") || strings.Contains(delivery.VerificationURL, store.action.TokenHash) {
		t.Fatalf("unexpected delivery URL: %q", delivery.VerificationURL)
	}
}

func TestPasswordResetCompletionPublishesAuthFact(t *testing.T) {
	store := &fakeStore{action: domain.ActionToken{
		ID: "action-1", UserID: "user-1", Email: "person@example.com", Purpose: domain.ActionPasswordReset,
	}}
	service := NewWithConfig(store, Config{PasswordResetURL: "https://app.example.com/auth/reset-password"})
	service.now = fixedTime

	action, err := service.PasswordReset(context.Background(), "raw-token")
	if err != nil {
		t.Fatalf("PasswordReset() error = %v", err)
	}
	if err := service.CompletePasswordReset(context.Background(), "raw-token", action); err != nil {
		t.Fatalf("CompletePasswordReset() error = %v", err)
	}
	if store.completedAction != domain.ActionPasswordReset || store.events[0].DetailType != autheventsv1.PasswordResetDetailType {
		t.Fatalf("unexpected completion: store=%+v", store)
	}
}

func TestApplyBillingEntitlements(t *testing.T) {
	store := &fakeStore{}
	service := New(store, "https://app.example.com/invitations/accept")
	limit, zero := 25, 0

	valid := []billingeventsv1.EntitlementsChanged{
		{OrganizationID: "org-1", Revision: 2, MemberLimit: &limit, ChangedAt: fixedTime()},
		{OrganizationID: "org-1", Revision: 3, ChangedAt: fixedTime()},
	}
	for _, entitlements := range valid {
		if err := service.ApplyBillingEntitlements(context.Background(), entitlements); err != nil {
			t.Fatalf("ApplyBillingEntitlements(%+v) error = %v", entitlements, err)
		}
	}
	if strings.Join(store.appliedEntitlements, "; ") != "org-1 r2 limit=25; org-1 r3 limit=none" {
		t.Fatalf("applied = %v", store.appliedEntitlements)
	}

	invalid := []billingeventsv1.EntitlementsChanged{
		{Revision: 1},
		{OrganizationID: "org-1"},
		{OrganizationID: "org-1", Revision: 4, MemberLimit: &zero},
	}
	for _, entitlements := range invalid {
		if err := service.ApplyBillingEntitlements(context.Background(), entitlements); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("ApplyBillingEntitlements(%+v) error = %v, want invalid", entitlements, err)
		}
	}
}

func fixedTime() time.Time {
	return time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
}
