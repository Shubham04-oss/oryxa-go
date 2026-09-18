package application

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	knowledgeeventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/knowledgeclient"
)

// Knowledge extracts documents from an organization's connected apps and
// publishes one event per document, whatever it came from. Rag holds no
// extractor of its own: this is the bridge that turns those events into a
// searchable corpus, entirely through the same Store.UpsertDocument and
// corpus-creation path every other document and corpus goes through, so
// chunking, embedding and vector deletion happen exactly as they would for
// anything a member added by hand.
//
// One knowledge corpus exists per owner in an organization — one for each
// member, one for each team, one for the organization at large — never mixing
// scopes, and it is created the first time a document needs it.

// knowledgeCorpusName is what every corpus this bridge creates is called,
// whichever scope it hangs at.
const knowledgeCorpusName = "Knowledge from connected apps"

// knowledgeCorpusOrigin marks a corpus this bridge created, in its metadata,
// so it reads as what it is rather than a member's own corpus that happens to
// share the name.
const knowledgeCorpusOrigin = "knowledge"

// knowledgeServiceCreator marks the creator of a team- or organization-scoped
// corpus or document this bridge makes on knowledge's behalf, for when the
// event that caused it names no member: knowledge's own placement rule ties
// MemberID to member scope alone, so a team- or organization-scoped document
// never carries one today (see knowledge/internal/domain.Place). rag's schema
// only requires created_by to be non-empty text, not a real member id, so
// this is what "whatever the schema allows" resolves to.
//
// It is deliberately not shaped like a real id — auth's are Cognito subjects
// (UUIDs) and teams' are "team_"-prefixed — so it can never equal one and
// pick up the "creator may delete their own corpus" right created_by
// otherwise carries. A team manager or an org admin can still remove it; see
// rag/README.md.
const knowledgeServiceCreator = "svc_knowledge"

