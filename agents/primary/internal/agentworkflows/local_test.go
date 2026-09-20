package agentworkflows

import (
	"context"
	"testing"
	"time"
)

type recordingWorkflowExecutor struct{ calls chan Dispatch }

func (executor recordingWorkflowExecutor) Execute(_ context.Context, dispatch Dispatch) (Execution, error) {
	executor.calls <- dispatch
	return Execution{ID: dispatch.ExecutionID, WorkflowID: dispatch.WorkflowID, Status: ExecutionSucceeded}, nil
}

// blockingWorkflowExecutor never returns on its own; it only reports whether
// the context it was launched with was cancelled.
type blockingWorkflowExecutor struct{ cancelled chan struct{} }

func (executor blockingWorkflowExecutor) Execute(ctx context.Context, _ Dispatch) (Execution, error) {
	<-ctx.Done()
	close(executor.cancelled)
	return Execution{}, ctx.Err()
}

func TestLocalRuntimeStartsTheSameIdentifierOnlyDispatch(t *testing.T) {
	executor := recordingWorkflowExecutor{calls: make(chan Dispatch, 1)}
	runtime := NewLocalRuntime(time.Second, nil)
	t.Cleanup(runtime.Close)
	if err := runtime.Bind(executor); err != nil {
		t.Fatal(err)
	}
	externalID, err := runtime.Start(context.Background(), StartRequest{
		ExecutionID: "awx_1", WorkflowID: "awf_1", OrganizationID: "org_1",
		OwnerUserID: "user_1", StartedBy: StartedByAPI,
	})
	if err != nil {
		t.Fatal(err)
	}
	if externalID != "local:execution:awx_1" {
		t.Fatalf("external id = %q", externalID)
	}
	select {
	case dispatch := <-executor.calls:
		if dispatch.ExecutionID != "awx_1" || dispatch.WorkflowID != "awf_1" || dispatch.StartedBy != StartedByAPI {
			t.Fatalf("dispatch = %+v", dispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("local execution was not dispatched")
	}
}

// Close used to cancel only pending schedule timers; a launched execution's
// goroutine built its context from context.Background(), so it kept running
// for up to the runtime timeout after shutdown. Close must now stop it too.
func TestLocalRuntimeCloseCancelsInFlightRun(t *testing.T) {
	executor := blockingWorkflowExecutor{cancelled: make(chan struct{})}
	runtime := NewLocalRuntime(time.Minute, nil) // long enough that only Close should end the run
	if err := runtime.Bind(executor); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Start(context.Background(), StartRequest{
		ExecutionID: "awx_1", WorkflowID: "awf_1", OrganizationID: "org_1",
		OwnerUserID: "user_1", StartedBy: StartedByAPI,
	}); err != nil {
		t.Fatal(err)
	}
	runtime.Close()
	select {
	case <-executor.cancelled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel the in-flight run's context")
	}
}

func TestLocalRuntimeFiresAtSchedule(t *testing.T) {
	executor := recordingWorkflowExecutor{calls: make(chan Dispatch, 1)}
	runtime := NewLocalRuntime(time.Second, nil)
	t.Cleanup(runtime.Close)
	if err := runtime.Bind(executor); err != nil {
		t.Fatal(err)
	}
	runtime.runSchedule(context.Background(), Workflow{
		ID: "awf_1", OrganizationID: "org_1", OwnerUserID: "user_1",
	}, 5*time.Millisecond, 0)
	select {
	case dispatch := <-executor.calls:
		if dispatch.ExecutionID != "" || dispatch.WorkflowID != "awf_1" || dispatch.StartedBy != StartedBySchedule || dispatch.ExternalID == "" {
			t.Fatalf("dispatch = %+v", dispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduled local execution was not dispatched")
	}
}

func TestLocalScheduleSupportsAWSRateAndAtExpressions(t *testing.T) {
	now := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	delay, repeat, err := localSchedule(Schedule{Expression: "rate(2 hours)", Timezone: "UTC"}, now)
	if err != nil || delay != 2*time.Hour || repeat != 2*time.Hour {
		t.Fatalf("rate delay=%s repeat=%s err=%v", delay, repeat, err)
	}
	delay, repeat, err = localSchedule(Schedule{Expression: "at(2026-09-19T12:00:05)", Timezone: "UTC"}, now)
	if err != nil || delay != 5*time.Second || repeat != 0 {
		t.Fatalf("at delay=%s repeat=%s err=%v", delay, repeat, err)
	}
	if _, _, err := localSchedule(Schedule{Expression: "cron(0 9 ? * MON *)", Timezone: "UTC"}, now); err == nil {
		t.Fatal("local cron schedule should be rejected explicitly")
	}
}
