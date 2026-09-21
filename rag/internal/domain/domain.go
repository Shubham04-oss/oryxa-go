// Package domain holds what retrieval-augmented generation is made of in
// Oryxa, and the rules that hold whatever backend or model is behind it: a
// corpus and the embedding spec it pins, the documents that go into it, the
// chunks they become, and the queries that come back out.
//
// Two rules earn their place here rather than in a store or a provider.
// A corpus pins its embedding spec at creation and never changes it, because
// vectors from two models do not share a space and a mixed index answers
// plausibly and wrongly. And metadata stays small and flat, because the same
// corpus has to be portable between pgvector and S3 Vectors, and S3 Vectors
// bounds what it will hold and filter on.
package domain

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInvalid      = errors.New("invalid input")
	ErrTooLarge     = errors.New("too large")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrForbidden    = errors.New("forbidden")
	ErrUnauthorized = errors.New("unauthorized")
)

// ID prefixes. They are visible in logs and in the vector keys S3 Vectors
// stores, so they stay short and say what they name.
const (
	CorpusIDPrefix   = "cor_"
	DocumentIDPrefix = "doc_"
	ChunkIDPrefix    = "chk_"
	EventIDPrefix    = "evt_"
)

// NewID mints a URL-safe identifier under one of the prefixes above. The
// entropy is 96 bits, which is what the other services mint.
func NewID(prefix string) (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate identifier: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(value), nil
}

// Scope is where a corpus hangs, exactly as it is for a file. Privilege still
// comes from auth, and from teams for a team corpus; scope only says where the
// thing lives and therefore who may look for it.
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

func ParseScope(raw string) (Scope, error) {
	value := Scope(strings.TrimSpace(raw))
	if value == "" {
		return ScopeMember, nil
	}
	if !value.Valid() {
		return "", fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
	return value, nil
}

// OrgRole and OrgMembership are what auth answers when this service asks who
// the caller is. Rag never decides identity itself.
type OrgRole string

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

func (role OrgRole) Valid() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin || role == OrgRoleMember
}

func (role OrgRole) IsOrgAdmin() bool {
	return role == OrgRoleOwner || role == OrgRoleAdmin
}

type OrgMembership struct {
	OrganizationID   string
	OrganizationName string
	UserID           string
	Email            string
	DisplayName      string
	Role             OrgRole
}

// TeamAccess is what teams answers about one team. An empty TeamID means the
// caller cannot see it, which the caller must report as not found.
type TeamAccess struct {
	TeamID    string
	CanManage bool
	Member    bool
}

func (access TeamAccess) CanSee() bool { return access.TeamID != "" }

// Metadata travels with every chunk and is what a filtered search narrows on.
//
// The bounds are not arbitrary. S3 Vectors caps the metadata it keeps per
// vector and the number of keys it will filter on, so a corpus written under
// looser rules would index on pgvector and then fail to move. Keeping one
// bound for both backends means the corpus is portable by construction.
// The total is the tighter of the two backends' per-vector budgets for
// metadata that can be filtered on, rather than the larger one, so a corpus
// indexed on pgvector can be moved to S3 Vectors without rewriting what its
// callers are allowed to store.
const (
	MaxMetadataKeys      = 16
	MaxMetadataKeyLength = 63
	MaxMetadataValue     = 1024
	MaxMetadataBytes     = 2 << 10
)

// metadataKeyPattern keeps keys safe to use as JSON paths, as S3 Vectors
// filter keys, and in logs.
var metadataKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// reservedMetadataKeys are the keys the pipeline writes itself. A caller that
// set them could shadow provenance and make a citation lie about its source.
var reservedMetadataKeys = []string{
	"chunk_id", "chunk_ordinal", "corpus_id", "document_id", "heading_path",
	"organization_id", "source_kind", "source_ref", "title",
}

type Metadata map[string]string

// Validate reports whether metadata is within the bounds every backend shares.
func (metadata Metadata) Validate() error {
	if len(metadata) > MaxMetadataKeys {
		return fmt.Errorf("metadata may carry at most %d keys: %w", MaxMetadataKeys, ErrInvalid)
	}
	total := 0
	for key, value := range metadata {
		if !metadataKeyPattern.MatchString(key) {
			return fmt.Errorf("metadata key %q must be lowercase letters, digits and underscores, at most %d characters: %w", key, MaxMetadataKeyLength, ErrInvalid)
		}
		if slices.Contains(reservedMetadataKeys, key) {
			return fmt.Errorf("metadata key %q is reserved for provenance: %w", key, ErrInvalid)
		}
		if !utf8.ValidString(value) {
			return fmt.Errorf("metadata value for %q is not valid UTF-8: %w", key, ErrInvalid)
		}
		if len(value) > MaxMetadataValue {
			return fmt.Errorf("metadata value for %q must be at most %d bytes: %w", key, MaxMetadataValue, ErrInvalid)
		}
		total += len(key) + len(value)
	}
	if total > MaxMetadataBytes {
		return fmt.Errorf("metadata must be at most %d bytes in total: %w", MaxMetadataBytes, ErrInvalid)
	}
	return nil
}