// DocumentPublished indexes a document knowledge extracted, or updates the
// one already indexed from it.
//
// Knowledge is the only extractor here; rag never turns bytes into text
// itself. Delivery is at least once and possibly out of order, so every step
// is safe to repeat: a re-delivery of what is already indexed does nothing, a
// genuinely new hash replaces the indexed text, and an event older than what
// is already recorded is ignored rather than regressing it.
func (service *Service) DocumentPublished(ctx context.Context, event knowledgeeventsv1.DocumentPublished) error {
	scope, err := domain.ParseScope(event.Scope)
	if err != nil {
		return fmt.Errorf("knowledge document %s: %w", event.DocumentID, err)
	}
	if err := knowledgeScopeSafe(scope, event.TeamID, event.MemberID); err != nil {
		// Indexing this anywhere else would risk a member-scoped document
		// landing where the whole organization, or the wrong team, can read
		// it. That is the one mistake this bridge must never make, so an
		// event that cannot be placed safely is refused rather than guessed.
		service.logger.WarnContext(ctx, "a knowledge document named no owner for its scope; refusing to index it",
			"document_id", event.DocumentID, "source_id", event.SourceID, "scope", string(scope), "error", err)
		return nil
	}

	mapping, found, err := service.knowledgeMapping(ctx, event.OrganizationID, event.DocumentID)
	if err != nil {
		return err
	}
	if found {
		if event.ExtractedAt.Before(mapping.ExtractedAt) {
			service.logger.InfoContext(ctx, "an out-of-order knowledge document event arrived; ignoring it",
				"document_id", event.DocumentID, "event_extracted_at", event.ExtractedAt, "mapped_extracted_at", mapping.ExtractedAt)
			return nil
		}
		if event.ContentHash == mapping.ContentHash {
			return nil
		}
	}

	body := event.Body
	if event.BodyOmitted {
		// The largest documents an organization has are the ones it most
		// wants to search, so a body that could not travel is read back
		// rather than lost.
		fetched, err := service.fetchKnowledgeBody(ctx, event)
		if err != nil {
			return err
		}
		if fetched == "" {
			// Nothing can be indexed for it. A version already held no
			// longer matches what the source has, so it is removed rather
			// than left answering from stale text.
			if !found {
				service.logger.InfoContext(ctx, "a knowledge document's body was too large to carry and could not be read back; skipping",
					"document_id", event.DocumentID, "source_id", event.SourceID)
				return nil
			}
			service.logger.InfoContext(ctx, "a knowledge document's body was too large to carry and could not be read back; removing the version already indexed",
				"document_id", event.DocumentID, "source_id", event.SourceID)
			return service.removeKnowledgeDocument(ctx, event.OrganizationID, mapping)
		}
		body = fetched
	}

	text, err := domain.Text(body)
	if err != nil {
		return service.dropUnindexable(ctx, event.OrganizationID, found, mapping, event.DocumentID, "had no usable text")
	}
	contentType, err := domain.ContentType(event.MIME)
	if err != nil {
		contentType = "text/plain"
	}
	normalized := chunking.Normalize(text, contentType)
	if strings.TrimSpace(normalized) == "" {
		return service.dropUnindexable(ctx, event.OrganizationID, found, mapping, event.DocumentID, "had no text once normalized")
	}

	creator := knowledgeCreator(scope, event.MemberID)
	corpus, err := service.knowledgeCorpus(ctx, event.OrganizationID, scope, event.TeamID, event.MemberID, creator)
	if err != nil {
		return fmt.Errorf("find or create the corpus for a knowledge document: %w", err)
	}

	ref := knowledgeDocumentRef(event)
	if found {
		previous, previousErr := service.store.Document(ctx, event.OrganizationID, mapping.DocumentID)
		switch {
		case previousErr == nil && previous.Source.Ref != ref:
			// The document changed what it points at — its URL moved. The
			// unique key UpsertDocument matches on is (corpus, ref), so
			// leaving the old row behind would orphan it: never updated,
			// never removed, indexed forever under a ref nothing points to
			// any more.
			if err := service.removeIndexedDocument(ctx, event.OrganizationID, corpus, previous); err != nil {
				return fmt.Errorf("remove the document a changed reference replaces: %w", err)
			}
		case previousErr != nil && !isMissing(previousErr):
			return previousErr
		}
	}

	title := strings.TrimSpace(event.Title)
	if title == "" {
		title = event.ExternalID
	}
	title, err = domain.Title(boundedTitle(title))
	if err != nil {
		return err
	}
	metadata := domain.Metadata{
		"knowledge_document_id": event.DocumentID,
		"knowledge_source_id":   event.SourceID,
		"provider":              event.Provider,
	}
	if err := metadata.Validate(); err != nil {
		return err
	}

	id, err := domain.NewID(domain.DocumentIDPrefix)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	document := domain.Document{
		ID: id, CorpusID: corpus.ID, OrganizationID: event.OrganizationID,
		Source: domain.Source{Kind: domain.SourceExternal, Ref: ref},
		Title:  title, ContentType: contentType, ContentSHA256: domain.Hash(normalized), ByteSize: int64(len(normalized)),
		Metadata: metadata, Status: domain.StatusPending,
		CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
	}
	received, err := service.event(rageventsv1.DocumentReceivedDetailType, rageventsv1.DocumentReceived{
		DocumentID: id, CorpusID: corpus.ID, OrganizationID: event.OrganizationID,
		SourceKind: string(domain.SourceExternal), SourceRef: ref,
		ContentType: contentType, ContentSHA256: document.ContentSHA256, ByteSize: document.ByteSize,
		CreatedBy: creator, ReceivedAt: now,
	})
	if err != nil {
		return err
	}
	stored, _, err := service.store.UpsertDocument(ctx, document, normalized, received)
	if err != nil {
		return err
	}

	return service.store.UpsertKnowledgeDocumentMapping(ctx, domain.KnowledgeDocumentMapping{
		KnowledgeDocumentID: event.DocumentID, OrganizationID: event.OrganizationID,
		CorpusID: corpus.ID, DocumentID: stored.ID,
		ContentHash: event.ContentHash, ExtractedAt: event.ExtractedAt, UpdatedAt: now,
	})
}

// DocumentRemoved deletes the rag document a knowledge document became, and
// forgets the mapping between them.
//
// A knowledge document rag never indexed — its body was always too large to
// carry, or it was refused for its scope — has no mapping to find, which is
// correct rather than a missed removal: there was nothing here to remove.
func (service *Service) DocumentRemoved(ctx context.Context, event knowledgeeventsv1.DocumentRemoved) error {
	mapping, found, err := service.knowledgeMapping(ctx, event.OrganizationID, event.DocumentID)
	if err != nil {
		return err
	}
	if !found {
		service.logger.InfoContext(ctx, "a removed knowledge document was never indexed; nothing to do",
			"document_id", event.DocumentID, "source_id", event.SourceID)
		return nil
	}
	if err := service.removeKnowledgeDocument(ctx, event.OrganizationID, mapping); err != nil {
		return err
	}
	service.logger.InfoContext(ctx, "removed a document whose knowledge source removed it",
		"document_id", mapping.DocumentID, "corpus_id", mapping.CorpusID, "knowledge_document_id", event.DocumentID)
	return nil
}

