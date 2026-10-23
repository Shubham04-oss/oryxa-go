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
	"github.com/Shubham04-oss/oryxa-go/internal/access"
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
// OrgRole is access.Role: the rules for it live there.
type OrgRole = access.Role

const (
	OrgRoleOwner  OrgRole = "owner"
	OrgRoleAdmin  OrgRole = "admin"
	OrgRoleMember OrgRole = "member"
)

type OrgMembership struct {
	OrganizationID   string
	OrganizationName string
	UserID           string
	Email            string
	DisplayName      string
	Role             OrgRole
	// Agent says the caller is an agent acting for this member rather than
	// the member themselves, which auth says of every token it mints for an
	// agent. An agent may run with its member absent, answering someone
	// else: a teammate's interaction request, a room it was mentioned in.
	// What it reads can end up in front of any of them, so it gets a plain
	// member's reach whatever its member's role; see ActsAsAdmin.
	Agent bool
	// AgentID and AgentRole name which of the member's agents it is (agt_…,
	// and primary, specialist or organization), when Agent is set.
	AgentID   string
	AgentRole string
	// AgentCorpora is the corpus ids a specialist's grant names, or "*" for
	// all. Nil, which a grant without the field gives, is all.
	AgentCorpora []string
}

// MayReadCorpus refuses a specialist a corpus its grant does not name, as
// not found, so a corpus outside the grant looks the same as one that does
// not exist. Every other caller is limited only by what its member can see.
func (membership OrgMembership) MayReadCorpus(corpusID string) error {
	if !membership.actor().Reaches(membership.AgentCorpora, corpusID) {
		return ErrNotFound
	}
	return nil
}

// CorporaLimited is whether the caller is a specialist whose grant names
// some corpora rather than all.
func (membership OrgMembership) CorporaLimited() bool {
	return membership.actor().Limited(membership.AgentCorpora)
}

// ActsAsAdmin reports whether the caller acts with an org admin's reach:
// every member's corpora, every team's whether or not they are on it, the
// organization's corpora to write to, and the documents only org owners and
// admins may read. Only a person's own session does. An agent acting for an
// admin gets what a plain member would: its member's own corpora, the teams
// its member is actually on, and the organization's members-level documents.
func (membership OrgMembership) ActsAsAdmin() bool { return membership.actor().ActsAsAdmin() }

// TeamReach narrows what teams answered about one team to what the caller may
// use (access.Actor.TeamReach): for an agent, its member's own place on the
// team. False means the caller can't see the team.
func (membership OrgMembership) TeamReach(team TeamAccess) (TeamAccess, bool) {
	visible, manage := membership.actor().TeamReach(team.CanSee(), team.Member, team.TeamAdmin, team.CanManage)
	team.CanManage = manage
	return team, visible
}

func (membership OrgMembership) actor() access.Actor {
	return access.Actor{Role: membership.Role, Agent: membership.Agent, AgentRole: membership.AgentRole}
}

// TeamAccess is what teams answers about one team. An empty TeamID means the
// caller cannot see it, which the caller must report as not found.
type TeamAccess struct {
	TeamID string
	// CanManage is teams' own answer, which is true for every org admin as
	// well as for the team's own admins.
	CanManage bool
	// Member says the caller is on the team. Teams also shows every team to
	// an org admin who is not.
	Member bool
	// TeamAdmin says the caller holds the team's admin role themselves,
	// rather than managing it as an org admin.
	TeamAdmin bool
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
	"access", "chunk_id", "chunk_ordinal", "corpus_id", "document_id", "heading_path",
	"organization_id", "source_kind", "source_ref", "title",
}

// knowledgeProvenanceKeys are what rag's Knowledge bridge writes on a document
// it indexes from Knowledge: which Knowledge document and source it is, the
// app it was read from, what kind of thing it is, and what Knowledge's judge
// classified it as. Search results and citations show them, and filters
// narrow on them, so a caller who could set them could make a note of their
// own cite as, say, a Gmail financial record. The bridge checks its own
// metadata with Validate, which lets them through; ValidateCallerDocument
// does not.
var knowledgeProvenanceKeys = []string{
	"category", "document_kind", "knowledge_document_id", "knowledge_source_id", "provider",
}

// corpusOriginKey marks a corpus rag's Knowledge bridge built. The bridge
// trusts it when it looks for its own corpus, and the web app shows it, so a
// caller may not set it; see ValidateCallerCorpus.
const corpusOriginKey = "origin"

type Metadata map[string]string

// Validate reports whether metadata is within the bounds every backend shares
// and stays off the keys the pipeline writes on every chunk. It is the check
// for metadata rag writes itself. Metadata a caller sends goes through
// ValidateCallerDocument or ValidateCallerCorpus instead, which also keep a
// caller off the keys only rag may set.
func (metadata Metadata) Validate() error {
	return metadata.validate(nil)
}

// ValidateCallerDocument is Validate for the metadata a caller sends with a
// document. The Knowledge provenance keys are refused too, so only a document
// rag indexed from Knowledge can carry them.
func (metadata Metadata) ValidateCallerDocument() error {
	return metadata.validate(knowledgeProvenanceKeys)
}

// ValidateCallerCorpus is Validate for the metadata a caller sends with a
// corpus. origin is refused too: it marks a corpus rag built from Knowledge,
// which only rag may say.
func (metadata Metadata) ValidateCallerCorpus() error {
	return metadata.validate([]string{corpusOriginKey})
}

// validate is Validate, refusing callerReserved as well.
func (metadata Metadata) validate(callerReserved []string) error {
	if len(metadata) > MaxMetadataKeys {
		return fmt.Errorf("metadata may carry at most %d keys: %w", MaxMetadataKeys, ErrInvalid)
	}
	total := 0
	for key, value := range metadata {
		if !metadataKeyPattern.MatchString(key) {
			return fmt.Errorf("metadata key %q must be lowercase letters, digits and underscores, at most %d characters: %w", key, MaxMetadataKeyLength, ErrInvalid)
		}
		if slices.Contains(reservedMetadataKeys, key) || slices.Contains(callerReserved, key) {
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
