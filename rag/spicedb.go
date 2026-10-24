package rag

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/application"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// withEngine switches rag to decide corpus access from SpiceDB when
// ACCESS_ENGINE_RAG says so (docs/design/permissions, step 5). It answers the
// engine's close, or nil when rag stays on the code.
func withEngine(platform host.Platform, service *application.Service) (func(), error) {
	engine, err := projector.OpenEngine(platform, Name)
	if err != nil || engine == nil {
		return nil, err
	}
	service.WithEngine(engine.Decider, relationships{engine})
	return engine.Close, nil
}

// relationships writes a corpus's place in SpiceDB the way the projector does
// from its events.
type relationships struct{ engine *projector.Engine }

func (r relationships) CorpusCreated(ctx context.Context, corpus domain.Corpus) error {
	return r.engine.Placed(ctx, "corpus", corpus.ID, projector.Placement{Org: corpus.OrganizationID, Scope: string(corpus.Scope),
		Team: corpus.TeamID, Member: corpus.MemberID, CreatedBy: corpus.CreatedBy}, time.Now())
}

func (r relationships) CorpusDeleted(ctx context.Context, organizationID, corpusID string) error {
	return r.engine.Deleted(ctx, "corpus", organizationID, corpusID, time.Now())
}