// knowledgeMapping is KnowledgeDocumentMapping with domain.ErrNotFound turned
// into a plain bool, which is what every caller here actually branches on.
func (service *Service) knowledgeMapping(ctx context.Context, organizationID, knowledgeDocumentID string) (domain.KnowledgeDocumentMapping, bool, error) {
	mapping, err := service.store.KnowledgeDocumentMapping(ctx, organizationID, knowledgeDocumentID)
	if err == nil {
		return mapping, true, nil
	}
	if errors.Is(err, domain.ErrNotFound) {
		return domain.KnowledgeDocumentMapping{}, false, nil
	}
	return domain.KnowledgeDocumentMapping{}, false, err
}

// dropUnindexable is what happens to a knowledge document rag cannot turn
// into text: valid UTF-8 text that is empty or all whitespace, either as
// delivered or once normalized for its content type. Rag holds nothing worth
// searching either way, so a version already indexed from an earlier, better
// event is removed rather than left to answer from text that no longer
// matches the source.
func (service *Service) dropUnindexable(ctx context.Context, organizationID string, found bool, mapping domain.KnowledgeDocumentMapping, knowledgeDocumentID, why string) error {
	service.logger.WarnContext(ctx, "a knowledge document could not be indexed; skipping it",
		"document_id", knowledgeDocumentID, "reason", why)
	if !found {
		return nil
	}
	return service.removeKnowledgeDocument(ctx, organizationID, mapping)
}

// removeKnowledgeDocument deletes the rag document a mapping points at, and
// the mapping itself. It is shared by an explicit removal and by a publish
// that can no longer be indexed, since both leave the same thing behind: a
// mapping that must no longer find anything.
func (service *Service) removeKnowledgeDocument(ctx context.Context, organizationID string, mapping domain.KnowledgeDocumentMapping) error {
	corpus, err := service.store.Corpus(ctx, organizationID, mapping.CorpusID)
	if err != nil {
		if isMissing(err) {
			// The corpus, and everything under it, is already gone — by
			// cascade, the mapping went with it. Nothing left to do.
			return nil
		}
		return err
	}
	document, err := service.store.Document(ctx, organizationID, mapping.DocumentID)
	if err != nil {
		if isMissing(err) {
			return service.forgetKnowledgeMapping(ctx, organizationID, mapping.KnowledgeDocumentID)
		}
		return err
	}
	if err := service.removeIndexedDocument(ctx, organizationID, corpus, document); err != nil {
		return err
	}
	return service.forgetKnowledgeMapping(ctx, organizationID, mapping.KnowledgeDocumentID)
}

// removeIndexedDocument deletes one rag document and its vectors, vectors
// first, exactly in the order every other removal in this service uses: a
// failure between the two must never leave a vector nothing points at.
func (service *Service) removeIndexedDocument(ctx context.Context, organizationID string, corpus domain.Corpus, document domain.Document) error {
	chunkIDs, err := service.store.ChunkIDs(ctx, organizationID, document.ID)
	if err != nil {
		return err
	}
	if err := service.deleteVectors(ctx, corpus, chunkIDs); err != nil {
		return err
	}
	removal, err := service.event(rageventsv1.DocumentRemovedDetailType, rageventsv1.DocumentRemoved{
		DocumentID: document.ID, CorpusID: corpus.ID, OrganizationID: organizationID,
		SourceKind: string(document.Source.Kind), SourceRef: document.Source.Ref,
		Reason: rageventsv1.ReasonSourceDeleted, RemovedAt: service.now().UTC(),
	})
	if err != nil {
		return err
	}
	if _, err := service.store.DeleteDocument(ctx, organizationID, document.ID, removal); err != nil && !isMissing(err) {
		return err
	}
	return nil
}

func (service *Service) forgetKnowledgeMapping(ctx context.Context, organizationID, knowledgeDocumentID string) error {
	if err := service.store.DeleteKnowledgeDocumentMapping(ctx, organizationID, knowledgeDocumentID); err != nil && !isMissing(err) {
		return err
	}
	return nil
}

