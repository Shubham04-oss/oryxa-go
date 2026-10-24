package application

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

// fakeRelationships records each write; err fails every one.
type fakeRelationships struct {
	writes []string
	err    error
}

func (fake *fakeRelationships) record(write string) error {
	fake.writes = append(fake.writes, write)
	return fake.err
}

func (fake *fakeRelationships) MemberJoined(_ context.Context, org, user string, role domain.Role) error {
	return fake.record("joined " + org + " " + user + " " + string(role))
}

func (fake *fakeRelationships) MemberRoleChanged(_ context.Context, org, user string, role domain.Role) error {
	return fake.record("role " + org + " " + user + " " + string(role))
}

func (fake *fakeRelationships) MemberRemoved(_ context.Context, org, user string) error {
	return fake.record("removed " + org + " " + user)
}

func (fake *fakeRelationships) OrganizationDeleted(_ context.Context, org string) error {
	return fake.record("deleted " + org)
}

// refusingStore's removal fails, as the store's own checks can.
type refusingStore struct{ *fakeStore }

func (refusingStore) RemoveMember(context.Context, string, string, domain.Role, domain.Event) error {
	return domain.ErrConflict
}

func TestEachOrgRoleChangeIsWrittenToSpiceDBAfterItCommits(t *testing.T) {
	for _, failing := range []error{nil, errors.New("spicedb is down")} {
		store := &fakeStore{
			user:             domain.User{ID: "owner-1", Email: "owner@example.com"},
			actorMembership:  domain.Membership{OrganizationID: "org-1", UserID: "owner-1", Role: domain.RoleOwner},
			targetMembership: domain.Membership{OrganizationID: "org-1", UserID: "member-1", Role: domain.RoleAdmin},
			organization:     domain.Organization{ID: "org-1", Name: "Acme"},
			invitation:       domain.Invitation{ID: "inv-1", OrganizationID: "org-2", Email: "owner@example.com", Role: domain.RoleMember, Status: domain.InvitationPending},
		}
		written := &fakeRelationships{err: failing}
		service := New(store, "https://app.example.com/invitations/accept").WithRelationships(written, slog.New(slog.NewTextHandler(io.Discard, nil)))
		service.now = fixedTime
		ctx := context.Background()

		// A failed write fails none of them: the projector follows.
		organization, err := service.CreateOrganization(ctx, "owner-1", CreateOrganizationInput{Name: "New"})
		if err != nil {
			t.Fatalf("CreateOrganization() error = %v", err)
		}
		if _, err := service.AcceptInvitation(ctx, "owner-1", "raw-token"); err != nil {
			t.Fatalf("AcceptInvitation() error = %v", err)
		}
		if _, err := service.ChangeMemberRole(ctx, "owner-1", "org-1", "member-1", domain.RoleMember); err != nil {
			t.Fatalf("ChangeMemberRole() error = %v", err)
		}
		if err := service.RemoveMember(ctx, "owner-1", "org-1", "member-1"); err != nil {
			t.Fatalf("RemoveMember() error = %v", err)
		}
		store.organization = domain.Organization{ID: "org-1", Name: "Acme"}
		if err := service.DeleteOrganization(ctx, "owner-1", "org-1"); err != nil {
			t.Fatalf("DeleteOrganization() error = %v", err)
		}
		want := []string{
			"joined " + organization.ID + " owner-1 owner",
			"joined org-2 owner-1 member",
			"role org-1 member-1 member",
			"removed org-1 member-1",
			"deleted org-1",
		}
		if !slices.Equal(written.writes, want) {
			t.Fatalf("writes (failing %v) = %q, want %q", failing, written.writes, want)
		}
	}
}

func TestARefusedChangeWritesNothing(t *testing.T) {
	store := refusingStore{&fakeStore{
		actorMembership:  domain.Membership{OrganizationID: "org-1", UserID: "owner-1", Role: domain.RoleOwner},
		targetMembership: domain.Membership{OrganizationID: "org-1", UserID: "member-1", Role: domain.RoleAdmin},
		organization:     domain.Organization{ID: "org-1", Name: "Acme"},
	}}
	written := &fakeRelationships{}
	service := New(store, "").WithRelationships(written, slog.New(slog.NewTextHandler(io.Discard, nil)))

	if err := service.RemoveMember(context.Background(), "owner-1", "org-1", "member-1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("RemoveMember() error = %v, want conflict", err)
	}
	if len(written.writes) != 0 {
		t.Fatalf("a refused removal wrote %q", written.writes)
	}
}
