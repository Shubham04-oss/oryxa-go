package projector

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb/projector/tokens"
)

// Reconciler compares SpiceDB with the sources of truth and repairs it.
type Reconciler struct {
	writer *Writer
	pool   *pgxpool.Pool
	logger *slog.Logger
	now    func() time.Time
}

func NewReconciler(writer *Writer, pool *pgxpool.Pool, logger *slog.Logger) *Reconciler {
	return &Reconciler{writer: writer, pool: pool, logger: logger, now: time.Now}
}

// Result counts what one run fixed. Skipped names the slices whose sources
// could not be read.
type Result struct {
	Deleted int      `json:"deleted"`
	Written int      `json:"written"`
	Skipped []string `json:"skipped,omitempty"`
}

// Run reconciles everything the projector owns. Sources are read before
// SpiceDB, so a relationship that arrives in between is deleted and comes
// back tomorrow, rather than one removed in between being written back:
// access missing, never left over.
//
// ponytail: it holds every owned relationship in memory at once, which is
// fine for thousands of organizations' worth; past that, page by resource.
func (reconciler *Reconciler) Run(ctx context.Context) (Result, error) {
	started := reconciler.now()
	world := ReadWorld(ctx, reconciler.pool, "", reconciler.logger)
	result, token, err := reconciler.fix(ctx, world, Filter{}, nil)
	if err != nil {
		return result, err
	}
	if token != nil {
		var orgs []string
		for _, membership := range world.Memberships {
			if !slices.Contains(orgs, membership.Org) {
				orgs = append(orgs, membership.Org)
			}
		}
		if err := reconciler.writer.tokens.SaveAll(ctx, orgs, token, started); err != nil {
			return result, err
		}
	}
	// SQS keeps a message at most 14 days, so no event older than that can
	// still arrive to be ordered against what is forgotten here.
	if _, err := tokens.NewSeen(reconciler.pool).Purge(ctx, started.Add(-seenRetention)); err != nil {
		reconciler.logger.WarnContext(ctx, "spicedb reconcile: purge seen events", "error", err)
	}
	reconciler.logger.InfoContext(ctx, "spicedb reconcile finished", "deleted", result.Deleted, "written", result.Written, "skipped", result.Skipped)
	return result, nil
}

const seenRetention = 15 * 24 * time.Hour

// SyncGrants brings one agent's grants in line with agents, so a grant
// change shows in SpiceDB without waiting for the night.
func (reconciler *Reconciler) SyncGrants(ctx context.Context, org, agent string) error {
	world := ReadWorld(ctx, reconciler.pool, org, reconciler.logger, "agents", "apps", "files")
	// A source that did not answer fails the event, so it comes back rather
	// than leaving a grant that changed unrepaired until the night.
	for _, source := range []string{"agents", "apps", "files"} {
		if !world.Read[source] {
			return fmt.Errorf("sync agent %s grants: %s is unreadable", agent, source)
		}
	}
	subject := Filter{SubjectType: "agent", SubjectID: agent}
	_, token, err := reconciler.fix(ctx, world, subject, func(s slice) bool {
		return slices.Contains(s.relations, "granted")
	})
	if err != nil || token == nil {
		return err
	}
	return reconciler.writer.tokens.Save(ctx, org, token)
}

// fix diffs each owned slice that is ready, within subject when it names
// one, deleting first and writing after.
func (reconciler *Reconciler) fix(ctx context.Context, world World, subject Filter, only func(slice) bool) (Result, *v1.ZedToken, error) {
	expected := map[[2]string][]Rel{}
	for _, rel := range Expected(world) {
		if subject.SubjectID == "" || (rel.SubjectType == subject.SubjectType && rel.SubjectID == subject.SubjectID) {
			key := [2]string{rel.ResourceType, rel.Relation}
			expected[key] = append(expected[key], rel)
		}
	}
	var result Result
	var remove, add []Rel
	for _, owned := range ownedSlices {
		if only != nil && !only(owned) {
			continue
		}
		if !owned.ready(world) {
			result.Skipped = append(result.Skipped, owned.resourceType+"#"+fmt.Sprint(owned.relations))
			continue
		}
		for _, relation := range owned.relations {
			actual, err := reconciler.writer.Read(ctx, Filter{ResourceType: owned.resourceType, Relation: relation, SubjectType: subject.SubjectType, SubjectID: subject.SubjectID})
			if err != nil {
				return result, nil, err
			}
			gone, missing := Diff(expected[[2]string{owned.resourceType, relation}], actual)
			remove, add = append(remove, gone...), append(add, missing...)
		}
	}
	for _, rel := range remove {
		reconciler.logger.WarnContext(ctx, "spicedb reconcile: deleting a relationship no source holds", "relationship", rel.String())
	}
	token, err := reconciler.writer.write(ctx, v1.RelationshipUpdate_OPERATION_DELETE, remove)
	if err != nil {
		return result, token, fmt.Errorf("reconcile deletes: %w", err)
	}
	result.Deleted = len(remove)
	for _, rel := range add {
		reconciler.logger.WarnContext(ctx, "spicedb reconcile: writing a missing relationship", "relationship", rel.String())
	}
	written, err := reconciler.writer.write(ctx, v1.RelationshipUpdate_OPERATION_TOUCH, add)
	if written != nil {
		token = written
	}
	if err != nil {
		return result, token, fmt.Errorf("reconcile writes: %w", err)
	}
	result.Written = len(add)
	return result, token, nil
}

// Nightly runs the reconcile at hour (UTC) every day until ctx ends. A wall
// clock rather than a 24h tick from start, because every release restarts
// the process.
func (reconciler *Reconciler) Nightly(ctx context.Context, hour int) {
	for {
		now := reconciler.now().UTC()
		next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
		if !next.After(now) {
			next = next.Add(24 * time.Hour)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(next.Sub(now)):
		}
		if _, err := reconciler.Run(ctx); err != nil && ctx.Err() == nil {
			reconciler.logger.ErrorContext(ctx, "spicedb reconcile failed", "error", err)
		}
	}
}
