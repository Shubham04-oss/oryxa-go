// Package v1 contains the stable version-one events emitted by knowledge:
// the documents it extracts from an organization's sources, the documents
// that are gone, and how each extraction run ended.
//
// A source is one thing an organization asked to be indexed: a repository, a
// workspace, a channel, its own files. Knowledge pulls documents out of it,
// normalizes them, and publishes each as one event, whatever the provider, so
// a retrieval pipeline consumes documents without knowing a provider.
//
// Scope is where the connection behind the source hangs: member, team or
// organization, as in connections. It travels on every document because it is
// who may read that document; a consumer that retrieves across an
// organization must honour it.
//
// A document is published whenever its content hash changes, and not
// otherwise, so an extraction run that finds nothing new publishes nothing.
// Delivery is at least once, as everywhere on the bus: deduplicate on the
// envelope's event_id, or treat ContentHash as the version it is. Events
// never carry a token, a secret or a credential.
package v1

import "time"

const (
	Source = "oryxa.knowledge"

	DocumentPublishedDetailType = "knowledge.document.published.v1"
	DocumentRemovedDetailType   = "knowledge.document.removed.v1"
	SyncCompletedDetailType     = "knowledge.sync.completed.v1"
	SyncFailedDetailType        = "knowledge.sync.failed.v1"
	SourcePausedDetailType      = "knowledge.source.paused.v1"
)

// MaxBodyBytes is the most of a document's text that an event carries.
// EventBridge takes events of at most 256 KiB, so a larger body is left out
// and flagged; DocumentID still names what to fetch from knowledge.
const MaxBodyBytes = 128 << 10

// Kinds name what a document was at its provider, in the provider's terms
// flattened to something a consumer can filter on.
const (
	KindIssue       = "issue"
	KindPullRequest = "pull_request"
	KindComment     = "comment"
	KindMessage     = "message"
	KindPage        = "page"
	KindFile        = "file"
)

// A run is either the first pass over everything a source holds, or the
// catch-up that follows a change.
const (
	SyncBackfill    = "backfill"
	SyncIncremental = "incremental"
)

// DocumentPublished is one document a source holds, as knowledge last read
// it. It is published when the document is first seen and whenever its
// ContentHash changes, so a consumer upserts on (SourceID, ExternalID).
type DocumentPublished struct {
	DocumentID     string `json:"document_id"`
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	// ConnectionID is the connection the document was read through. An
	// internal source, such as the organization's own files, has none.
	ConnectionID string `json:"connection_id,omitempty"`
	Provider     string `json:"provider"`
	// Scope, TeamID and MemberID are who may read this document, taken from
	// the connection behind its source.
	Scope    string `json:"scope"`
	TeamID   string `json:"team_id,omitempty"`
	MemberID string `json:"member_id,omitempty"`
	// ExternalID is the provider's own identifier, stable across runs. It is
	// what makes a document the same document.
	ExternalID string `json:"external_id"`
	Kind       string `json:"kind"`
	Title      string `json:"title,omitempty"`
	// URL is where a person reads this document at the provider, for a
	// citation to point at.
	URL  string `json:"url,omitempty"`
	MIME string `json:"mime"`
	// ContentHash is over the normalized body. It is the version: equal
	// hashes mean nothing changed, whatever else moved.
	ContentHash string `json:"content_hash"`
	// Body is the normalized text, when it fits MaxBodyBytes. BodyOmitted
	// says it did not, and the body must be read from knowledge instead.
	Body        string `json:"body,omitempty"`
	BodyOmitted bool   `json:"body_omitted,omitempty"`
	// Metadata is what the provider said about the document beyond its text:
	// an author, a label, a channel. Small, flat, and never a credential.
	Metadata map[string]string `json:"metadata,omitempty"`
	// SourceUpdatedAt is when the provider last changed the document, when it
	// says; ExtractedAt is when knowledge read it.
	SourceUpdatedAt *time.Time `json:"source_updated_at,omitempty"`
	ExtractedAt     time.Time  `json:"extracted_at"`
}

