// Package domain holds knowledge's model: the sources an organization asked
// to be indexed, the documents extracted from them, the runs that do the
// extracting, and the rules all three follow. It depends on nothing but the
// standard library.
package domain

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
)

var (
	ErrInvalid             = errors.New("invalid input")
	ErrNotFound            = errors.New("not found")
	ErrForbidden           = errors.New("forbidden")
	ErrUnauthorized        = errors.New("unauthorized")
	ErrConflict            = errors.New("conflict")
	ErrNeedsReconnect      = errors.New("connection needs reconnect")
	ErrCredentialRejected  = errors.New("provider rejected the credential")
	ErrScopeRequired       = errors.New("scope required")
	ErrRateLimited         = errors.New("too many requests")
	ErrProviderUnavailable = errors.New("provider unavailable")
	// ErrLeaseLost means another replica claimed the source while this run was
	// working it. The run stops rather than write what it no longer owns.
	ErrLeaseLost = errors.New("the source was claimed by another worker")
	// ErrUnsupportedType means the document's content type holds no text
	// knowledge can extract.
	ErrUnsupportedType = errors.New("content type cannot be extracted")
)

type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

func (role OrgRole) IsOrgAdmin() bool { return role == OrgRoleOwner || role == OrgRoleAdmin }

// OrgMembership is what auth answered for the caller and one organization.
type OrgMembership struct {
	OrganizationID string
	UserID         string
	Role           OrgRole
	// Agent says the caller is an agent acting for this member. An agent may
	// read what a source holds, but never register or remove one.
	Agent bool
}

// Scope is where the connection behind a source hangs, and so who may read
// the documents it yields.
type Scope string

const (
	ScopeMember       Scope = "member"
	ScopeTeam         Scope = "team"
	ScopeOrganization Scope = "organization"
)

func (scope Scope) Valid() bool {
	switch scope {
	case ScopeMember, ScopeTeam, ScopeOrganization:
		return true
	default:
		return false
	}
}

// Status is where a source stands. A removed source keeps its row until its
// documents have been retracted.
type Status string

const (
	StatusActive  Status = "active"
	StatusPaused  Status = "paused"
	StatusRemoved Status = "removed"
)

// Purpose is what knowledge asks connections for a credential for. Extraction
// only ever reads, so it asks as an action.
type Purpose string

const PurposeAction Purpose = "action"

// RenewBefore is how long before a credential lapses it stops being used.
// Connections renews inside this window itself, so a caller holding one any
// longer would be using a token connections has already replaced.
const RenewBefore = 2 * time.Minute

// Credential is a token connections issued for a connection, with where it
// goes on a request.
type Credential struct {
	AccessToken string
	// In is "header" or "query"; Name and Prefix say how the token is sent.
	In       string
	Name     string
	Prefix   string
	Domain   string
	Metadata map[string]string
}

// Connection is what connections tells knowledge about a connection.
type Connection struct {
	ID              string `json:"id"`
	OrganizationID  string `json:"organization_id"`
	AppID           string `json:"app_id"`
	Provider        string `json:"provider"`
	Scope           string `json:"scope"`
	TeamID          string `json:"team_id,omitempty"`
	MemberID        string `json:"member_id,omitempty"`
	ProviderAccount struct {
		ID   string `json:"id"`
		Name string `json:"name,omitempty"`
	} `json:"provider_account"`
	Status string `json:"status"`
	Usage  string `json:"usage"`
}

// UsageAdmins is the connection usage a bare service may not act with: an
// organization connection its admins reserved. A source on one would fail
// every run, so registering it is refused instead.
const UsageAdmins = "admins"

