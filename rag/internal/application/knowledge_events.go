package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"time"

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
//
// It has only the event's own ExtractedAt to order it by; the bus delivers
// through DocumentPublishedAt, which also knows when the event was published.
func (service *Service) DocumentPublished(ctx context.Context, event knowledgeeventsv1.DocumentPublished) error {
	return service.DocumentPublishedAt(ctx, event, time.Time{})
}

// DocumentPublishedAt is DocumentPublished for a delivery that says when
// knowledge published the event: occurredAt, the envelope's signed
// occurred_at.
//
// That, rather than ExtractedAt, is what a publish is ordered by, against a
// removal and against the version already indexed. Knowledge republishes a
// document it read earlier with that earlier ExtractedAt — re-including one
// that a change of kinds had excluded does exactly this — so ordered by
// ExtractedAt, a republish that undoes a removal reads as older than the
// removal and is dropped for good. A publish that really is stale, such as a
// redelivery of one sent before the removal, was also published before it,
// and is still refused.
func (service *Service) DocumentPublishedAt(ctx context.Context, event knowledgeeventsv1.DocumentPublished, occurredAt time.Time) error {
	publishedAt := knowledgePublishedAt(event, occurredAt)
	return service.store.WithKnowledgeDocumentLock(ctx, event.OrganizationID, event.DocumentID, func() error {
		return service.documentPublished(ctx, event, publishedAt)
	})
}

// knowledgePublishedAt is the time a publish is ordered by: when knowledge
// published it, where the delivery says, and when knowledge read the
// document otherwise. Knowledge builds the event after reading the document,
// so the envelope's time is the later of the two; taking the later one either
// way means a disagreeing clock can only order an event later than its own
// extraction, never earlier.
//
// The mapping keeps this time as its ExtractedAt, which is documented as
// knowledge's time for the version the mapping reflects, and a removal is
// compared against it.
func knowledgePublishedAt(event knowledgeeventsv1.DocumentPublished, occurredAt time.Time) time.Time {
	if occurredAt.After(event.ExtractedAt) {
		return occurredAt.UTC()
	}
	return event.ExtractedAt
}

