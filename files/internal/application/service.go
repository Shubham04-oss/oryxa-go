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
	"log/slog"
	"strings"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/blob"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

const downloadURLTTL = time.Minute

type Store interface {
	UpsertOrganization(context.Context, string, string, time.Time) error
	CreateObject(context.Context, string, domain.Object, domain.Event) error
	Object(context.Context, string, string, time.Time) (domain.Object, error)
	Objects(context.Context, string, time.Time, domain.ObjectQuery) ([]domain.Object, error)
	DeleteObject(context.Context, string, string, domain.Event) error
	DeleteMemberScope(context.Context, string, string, func(domain.Object) (domain.Event, error)) error
	DeleteTeamFiles(context.Context, string, string, func(domain.Object) (domain.Event, error)) error
	DeleteOrganizationFiles(context.Context, string, func(domain.Object) (domain.Event, error)) error
}

type Directory interface {
	Membership(context.Context, string, authclient.Credentials) (domain.OrgMembership, error)
}

type Teams interface {
	Team(ctx context.Context, organizationID, teamID string, credentials authclient.Credentials) (domain.TeamAccess, error)
}

type CreateInput struct {
	Filename      string
	ContentType   string
	Purpose       domain.Purpose
	Access        domain.Access
	Scope         domain.Scope
	TeamID        string
	RetentionDays int
	Content       []byte
}

type ListFilter struct {
	Scope   domain.Scope
	TeamID  string
	Purpose domain.Purpose
	Date    domain.DateFilter
}

type DownloadURL struct {
	URL       string    `json:"url"`
	ExpiresAt time.Time `json:"expires_at"`
}

type Service struct {
	store     Store
	blobs     blob.Store
	directory Directory
	teams     Teams
	logger    *slog.Logger
	now       func() time.Time
}

func New(store Store, blobs blob.Store, directory Directory, teams Teams, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, blobs: blobs, directory: directory, teams: teams, logger: logger, now: time.Now}
}

// InputReader supplies a create's input once the caller is known. Reading an
// upload's body is the costly part of it, so it waits until the caller has been
// admitted: someone with no business here never has their body read.
type InputReader func() (CreateInput, error)

func (s *Service) Create(ctx context.Context, credentials authclient.Credentials, organizationID string, read InputReader) (domain.Object, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Object{}, err
	}
	input, err := read()
	if err != nil {
		return domain.Object{}, err
	}
	filename, err := domain.Filename(input.Filename)
	if err != nil {
		return domain.Object{}, err
	}
	contentType, err := domain.ContentType(input.ContentType)
	if err != nil {
		return domain.Object{}, err
	}
	purpose := input.Purpose
	if purpose == "" {
		purpose = domain.PurposeUpload
	}
	if !purpose.Valid() {
		return domain.Object{}, fmt.Errorf("purpose is not allowed: %w", domain.ErrInvalid)
	}
	scope := input.Scope
	if scope == "" {
		scope = domain.ScopeMember
	}
	if !scope.Valid() {
		return domain.Object{}, fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	}
	if err := purpose.Fits(scope); err != nil {
		return domain.Object{}, err
	}
	access := input.Access
	if access == "" {
		access = domain.AccessMembers
	}
	if !access.Valid() {
		return domain.Object{}, fmt.Errorf("access is not allowed: %w", domain.ErrInvalid)
	}
	if purpose == domain.PurposeBillingAttachment {
		access = domain.AccessAdmins
	}
	if access == domain.AccessAdmins && (scope != domain.ScopeOrganization || !org.ActsAsAdmin()) {
		return domain.Object{}, domain.ErrForbidden
	}
	teamID, memberID, err := domain.Place(scope, input.TeamID, org.UserID)
	if err != nil {
		return domain.Object{}, err
	}
	if err := s.mayCreate(ctx, credentials, org, scope, teamID); err != nil {
		return domain.Object{}, err
	}
	if err := domain.Size(len(input.Content)); err != nil {
		return domain.Object{}, err
	}
	days, err := domain.RetentionDays(purpose, input.RetentionDays)
	if err != nil {
		return domain.Object{}, err
	}
	fileID, err := randomID("file_", 12)
	if err != nil {
		return domain.Object{}, err
	}
	now := s.now().UTC()
	sum := sha256.Sum256(input.Content)
	object := domain.Object{
		ID:             fileID,
		OrganizationID: organizationID,
		Scope:          scope,
		TeamID:         teamID,
		MemberID:       memberID,
		Filename:       filename,
		ContentType:    contentType,
		Purpose:        purpose,
		Access:         access,
		SizeBytes:      int64(len(input.Content)),
		SHA256:         hex.EncodeToString(sum[:]),
		CreatedBy:      org.UserID,
		CreatedAt:      now,
		ExpiresAt:      now.Add(time.Duration(days) * 24 * time.Hour),
	}
	key := object.Key()
	if err := s.blobs.Put(ctx, blob.Object{Key: key, ContentType: contentType, SHA256: object.SHA256, Body: input.Content}); err != nil {
		return domain.Object{}, err
	}
	created, err := s.event(fileseventsv1.ObjectCreatedDetailType, fileseventsv1.ObjectCreated{
		FileID:         object.ID,
		OrganizationID: organizationID,
		Scope:          string(object.Scope),
		TeamID:         object.TeamID,
		MemberID:       object.MemberID,
		Filename:       object.Filename,
		ContentType:    object.ContentType,
		Purpose:        string(object.Purpose),
		Access:         string(object.Access),
		SizeBytes:      object.SizeBytes,
		SHA256:         object.SHA256,
		CreatedBy:      object.CreatedBy,
		CreatedAt:      object.CreatedAt,
		ExpiresAt:      object.ExpiresAt,
	}, now)
	if err != nil {
		_ = s.blobs.Delete(ctx, key)
		return domain.Object{}, err
	}
	if err := s.store.CreateObject(ctx, org.OrganizationName, object, created); err != nil {
		_ = s.blobs.Delete(ctx, key)
		return domain.Object{}, err
	}
	return object, nil
}

