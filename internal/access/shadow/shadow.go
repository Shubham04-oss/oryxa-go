// Package shadow asks SpiceDB every access decision the code makes and logs
// where the two disagree (docs/design/permissions, §6 step 4). The code keeps
// deciding: nothing here changes, delays or fails a request.
package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/authzed/authzed-go/v1"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
)

// The latency budget. A request pays one non-blocking channel send; SpiceDB
// is asked off the request path, and an answer slower than Timeout is no
// answer. A full queue drops the comparison rather than wait.
const (
	Timeout   = 100 * time.Millisecond
	queueSize = 1024
	workers   = 8
)

// Checker answers whether every check holds. An error is no answer.
type Checker interface {
	Check(ctx context.Context, organizationID string, checks []spicedb.Check) (bool, error)
}

// Recorder keeps disagreements; *access.Log is one.
type Recorder interface {
	Disagree(context.Context, access.Disagreement)
}

// Shadow compares. A nil *Shadow is off and costs a nil check.
type Shadow struct {
	checker  Checker
	recorder Recorder
	logger   *slog.Logger
	checks   func(access.Actor, string, string, string, spicedb.Object, bool) ([]spicedb.Check, bool)
	timeout  time.Duration
	jobs     chan job
	done     chan struct{}
	stop     sync.Once
	workers  sync.WaitGroup
	outcomes metric.Int64Counter
	// lastWarned is when an unanswered check was last logged, in Unix
	// nanoseconds, so SpiceDB being down logs once a minute, not per request.
	lastWarned atomic.Int64
}

type job struct {
	ctx     context.Context
	ask     access.Ask
	allowed bool
}

// New starts a shadow that asks checker and records into recorder. Close
// stops it.
func New(checker Checker, recorder Recorder, logger *slog.Logger) *Shadow {
	if logger == nil {
		logger = slog.Default()
	}
	outcomes, _ := otel.Meter("github.com/Shubham04-oss/oryxa-go/internal/access/shadow").Int64Counter(
		"access.shadow.checks", metric.WithDescription("Access decisions compared with SpiceDB, by outcome"))
	shadow := &Shadow{
		checker: checker, recorder: recorder, logger: logger, checks: spicedb.Checks, timeout: Timeout,
		jobs: make(chan job, queueSize), done: make(chan struct{}), outcomes: outcomes,
	}
	for range workers {
		shadow.workers.Add(1)
		go shadow.work()
	}
	return shadow
}

// Open starts a shadow on the SpiceDB config names, or answers nil (off) when
// it names none.
func Open(config spicedb.Config, tokens Tokens, recorder Recorder, logger *slog.Logger) (*Shadow, error) {
	if !config.Enabled() {
		return nil, nil
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		return nil, err
	}
	return New(Client{SpiceDB: client, Tokens: tokens}, recorder, logger), nil
}

// Close stops the workers; comparisons still queued are dropped.
func (shadow *Shadow) Close() {
	if shadow == nil {
		return
	}
	shadow.stop.Do(func() { close(shadow.done) })
	shadow.workers.Wait()
}

// Compare queues the code's answer for comparison and returns at once.
func (shadow *Shadow) Compare(ctx context.Context, ask access.Ask, allowed bool) {
	if shadow == nil {
		return
	}
	select {
	case shadow.jobs <- job{ctx: context.WithoutCancel(ctx), ask: ask, allowed: allowed}:
	default:
		shadow.count(ctx, ask, "dropped")
	}
}

func (shadow *Shadow) work() {
	defer shadow.workers.Done()
	for {
		select {
		case <-shadow.done:
			return
		case next := <-shadow.jobs:
			shadow.compare(next)
		}
	}
}