func (service *Service) documentPublished(ctx context.Context, event knowledgeeventsv1.DocumentPublished, publishedAt time.Time) error {
	access := domain.DocumentAccess(event.Access)
	if access == "" {
		access = domain.AccessMembers
	}
	if event.Provider == "files" && (event.Metadata["purpose"] == "billing.attachment" || event.Metadata["access"] == "admins") {
		access = domain.AccessAdmins
	}
	if !access.Valid() {
		return fmt.Errorf("knowledge document %s has invalid access: %w", event.DocumentID, domain.ErrInvalid)
	}
	if access == domain.AccessAdmins && (event.Provider != "files" ||
		(event.Metadata["purpose"] != "billing.attachment" && event.Metadata["access"] != "admins") ||
		event.Scope != string(domain.ScopeOrganization)) {
		return fmt.Errorf("knowledge document %s has unsupported admin access: %w", event.DocumentID, domain.ErrInvalid)
	}
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
	removedAt, err := service.store.KnowledgeDocumentRemovedAt(ctx, event.OrganizationID, event.DocumentID)
	if err == nil && !publishedAt.After(removedAt) {
		service.logger.InfoContext(ctx, "ignoring a knowledge publish older than its removal",
			"document_id", event.DocumentID, "published_at", publishedAt, "removed_at", removedAt)
		return nil
	}
	if err != nil && !isMissing(err) {
		return err
	}

	mapping, found, err := service.knowledgeMapping(ctx, event.OrganizationID, event.DocumentID)
	if err != nil {
		return err
	}
	if found && publishedAt.Before(mapping.ExtractedAt) {
		service.logger.InfoContext(ctx, "an out-of-order knowledge document event arrived; ignoring it",
			"document_id", event.DocumentID, "published_at", publishedAt, "mapped_at", mapping.ExtractedAt)
		return nil
	}

	contentType, err := domain.ContentType(event.MIME)
	if err != nil {
		contentType = "text/plain"
	}
	ref := knowledgeDocumentRef(event)
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
	if event.Kind != "" {
		metadata["document_kind"] = event.Kind
	}
	if category := event.Metadata["knowledge_category"]; category != "" {
		metadata["category"] = category
	}
	if err := metadata.Validate(); err != nil {
		return err
	}

	if found {
		// A redelivery, or a republish that changed nothing rag keeps, is
		// recognised before anything is read back: there is nothing to index
		// again, and a body left off the bus is not fetched only to find
		// that out.
		unchanged, err := service.knowledgeDocumentUnchanged(ctx, event, mapping, scope, access, ref, title, contentType, metadata)
		if err != nil {
			return err
		}
		if unchanged {
			if publishedAt.After(mapping.ExtractedAt) {
				mapping.ExtractedAt = publishedAt
				mapping.UpdatedAt = service.now().UTC()
				return service.store.UpsertKnowledgeDocumentMapping(ctx, mapping)
			}
			return nil
		}
	}

	// normalized is the text this document is indexed from, when it has
	// any; a document with an Original carries none — see the field's own
	// comment on knowledgeeventsv1.DocumentPublished — and originalBucket
	// and originalKey are what an ingest worker hands a knowledge base to
	// parse in its place. contentHash and byteSize describe whichever of
	// the two is what actually got indexed, since that is what a caller
	// asking about this document should see.
	var normalized, originalBucket, originalKey, contentHash string
	var byteSize int64
	switch {
	case event.Original != nil:
		originalBucket, originalKey = event.Original.Bucket, event.Original.Key
		contentHash, byteSize = event.Original.SHA256, event.Original.Size
	default:
		body := event.Body
		if event.BodyOmitted {
			// The largest documents an organization has are the ones it
			// most wants to search, so a body that could not travel is
			// read back rather than lost.
			fetched, outcome, err := service.fetchKnowledgeBody(ctx, event)
			if err != nil {
				return err
			}
			if outcome != bodyRead {
				return service.unreadKnowledgeBody(ctx, event, found, mapping, outcome)
			}
			body = fetched
		}
		text, err := domain.Text(body)
		if err != nil {
			return service.dropUnindexable(ctx, event.OrganizationID, found, mapping, event.DocumentID, "had no usable text")
		}
		normalized = chunking.Normalize(text, contentType)
		if strings.TrimSpace(normalized) == "" {
			return service.dropUnindexable(ctx, event.OrganizationID, found, mapping, event.DocumentID, "had no text once normalized")
		}
		contentHash, byteSize = domain.Hash(normalized), int64(len(normalized))
	}

	creator := knowledgeCreator(scope, event.MemberID)
	corpus, err := service.knowledgeCorpus(ctx, event.OrganizationID, scope, event.TeamID, event.MemberID, creator)
	if err != nil {
		return fmt.Errorf("find or create the corpus for a knowledge document: %w", err)
	}
	if event.Original != nil && corpus.Backend != domain.BackendBedrockKB {
		// Turning these bytes into something searchable means a backend
		// that parses them itself — today, a Bedrock knowledge base.
		// Anywhere else there is nothing here to chunk or embed, so this is
		// skipped rather than indexed as an empty shell with a title and
		// nothing to search inside it.
		return service.dropUnindexable(ctx, event.OrganizationID, found, mapping, event.DocumentID,
			"has only its original bytes, and this corpus cannot parse them")
	}

	if found {
		previous, previousErr := service.store.Document(ctx, event.OrganizationID, mapping.DocumentID)
		switch {
		case previousErr == nil && (mapping.CorpusID != corpus.ID || previous.Source.Ref != ref):
			// A moved owner or reference must lose its old indexed copy
			// before the new placement is written. The mapping cascades
			// when the old document is deleted.
			if err := service.removeKnowledgeDocument(ctx, event.OrganizationID, mapping); err != nil {
				return fmt.Errorf("remove the document a changed placement replaces: %w", err)
			}
		case isMissing(previousErr):
			if err := service.forgetKnowledgeMapping(ctx, event.OrganizationID, event.DocumentID); err != nil {
				return err
			}
		case previousErr != nil:
			return previousErr
		}
	}

	id, err := domain.NewID(domain.DocumentIDPrefix)
	if err != nil {
		return err
	}
	now := service.now().UTC()
	document := domain.Document{
		ID: id, CorpusID: corpus.ID, OrganizationID: event.OrganizationID,
		Source: domain.Source{Kind: domain.SourceExternal, Ref: ref},
		Title:  title, ContentType: contentType, ContentSHA256: contentHash, ByteSize: byteSize,
		Metadata: metadata, Access: access, Status: domain.StatusPending,
		OriginalBucket: originalBucket, OriginalKey: originalKey,
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
		ContentHash: event.ContentHash, ExtractedAt: publishedAt, UpdatedAt: now,
	})
}