// Source is one thing an organization asked to be indexed: a repository, a
// workspace, a channel, its own files. The member who registers it owns it,
// and extraction reads as them, so it stops when they lose the connection.
type Source struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	// ConnectionID is what the source is read through, and AppID the app that
	// connection belongs to. An internal source, such as the organization's
	// own files, has neither.
	ConnectionID string `json:"connection_id,omitempty"`
	AppID        string `json:"app_id,omitempty"`
	Provider     string `json:"provider"`
	// Target is what to index at the provider, in the provider's terms, such
	// as {"repository": "acme/api"}.
	Target   map[string]string `json:"target"`
	Scope    Scope             `json:"scope"`
	TeamID   string            `json:"team_id,omitempty"`
	MemberID string            `json:"member_id,omitempty"`
	// CreatedBy is the member whose source this is, and whom extraction reads
	// as.
	CreatedBy string    `json:"created_by"`
	Status    Status    `json:"status"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	// Interval is how often the source is swept for changes.
	Interval time.Duration `json:"interval"`
	// SyncAt is when the next run is due, and LastSyncedAt when one last
	// finished.
	SyncAt       *time.Time `json:"sync_at,omitempty"`
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	// FullSweepAt is when a pass last read this source from the beginning and
	// retracted what it did not see. Nil means never: the source is due for
	// one now, the same as if it had just been registered.
	FullSweepAt *time.Time `json:"full_sweep_at,omitempty"`
	// Attempts counts consecutive failures, and backs the next try off.
	Attempts int `json:"-"`
	// LeaseToken and LeaseUntil are how one replica claims a run. A run that
	// no longer holds the lease stops.
	LeaseToken string     `json:"-"`
	LeaseUntil *time.Time `json:"-"`
	Version    int64      `json:"-"`
}

// Extractable says whether a run may read this source now.
func (source Source) Extractable() error {
	switch source.Status {
	case StatusActive:
		return nil
	case StatusPaused:
		return fmt.Errorf("the source is paused: %w", ErrConflict)
	default:
		return ErrNotFound
	}
}

// SourceQuery narrows a listing. An org admin sees the organization's
// sources; anyone else sees the ones they registered.
type SourceQuery struct {
	CreatedBy    string
	ConnectionID string
	Provider     string
	Admin        bool
}

// Document is one thing a source holds, normalized. Body is the text a
// retrieval pipeline indexes; ContentHash over it is the version.
type Document struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	// ExternalID is the provider's own identifier, stable across runs. It is
	// what makes a document the same document.
	ExternalID      string            `json:"external_id"`
	Kind            string            `json:"kind"`
	Title           string            `json:"title,omitempty"`
	URL             string            `json:"url,omitempty"`
	MIME            string            `json:"mime"`
	ContentHash     string            `json:"content_hash"`
	Body            string            `json:"-"`
	ByteSize        int               `json:"byte_size"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	SourceUpdatedAt *time.Time        `json:"source_updated_at,omitempty"`
	ExtractedAt     time.Time         `json:"extracted_at"`
	// LastSeenAt is when a run last read this document at its source. A
	// document a full pass did not see is one the source no longer holds.
	LastSeenAt time.Time  `json:"last_seen_at"`
	RemovedAt  *time.Time `json:"removed_at,omitempty"`
}

// Cursor is where a run stopped, so the next one carries on rather than
// starting over.
type Cursor struct {
	SourceID string
	// Value is the provider's own page token, opaque to knowledge.
	Value string
	// Watermark is the newest change knowledge has seen, for a provider that
	// filters by time rather than paging.
	Watermark *time.Time
	// WalkStartedAt is when the walk this cursor is mid-way through began:
	// the first of what can be several runs, when a source outgrows one
	// run's page budget. Set when a walk starts from the beginning, carried
	// unchanged while it resumes, and cleared once it drains. A full pass
	// retracts what it did not see against this, not against whichever run
	// happens to finish it.
	WalkStartedAt *time.Time
	UpdatedAt     time.Time
}

// RunStatus is where an extraction run ended up.
type RunStatus string

const (
	RunRunning   RunStatus = "running"
	RunCompleted RunStatus = "completed"
	RunFailed    RunStatus = "failed"
)