// knowledgeCorpus finds the one corpus a knowledge document of this scope and
// owner belongs in, creating it with rag's own defaults the first time it is
// needed.
//
// The slug is deterministic per owner, so two replicas handling the same
// organization's events at once compute the same one and race on rag's own
// unique index rather than on anything of this bridge's own making: the
// loser reads back what the winner just created.
func (service *Service) knowledgeCorpus(ctx context.Context, organizationID string, scope domain.Scope, teamID, memberID, creator string) (domain.Corpus, error) {
	slug, err := knowledgeCorpusSlug(scope, teamID, memberID)
	if err != nil {
		return domain.Corpus{}, err
	}
	existing, err := service.store.CorpusBySlug(ctx, organizationID, slug)
	switch {
	case err == nil:
		return existing, nil
	case !errors.Is(err, domain.ErrNotFound):
		return domain.Corpus{}, err
	}

	placedTeamID, placedMemberID, err := domain.Place(scope, teamID, memberID)
	if err != nil {
		return domain.Corpus{}, err
	}
	// The same defaults a member's own corpus gets when they name none: see
	// Service.CreateCorpus. Nothing here invents a model of its own.
	spec, err := domain.ResolveSpec(service.defaults.Provider, service.defaults.Model, 0, service.defaults.Metric)
	if err != nil {
		return domain.Corpus{}, err
	}
	backend := service.defaults.Backend
	if err := backend.Supports(spec.Metric); err != nil {
		return domain.Corpus{}, err
	}
	if _, err := service.vectors.For(domain.Corpus{Backend: backend}); err != nil {
		return domain.Corpus{}, fmt.Errorf("this deployment cannot serve %q: %w", backend, domain.ErrInvalid)
	}
	name, err := domain.Name(knowledgeCorpusName)
	if err != nil {
		return domain.Corpus{}, err
	}
	metadata := domain.Metadata{"origin": knowledgeCorpusOrigin}
	if err := metadata.Validate(); err != nil {
		return domain.Corpus{}, err
	}

	id, err := domain.NewID(domain.CorpusIDPrefix)
	if err != nil {
		return domain.Corpus{}, err
	}
	now := service.now().UTC()
	corpus := domain.Corpus{
		ID: id, OrganizationID: organizationID, Slug: slug, Name: name,
		Scope: scope, TeamID: placedTeamID, MemberID: placedMemberID,
		Spec: spec, Backend: backend, Chunking: domain.DefaultChunkPolicy(spec), Metadata: metadata,
		CreatedBy: creator, CreatedAt: now, UpdatedAt: now,
	}
	event, err := service.event(rageventsv1.CorpusCreatedDetailType, rageventsv1.CorpusCreated{
		CorpusID: corpus.ID, OrganizationID: organizationID, Slug: slug, Name: name,
		Scope: string(scope), TeamID: placedTeamID, MemberID: placedMemberID,
		Provider: string(spec.Provider), Model: spec.Model, Dimensions: spec.Dimensions,
		Metric: string(spec.Metric), Backend: string(backend),
		CreatedBy: creator, CreatedAt: now,
	})
	if err != nil {
		return domain.Corpus{}, err
	}
	if err := service.store.CreateCorpus(ctx, corpus, event); err != nil {
		if errors.Is(err, domain.ErrConflict) {
			// Another replica created it between our lookup and our insert.
			return service.store.CorpusBySlug(ctx, organizationID, slug)
		}
		return domain.Corpus{}, err
	}
	return corpus, nil
}

// knowledgeScopeSafe refuses a knowledge document whose event does not carry
// the id its scope needs to be placed safely.
func knowledgeScopeSafe(scope domain.Scope, teamID, memberID string) error {
	switch scope {
	case domain.ScopeMember:
		if strings.TrimSpace(memberID) == "" {
			return fmt.Errorf("a member-scoped document named no member: %w", domain.ErrInvalid)
		}
	case domain.ScopeTeam:
		if strings.TrimSpace(teamID) == "" {
			return fmt.Errorf("a team-scoped document named no team: %w", domain.ErrInvalid)
		}
	case domain.ScopeOrganization:
	default:
		return fmt.Errorf("scope %q is not allowed: %w", scope, domain.ErrInvalid)
	}
	return nil
}

// knowledgeCreator is who a corpus or a document this bridge writes is
// recorded as created by: the member for member scope (knowledgeScopeSafe
// already guarantees one is present by the time this is called), or whoever
// the event named even at a broader scope should knowledge ever start
// sending one, or knowledgeServiceCreator otherwise.
func knowledgeCreator(scope domain.Scope, memberID string) string {
	if strings.TrimSpace(memberID) != "" {
		return memberID
	}
	return knowledgeServiceCreator
}