// knowledgeDocumentUnchanged reports whether what a mapping already points at
// is exactly what this event would index: the same content, in a corpus of
// this bridge's placed where the event says, under the same reference,
// access, title, content type and metadata. It creates nothing and reads
// nothing back from knowledge to answer.
//
// A corpus is compared by its recorded placement rather than by the one
// knowledgeCorpus would pick, which may create one: an unchanged document
// already sits in a corpus that is its owner's.
func (service *Service) knowledgeDocumentUnchanged(ctx context.Context, event knowledgeeventsv1.DocumentPublished, mapping domain.KnowledgeDocumentMapping,
	scope domain.Scope, access domain.DocumentAccess, ref, title, contentType string, metadata domain.Metadata) (bool, error) {
	if event.ContentHash != mapping.ContentHash {
		return false, nil
	}
	previous, err := service.store.Document(ctx, event.OrganizationID, mapping.DocumentID)
	if isMissing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	corpus, err := service.store.Corpus(ctx, event.OrganizationID, mapping.CorpusID)
	if isMissing(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	teamID, memberID, err := domain.Place(scope, event.TeamID, event.MemberID)
	if err != nil {
		return false, err
	}
	return previous.CorpusID == corpus.ID && knowledgeCorpusMatches(corpus, event.OrganizationID, scope, teamID, memberID) &&
		previous.Source.Ref == ref && previous.Access == access && previous.Title == title &&
		previous.ContentType == contentType && maps.Equal(previous.Metadata, metadata), nil
}

// unreadKnowledgeBody is what becomes of an event whose body did not travel
// and could not be read back for it.
//
// What is already indexed stays, except in the one case where keeping it
// would mean answering from text the source no longer holds with nothing
// coming to replace it: the content changed, and this deployment cannot read
// the new version at all. A version knowledge has already replaced, or a
// document it no longer holds, is left alone — the event describing that
// version, or the removal, follows this one — so a redelivered or older
// event can never take away the document a newer one indexed.
func (service *Service) unreadKnowledgeBody(ctx context.Context, event knowledgeeventsv1.DocumentPublished, found bool, mapping domain.KnowledgeDocumentMapping, outcome bodyOutcome) error {
	if found && outcome == bodyUnreadable && event.ContentHash != mapping.ContentHash {
		service.logger.InfoContext(ctx, "a knowledge document's body was too large to carry and could not be read back; removing the version already indexed",
			"document_id", event.DocumentID, "source_id", event.SourceID)
		return service.removeKnowledgeDocument(ctx, event.OrganizationID, mapping)
	}
	service.logger.InfoContext(ctx, "a knowledge document's body was too large to carry and could not be read back for this version; skipping it",
		"document_id", event.DocumentID, "source_id", event.SourceID, "reason", outcome.String())
	return nil
}

// DocumentRemoved deletes the rag document a knowledge document became, and
// forgets the mapping between them.
//
// A knowledge document rag never indexed — its body was always too large to
// carry, or it was refused for its scope — has no mapping to find, which is
// correct rather than a missed removal: there was nothing here to remove.
func (service *Service) DocumentRemoved(ctx context.Context, event knowledgeeventsv1.DocumentRemoved) error {
	return service.store.WithKnowledgeDocumentLock(ctx, event.OrganizationID, event.DocumentID, func() error {
		return service.documentRemoved(ctx, event)
	})
}

func (service *Service) documentRemoved(ctx context.Context, event knowledgeeventsv1.DocumentRemoved) error {
	// A removal may overtake a publish on the bus. Keep its timestamp even
	// when there is no mapped document yet, so that earlier publish cannot
	// recreate content that Knowledge has already removed.
	if err := service.store.RecordKnowledgeDocumentRemoval(ctx, event.OrganizationID, event.DocumentID, event.RemovedAt); err != nil {
		return err
	}
	mapping, found, err := service.knowledgeMapping(ctx, event.OrganizationID, event.DocumentID)
	if err != nil {
		return err
	}
	if !found {
		service.logger.InfoContext(ctx, "a removed knowledge document was never indexed; nothing to do",
			"document_id", event.DocumentID, "source_id", event.SourceID)
		return nil
	}
	if event.RemovedAt.Before(mapping.ExtractedAt) {
		service.logger.InfoContext(ctx, "ignoring a knowledge removal older than the indexed version",
			"document_id", event.DocumentID, "removed_at", event.RemovedAt, "extracted_at", mapping.ExtractedAt)
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
	if err := service.removeDocumentVectors(ctx, corpus, document); err != nil {
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
	placedTeamID, placedMemberID, err := domain.Place(scope, teamID, memberID)
	if err != nil {
		return domain.Corpus{}, err
	}
	// Reuse an older corpus only when its recorded placement and origin match.
	// The old slug conversion was lossy, so another owner may have inherited
	// the same slug. Never index into that corpus.
	legacySlug, err := legacyKnowledgeCorpusSlug(scope, teamID, memberID)
	if err != nil {
		return domain.Corpus{}, err
	}
	legacy, err := service.store.CorpusBySlug(ctx, organizationID, legacySlug)
	switch {
	case err == nil && knowledgeCorpusMatches(legacy, organizationID, scope, placedTeamID, placedMemberID):
		return legacy, nil
	case err != nil && !errors.Is(err, domain.ErrNotFound):
		return domain.Corpus{}, err
	}

	slug, err := knowledgeCorpusSlug(scope, teamID, memberID)
	if err != nil {
		return domain.Corpus{}, err
	}
	existing, err := service.store.CorpusBySlug(ctx, organizationID, slug)
	switch {
	case err == nil:
		return checkedKnowledgeCorpus(existing, organizationID, scope, placedTeamID, placedMemberID)
	case !errors.Is(err, domain.ErrNotFound):
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
	if err := requireBackendSpec(backend, spec); err != nil {
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
			existing, err := service.store.CorpusBySlug(ctx, organizationID, slug)
			if err != nil {
				return domain.Corpus{}, err
			}
			return checkedKnowledgeCorpus(existing, organizationID, scope, placedTeamID, placedMemberID)
		}
		return domain.Corpus{}, err
	}
	return corpus, nil
}

func knowledgeCorpusMatches(corpus domain.Corpus, organizationID string, scope domain.Scope, teamID, memberID string) bool {
	return corpus.OrganizationID == organizationID && corpus.Scope == scope &&
		corpus.TeamID == teamID && corpus.MemberID == memberID &&
		corpus.Metadata["origin"] == knowledgeCorpusOrigin
}

func checkedKnowledgeCorpus(corpus domain.Corpus, organizationID string, scope domain.Scope, teamID, memberID string) (domain.Corpus, error) {
	if !knowledgeCorpusMatches(corpus, organizationID, scope, teamID, memberID) {
		return domain.Corpus{}, fmt.Errorf("knowledge corpus slug is occupied by another owner or origin: %w", domain.ErrConflict)
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

// knowledgeCorpusSlug hashes the full owner id. A readable slug conversion
// loses case, punctuation and long suffixes, which can merge different owners.
func knowledgeCorpusSlug(scope domain.Scope, teamID, memberID string) (string, error) {
	var prefix, owner string
	switch scope {
	case domain.ScopeMember:
		prefix, owner = "knowledge-member-", memberID
	case domain.ScopeTeam:
		prefix, owner = "knowledge-team-", teamID
	case domain.ScopeOrganization:
		return domain.Slug("knowledge-organization")
	default:
		return "", fmt.Errorf("a knowledge document must be member, team or organization scoped, not %q: %w", scope, domain.ErrInvalid)
	}
	digest := sha256.Sum256([]byte(string(scope) + "\x00" + owner))
	return domain.Slug(prefix + hex.EncodeToString(digest[:16]))
}

// legacyKnowledgeCorpusSlug preserves lookup of corpora created before
// collision-resistant slugs were introduced. It is never trusted without
// checking the corpus's stored owner and origin.
func legacyKnowledgeCorpusSlug(scope domain.Scope, teamID, memberID string) (string, error) {
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

// bodyOutcome is what reading back a body that did not travel on the bus
// came to.
type bodyOutcome int

const (
	// bodyRead: the body this event describes.
	bodyRead bodyOutcome = iota
	// bodySuperseded: knowledge read the document again since publishing
	// this event and now holds another version. The event describing that
	// version follows this one.
	bodySuperseded
	// bodyGone: knowledge no longer holds the document. Its removal follows.
	bodyGone
	// bodyUnreadable: nothing here can read it — this deployment cannot
	// reach knowledge's internal API, or knowledge answered with no body —
	// and redelivering would not change that.
	bodyUnreadable
)

func (outcome bodyOutcome) String() string {
	switch outcome {
	case bodyRead:
		return "read"
	case bodySuperseded:
		return "superseded"
	case bodyGone:
		return "gone"
	default:
		return "unreadable"
	}
}

// fetchKnowledgeBody reads back a body that was too large to travel on the
// bus, and says what came of it when there is nothing to index.
//
// Only an outage is returned as an error, so the bus redelivers it. A
// deployment that cannot reach knowledge's internal API, or a document it no
// longer holds, is reported once and dropped: redelivering forever would not
// make either true.
func (service *Service) fetchKnowledgeBody(ctx context.Context, event knowledgeeventsv1.DocumentPublished) (string, bodyOutcome, error) {
	if service.documents == nil {
		return "", bodyUnreadable, nil
	}
	document, err := service.documents.Body(ctx, event.OrganizationID, event.DocumentID)
	switch {
	case err == nil:
	case errors.Is(err, knowledgeclient.ErrNotFound):
		service.logger.InfoContext(ctx, "a knowledge document's body could not be read back",
			"document_id", event.DocumentID, "error", err)
		return "", bodyGone, nil
	case errors.Is(err, knowledgeclient.ErrNotConfigured):
		service.logger.InfoContext(ctx, "a knowledge document's body could not be read back",
			"document_id", event.DocumentID, "error", err)
		return "", bodyUnreadable, nil
	default:
		return "", 0, fmt.Errorf("read back a knowledge document's body: %w", err)
	}
	// Knowledge may have read the document again between publishing this
	// event and answering, and what came back would then be a different
	// version than the one this event describes. Indexing it under this
	// event's hash would leave the two disagreeing about what is stored, and
	// the next publish for it would look like no change at all.
	if document.ContentHash != "" && event.ContentHash != "" && document.ContentHash != event.ContentHash {
		service.logger.InfoContext(ctx, "a knowledge document changed while its body was being read back; waiting for the event that describes it",
			"document_id", event.DocumentID)
		return "", bodySuperseded, nil
	}
	if document.Body == "" {
		return "", bodyUnreadable, nil
	}
	return document.Body, bodyRead, nil
}
