package agentworkflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/scheduler"
	schedulertypes "github.com/aws/aws-sdk-go-v2/service/scheduler/types"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/smithy-go"
)

type stepFunctionsAPI interface {
	StartExecution(context.Context, *sfn.StartExecutionInput, ...func(*sfn.Options)) (*sfn.StartExecutionOutput, error)
}

// StepFunctions starts the generic Standard workflow. The execution input
// contains only Oryxa identifiers; assignments and provider data remain in
// PostgreSQL and are loaded by the callback worker.
type StepFunctions struct {
	client          stepFunctionsAPI
	stateMachineARN string
}

func NewStepFunctions(client stepFunctionsAPI, stateMachineARN string) (*StepFunctions, error) {
	if client == nil || strings.TrimSpace(stateMachineARN) == "" {
		return nil, errors.New("Step Functions client and state machine ARN are required")
	}
	return &StepFunctions{client: client, stateMachineARN: strings.TrimSpace(stateMachineARN)}, nil
}

func (stepFunctions *StepFunctions) Start(ctx context.Context, request StartRequest) (string, error) {
	payload, err := json.Marshal(request)
	if err != nil {
		return "", err
	}
	output, err := stepFunctions.client.StartExecution(ctx, &sfn.StartExecutionInput{
		StateMachineArn: aws.String(stepFunctions.stateMachineARN),
		Name:            aws.String(request.ExecutionID),
		Input:           aws.String(string(payload)),
	})
	if err != nil {
		return "", err
	}
	return aws.ToString(output.ExecutionArn), nil
}

type schedulerAPI interface {
	CreateSchedule(context.Context, *scheduler.CreateScheduleInput, ...func(*scheduler.Options)) (*scheduler.CreateScheduleOutput, error)
	UpdateSchedule(context.Context, *scheduler.UpdateScheduleInput, ...func(*scheduler.Options)) (*scheduler.UpdateScheduleOutput, error)
	DeleteSchedule(context.Context, *scheduler.DeleteScheduleInput, ...func(*scheduler.Options)) (*scheduler.DeleteScheduleOutput, error)
}

type SchedulerConfig struct {
	StateMachineARN string
	RoleARN         string
	DeadLetterARN   string
	GroupName       string
}

// EventBridgeScheduler keeps one deterministic schedule per workflow and
// targets the same Standard state machine used by the manual execution API.
type EventBridgeScheduler struct {
	client schedulerAPI
	config SchedulerConfig
}

func NewEventBridgeScheduler(client schedulerAPI, config SchedulerConfig) (*EventBridgeScheduler, error) {
	config.StateMachineARN = strings.TrimSpace(config.StateMachineARN)
	config.RoleARN = strings.TrimSpace(config.RoleARN)
	config.DeadLetterARN = strings.TrimSpace(config.DeadLetterARN)
	config.GroupName = strings.TrimSpace(config.GroupName)
	if client == nil || config.StateMachineARN == "" || config.RoleARN == "" {
		return nil, errors.New("EventBridge Scheduler client, state machine ARN and role ARN are required")
	}
	if config.GroupName == "" {
		config.GroupName = "default"
	}
	return &EventBridgeScheduler{client: client, config: config}, nil
}

func (backend *EventBridgeScheduler) Upsert(ctx context.Context, workflow Workflow) error {
	if workflow.Schedule == nil {
		return errors.New("workflow has no schedule")
	}
	payload, err := json.Marshal(StartRequest{
		WorkflowID: workflow.ID, OrganizationID: workflow.OrganizationID,
		OwnerUserID: workflow.OwnerUserID, StartedBy: StartedBySchedule,
	})
	if err != nil {
		return err
	}
	name := scheduleName(workflow.ID)
	state := schedulertypes.ScheduleStateEnabled
	if workflow.Status != StatusActive || workflow.Schedule.State != ScheduleEnabled {
		state = schedulertypes.ScheduleStateDisabled
	}
	target := &schedulertypes.Target{
		Arn: aws.String(backend.config.StateMachineARN), RoleArn: aws.String(backend.config.RoleARN),
		Input: aws.String(string(payload)),
		RetryPolicy: &schedulertypes.RetryPolicy{
			MaximumEventAgeInSeconds: aws.Int32(3600), MaximumRetryAttempts: aws.Int32(3),
		},
	}
	if backend.config.DeadLetterARN != "" {
		target.DeadLetterConfig = &schedulertypes.DeadLetterConfig{Arn: aws.String(backend.config.DeadLetterARN)}
	}
	window := &schedulertypes.FlexibleTimeWindow{Mode: schedulertypes.FlexibleTimeWindowModeOff}
	_, err = backend.client.CreateSchedule(ctx, &scheduler.CreateScheduleInput{
		Name: aws.String(name), GroupName: aws.String(backend.config.GroupName),
		Description:                aws.String("Oryxa agent workflow " + workflow.ID),
		ScheduleExpression:         aws.String(workflow.Schedule.Expression),
		ScheduleExpressionTimezone: aws.String(workflow.Schedule.Timezone),
		FlexibleTimeWindow:         window, State: state, Target: target,
	})
	if !apiError(err, "ConflictException") {
		return err
	}
	_, err = backend.client.UpdateSchedule(ctx, &scheduler.UpdateScheduleInput{
		Name: aws.String(name), GroupName: aws.String(backend.config.GroupName),
		Description:                aws.String("Oryxa agent workflow " + workflow.ID),
		ScheduleExpression:         aws.String(workflow.Schedule.Expression),
		ScheduleExpressionTimezone: aws.String(workflow.Schedule.Timezone),
		FlexibleTimeWindow:         window, State: state, Target: target,
	})
	return err
}

func (backend *EventBridgeScheduler) Delete(ctx context.Context, workflow Workflow) error {
	_, err := backend.client.DeleteSchedule(ctx, &scheduler.DeleteScheduleInput{
		Name: aws.String(scheduleName(workflow.ID)), GroupName: aws.String(backend.config.GroupName),
	})
	if apiError(err, "ResourceNotFoundException") {
		return nil
	}
	return err
}

func scheduleName(workflowID string) string {
	// Workflow IDs are URL-safe and the prefix keeps this resource namespace
	// distinct. EventBridge Scheduler names are limited to 64 characters.
	name := "oryxa-" + workflowID
	if len(name) > 64 {
		return name[:64]
	}
	return name
}

func apiError(err error, code string) bool {
	var api smithy.APIError
	return errors.As(err, &api) && api.ErrorCode() == code
}

func workflowErrorCode(err error) string {
	switch {
	case errors.Is(err, ErrDisabled):
		return "WorkflowDisabled"
	case errors.Is(err, ErrNotFound):
		return "WorkflowNotFound"
	case errors.Is(err, ErrInProgress):
		return "WorkflowInProgress"
	default:
		return fmt.Sprintf("%T", err)
	}
}
