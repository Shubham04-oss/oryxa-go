package agentworkflows

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
)

// LocalRuntime is an explicit development adapter for exercising the real
// workflow HTTP API and model runtime without AWS infrastructure. Production
// never selects it unless AGENTS_WORKFLOW_LOCAL is deliberately enabled.
type LocalRuntime struct {
	mu       sync.Mutex
	executor workflowExecutor
	timers   map[string]context.CancelFunc
	timeout  time.Duration
	logger   *slog.Logger
	closed   bool
}

func NewLocalRuntime(timeout time.Duration, logger *slog.Logger) *LocalRuntime {
	if timeout <= 0 {
		timeout = 15 * time.Minute
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &LocalRuntime{timers: map[string]context.CancelFunc{}, timeout: timeout, logger: logger}
}

func (runtime *LocalRuntime) Bind(executor workflowExecutor) error {
	if executor == nil {
		return errors.New("local workflow runtime: executor is required")
	}
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if runtime.executor != nil {
		return errors.New("local workflow runtime is already bound")
	}
	runtime.executor = executor
	return nil
}

func (runtime *LocalRuntime) Start(_ context.Context, request StartRequest) (string, error) {
	externalID := "local:execution:" + request.ExecutionID
	dispatch := Dispatch{
		ExecutionID: request.ExecutionID, WorkflowID: request.WorkflowID,
		OrganizationID: request.OrganizationID, OwnerUserID: request.OwnerUserID,
		StartedBy: request.StartedBy, ExternalID: externalID,
	}
	if err := runtime.launch(dispatch); err != nil {
		return "", err
	}
	return externalID, nil
}

func (runtime *LocalRuntime) Upsert(_ context.Context, workflow Workflow) error {
	if workflow.Schedule == nil {
		return errors.New("workflow has no schedule")
	}
	runtime.stop(workflow.ID)
	if workflow.Status != StatusActive || workflow.Schedule.State != ScheduleEnabled {
		return nil
	}
	delay, repeat, err := localSchedule(*workflow.Schedule, time.Now())
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	runtime.mu.Lock()
	if runtime.closed {
		runtime.mu.Unlock()
		cancel()
		return errors.New("local workflow runtime is closed")
	}
	runtime.timers[workflow.ID] = cancel
	runtime.mu.Unlock()
	go runtime.runSchedule(ctx, workflow, delay, repeat)
	return nil
}

func (runtime *LocalRuntime) Delete(_ context.Context, workflow Workflow) error {
	runtime.stop(workflow.ID)
	return nil
}

func (runtime *LocalRuntime) Close() {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	runtime.closed = true
	for id, cancel := range runtime.timers {
		cancel()
		delete(runtime.timers, id)
	}
}

func (runtime *LocalRuntime) launch(dispatch Dispatch) error {
	runtime.mu.Lock()
	executor, closed := runtime.executor, runtime.closed
	runtime.mu.Unlock()
	if closed || executor == nil {
		return ErrUnavailable
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), runtime.timeout)
		defer cancel()
		if _, err := executor.Execute(ctx, dispatch); err != nil && !errors.Is(err, ErrInProgress) {
			runtime.logger.ErrorContext(ctx, "local agent workflow failed", "workflow_id", dispatch.WorkflowID, "error", err)
		}
	}()
	return nil
}

func (runtime *LocalRuntime) runSchedule(ctx context.Context, workflow Workflow, delay, repeat time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case firedAt := <-timer.C:
			externalID := fmt.Sprintf("local:schedule:%s:%d", workflow.ID, firedAt.UnixNano())
			_ = runtime.launch(Dispatch{
				WorkflowID: workflow.ID, OrganizationID: workflow.OrganizationID,
				OwnerUserID: workflow.OwnerUserID, StartedBy: StartedBySchedule, ExternalID: externalID,
			})
			if repeat <= 0 {
				runtime.stop(workflow.ID)
				return
			}
			timer.Reset(repeat)
		}
	}
}

func (runtime *LocalRuntime) stop(id string) {
	runtime.mu.Lock()
	defer runtime.mu.Unlock()
	if cancel := runtime.timers[id]; cancel != nil {
		cancel()
		delete(runtime.timers, id)
	}
}

func localSchedule(schedule Schedule, now time.Time) (time.Duration, time.Duration, error) {
	location, err := time.LoadLocation(schedule.Timezone)
	if err != nil {
		return 0, 0, err
	}
	expression := strings.TrimSpace(schedule.Expression)
	switch {
	case strings.HasPrefix(expression, "at(") && strings.HasSuffix(expression, ")"):
		value := strings.TrimSuffix(strings.TrimPrefix(expression, "at("), ")")
		at, err := time.ParseInLocation("2006-01-02T15:04:05", value, location)
		if err != nil || !at.After(now) {
			return 0, 0, fmt.Errorf("%w: local at(...) schedule must name a future second", ErrInvalid)
		}
		return at.Sub(now), 0, nil
	case strings.HasPrefix(expression, "rate(") && strings.HasSuffix(expression, ")"):
		fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(expression, "rate("), ")"))
		if len(fields) != 2 {
			return 0, 0, fmt.Errorf("%w: invalid rate expression", ErrInvalid)
		}
		value, err := strconv.Atoi(fields[0])
		if err != nil || value < 1 {
			return 0, 0, fmt.Errorf("%w: invalid rate expression", ErrInvalid)
		}
		var unit time.Duration
		switch strings.TrimSuffix(fields[1], "s") {
		case "minute":
			unit = time.Minute
		case "hour":
			unit = time.Hour
		case "day":
			unit = 24 * time.Hour
		default:
			return 0, 0, fmt.Errorf("%w: invalid rate unit", ErrInvalid)
		}
		repeat := time.Duration(value) * unit
		return repeat, repeat, nil
	default:
		return 0, 0, fmt.Errorf("%w: local workflow mode supports at(...) and rate(...); AWS mode also supports cron(...) safely", ErrInvalid)
	}
}