// DocumentRemoved is a document a consumer should forget. It is published
// when the document is gone from its source, and for every document of a
// source that is itself removed.
type DocumentRemoved struct {
	DocumentID     string    `json:"document_id"`
	OrganizationID string    `json:"organization_id"`
	SourceID       string    `json:"source_id"`
	Provider       string    `json:"provider"`
	ExternalID     string    `json:"external_id"`
	Reason         string    `json:"reason"`
	RemovedAt      time.Time `json:"removed_at"`
}

// SyncCompleted is one extraction run that finished. Counts are what the run
// did, not what the source holds.
type SyncCompleted struct {
	RunID          string `json:"run_id"`
	OrganizationID string `json:"organization_id"`
	SourceID       string `json:"source_id"`
	Provider       string `json:"provider"`
	Kind           string `json:"kind"`
	// DocumentsSeen is everything the run read; Published is what had changed.
	DocumentsSeen      int       `json:"documents_seen"`
	DocumentsPublished int       `json:"documents_published"`
	DocumentsRemoved   int       `json:"documents_removed"`
	StartedAt          time.Time `json:"started_at"`
	CompletedAt        time.Time `json:"completed_at"`
}

// SyncFailed is a run that stopped early. What it published before stopping
// stands: a run resumes from its cursor rather than starting over.
type SyncFailed struct {
	RunID              string `json:"run_id"`
	OrganizationID     string `json:"organization_id"`
	SourceID           string `json:"source_id"`
	Provider           string `json:"provider"`
	Kind               string `json:"kind"`
	Error              string `json:"error"`
	Retryable          bool   `json:"retryable"`
	DocumentsPublished int    `json:"documents_published"`
	// Resumable says a cursor was kept, so the next run carries on from where
	// this one stopped.
	Resumable bool      `json:"resumable"`
	StartedAt time.Time `json:"started_at"`
	FailedAt  time.Time `json:"failed_at"`
}

// SourcePaused is a source knowledge stopped extracting from, because what it
// reads through went away. Its documents are left alone; a paused source is
// resumed by reconnecting, never on its own.
type SourcePaused struct {
	SourceID       string    `json:"source_id"`
	OrganizationID string    `json:"organization_id"`
	ConnectionID   string    `json:"connection_id,omitempty"`
	Provider       string    `json:"provider"`
	Reason         string    `json:"reason"`
	PausedAt       time.Time `json:"paused_at"`
}

// Sync errors are the reasons a run stopped, in the caller's terms. A
// provider's own error body never reaches an event.
const (
	SyncErrorNeedsReconnect      = "needs_reconnect"
	SyncErrorScopeRequired       = "scope_required"
	SyncErrorCredentialRejected  = "credential_rejected"
	SyncErrorForbidden           = "forbidden"
	SyncErrorNotFound            = "not_found"
	SyncErrorRateLimited         = "rate_limited"
	SyncErrorProviderUnavailable = "provider_unavailable"
	SyncErrorProviderError       = "provider_error"
	// SyncErrorLeaseLost: another replica claimed the source mid-run, so this
	// run stopped rather than write twice.
	SyncErrorLeaseLost = "lease_lost"
)

// Removal reasons say why a document is gone.
const (
	RemovedReasonDeleted       = "deleted"
	RemovedReasonSourceRemoved = "source_removed"
	// RemovedReasonConnectionLost: the credential behind the source stopped
	// working and could not be renewed. RemovedReasonConnectionRemoved: the
	// connection was disconnected on purpose. A consumer telling a member why
	// their documents went needs the difference.
	RemovedReasonConnectionLost      = "connection_lost"
	RemovedReasonConnectionRemoved   = "connection_removed"
	RemovedReasonOrganizationDeleted = "organization_deleted"
	RemovedReasonMemberLeft          = "member_left"
	RemovedReasonNoLongerReadable    = "no_longer_readable"
)

// Pause reasons say what stopped a source.
const (
	PausedReasonDisconnected   = "disconnected"
	PausedReasonNeedsReconnect = "needs_reconnect"
	PausedReasonAppDeleted     = "app_deleted"
	PausedReasonScopeRequired  = "scope_required"
)