// knowledgeCorpusSlug is deterministic per owner: same scope and owner always
// yields the same slug, which is what lets a corpus be found rather than
// re-created on every event.
//
// auth mints member ids, and teams mints team ids, as a prefix plus
// unpadded-base64 (see auth's and teams' randomID), which can hold
// underscores and uppercase letters that a rag slug refuses — domain.Slug
// allows only lowercase letters, digits and hyphens. slugSafe lowercases and
// turns anything else into a hyphen, which keeps the result deterministic and
// still readable, at the cost of no longer being reversible in the rare case
// where two different ids collide once transformed.
func knowledgeCorpusSlug(scope domain.Scope, teamID, memberID string) (string, error) {
	var raw string
	switch scope {
	case domain.ScopeMember:
		raw = "knowledge-member-" + slugSafe(memberID)
	case domain.ScopeTeam:
		raw = "knowledge-team-" + slugSafe(teamID)
	case domain.ScopeOrganization:
		raw = "knowledge-organization"
	default:
		return "", fmt.Errorf("a knowledge document must be member, team or organization scoped, not %q: %w", scope, domain.ErrInvalid)
	}
	if len(raw) > 63 {
		raw = strings.TrimRight(raw[:63], "-")
	}
	return domain.Slug(raw)
}

// slugSafe derives a stable, lowercase, slug-safe form of an id. See
// knowledgeCorpusSlug for why one is needed.
func slugSafe(id string) string {
	lower := strings.ToLower(strings.TrimSpace(id))
	safe := make([]rune, 0, len(lower))
	for _, r := range lower {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			safe = append(safe, r)
		} else {
			safe = append(safe, '-')
		}
	}
	return string(safe)
}

// knowledgeDocumentRef is the source reference a knowledge document is
// indexed under: its URL when that is usable as one, so a citation can point
// back at where a person reads it, and a stable fallback otherwise so the
// document still has a ref to be found and updated by.
func knowledgeDocumentRef(event knowledgeeventsv1.DocumentPublished) string {
	raw := strings.TrimSpace(event.URL)
	if raw != "" && len(raw) <= 2048 {
		if parsed, err := url.Parse(raw); err == nil && parsed.IsAbs() {
			return raw
		}
	}
	return "knowledge:" + event.DocumentID
}

// boundedTitle trims a title to what domain.Title accepts, rather than
// refusing an otherwise good document over a display field a provider let
// run long.
func boundedTitle(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if runes := []rune(trimmed); len(runes) > 200 {
		trimmed = string(runes[:200])
	}
	return trimmed
}

// fetchKnowledgeBody reads back a body that was too large to travel on the
// bus. An empty answer means there is nothing to index and the caller
// decides what to do about a version already held.
//
// Only an outage is returned as an error, so the bus redelivers it. A
// deployment that cannot reach knowledge's internal API, or a document it no
// longer holds, is reported once and dropped: redelivering forever would not
// make either true.
func (service *Service) fetchKnowledgeBody(ctx context.Context, event knowledgeeventsv1.DocumentPublished) (string, error) {
	if service.documents == nil {
		return "", nil
	}
	document, err := service.documents.Body(ctx, event.OrganizationID, event.DocumentID)
	switch {
	case err == nil:
	case errors.Is(err, knowledgeclient.ErrNotFound), errors.Is(err, knowledgeclient.ErrNotConfigured):
		service.logger.InfoContext(ctx, "a knowledge document's body could not be read back",
			"document_id", event.DocumentID, "error", err)
		return "", nil
	default:
		return "", fmt.Errorf("read back a knowledge document's body: %w", err)
	}
	if document.Body == "" {
		return "", nil
	}
	// Knowledge may have read the document again between publishing this
	// event and answering, and what came back would then be a different
	// version than the one this event describes. Indexing it under this
	// event's hash would leave the two disagreeing about what is stored, and
	// the next publish for it would look like no change at all.
	if document.ContentHash != "" && event.ContentHash != "" && document.ContentHash != event.ContentHash {
		service.logger.InfoContext(ctx, "a knowledge document changed while its body was being read back; waiting for the event that describes it",
			"document_id", event.DocumentID)
		return "", nil
	}
	return document.Body, nil
}
