// Package shadow asks SpiceDB every access decision the code makes and logs
// where the two disagree (docs/design/permissions, §6 step 4). The code keeps
// deciding: nothing here changes, delays or fails a request.
package shadow

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
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
	// Settle is how long a disagreement waits to be asked again. A check
	// that follows a change can reach SpiceDB before the projector has
	// written it; only a disagreement that outlasts that lag is recorded.
	Settle = 5 * time.Second
)

// Checker answers whether every check holds, or, with CheckEach, each one.
// An error is no answer.
type Checker interface {
	Check(ctx context.Context, organizationID string, checks []spicedb.Check) (bool, error)
	CheckEach(ctx context.Context, organizationID string, checks []spicedb.Check) ([]bool, error)
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
	settle   time.Duration
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
	// again is a disagreement asked a second time, after settle.
	again bool
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
		checker: checker, recorder: recorder, logger: logger, checks: spicedb.Checks, timeout: Timeout, settle: Settle,
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
		if next.again {
			shadow.count(next.ctx, ask, "settled")
		} else {
			shadow.count(next.ctx, ask, "agreed")
		}
		return
	}
	if !next.again && shadow.settle > 0 {
		next.again = true
		time.AfterFunc(shadow.settle, func() {
			select {
			case <-shadow.done:
			case shadow.jobs <- next:
			default:
				shadow.count(next.ctx, ask, "dropped")
			}
		})
		return
	}
	shadow.disagree(next.ctx, ask, next.allowed, answer, checks)
}

// disagree counts, logs and records a difference between the code and SpiceDB.
func (shadow *Shadow) disagree(ctx context.Context, ask access.Ask, code, answer bool, checks []spicedb.Check) {
	shadow.count(ctx, ask, "disagreed")
	asked := make([]string, len(checks))
	for i, check := range checks {
		asked[i] = fmt.Sprintf("%s:%s#%s@%s:%s", check.Resource.Type, check.Resource.ID, check.Permission, check.Subject.Type, check.Subject.ID)
	}
	shadow.logger.WarnContext(ctx, "spicedb disagrees with the code", "service", ask.Service, "action", ask.Action,
		"resource_kind", ask.ResourceKind, "resource_id", ask.ResourceID, "code", code, "spicedb", answer)
	if shadow.recorder != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		shadow.recorder.Disagree(ctx, access.Disagreement{Ask: ask, Code: code, SpiceDB: answer, Checks: asked})
		cancel()
	}
}

// DecideTimeout bounds a decision a request waits for. Slower is no answer,
// and no answer refuses.
const DecideTimeout = 500 * time.Millisecond

// Decide answers ask from SpiceDB, for a service switched to it (step 5).
// codeAllowed is what the code answered; a difference is recorded, without
// waiting to settle, since SpiceDB's answer is the one the caller gets.
func (shadow *Shadow) Decide(ctx context.Context, ask access.Ask, codeAllowed bool) (bool, error) {
	if shadow == nil {
		return false, access.ErrNoAnswer
	}
	checks, ok := shadow.checks(ask.Actor, ask.UserID, ask.AgentID, ask.Action,
		spicedb.Object{Type: ask.ResourceKind, ID: ask.ResourceID}, ask.Limited)
	if !ok || len(checks) == 0 {
		shadow.count(ctx, ask, "skipped")
		return false, access.ErrNoAnswer
	}
	checkCtx, cancel := context.WithTimeout(ctx, DecideTimeout)
	answer, err := shadow.checker.Check(checkCtx, ask.OrganizationID, checks)
	cancel()
	if err != nil {
		shadow.count(ctx, ask, "unanswered")
		return false, fmt.Errorf("spicedb did not decide: %w", err)
	}
	if answer != codeAllowed {
		shadow.disagree(ctx, ask, codeAllowed, answer, checks)
	} else {
		shadow.count(ctx, ask, "agreed")
	}
	return answer, nil
}

