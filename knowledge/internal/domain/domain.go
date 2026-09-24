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
	// ErrGrantRequired means acting as a member needs the standing grant they
	// give auth, and they have not given it or auth has dropped it. Only the
	// member can fix that, so asking again changes nothing until they do.
	ErrGrantRequired = errors.New("the member has not granted standing access")
	// ErrQualityCheckUnavailable means the model that judges whether a
	// document is worth indexing could not be reached. Nothing is published
	// unjudged, so the run stops and is tried again.
	ErrQualityCheckUnavailable = errors.New("the quality check is unavailable")
	// ErrUnreadable means the member a source reads as cannot read what it
	// names at all: Files refuses them the placement, because they left the
	// team or stopped being an admin. Trying again as them changes nothing;
	// only reading as someone who can does. It travels alongside the
	// refusal itself (ErrNotFound or ErrForbidden), never instead of it.
	ErrUnreadable = errors.New("the member it reads as cannot read it")
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

// Credential is the token a source is read with, and where it goes on a
// request.
type Credential struct {
	AccessToken string
	// In is "header" or "query"; Name and Prefix say how the token is sent.
	In       string
	Name     string
	Prefix   string
	Domain   string
	Metadata map[string]string
}

// Source is one thing an organization asked to be indexed, such as its own
// files. The member who registers it owns it, and extraction reads as them.
type Source struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	// ConnectionID and AppID name what a provider source reads through. An
	// internal source, such as the organization's own files, has neither.
	ConnectionID string `json:"connection_id,omitempty"`
	AppID        string `json:"app_id,omitempty"`
	// ComposioAccountID is the connected account Composio proxies calls
	// through for this source, taken from the connection at registration.
	// Empty for an internal source, which is read with a grant instead.
	ComposioAccountID string `json:"-"`
	Provider          string `json:"provider"`
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
	// IncludeKinds is what the source indexes, by kind of document (see
	// Kinds); nil is DefaultKinds.
	IncludeKinds []string `json:"include_kinds,omitempty"`
	// KindsPending says IncludeKinds changed since the source's documents
	// were last decided, so its next pass decides them again.
	KindsPending bool `json:"-"`
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

// Placement is where a source hangs: a scope, and the team or member it
// hangs on for a team or member scope.
type Placement struct {
	Scope    Scope
	TeamID   string
	MemberID string
}

// SourceQuery narrows a listing. An org admin sees the organization's
// sources; anyone else sees the ones they registered.
type SourceQuery struct {
	CreatedBy    string
	ConnectionID string
	Provider     string
	// Placement narrows the listing to the sources at one placement; nil
	// answers every placement.
	Placement *Placement
	Admin     bool
	// IncludeRemoved also answers sources a member removed. A listing never
	// wants them; deciding whether to register something on a member's
	// behalf does, since a source they removed is theirs saying no.
	IncludeRemoved bool
}

// CategoryMetadataKey is where a judged document's semantic category travels
// in its metadata.
const CategoryMetadataKey = "knowledge_category"

// searchableMetadata are the metadata keys a consumer indexes a document by:
// its category, and the purpose and access label Files gives a stored file,
// which decide who may read it. The rest of what a provider says about a
// document — a label, a comment count, a state — is kept with it, but is not
// on its own a new version of what is searched: an issue commented on, or a
// message archived, still says what it said.
var searchableMetadata = []string{CategoryMetadataKey, "purpose", "access"}

// SearchableMetadataEqual says whether two versions of a document's metadata
// agree on everything a consumer indexes it by, present or not.
func SearchableMetadataEqual(previous, current map[string]string) bool {
	for _, key := range searchableMetadata {
		before, had := previous[key]
		after, has := current[key]
		if had != has || before != after {
			return false
		}
	}
	return true
}

// AppConnection is what apps answered about one of a member's connections to
// a third-party app: the little of it a provider source needs to decide
// whether it may index through it, and what to read it with. Apps forwards
// nothing secret; the credential itself never leaves Composio.
type AppConnection struct {
	ID                string
	Toolkit           string
	Status            string
	MemberID          string
	ComposioAccountID string
	Label             string
}

// AppTarget is one thing inside a connection that could be indexed, as apps
// discovered it from the app itself. Target is what a source is registered
// with, so nothing has to know each provider's spelling of one.
type AppTarget struct {
	// Kind is repository, channel or database.
	Kind string
	ID   string
	// Name is what a member would read: "#engineering" rather than an id.
	Name   string
	Target map[string]string
}

// Document is one thing a source holds, normalized. Body is the text a
// retrieval pipeline indexes; ContentHash over it is the version.
type Document struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	// ExternalID is the provider's own identifier, stable across runs. It is
	// what makes a document the same document.
	ExternalID  string `json:"external_id"`
	Kind        string `json:"kind"`
	Title       string `json:"title,omitempty"`
	URL         string `json:"url,omitempty"`
	MIME        string `json:"mime"`
	ContentHash string `json:"content_hash"`
	Body        string `json:"-"`
	ByteSize    int    `json:"byte_size"`
	// Original is where this document's own bytes live in the configured
	// bucket, for one whose text knowledge does not read itself — a PDF, an
	// Office file. Nil for every other document, which is the vast majority.
	Original        *Original         `json:"-"`
	Metadata        map[string]string `json:"metadata,omitempty"`
	SourceUpdatedAt *time.Time        `json:"source_updated_at,omitempty"`
	ExtractedAt     time.Time         `json:"extracted_at"`
	// LastSeenAt is when a run last read this document at its source. A
	// document a full pass did not see is one the source no longer holds.
	LastSeenAt time.Time  `json:"last_seen_at"`
	RemovedAt  *time.Time `json:"removed_at,omitempty"`
	// Excluded says why this document was judged not worth indexing; empty
	// for one that was not. An excluded document is removed like any other,
	// but its row stays, so the same content is not judged twice.
	Excluded string `json:"excluded,omitempty"`
	// JudgedPolicy is the quality policy this document's content was last
	// judged under; zero when it never was. A newer policy judges it again.
	JudgedPolicy int `json:"-"`
	// Judgment is the judge's own answer about this content, as JSON: what
	// its verdict can be drawn from again when the source's choice of kinds
	// changes. Nil for a document no judge was asked about.
	Judgment []byte `json:"-"`
}

// Original names the object that holds a document's bytes as its provider
// gave them, in the bucket KNOWLEDGE_ORIGINALS_BUCKET configures. It mirrors
// contracts/events/knowledge/v1.Original, which is built from this at
// publish time; domain depends on nothing outside the standard library, so
// the two stay separate types even though their fields match.
type Original struct {
	Bucket string
	Key    string
	Size   int64
	// SHA256 is over the bytes, hex encoded, and equals the owning
	// Document's ContentHash: the object's key is content addressed on it.
	SHA256 string
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
	// Token is Watermark's counterpart for a provider whose watermark is an
	// opaque cursor rather than a moment — Drive's changes.startPageToken,
	// Gmail's historyId. At most one of Watermark and Token is ever set, by
	// the extractor that reads this source; like Watermark, it moves only
	// once a pass drains, never mid-walk, so a run that stops partway keeps
	// what it saved rather than narrowing the next incremental pass's
	// starting point before this one actually finished.
	Token string
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
	DocumentsExcluded  int        `json:"documents_excluded"`
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
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. Code building an Event to record leaves it unset:
	// the write path captures the current trace itself (see insertEvents),
	// rather than trusting a caller to have filled this in.
	TraceContext string
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
