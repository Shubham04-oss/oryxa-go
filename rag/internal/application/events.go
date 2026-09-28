package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	autheventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/auth/v1"
	fileseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/files/v1"
	rageventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"
	teamseventsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/teams/v1"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// What rag does when something it depends on goes away.
//
// An index outliving what it was built from is the failure mode that matters
// here. A deleted organization, a member who left, a deleted team, a deleted
// file: in each case a corpus or a document would otherwise keep answering
// questions from text nobody is entitled to see any more. These handlers are
// how that stops happening without anyone having to remember.

func (service *Service) OrganizationCreated(ctx context.Context, event autheventsv1.OrganizationCreated) error {
	return service.store.UpsertOrganization(ctx, event.OrganizationID, event.Name, event.CreatedAt)
}

func (service *Service) OrganizationUpdated(ctx context.Context, event autheventsv1.OrganizationUpdated) error {
	return service.store.UpsertOrganization(ctx, event.OrganizationID, event.Name, event.UpdatedAt)
}

// OrganizationDeleted removes every corpus the organization had, the vectors
// behind them, and whatever Cedar policies it had stored on any of its
// knowledge bases or on the organization itself.
func (service *Service) OrganizationDeleted(ctx context.Context, event autheventsv1.OrganizationDeleted) error {
	if err := service.wipe(ctx, event.OrganizationID, "", "",
		rageventsv1.ReasonOrganizationDeleted, event.DeletedAt); err != nil {
		return err
	}
	return service.authzStore.DeleteOrganization(ctx, event.OrganizationID)
}

// MemberRemoved removes the corpora that hung under that member.
//
// Team and organization corpora they created stay, exactly as their files do:
// those belong to the team or the organization, not to the person who happened
// to make them.
func (service *Service) MemberRemoved(ctx context.Context, event autheventsv1.MemberRemoved) error {
	// The organization going away already wipes everything under it; acting
	// on this as well would be doing the same work twice.
	if event.Reason == "organization_deleted" {
		return nil
	}
	return service.wipe(ctx, event.OrganizationID, domain.ScopeMember, event.UserID,
		rageventsv1.ReasonOrganizationMembershipEnded, event.RemovedAt)
}

// TeamDeleted removes the corpora that hung on that team.
func (service *Service) TeamDeleted(ctx context.Context, event teamseventsv1.TeamDeleted) error {
	return service.wipe(ctx, event.OrganizationID, domain.ScopeTeam, event.TeamID,
		rageventsv1.ReasonTeamDeleted, event.DeletedAt)
}

// ObjectDeleted removes the documents that were indexed from a file that has
// since been deleted.
//
// Without this, a file a member deleted would go on answering questions out of
// the index, quoting text that no longer exists anywhere they can check.
func (service *Service) ObjectDeleted(ctx context.Context, event fileseventsv1.ObjectDeleted) error {
	// Found by source rather than by reading each corpus's newest documents:
	// that would miss the ones in a corpus holding more than a page of them,
	// and the deleted file would go on answering questions out of the index.
	documents, err := service.store.DocumentsBySource(ctx, event.OrganizationID, domain.SourceFile, event.FileID)
	if err != nil {
		return err
	}
	for _, document := range documents {
		corpus, err := service.store.Corpus(ctx, event.OrganizationID, document.CorpusID)
		if err != nil {
			if isMissing(err) {
				// The corpus went first and took the document with it.
				continue
			}
			return err
		}
		removal, err := service.event(rageventsv1.DocumentRemovedDetailType, rageventsv1.DocumentRemoved{
			DocumentID: document.ID, CorpusID: corpus.ID, OrganizationID: event.OrganizationID,
			SourceKind: string(document.Source.Kind), SourceRef: document.Source.Ref,
			Reason: rageventsv1.ReasonSourceDeleted, RemovedAt: event.DeletedAt,
		})
		if err != nil {
			return err
		}
		if err := service.removeDocumentVectors(ctx, corpus, document); err != nil {
			return err
		}
		if _, err := service.store.DeleteDocument(ctx, event.OrganizationID, document.ID, removal); err != nil {
			if isMissing(err) {
				continue
			}
			return err
		}
		service.logger.InfoContext(ctx, "removed a document whose file was deleted",
			"document_id", document.ID, "corpus_id", corpus.ID, "file_id", event.FileID)
	}
	return nil
}

// wipe removes a set of corpora and the vectors behind them.
//
// The vectors go before the rows. If the rows went first and dropping the
// index failed, the vectors would be left with nothing pointing at them and
// nothing to find them by — an orphan that still costs money to store.
func (service *Service) wipe(ctx context.Context, organizationID string, scope domain.Scope, ownerID, reason string, at time.Time) error {
	if at.IsZero() {
		at = service.now().UTC()
	}
	corpora, err := service.store.CorporaForWipe(ctx, organizationID, scope, ownerID)
	if err != nil {
		return err
	}
	for _, corpus := range corpora {
		if err := service.dropVectors(ctx, corpus); err != nil {
			return fmt.Errorf("drop the vectors of corpus %s: %w", corpus.ID, err)
		}
		event, err := service.event(rageventsv1.CorpusDeletedDetailType, rageventsv1.CorpusDeleted{
			CorpusID: corpus.ID, OrganizationID: organizationID, Scope: string(corpus.Scope),
			TeamID: corpus.TeamID, MemberID: corpus.MemberID, Reason: reason, DeletedAt: at.UTC(),
		})
		if err != nil {
			return err
		}
		if err := service.store.DeleteCorpus(ctx, organizationID, corpus.ID, event); err != nil {
			// Another delivery of the same event may have removed it already,
			// which is the outcome this wanted anyway.
			if isMissing(err) {
				continue
			}
			return err
		}
		service.logger.InfoContext(ctx, "removed a corpus", "corpus_id", corpus.ID, "reason", reason)
	}
	return nil
}

// isMissing reports whether something was already gone. Another delivery of
// the same event may have removed it, which is the outcome this wanted anyway.
func isMissing(err error) bool {
	return errors.Is(err, domain.ErrNotFound)
}