// Merge returns a copy of metadata with the entries of other laid over it.
// Neither input is modified, because a document's metadata is shared by every
// chunk it produces.
func (metadata Metadata) Merge(other Metadata) Metadata {
	merged := make(Metadata, len(metadata)+len(other))
	maps.Copy(merged, metadata)
	maps.Copy(merged, other)
	return merged
}

// Clone copies metadata so a caller cannot reach back into stored state.
func (metadata Metadata) Clone() Metadata {
	if metadata == nil {
		return nil
	}
	return maps.Clone(metadata)
}

// Name bounds a human-facing corpus name.
func Name(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if name == "" {
		return "", fmt.Errorf("name is required: %w", ErrInvalid)
	}
	if utf8.RuneCountInString(name) > 120 {
		return "", fmt.Errorf("name must be at most 120 characters: %w", ErrInvalid)
	}
	if strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("name is invalid: %w", ErrInvalid)
	}
	return name, nil
}

// slugPattern is what a corpus slug must look like. The slug is unique per
// organization, so a caller can address a corpus by a name it chose rather
// than by an identifier it has to store.
var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func Slug(raw string) (string, error) {
	slug := strings.ToLower(strings.TrimSpace(raw))
	if !slugPattern.MatchString(slug) {
		return "", fmt.Errorf("slug must be 2 to 63 lowercase letters, digits or hyphens: %w", ErrInvalid)
	}
	return slug, nil
}

// Corpus is one searchable collection inside an organization. Its embedding
// spec and backend are fixed at creation: everything already indexed was
// written under them, so changing either would leave the index answering from
// two different spaces. Re-indexing means a new corpus.
type Corpus struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organization_id"`
	Slug           string        `json:"slug"`
	Name           string        `json:"name"`
	Scope          Scope         `json:"scope"`
	TeamID         string        `json:"team_id,omitempty"`
	MemberID       string        `json:"member_id,omitempty"`
	Spec           EmbeddingSpec `json:"embedding"`
	Backend        Backend       `json:"backend"`
	Chunking       ChunkPolicy   `json:"chunking"`
	Metadata       Metadata      `json:"metadata,omitempty"`
	DocumentCount  int64         `json:"document_count"`
	ChunkCount     int64         `json:"chunk_count"`
	CreatedBy      string        `json:"created_by"`
	CreatedAt      time.Time     `json:"created_at"`
	UpdatedAt      time.Time     `json:"updated_at"`
}

// Place decides where a corpus hangs, and refuses the combinations that would
// leave it unreachable or ambiguous. It mirrors what files does, so a member
// who can see a file can reason about a corpus the same way.
func Place(scope Scope, teamID, createdBy string) (placedTeamID, memberID string, err error) {
	switch scope {
	case ScopeMember:
		if strings.TrimSpace(teamID) != "" {
			return "", "", fmt.Errorf("member corpora cannot name a team: %w", ErrInvalid)
		}
		if createdBy == "" {
			return "", "", fmt.Errorf("member corpora need an owner: %w", ErrInvalid)
		}
		return "", createdBy, nil
	case ScopeTeam:
		teamID = strings.TrimSpace(teamID)
		if teamID == "" {
			return "", "", fmt.Errorf("team corpora need a team_id: %w", ErrInvalid)
		}
		return teamID, "", nil
	case ScopeOrganization:
		if strings.TrimSpace(teamID) != "" {
			return "", "", fmt.Errorf("organization corpora cannot name a team: %w", ErrInvalid)
		}
		return "", "", nil
	default:
		return "", "", fmt.Errorf("scope is not allowed: %w", ErrInvalid)
	}
}

// Event is one thing that happened, waiting in the outbox for the dispatcher.
type Event struct {
	ID         string
	DetailType string
	OccurredAt time.Time
	Data       []byte
	// TraceContext is the outbox row's stored trace, carried through from
	// outbox.Event so Publisher can link its producer span to the write that
	// created this event. Code building an Event to record leaves it unset:
	// the write path captures the current trace itself (see insertEvents),
	// rather than trusting a caller to have filled this in.
	TraceContext string
}
