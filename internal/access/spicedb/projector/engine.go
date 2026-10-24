package projector

import (
	"context"
	"errors"
	"fmt"
	"strings"

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
	writer  *Writer
	close   func()
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
		close:   func() { _ = client.Close() },
	}, nil
}

// Placed writes a corpus's or file's relationships as it is created.
func (engine *Engine) Placed(ctx context.Context, resourceType, id string, placement Placement) error {
	change, err := Placed(resourceType, id, placement)
	if err != nil {
		return err
	}
	_, err = engine.writer.Apply(ctx, change)
	return err
}

// SourceRegistered writes a knowledge source's relationships as it is added.
func (engine *Engine) SourceRegistered(ctx context.Context, org, id, connection, createdBy string) error {
	change, err := SourceRegistered(org, id, connection, createdBy)
	if err != nil {
		return err
	}
	_, err = engine.writer.Apply(ctx, change)
	return err
}

// Deleted drops a resource's relationships as it is deleted.
func (engine *Engine) Deleted(ctx context.Context, resourceType, organizationID, id string) error {
	change, err := Deleted(resourceType, organizationID, id)
	if err != nil {
		return err
	}
	_, err = engine.writer.Apply(ctx, change)
	return err
}

// Close releases the SpiceDB client; a nil Engine has none.
func (engine *Engine) Close() {
	if engine != nil && engine.close != nil {
		engine.close()
	}
}
