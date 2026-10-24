package projector

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
	"github.com/Shubham04-oss/oryxa-go/internal/host"
)

// Engine is what a service switched to SpiceDB needs (docs/design/permissions,
// step 5): the decider, and a writer for the relationships of the resources it
// creates and deletes itself, so they are there before the projector would
// write them from the events.
type Engine struct {
	Decider access.Decider
	writer  applier
	// seen is the projector's: a write here records its keys, so an older
	// event delivered late can't undo it (#457).
	seen orderer
	// grants re-reads an agent's grants; only OpenWriter's engine has it.
	grants *Reconciler
	close  func()
}

// OpenEngine reads ACCESS_ENGINE_<SERVICE>. It answers nil when the service
// stays on its code, and refuses an unknown engine or spicedb without SpiceDB.
func OpenEngine(platform host.Platform, service string) (*Engine, error) {
	engine, err := access.Engine(platform.Getenv, service)
	if err != nil || engine != access.EngineSpiceDB {
		return nil, err
	}
	decider, ok := platform.Shadow.(access.Decider)
	if !ok || decider == nil {
		return nil, fmt.Errorf("ACCESS_ENGINE_%s=spicedb needs SpiceDB: set SPICEDB_ENDPOINT", strings.ToUpper(service))
	}
	if platform.Database == nil {
		return nil, errors.New("the spicedb engine needs the database for its tokens")
	}
	client, err := spicedb.Dial(spicedb.FromEnv())
	if err != nil {
		return nil, err
	}
	platform.Logger.Info("access decided from spicedb", "service", service)
	return &Engine{
		Decider: decider,
		writer:  NewWriter(client, tokens.New(platform.Database.Pool)),
		seen:    tokens.NewSeen(platform.Database.Pool),
		close:   func() { _ = client.Close() },
	}, nil
}

// Apply writes change, as a mapping in this package made it, at the time
// the source committed it: the event's OccurredAt when the caller has it,
// otherwise time.Now() just after the commit. Like an event, it is skipped
// when a newer change already touched the same relationships.
func (engine *Engine) Apply(ctx context.Context, change Change, at time.Time) error {
	check, record := orderingKeys(change)
	_, err := engine.seen.Guard(ctx, check, record, at, func(ctx context.Context) error {
		_, err := engine.writer.Apply(ctx, change)
		return err
	})
	return err
}

// OpenWriter is for the sources every service's decisions hang on, org roles
// (auth) and agents and their grants (agents), so it is on whenever SpiceDB
// is, whatever ACCESS_ENGINE_* says: they write a change as it commits rather
// than when the projector reaches its event. It has no Decider, and is nil
// while SPICEDB_ENDPOINT is unset.
func OpenWriter(platform host.Platform) (*Engine, error) {
	if !enabled(platform.Getenv) {
		return nil, nil
	}
	if platform.Database == nil {
		return nil, errors.New("the spicedb writer needs the database for its tokens")
	}
	reconciler, closeClient, err := Open(platform)
	if err != nil {
		return nil, err
	}
	return &Engine{writer: reconciler.writer, seen: tokens.NewSeen(platform.Database.Pool), grants: reconciler, close: closeClient}, nil
}

// SyncGrants brings one agent's grants in line with agents, reading every
// source the grant names, as the projector does after an agent's event.
func (engine *Engine) SyncGrants(ctx context.Context, org, agent string) error {
	if engine.grants == nil {
		return errors.New("sync grants: this engine was not opened by OpenWriter")
	}
	return engine.grants.SyncGrants(ctx, org, agent)
}

// Placed writes a corpus's or file's relationships as it is created.
func (engine *Engine) Placed(ctx context.Context, resourceType, id string, placement Placement, at time.Time) error {
	change, err := Placed(resourceType, id, placement)
	if err != nil {
		return err
	}
	return engine.Apply(ctx, change, at)
}

// SourceRegistered writes a knowledge source's relationships as it is added.
func (engine *Engine) SourceRegistered(ctx context.Context, org, id, connection, createdBy string, at time.Time) error {
	change, err := SourceRegistered(org, id, connection, createdBy)
	if err != nil {
		return err
	}
	return engine.Apply(ctx, change, at)
}

// ConnectionOwned writes an apps connection's relationships as it starts.
func (engine *Engine) ConnectionOwned(ctx context.Context, org, id, member string, at time.Time) error {
	change, err := ConnectionOwned(org, id, member)
	if err != nil {
		return err
	}
	return engine.Apply(ctx, change, at)
}

// Deleted drops a resource's relationships as it is deleted.
func (engine *Engine) Deleted(ctx context.Context, resourceType, organizationID, id string, at time.Time) error {
	change, err := Deleted(resourceType, organizationID, id)
	if err != nil {
		return err
	}
	return engine.Apply(ctx, change, at)
}

// Close releases the SpiceDB client; a nil Engine has none.
func (engine *Engine) Close() {
	if engine != nil && engine.close != nil {
		engine.close()
	}
}
