package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	integrationevents "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

type fakeStore struct {
	connections  []domain.Connection
	apps         []domain.OAuthAppProfile
	disconnected []string
	disabled     []string
	err          error
}

func (fake *fakeStore) OpenConnections(_ context.Context, organizationID, memberID string) ([]domain.Connection, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	var result []domain.Connection
	for _, connection := range fake.connections {
		if connection.OrganizationID != organizationID {
			continue
		}
		if memberID != "" && connection.MemberID != memberID {
			continue
		}
		if connection.Status == domain.ConnectionStatusDisconnected {
			continue
		}
		result = append(result, connection)
	}
	return result, nil
}

func (fake *fakeStore) DisconnectOpenConnectionWithEvent(_ context.Context, _, _, connectionID string, _ time.Time, _ domain.Event) error {
	if fake.err != nil {
		return fake.err
	}
	fake.disconnected = append(fake.disconnected, connectionID)
	for i, connection := range fake.connections {
		if connection.ID == connectionID {
			fake.connections[i].Status = domain.ConnectionStatusDisconnected
		}
	}
	return nil
}

func (fake *fakeStore) ActiveOAuthApps(_ context.Context, organizationID string) ([]domain.OAuthAppProfile, error) {
	if fake.err != nil {
		return nil, fake.err
	}
	var result []domain.OAuthAppProfile
	for _, app := range fake.apps {
		if app.OrganizationID == organizationID && app.Status == domain.OAuthAppStatusActive {
			result = append(result, app)
		}
	}
	return result, nil
}

func (fake *fakeStore) DisableOAuthAppWithEvent(_ context.Context, _, profileID string, _ time.Time, _ domain.Event) error {
	if fake.err != nil {
		return fake.err
	}
	fake.disabled = append(fake.disabled, profileID)
	for i, app := range fake.apps {
		if app.ID == profileID {
			fake.apps[i].Status = domain.OAuthAppStatusDisabled
		}
	}
	return nil
}

func testService(store *fakeStore) Service {
	n := 0
	return Service{
		Store: store,
		IDs: func(prefix string) (string, error) {
			n++
			return fmt.Sprintf("%s-%d", prefix, n), nil
		},
		Now: func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) },
	}
}

func TestOrganizationDeletedDisconnectsConnectionsAndDisablesApps(t *testing.T) {
	store := &fakeStore{
		connections: []domain.Connection{
			{ID: "con-1", OrganizationID: "org-1", MemberID: "member-1", Provider: "slack", Status: domain.ConnectionStatusActive},
			{ID: "con-2", OrganizationID: "org-1", MemberID: "member-2", Provider: "github", Status: domain.ConnectionStatusReauthorizationRequired},
			{ID: "con-other", OrganizationID: "org-2", MemberID: "member-1", Provider: "slack", Status: domain.ConnectionStatusActive},
		},
		apps: []domain.OAuthAppProfile{
			{ID: "app-1", OrganizationID: "org-1", Provider: "slack", Status: domain.OAuthAppStatusActive},
			{ID: "app-other", OrganizationID: "org-2", Provider: "slack", Status: domain.OAuthAppStatusActive},
		},
	}
	service := testService(store)

	if err := service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{
		OrganizationID: "org-1", DeletedBy: "admin-1", DeletedAt: time.Date(2026, 9, 14, 13, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.disconnected) != "[con-1 con-2]" || fmt.Sprint(store.disabled) != "[app-1]" {
		t.Fatalf("disconnected=%v disabled=%v", store.disconnected, store.disabled)
	}
}

func TestMemberRemovedDisconnectsOnlyThatMember(t *testing.T) {
	store := &fakeStore{
		connections: []domain.Connection{
			{ID: "con-1", OrganizationID: "org-1", MemberID: "member-1", Provider: "slack", Status: domain.ConnectionStatusActive},
			{ID: "con-2", OrganizationID: "org-1", MemberID: "member-2", Provider: "google", Status: domain.ConnectionStatusActive},
		},
		apps: []domain.OAuthAppProfile{
			{ID: "app-1", OrganizationID: "org-1", Provider: "slack", Status: domain.OAuthAppStatusActive},
		},
	}
	service := testService(store)

	if err := service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{
		OrganizationID: "org-1", UserID: "member-1", Reason: autheventsv1.MemberRemovedReasonLeft, RemovedBy: "admin-1",
	}); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(store.disconnected) != "[con-1]" || len(store.disabled) != 0 {
		t.Fatalf("disconnected=%v disabled=%v", store.disconnected, store.disabled)
	}
}

func TestMemberRemovedIgnoresOrganizationDeletedReason(t *testing.T) {
	store := &fakeStore{
		connections: []domain.Connection{
			{ID: "con-1", OrganizationID: "org-1", MemberID: "member-1", Provider: "slack", Status: domain.ConnectionStatusActive},
		},
	}
	service := testService(store)

	if err := service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{
		OrganizationID: "org-1", UserID: "member-1", Reason: autheventsv1.MemberRemovedReasonOrganizationDeleted,
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.disconnected) != 0 {
		t.Fatalf("disconnected=%v, want none", store.disconnected)
	}
}

func TestLifecycleRejectsIncompleteEvents(t *testing.T) {
	service := testService(&fakeStore{})
	if err := service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{}); err == nil {
		t.Fatal("OrganizationDeleted() error = nil")
	}
	if err := service.MemberRemoved(context.Background(), autheventsv1.MemberRemoved{OrganizationID: "org-1"}); err == nil {
		t.Fatal("MemberRemoved() error = nil")
	}
}

func TestOrganizationDeletedPropagatesStoreErrors(t *testing.T) {
	service := testService(&fakeStore{err: errors.New("database down")})
	if err := service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{OrganizationID: "org-1"}); err == nil {
		t.Fatal("OrganizationDeleted() error = nil")
	}
}

func TestDisconnectEventUsesAuthActor(t *testing.T) {
	store := &recordingStore{connections: []domain.Connection{
		{ID: "con-1", OrganizationID: "org-1", MemberID: "member-1", Provider: "slack", Status: domain.ConnectionStatusActive},
	}}
	n := 0
	service := Service{Store: store, IDs: func(prefix string) (string, error) {
		n++
		return fmt.Sprintf("%s-%d", prefix, n), nil
	}}
	now := time.Date(2026, 9, 14, 15, 0, 0, 0, time.UTC)
	if err := service.OrganizationDeleted(context.Background(), autheventsv1.OrganizationDeleted{
		OrganizationID: "org-1", DeletedBy: "admin-9", DeletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 {
		t.Fatalf("events = %+v", store.events)
	}
	data, ok := store.events[0].Data.(integrationevents.ConnectionDisconnected)
	if !ok || data.DisconnectedBy != "admin-9" || data.ConnectionID != "con-1" || data.DisconnectedAt != now {
		t.Fatalf("event data = %+v", store.events[0].Data)
	}
}

type recordingStore struct {
	connections []domain.Connection
	events      []domain.Event
}

func (fake *recordingStore) OpenConnections(context.Context, string, string) ([]domain.Connection, error) {
	return fake.connections, nil
}
func (fake *recordingStore) DisconnectOpenConnectionWithEvent(_ context.Context, _, _, _ string, _ time.Time, event domain.Event) error {
	fake.events = append(fake.events, event)
	return nil
}
func (fake *recordingStore) ActiveOAuthApps(context.Context, string) ([]domain.OAuthAppProfile, error) {
	return nil, nil
}
func (fake *recordingStore) DisableOAuthAppWithEvent(context.Context, string, string, time.Time, domain.Event) error {
	return nil
}