func (s *Service) List(ctx context.Context, credentials authclient.Credentials, organizationID string, filter ListFilter) ([]domain.Object, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return nil, err
	}
	filter, err = normalizeList(filter)
	if err != nil {
		return nil, err
	}
	from, to, err := filter.Date.Range()
	if err != nil {
		return nil, err
	}
	if filter.Scope == domain.ScopeTeam {
		if !org.ActsAsAdmin() {
			if _, err := s.teamAccess(ctx, credentials, org, organizationID, filter.TeamID); err != nil {
				return nil, err
			}
		}
	}
	return s.store.Objects(ctx, organizationID, s.now().UTC(), domain.ObjectQuery{
		Scope:       filter.Scope,
		TeamID:      filter.TeamID,
		MemberID:    org.UserID,
		Admin:       org.ActsAsAdmin(),
		Purpose:     filter.Purpose,
		CreatedFrom: from,
		CreatedTo:   to,
	})
}

func (s *Service) Get(ctx context.Context, credentials authclient.Credentials, organizationID, fileID string) (domain.Object, error) {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return domain.Object{}, err
	}
	object, err := s.store.Object(ctx, organizationID, strings.TrimSpace(fileID), s.now().UTC())
	if err != nil {
		return domain.Object{}, conceal(err)
	}
	if err := s.canSee(ctx, credentials, org, object); err != nil {
		return domain.Object{}, err
	}
	return object, nil
}

func (s *Service) Content(ctx context.Context, credentials authclient.Credentials, organizationID, fileID string) (domain.Object, []byte, error) {
	object, err := s.Get(ctx, credentials, organizationID, fileID)
	if err != nil {
		return domain.Object{}, nil, err
	}
	stored, err := s.blobs.Get(ctx, object.Key())
	if err != nil {
		return domain.Object{}, nil, conceal(err)
	}
	return object, stored.Body, nil
}

func (s *Service) DownloadURL(ctx context.Context, credentials authclient.Credentials, organizationID, fileID string) (DownloadURL, error) {
	object, err := s.Get(ctx, credentials, organizationID, fileID)
	if err != nil {
		return DownloadURL{}, err
	}
	url, err := s.blobs.PresignGet(ctx, object.Key(), downloadURLTTL)
	if err != nil {
		return DownloadURL{}, conceal(err)
	}
	return DownloadURL{URL: url, ExpiresAt: s.now().UTC().Add(downloadURLTTL)}, nil
}

