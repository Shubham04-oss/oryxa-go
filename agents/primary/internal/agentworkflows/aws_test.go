package agentworkflows

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
	"github.com/aws/smithy-go"
)

type fakeSFNStarter struct{ input *sfn.StartExecutionInput }

func (fake *fakeSFNStarter) StartExecution(_ context.Context, input *sfn.StartExecutionInput, _ ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error) {
	fake.input = input
	return &sfn.StartExecutionOutput{ExecutionArn: aws.String("arn:execution:" + aws.ToString(input.Name))}, nil
}

func TestStepFunctionsStartsWithIdentifierOnlyInput(t *testing.T) {
	client := &fakeSFNStarter{}
	orchestrator, err := NewStepFunctions(client, "arn:state-machine")
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{ExecutionID: "awx_1", WorkflowID: "awf_1", OrganizationID: "org_1", OwnerUserID: "user_1", StartedBy: StartedByAPI}
	arn, err := orchestrator.Start(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	var sent StartRequest
	if json.Unmarshal([]byte(aws.ToString(client.input.Input)), &sent) != nil || sent != request || arn != "arn:execution:awx_1" {
		t.Fatalf("input=%s sent=%+v arn=%q", aws.ToString(client.input.Input), sent, arn)
	}
}

type fakeScheduleAPI struct {
	create   *scheduler.CreateScheduleInput
	update   *scheduler.UpdateScheduleInput
	deleted  *scheduler.DeleteScheduleInput
	conflict bool
}

func (fake *fakeScheduleAPI) CreateSchedule(_ context.Context, input *scheduler.CreateScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error) {
	fake.create = input
	if fake.conflict {
		return nil, &smithy.GenericAPIError{Code: "ConflictException", Message: "exists"}
	}
	return &scheduler.CreateScheduleOutput{}, nil
}
func (fake *fakeScheduleAPI) UpdateSchedule(_ context.Context, input *scheduler.UpdateScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error) {
	fake.update = input
	return &scheduler.UpdateScheduleOutput{}, nil
}
func (fake *fakeScheduleAPI) DeleteSchedule(_ context.Context, input *scheduler.DeleteScheduleInput, _ ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error) {
	fake.deleted = input
	return &scheduler.DeleteScheduleOutput{}, nil
}

func TestEventBridgeSchedulerTargetsTheStandardWorkflow(t *testing.T) {
	client := &fakeScheduleAPI{conflict: true}
	backend, err := NewEventBridgeScheduler(client, SchedulerConfig{StateMachineARN: "arn:state-machine", RoleARN: "arn:role", DeadLetterARN: "arn:dlq", GroupName: "oryxa"})
	if err != nil {
		t.Fatal(err)
	}
	workflow := Workflow{ID: "awf_1", OrganizationID: "org_1", OwnerUserID: "user_1", Status: StatusActive, Schedule: &Schedule{Expression: "rate(1 day)", Timezone: "Asia/Kolkata", State: ScheduleEnabled}}
	if err := backend.Upsert(context.Background(), workflow); err != nil {
		t.Fatal(err)
	}
	if client.create == nil || client.update == nil || aws.ToString(client.update.Target.Arn) != "arn:state-machine" || aws.ToString(client.update.Target.RoleArn) != "arn:role" {
		t.Fatalf("create=%+v update=%+v", client.create, client.update)
	}
	var request StartRequest
	if json.Unmarshal([]byte(aws.ToString(client.update.Target.Input)), &request) != nil || request.WorkflowID != "awf_1" || request.StartedBy != StartedBySchedule || request.ExecutionID != "" {
		t.Fatalf("target input=%s", aws.ToString(client.update.Target.Input))
	}
	if err := backend.Delete(context.Background(), workflow); err != nil || client.deleted == nil {
		t.Fatalf("delete=%+v err=%v", client.deleted, err)
	}
}

type fakeWorkerQueue struct{}

func (fakeWorkerQueue) ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	return &sqs.ReceiveMessageOutput{}, nil
}
func (fakeWorkerQueue) DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error) {
	return &sqs.DeleteMessageOutput{}, nil
}
func (fakeWorkerQueue) ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error) {
	return &sqs.ChangeMessageVisibilityOutput{}, nil
}