func (shadow *Shadow) compare(next job) {
	ask := next.ask
	checks, ok := shadow.checks(ask.Actor, ask.UserID, ask.AgentID, ask.Action,
		spicedb.Object{Type: ask.ResourceKind, ID: ask.ResourceID}, ask.Limited)
	if !ok || len(checks) == 0 {
		shadow.count(next.ctx, ask, "skipped")
		return
	}
	ctx, cancel := context.WithTimeout(next.ctx, shadow.timeout)
	answer, err := shadow.checker.Check(ctx, ask.OrganizationID, checks)
	cancel()
	if err != nil {
		shadow.count(next.ctx, ask, "unanswered")
		if now := time.Now().UnixNano(); now-shadow.lastWarned.Load() > int64(time.Minute) {
			shadow.lastWarned.Store(now)
			shadow.logger.WarnContext(next.ctx, "spicedb did not answer a shadow check", "service", ask.Service, "error", err)
		}
		return
	}
	if answer == next.allowed {
		shadow.count(next.ctx, ask, "agreed")
		return
	}
	shadow.count(next.ctx, ask, "disagreed")
	asked := make([]string, len(checks))
	for i, check := range checks {
		asked[i] = fmt.Sprintf("%s:%s#%s@%s:%s", check.Resource.Type, check.Resource.ID, check.Permission, check.Subject.Type, check.Subject.ID)
	}
	shadow.logger.WarnContext(next.ctx, "spicedb disagrees with the code", "service", ask.Service, "action", ask.Action,
		"resource_kind", ask.ResourceKind, "resource_id", ask.ResourceID, "code", next.allowed, "spicedb", answer)
	if shadow.recorder != nil {
		ctx, cancel := context.WithTimeout(next.ctx, 5*time.Second)
		shadow.recorder.Disagree(ctx, access.Disagreement{Ask: ask, Code: next.allowed, SpiceDB: answer, Checks: asked})
		cancel()
	}
}

func (shadow *Shadow) count(ctx context.Context, ask access.Ask, outcome string) {
	if shadow.outcomes != nil {
		shadow.outcomes.Add(ctx, 1, metric.WithAttributes(attribute.String("service", ask.Service), attribute.String("outcome", outcome)))
	}
}

// Client asks a real SpiceDB.
type Client struct {
	SpiceDB *authzed.Client
	// Tokens is where the projector keeps each org's latest ZedToken; nil, or
	// an org with none, asks at minimize_latency.
	Tokens Tokens
}

// Tokens reads an org's latest ZedToken (projector/tokens.Postgres).
type Tokens interface {
	Latest(ctx context.Context, org string) (*v1.ZedToken, error)
}

// consistency asks for an answer at least as fresh as the org's last
// projected change, so a check that follows a change isn't read from an
// older snapshot and logged as a disagreement (design, Risks: new-enemy).
func (client Client) consistency(ctx context.Context, org string) *v1.Consistency {
	if client.Tokens != nil && org != "" {
		if token, err := client.Tokens.Latest(ctx, org); err == nil && token.GetToken() != "" {
			return &v1.Consistency{Requirement: &v1.Consistency_AtLeastAsFresh{AtLeastAsFresh: token}}
		}
	}
	return &v1.Consistency{Requirement: &v1.Consistency_MinimizeLatency{MinimizeLatency: true}}
}

// Check asks every check in one CheckBulkPermissions call.
func (client Client) Check(ctx context.Context, org string, checks []spicedb.Check) (bool, error) {
	items := make([]*v1.CheckBulkPermissionsRequestItem, len(checks))
	for i, check := range checks {
		items[i] = &v1.CheckBulkPermissionsRequestItem{
			Resource:   &v1.ObjectReference{ObjectType: check.Resource.Type, ObjectId: check.Resource.ID},
			Permission: check.Permission,
			Subject:    &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: check.Subject.Type, ObjectId: check.Subject.ID}},
		}
	}
	response, err := client.SpiceDB.CheckBulkPermissions(ctx, &v1.CheckBulkPermissionsRequest{
		Consistency: client.consistency(ctx, org),
		Items:       items,
	})
	if err != nil {
		return false, err
	}
	if len(response.Pairs) != len(items) {
		return false, fmt.Errorf("spicedb answered %d of %d checks", len(response.Pairs), len(items))
	}
	allowed := true
	for _, pair := range response.Pairs {
		if failed := pair.GetError(); failed != nil {
			return false, errors.New(failed.GetMessage())
		}
		switch pair.GetItem().GetPermissionship() {
		case v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION:
		case v1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION:
			allowed = false
		default:
			// Conditional on a caveat's context, which the shadow doesn't pass.
			return false, errors.New("spicedb answered conditionally")
		}
	}
	return allowed, nil
}