// DecideMany is Decide for a list: every modelled ask's checks go to SpiceDB
// in one CheckEach, and each answer is narrowed by the code's as a caller of
// Decide narrows it (access.Decider).
func (shadow *Shadow) DecideMany(ctx context.Context, asks []access.Ask, codeAllowed []bool) ([]bool, error) {
	if len(asks) != len(codeAllowed) {
		return nil, fmt.Errorf("%d asks, %d code answers", len(asks), len(codeAllowed))
	}
	allowed := slices.Clone(codeAllowed)
	if shadow == nil || len(asks) == 0 {
		return allowed, nil
	}
	// asked is one modelled ask, and where its checks are in all.
	type asked struct{ ask, from, to int }
	var (
		all     []spicedb.Check
		modeled []asked
	)
	for i, ask := range asks {
		if ask.OrganizationID != asks[0].OrganizationID {
			return nil, errors.New("spicedb is asked about one organization at a time")
		}
		checks, ok := shadow.checks(ask.Actor, ask.UserID, ask.AgentID, ask.Action,
			spicedb.Object{Type: ask.ResourceKind, ID: ask.ResourceID}, ask.Limited)
		if !ok || len(checks) == 0 {
			shadow.count(ctx, ask, "skipped")
			continue
		}
		modeled = append(modeled, asked{i, len(all), len(all) + len(checks)})
		all = append(all, checks...)
	}
	if len(modeled) == 0 {
		return allowed, nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, DecideTimeout)
	answers, err := shadow.checker.CheckEach(checkCtx, asks[0].OrganizationID, all)
	cancel()
	if err == nil && len(answers) != len(all) {
		err = fmt.Errorf("spicedb answered %d of %d checks", len(answers), len(all))
	}
	if err != nil {
		for _, m := range modeled {
			shadow.count(ctx, asks[m.ask], "unanswered")
		}
		return nil, fmt.Errorf("spicedb did not decide: %w", err)
	}
	for _, m := range modeled {
		ask, code := asks[m.ask], codeAllowed[m.ask]
		answer := !slices.Contains(answers[m.from:m.to], false)
		if answer != code {
			shadow.disagree(ctx, ask, code, answer, all[m.from:m.to])
		} else {
			shadow.count(ctx, ask, "agreed")
		}
		allowed[m.ask] = code && answer
	}
	return allowed, nil
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
	answers, err := client.CheckEach(ctx, org, checks)
	if err != nil {
		return false, err
	}
	return !slices.Contains(answers, false), nil
}

// bulkLimit bounds one CheckBulkPermissions, so a long list takes a few calls
// rather than one SpiceDB might refuse as too large.
const bulkLimit = 1000

// CheckEach answers each check, in CheckBulkPermissions calls of bulkLimit.
func (client Client) CheckEach(ctx context.Context, org string, checks []spicedb.Check) ([]bool, error) {
	consistency := client.consistency(ctx, org)
	answers := make([]bool, 0, len(checks))
	for chunk := range slices.Chunk(checks, bulkLimit) {
		chunkAnswers, err := client.checkBulk(ctx, consistency, chunk)
		if err != nil {
			return nil, err
		}
		answers = append(answers, chunkAnswers...)
	}
	return answers, nil
}

func (client Client) checkBulk(ctx context.Context, consistency *v1.Consistency, checks []spicedb.Check) ([]bool, error) {
	items := make([]*v1.CheckBulkPermissionsRequestItem, len(checks))
	for i, check := range checks {
		items[i] = &v1.CheckBulkPermissionsRequestItem{
			Resource:   &v1.ObjectReference{ObjectType: check.Resource.Type, ObjectId: check.Resource.ID},
			Permission: check.Permission,
			Subject:    &v1.SubjectReference{Object: &v1.ObjectReference{ObjectType: check.Subject.Type, ObjectId: check.Subject.ID}},
		}
	}
	response, err := client.SpiceDB.CheckBulkPermissions(ctx, &v1.CheckBulkPermissionsRequest{
		Consistency: consistency,
		Items:       items,
	})
	if err != nil {
		return nil, err
	}
	if len(response.Pairs) != len(items) {
		return nil, fmt.Errorf("spicedb answered %d of %d checks", len(response.Pairs), len(items))
	}
	answers := make([]bool, len(items))
	for i, pair := range response.Pairs {
		if failed := pair.GetError(); failed != nil {
			return nil, errors.New(failed.GetMessage())
		}
		switch pair.GetItem().GetPermissionship() {
		case v1.CheckPermissionResponse_PERMISSIONSHIP_HAS_PERMISSION:
			answers[i] = true
		case v1.CheckPermissionResponse_PERMISSIONSHIP_NO_PERMISSION:
		default:
			// Conditional on a caveat's context, which the shadow doesn't pass.
			return nil, errors.New("spicedb answered conditionally")
		}
	}
	return answers, nil
}