func (s *Service) Delete(ctx context.Context, credentials authclient.Credentials, organizationID, fileID string) error {
	org, err := s.caller(ctx, credentials, organizationID)
	if err != nil {
		return err
	}
	object, err := s.store.Object(ctx, organizationID, strings.TrimSpace(fileID), s.now().UTC())
	if err != nil {
		return conceal(err)
	}
	if err := s.canSee(ctx, credentials, org, object); err != nil {
		return err
	}
	if err := s.canDelete(ctx, credentials, org, object); err != nil {
		return err
	}
	now := s.now().UTC()
	deleted, err := s.deletedEvent(object, org.UserID, fileseventsv1.DeletedReasonDeleted, now)
	if err != nil {
		return err
	}
	if err := s.blobs.Delete(ctx, object.Key()); err != nil && !errors.Is(err, domain.ErrNotFound) {
		return err
	}
	return s.store.DeleteObject(ctx, organizationID, object.ID, deleted)
}

func (s *Service) OrganizationCreated(ctx context.Context, data autheventsv1.OrganizationCreated) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	return s.store.UpsertOrganization(ctx, data.OrganizationID, data.Name, data.CreatedAt)
}

func (s *Service) OrganizationUpdated(ctx context.Context, data autheventsv1.OrganizationUpdated) error {
	if data.OrganizationID == "" || data.Name == "" {
		return errors.New("organization event has no organization_id or name")
	}
	return s.store.UpsertOrganization(ctx, data.OrganizationID, data.Name, data.UpdatedAt)
}

func (s *Service) OrganizationDeleted(ctx context.Context, data autheventsv1.OrganizationDeleted) error {
	if data.OrganizationID == "" {
		return errors.New("organization event has no organization_id")
	}
	now := data.DeletedAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	if err := s.blobs.DeletePrefix(ctx, domain.OrganizationPrefix(data.OrganizationID)); err != nil {
		return err
	}
	return s.store.DeleteOrganizationFiles(ctx, data.OrganizationID, func(object domain.Object) (domain.Event, error) {
		return s.deletedEvent(object, data.DeletedBy, fileseventsv1.DeletedReasonOrganizationDeleted, now)
	})
}

func (s *Service) MemberRemoved(ctx context.Context, data autheventsv1.MemberRemoved) error {
	if data.OrganizationID == "" || data.UserID == "" {
		return errors.New("member event has no organization_id or user_id")
	}
	if data.Reason == autheventsv1.MemberRemovedReasonOrganizationDeleted {
		return nil
	}
	now := data.RemovedAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	if err := s.blobs.DeletePrefix(ctx, domain.MemberPrefix(data.OrganizationID, data.UserID)); err != nil {
		return err
	}
	return s.store.DeleteMemberScope(ctx, data.OrganizationID, data.UserID, func(object domain.Object) (domain.Event, error) {
		return s.deletedEvent(object, data.RemovedBy, fileseventsv1.DeletedReasonOrganizationMembershipEnded, now)
	})
}

func (s *Service) TeamDeleted(ctx context.Context, data teamseventsv1.TeamDeleted) error {
	if data.OrganizationID == "" || data.TeamID == "" {
		return errors.New("team event has no organization_id or team_id")
	}
	now := data.DeletedAt.UTC()
	if now.IsZero() {
		now = s.now().UTC()
	}
	if err := s.blobs.DeletePrefix(ctx, domain.TeamPrefix(data.OrganizationID, data.TeamID)); err != nil {
		return err
	}
	return s.store.DeleteTeamFiles(ctx, data.OrganizationID, data.TeamID, func(object domain.Object) (domain.Event, error) {
		return s.deletedEvent(object, data.DeletedBy, fileseventsv1.DeletedReasonTeamDeleted, now)
	})
}

func (s *Service) mayCreate(ctx context.Context, credentials authclient.Credentials, org domain.OrgMembership, scope domain.Scope, teamID string) error {
	switch scope {
	case domain.ScopeMember:
		return nil
	case domain.ScopeOrganization:
		if !org.ActsAsAdmin() {
			return domain.ErrForbidden
		}
		return nil
	case domain.ScopeTeam:
		_, err := s.teamAccess(ctx, credentials, org, org.OrganizationID, teamID)
		return err
	default:
		return fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	}
}

func (s *Service) canSee(ctx context.Context, credentials authclient.Credentials, org domain.OrgMembership, object domain.Object) error {
	switch object.Scope {
	case domain.ScopeMember:
		if object.MemberID == org.UserID || org.ActsAsAdmin() {
			return nil
		}
		return domain.ErrNotFound
	case domain.ScopeOrganization:
		if object.EffectiveAccess() != domain.AccessMembers && !org.ActsAsAdmin() {
			return domain.ErrNotFound
		}
		return nil
	case domain.ScopeTeam:
		if org.ActsAsAdmin() {
			return nil
		}
		_, err := s.teamAccess(ctx, credentials, org, object.OrganizationID, object.TeamID)
		return err
	default:
		return domain.ErrNotFound
	}
}