type fakeCallbacks struct {
	success, failure, heartbeat int
	successInput                *sfn.SendTaskSuccessInput
	failureInput                *sfn.SendTaskFailureInput
}

func (fake *fakeCallbacks) SendTaskSuccess(_ context.Context, input *sfn.SendTaskSuccessInput, _ ...func(*sfn.Options)) (*sfn.SendTaskSuccessOutput, error) {
	fake.success++
	fake.successInput = input
	return &sfn.SendTaskSuccessOutput{}, nil
}
func (fake *fakeCallbacks) SendTaskFailure(_ context.Context, input *sfn.SendTaskFailureInput, _ ...func(*sfn.Options)) (*sfn.SendTaskFailureOutput, error) {
	fake.failure++
	fake.failureInput = input
	return &sfn.SendTaskFailureOutput{}, nil
}
func (fake *fakeCallbacks) SendTaskHeartbeat(context.Context, *sfn.SendTaskHeartbeatInput, ...func(*sfn.Options)) (*sfn.SendTaskHeartbeatOutput, error) {
	fake.heartbeat++
	return &sfn.SendTaskHeartbeatOutput{}, nil
}

type fakeWorkflowExecutor struct {
	execution Execution
	err       error
	dispatch  Dispatch
}

func (fake *fakeWorkflowExecutor) Execute(_ context.Context, dispatch Dispatch) (Execution, error) {
	fake.dispatch = dispatch
	return fake.execution, fake.err
}

func TestWorkerCompletesTheTaskTokenWithoutPersistingIt(t *testing.T) {
	callbacks := &fakeCallbacks{}
	executor := &fakeWorkflowExecutor{execution: Execution{ID: "awx_1", Status: ExecutionNeedsConfirmation}}
	worker, err := NewWorker(fakeWorkerQueue{}, callbacks, executor, WorkerConfig{QueueURL: "queue", HeartbeatInterval: time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"task_token":"secret-token","workflow_id":"awf_1","organization_id":"org_1","owner_user_id":"user_1","started_by":"schedule","step_functions_execution_arn":"arn:execution:1"}`
	if err := worker.handleMessage(context.Background(), types.Message{Body: aws.String(body), ReceiptHandle: aws.String("receipt")}); err != nil {
		t.Fatal(err)
	}
	if callbacks.success != 1 || callbacks.failure != 0 || executor.dispatch.WorkflowID != "awf_1" {
		t.Fatalf("callbacks=%+v dispatch=%+v", callbacks, executor.dispatch)
	}
	if got := aws.ToString(callbacks.successInput.Output); got != `{"execution_id":"awx_1","status":"needs_confirmation"}` {
		t.Fatalf("callback output = %s", got)
	}
}

func TestWorkerKeepsFailureDetailsOutOfStepFunctions(t *testing.T) {
	callbacks := &fakeCallbacks{}
	executor := &fakeWorkflowExecutor{
		execution: Execution{ID: "awx_1", Status: ExecutionFailed, Error: "provider returned secret customer content"},
	}
	worker, err := NewWorker(fakeWorkerQueue{}, callbacks, executor, WorkerConfig{QueueURL: "queue", HeartbeatInterval: time.Hour}, nil)
	if err != nil {
		t.Fatal(err)
	}
	body := `{"task_token":"secret-token","workflow_id":"awf_1","organization_id":"org_1","owner_user_id":"user_1","started_by":"schedule"}`
	if err := worker.handleMessage(context.Background(), types.Message{Body: aws.String(body), ReceiptHandle: aws.String("receipt")}); err != nil {
		t.Fatal(err)
	}
	if callbacks.failure != 1 || callbacks.success != 0 {
		t.Fatalf("callbacks=%+v", callbacks)
	}
	if got := aws.ToString(callbacks.failureInput.Cause); got != "Agent workflow failed; inspect workflow execution awx_1." {
		t.Fatalf("failure cause = %q", got)
	}
}
