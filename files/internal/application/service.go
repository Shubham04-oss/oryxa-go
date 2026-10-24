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
	"github.com/Shubham04-oss/oryxa-go/internal/access"
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

// Recorder keeps the access decisions files makes, for the member's Activity
// page; *access.Log is one. It never fails the caller, so it returns nothing.
type Recorder interface {
	Record(ctx context.Context, decision access.Decision)
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
	decisions Recorder
	shadow    access.Comparer
	// engine, when set, decides canSee and canDelete from SpiceDB
	// (ACCESS_ENGINE_FILES=spicedb), and relationships writes a file's place
	// in it as the file is created or deleted, so the uploader can open it at
	// once rather than when the projector catches up.
	engine        access.Decider
	relationships Relationships
	logger        *slog.Logger
	now           func() time.Time
}

// New builds the service. A nil decisions records nothing.
func New(store Store, blobs blob.Store, directory Directory, teams Teams, decisions Recorder, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Service{store: store, blobs: blobs, directory: directory, teams: teams, decisions: decisions, logger: logger, now: time.Now}
}

// Relationships keeps a file's relationships in SpiceDB as the file comes and
// goes. The projector also writes them from the file's events; this only
// makes them there sooner.
type Relationships interface {
	FileCreated(ctx context.Context, object domain.Object) error
	FileDeleted(ctx context.Context, organizationID, fileID string) error
}

// WithEngine makes SpiceDB decide what canSee and canDelete answer, and
// writes each file's relationships as it is created or deleted.
func (s *Service) WithEngine(engine access.Decider, relationships Relationships) *Service {
	s.engine, s.relationships = engine, relationships
	return s
}