func (s *Service) canDelete(ctx context.Context, credentials authclient.Credentials, org domain.OrgMembership, object domain.Object) error {
	if org.ActsAsAdmin() {
		return nil
	}
	switch object.Scope {
	case domain.ScopeMember:
		if object.MemberID == org.UserID || object.CreatedBy == org.UserID {
			return nil
		}
	case domain.ScopeTeam:
		if object.CreatedBy == org.UserID {
			return nil
		}
		access, err := s.teamAccess(ctx, credentials, org, object.OrganizationID, object.TeamID)
		if err != nil {
			return err
		}
		if access.CanManage {
			return nil
		}
	}
	return domain.ErrNotFound
}

// teamAccess asks teams. A team the caller cannot see reads as missing,
// because teams conceals it and so does files.
//
// Teams answers an agent's token as its member, so for an agent acting for an
// org admin it shows every team and says the agent can manage each one. An
// agent gets a plain member's reach, so for one only its member's own place
// on the team counts: it reaches a team its member is on, and manages one
// its member is a team admin of.
func (s *Service) teamAccess(ctx context.Context, credentials authclient.Credentials, org domain.OrgMembership, organizationID, teamID string) (domain.TeamAccess, error) {
	if s.teams == nil {
		return domain.TeamAccess{}, fmt.Errorf("teams URL is required for team files: %w", domain.ErrInvalid)
	}
	access, err := s.teams.Team(ctx, organizationID, teamID, credentials)
	if err != nil {
		return domain.TeamAccess{}, conceal(err)
	}
	if !access.CanSee() {
		return domain.TeamAccess{}, domain.ErrNotFound
	}
	if org.Agent {
		if !access.Member {
			return domain.TeamAccess{}, domain.ErrNotFound
		}
		access.CanManage = access.TeamAdmin
	}
	return access, nil
}

func (s *Service) caller(ctx context.Context, credentials authclient.Credentials, organizationID string) (domain.OrgMembership, error) {
	if strings.TrimSpace(organizationID) == "" {
		return domain.OrgMembership{}, domain.ErrNotFound
	}
	org, err := s.directory.Membership(ctx, organizationID, credentials)
	if err != nil {
		return domain.OrgMembership{}, conceal(err)
	}
	return org, nil
}

func (s *Service) deletedEvent(object domain.Object, deletedBy, reason string, at time.Time) (domain.Event, error) {
	return s.event(fileseventsv1.ObjectDeletedDetailType, fileseventsv1.ObjectDeleted{
		FileID:         object.ID,
		OrganizationID: object.OrganizationID,
		Scope:          string(object.Scope),
		TeamID:         object.TeamID,
		MemberID:       object.MemberID,
		Purpose:        string(object.Purpose),
		DeletedBy:      deletedBy,
		Reason:         reason,
		DeletedAt:      at,
	}, at)
}

func (s *Service) event(detailType string, data any, occurredAt time.Time) (domain.Event, error) {
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

func normalizeList(filter ListFilter) (ListFilter, error) {
	filter.TeamID = strings.TrimSpace(filter.TeamID)
	if filter.Scope != "" && !filter.Scope.Valid() {
		return ListFilter{}, fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	}
	if filter.Scope == domain.ScopeTeam && filter.TeamID == "" {
		return ListFilter{}, fmt.Errorf("team files need a team_id: %w", domain.ErrInvalid)
	}
	if filter.TeamID != "" && filter.Scope != "" && filter.Scope != domain.ScopeTeam {
		return ListFilter{}, fmt.Errorf("team_id is only for team files: %w", domain.ErrInvalid)
	}
	if filter.TeamID != "" {
		filter.Scope = domain.ScopeTeam
	}
	if filter.Purpose != "" && !filter.Purpose.Valid() {
		return ListFilter{}, fmt.Errorf("purpose is not allowed: %w", domain.ErrInvalid)
	}
	if _, _, err := filter.Date.Range(); err != nil {
		return ListFilter{}, err
	}
	return filter, nil
}

func conceal(err error) error {
	if errors.Is(err, domain.ErrNotFound) {
		return domain.ErrNotFound
	}
	return err
}

func randomID(prefix string, size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}