// SyncRun is one pass over a source. Counts are what the run did, not what
// the source holds.
type SyncRun struct {
	ID                 string     `json:"id"`
	OrganizationID     string     `json:"organization_id"`
	SourceID           string     `json:"source_id"`
	Kind               string     `json:"kind"`
	Status             RunStatus  `json:"status"`
	DocumentsSeen      int        `json:"documents_seen"`
	DocumentsPublished int        `json:"documents_published"`
	DocumentsRemoved   int        `json:"documents_removed"`
	Error              string     `json:"error,omitempty"`
	Retryable          bool       `json:"retryable,omitempty"`
	StartedAt          time.Time  `json:"started_at"`
	FinishedAt         *time.Time `json:"finished_at,omitempty"`
}

type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       json.RawMessage
}

type OutboxEvent struct {
	Event
	Attempts int
}

const (
	// MinInterval and MaxInterval bound how often a source may be swept.
	MinInterval     = 5 * time.Minute
	MaxInterval     = 7 * 24 * time.Hour
	DefaultInterval = time.Hour

	// DefaultFullSweepInterval is how often a source already indexed is read
	// from the beginning again when nothing configures otherwise, so a
	// document removed at the provider is eventually noticed even long after
	// a source's first pass. serviceconfig applies the same default to
	// KNOWLEDGE_FULL_SWEEP_INTERVAL; this is the fallback for a caller that
	// builds the application directly, such as a test.
	DefaultFullSweepInterval = 7 * 24 * time.Hour

	// MaxTargetFields and maxTargetValue bound what a target may say.
	MaxTargetFields = 8
	maxTargetValue  = 200
)

var targetField = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)

// CheckTarget returns the target as it will be stored, refusing anything a
// provider could not read back.
func CheckTarget(target map[string]string) (map[string]string, error) {
	if len(target) > MaxTargetFields {
		return nil, fmt.Errorf("a target takes at most %d fields: %w", MaxTargetFields, ErrInvalid)
	}
	checked := make(map[string]string, len(target))
	for _, name := range slices.Sorted(maps.Keys(target)) {
		if !targetField.MatchString(name) {
			return nil, fmt.Errorf("target field %q must be lowercase letters, digits and underscores: %w", name, ErrInvalid)
		}
		value := strings.TrimSpace(target[name])
		if value == "" {
			return nil, fmt.Errorf("target field %q has no value: %w", name, ErrInvalid)
		}
		if len(value) > maxTargetValue {
			return nil, fmt.Errorf("target field %q is longer than %d characters: %w", name, maxTargetValue, ErrInvalid)
		}
		checked[name] = value
	}
	return checked, nil
}

// CheckInterval returns the sweep interval to store, defaulting an unset one.
func CheckInterval(interval time.Duration) (time.Duration, error) {
	if interval == 0 {
		return DefaultInterval, nil
	}
	if interval < MinInterval || interval > MaxInterval {
		return 0, fmt.Errorf("interval must be between %s and %s: %w", MinInterval, MaxInterval, ErrInvalid)
	}
	return interval, nil
}

// Place says where a source hangs, from the connection it reads through.
func Place(scope Scope, teamID, memberID string) (string, string, error) {
	switch scope {
	case ScopeMember:
		if strings.TrimSpace(memberID) == "" {
			return "", "", fmt.Errorf("a member source needs a member: %w", ErrInvalid)
		}
		return "", memberID, nil
	case ScopeTeam:
		if strings.TrimSpace(teamID) == "" {
			return "", "", fmt.Errorf("a team source needs a team: %w", ErrInvalid)
		}
		return teamID, "", nil
	case ScopeOrganization:
		return "", "", nil
	default:
		return "", "", fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
}

// retryCeiling caps how far a failing source backs off between runs.
const retryCeiling = 6

// RetryAfter is how long a source waits after a failed run, doubling with
// each consecutive failure so a provider that is down is not hammered.
func RetryAfter(attempts int) time.Duration {
	return MinInterval * time.Duration(1<<min(max(attempts, 0), retryCeiling))
}