// WithShadow sets where the service's decisions are compared with SpiceDB.
// Without one it compares nothing.
func (s *Service) WithShadow(shadow access.Comparer) *Service {
	s.shadow = shadow
	return s
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
	level := input.Access
	if level == "" {
		level = domain.AccessMembers
	}
	if !level.Valid() {
		return domain.Object{}, fmt.Errorf("access is not allowed: %w", domain.ErrInvalid)
	}
	if purpose == domain.PurposeBillingAttachment {
		level = domain.AccessAdmins
	}
	if level == domain.AccessAdmins && (scope != domain.ScopeOrganization || !org.ActsAsAdmin()) {
		return domain.Object{}, domain.ErrForbidden
	}
	teamID, memberID, err := domain.Place(scope, input.TeamID, org.UserID)
	if err != nil {
		return domain.Object{}, err
	}
	teams := s.teamLookup(ctx, credentials, org, organizationID)
	if err := s.mayCreate(org, teams, scope, teamID); err != nil {
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
		Access:         level,
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
	if s.relationships != nil {
		// The file stands either way: its event brings the projector here too.
		if err := s.relationships.FileCreated(ctx, object); err != nil {
			s.logger.WarnContext(ctx, "file relationships not written to spicedb; the projector will", "file_id", object.ID, "error", err)
		}
	}
	s.record(ctx, org, teams, object, access.ActionWrite)
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
	teams := s.teamLookup(ctx, credentials, org, organizationID)
	if filter.Scope == domain.ScopeTeam {
		// A team's files may be listed only by a caller who could read a
		// file on it, so a team they can't reach, or one outside a
		// specialist's grant, is as missing as one that doesn't exist.
		if err := s.canSee(ctx, org, teams, domain.Object{OrganizationID: organizationID, Scope: domain.ScopeTeam, TeamID: filter.TeamID}); err != nil {
			return nil, err
		}
	}
	// The query only narrows by placement; whether each file it returns is
	// listed is canSee's to say, exactly as for Get. A list records no
	// decisions: opening a file does, at Get.
	objects, err := s.store.Objects(ctx, organizationID, s.now().UTC(), domain.ObjectQuery{
		Scope:       filter.Scope,
		TeamID:      filter.TeamID,
		MemberID:    org.UserID,
		Admin:       org.ActsAsAdmin(),
		Purpose:     filter.Purpose,
		CreatedFrom: from,
		CreatedTo:   to,
	})
	if err != nil {
		return nil, err
	}
	visible := objects[:0]
	for _, object := range objects {
		err := s.canSee(ctx, org, teams, object)
		if errors.Is(err, domain.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		visible = append(visible, object)
	}
	return visible, nil
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
	teams := s.teamLookup(ctx, credentials, org, organizationID)
	if err := s.canSee(ctx, org, teams, object); err != nil {
		return domain.Object{}, err
	}
	// Content and DownloadURL read through here, so each records this once.
	s.record(ctx, org, teams, object, access.ActionRead)
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
	teams := s.teamLookup(ctx, credentials, org, organizationID)
	if err := s.canSee(ctx, org, teams, object); err != nil {
		return err
	}
	if err := s.canDelete(ctx, org, teams, object); err != nil {
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
	if err := s.store.DeleteObject(ctx, organizationID, object.ID, deleted); err != nil {
		return err
	}
	if s.relationships != nil {
		if err := s.relationships.FileDeleted(ctx, organizationID, object.ID); err != nil {
			s.logger.WarnContext(ctx, "file relationships not deleted from spicedb; the projector will", "file_id", object.ID, "error", err)
		}
	}
	s.record(ctx, org, teams, object, access.ActionDelete)
	return nil
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

func (s *Service) mayCreate(org domain.OrgMembership, teams teamLookup, scope domain.Scope, teamID string) error {
	// A specialist writes only where its grant lets it read, so it can read
	// back what it stores. A team outside the grant is not found, as one its
	// member is not on would be; its member's own or the organization's
	// files, which it knows exist, are forbidden.
	if err := org.MayReachFiles(scope, teamID); err != nil {
		if scope == domain.ScopeTeam {
			return err
		}
		return domain.ErrForbidden
	}
	switch scope {
	case domain.ScopeMember:
		return nil
	case domain.ScopeOrganization:
		if !org.ActsAsAdmin() {
			return domain.ErrForbidden
		}
		return nil
	case domain.ScopeTeam:
		_, err := teams(teamID)
		return err
	default:
		return fmt.Errorf("scope is not allowed: %w", domain.ErrInvalid)
	}
}

// record keeps one allowed decision on a single file. Lists don't call it.
func (s *Service) record(ctx context.Context, org domain.OrgMembership, teams teamLookup, object domain.Object, action string) {
	if s.decisions == nil {
		return
	}
	var ownerID string
	if object.Scope == domain.ScopeMember {
		ownerID = object.MemberID
	}
	var agentID, agentRole string
	if org.Agent {
		agentID, agentRole = org.AgentID, org.AgentRole
	}
	s.decisions.Record(ctx, access.Decision{
		OrganizationID: object.OrganizationID,
		Service:        "files",
		Action:         action,
		ResourceKind:   "file",
		ResourceID:     object.ID,
		OwnerID:        ownerID,
		ActorUserID:    org.UserID,
		ActorAgentID:   agentID,
		ActorAgentRole: agentRole,
		Reason:         reason(org, teams, object, action),
	})
}

// reason names the rule that let the caller act on object, once canSee (and
// canDelete or mayCreate) allowed it: the most specific rule that was
// actually needed, taken in this order.
//
//  1. grant: a specialist whose grant names some file scopes rather than
//     all. Its grant named this one, and that is the narrowest thing that let
//     it in, whatever its member's own standing.
//  2. owner: the caller's own member file, or, to delete, a member or team
//     file they uploaded (canDelete allows a creator).
//  3. organization: an organization file every member may read. Writing or
//     deleting one, or reading one only admins may, is org_admin's.
//  4. team_member / team_admin: the caller's own place on the file's team;
//     team_admin when managing the team was needed (deleting a teammate's
//     file) or when the caller sees the team as its admin without being on it.
//  5. org_admin: an owner's or admin's own session reaching what none of the
//     above gives: another member's file, an admin-only or organization
//     write, and every team's files. canSee never asks teams about an admin's
//     own session, so an admin's team file is org_admin even when they are on
//     the team.
//
// An agent never acts as an admin, so 5 is only ever a person's own session.
func reason(org domain.OrgMembership, teams teamLookup, object domain.Object, action string) string {
	if org.FilesLimited() {
		return access.ReasonGrant
	}
	creator := action == access.ActionDelete && object.CreatedBy == org.UserID
	switch object.Scope {
	case domain.ScopeMember:
		if object.MemberID == org.UserID || creator {
			return access.ReasonOwner
		}
	case domain.ScopeOrganization:
		if action == access.ActionRead && object.EffectiveAccess() == domain.AccessMembers {
			return access.ReasonOrganization
		}
	case domain.ScopeTeam:
		if creator {
			return access.ReasonOwner
		}
		if org.ActsAsAdmin() {
			return access.ReasonOrgAdmin
		}
		// Answered already, for the check this follows, so it asks nobody.
		team, _ := teams(object.TeamID)
		if action == access.ActionDelete || (team.TeamAdmin && !team.Member) {
			return access.ReasonTeamAdmin
		}
		return access.ReasonTeamMember
	}
	return access.ReasonOrgAdmin
}

// canSee is the one place every read of a file, its delete, and the list
// decide whether the caller may reach it, so a specialist's grant is enforced
// here once and a list can't show what Get would refuse.
func (s *Service) canSee(ctx context.Context, org domain.OrgMembership, teams teamLookup, object domain.Object) error {
	return s.decide(ctx, org, object, access.ActionRead, s.see(org, teams, object))
}

func (s *Service) see(org domain.OrgMembership, teams teamLookup, object domain.Object) error {
	if err := org.MayReachFiles(object.Scope, object.TeamID); err != nil {
		return err
	}
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
		_, err := teams(object.TeamID)
		return err
	default:
		return domain.ErrNotFound
	}
}

func (s *Service) canDelete(ctx context.Context, org domain.OrgMembership, teams teamLookup, object domain.Object) error {
	return s.decide(ctx, org, object, access.ActionDelete, s.mayDelete(org, teams, object))
}

func (s *Service) mayDelete(org domain.OrgMembership, teams teamLookup, object domain.Object) error {
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
		access, err := teams(object.TeamID)
		if err != nil {
			return err
		}
		if access.CanManage {
			return nil
		}
	}
	return domain.ErrNotFound
}

// decide returns what the caller gets for action on object, given the code's
// answer. With no engine the code's answer stands and SpiceDB is asked in
// shadow. With one, SpiceDB's stands, except where it has no answer (the org
// agent) or the code decided nothing (teams unreachable, a team check with no
// file behind it). A SpiceDB failure refuses.
//
// ponytail: a list still comes from SQL filtered by the code, so SpiceDB can
// only narrow it; LookupResources replaces that once lists flip.
func (s *Service) decide(ctx context.Context, org domain.OrgMembership, object domain.Object, action string, code error) error {
	decided := code == nil || errors.Is(code, domain.ErrNotFound) || errors.Is(code, domain.ErrForbidden)
	if s.engine == nil || object.ID == "" || !decided {
		if decided && object.ID != "" && s.shadow != nil {
			s.shadow.Compare(ctx, s.ask(org, object, action), code == nil)
		}
		return code
	}
	allowed, err := s.engine.Decide(ctx, s.ask(org, object, action), code == nil)
	switch {
	case errors.Is(err, access.ErrNoAnswer):
		return code
	case err != nil:
		return fmt.Errorf("decide access to file %s: %w", object.ID, err)
	case allowed:
		return nil
	case code != nil:
		return code
	default:
		return domain.ErrNotFound
	}
}

// ask is a decision on object in the terms SpiceDB is asked about it.
func (s *Service) ask(org domain.OrgMembership, object domain.Object, action string) access.Ask {
	ask := access.Ask{
		OrganizationID: object.OrganizationID, Service: "files", Action: action,
		ResourceKind: "file", ResourceID: object.ID, UserID: org.UserID,
		Actor:   access.Actor{Role: org.Role, Agent: org.Agent, AgentRole: org.AgentRole},
		Limited: org.FilesLimited(),
	}
	if org.Agent {
		ask.AgentID = org.AgentID
	}
	return ask
}

// teamLookup answers teamAccess for one team of the organization.
type teamLookup func(teamID string) (domain.TeamAccess, error)

// teamLookup is teamAccess for one request: it remembers each team's answer,
// so a list of a team's files asks teams once, not once per file.
func (s *Service) teamLookup(ctx context.Context, credentials authclient.Credentials, org domain.OrgMembership, organizationID string) teamLookup {
	type answer struct {
		access domain.TeamAccess
		err    error
	}
	answers := map[string]answer{}
	return func(teamID string) (domain.TeamAccess, error) {
		if known, ok := answers[teamID]; ok {
			return known.access, known.err
		}
		access, err := s.teamAccess(ctx, credentials, org, organizationID, teamID)
		answers[teamID] = answer{access, err}
		return access, err
	}
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
	access, visible := org.TeamReach(access)
	if !visible {
		return domain.TeamAccess{}, domain.ErrNotFound
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
