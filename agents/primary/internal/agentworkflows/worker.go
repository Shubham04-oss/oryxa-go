package agentworkflows

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sfn"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

type workflowExecutor interface {
	Execute(context.Context, Dispatch) (Execution, error)
}

type workflowQueue interface {
	ReceiveMessage(context.Context, *sqs.ReceiveMessageInput, ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error)
	DeleteMessage(context.Context, *sqs.DeleteMessageInput, ...func(*sqs.Options)) (*sqs.DeleteMessageOutput, error)
	ChangeMessageVisibility(context.Context, *sqs.ChangeMessageVisibilityInput, ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityOutput, error)
}

type taskCallbacks interface {
	SendTaskSuccess(context.Context, *sfn.SendTaskSuccessInput, ...func(*sfn.Options)) (*sfn.SendTaskSuccessOutput, error)
	SendTaskFailure(context.Context, *sfn.SendTaskFailureInput, ...func(*sfn.Options)) (*sfn.SendTaskFailureOutput, error)
	SendTaskHeartbeat(context.Context, *sfn.SendTaskHeartbeatInput, ...func(*sfn.Options)) (*sfn.SendTaskHeartbeatOutput, error)
}

type WorkerConfig struct {
	QueueURL          string
	Visibility        time.Duration
	HeartbeatInterval time.Duration
}

// Worker receives callback tasks from the Standard workflow. The task token
// stays only in the SQS message and callback call; it is never persisted with
// member content or emitted in a log.
type Worker struct {
	queue     workflowQueue
	callbacks taskCallbacks
	executor  workflowExecutor
	config    WorkerConfig
	logger    *slog.Logger
}

func NewWorker(queue workflowQueue, callbacks taskCallbacks, executor workflowExecutor, config WorkerConfig, logger *slog.Logger) (*Worker, error) {
	if queue == nil || callbacks == nil || executor == nil || config.QueueURL == "" {
		return nil, errors.New("workflow worker: queue, callbacks, executor and queue URL are required")
	}
	if config.Visibility <= 0 {
		config.Visibility = 15 * time.Minute
	}
	if config.Visibility > 12*time.Hour {
		config.Visibility = 12 * time.Hour
	}
	if config.HeartbeatInterval <= 0 {
		config.HeartbeatInterval = 30 * time.Second
	}
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Worker{queue: queue, callbacks: callbacks, executor: executor, config: config, logger: logger}, nil
}

type callbackMessage struct {
	TaskToken string `json:"task_token"`
	Dispatch
}

func (worker *Worker) Poll(ctx context.Context) {
	for ctx.Err() == nil {
		output, err := worker.queue.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl: aws.String(worker.config.QueueURL), MaxNumberOfMessages: 1,
			WaitTimeSeconds: 20, VisibilityTimeout: int32(worker.config.Visibility / time.Second),
			MessageSystemAttributeNames: []sqstypes.MessageSystemAttributeName{sqstypes.MessageSystemAttributeNameApproximateReceiveCount},
		})
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			worker.logger.ErrorContext(ctx, "receive agent workflow task", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
			continue
		}
		for _, message := range output.Messages {
			if err := worker.handleMessage(ctx, message); err != nil {
				worker.logger.ErrorContext(ctx, "handle agent workflow task", "error", err)
				continue
			}
			if _, err := worker.queue.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl: aws.String(worker.config.QueueURL), ReceiptHandle: message.ReceiptHandle,
			}); err != nil && ctx.Err() == nil {
				worker.logger.ErrorContext(ctx, "delete agent workflow task", "error", err)
			}
		}
	}
}

func (worker *Worker) handleMessage(ctx context.Context, message sqstypes.Message) error {
	var callback callbackMessage
	if err := json.Unmarshal([]byte(aws.ToString(message.Body)), &callback); err != nil {
		return fmt.Errorf("decode workflow callback: %w", err)
	}
	if callback.TaskToken == "" || callback.WorkflowID == "" || callback.OrganizationID == "" || callback.OwnerUserID == "" {
		return errors.New("workflow callback is missing its task token or routing identity")
	}
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(worker.config.HeartbeatInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = worker.callbacks.SendTaskHeartbeat(ctx, &sfn.SendTaskHeartbeatInput{TaskToken: aws.String(callback.TaskToken)})
				_, _ = worker.queue.ChangeMessageVisibility(ctx, &sqs.ChangeMessageVisibilityInput{
					QueueUrl: aws.String(worker.config.QueueURL), ReceiptHandle: message.ReceiptHandle,
					VisibilityTimeout: int32(worker.config.Visibility / time.Second),
				})
			}
		}
	}()
	execution, executeErr := worker.executor.Execute(ctx, callback.Dispatch)
	close(stop)
	<-done
	if errors.Is(executeErr, ErrInProgress) {
		// Another delivery still owns the durable execution lease. Leave this
		// message for a later delivery rather than racing its callback.
		return executeErr
	}
	if executeErr != nil {
		code, cause := workflowErrorCode(executeErr), callbackFailureCause(execution.ID)
		_, callbackErr := worker.callbacks.SendTaskFailure(ctx, &sfn.SendTaskFailureInput{
			TaskToken: aws.String(callback.TaskToken), Error: aws.String(code), Cause: aws.String(cause),
		})
		return callbackErr
	}
	if execution.Status == ExecutionFailed {
		_, err := worker.callbacks.SendTaskFailure(ctx, &sfn.SendTaskFailureInput{
			TaskToken: aws.String(callback.TaskToken), Error: aws.String("WorkflowFailed"),
			Cause: aws.String(callbackFailureCause(execution.ID)),
		})
		return err
	}
	result, err := json.Marshal(map[string]any{"execution_id": execution.ID, "status": execution.Status})
	if err != nil {
		return err
	}
	_, err = worker.callbacks.SendTaskSuccess(ctx, &sfn.SendTaskSuccessInput{
		TaskToken: aws.String(callback.TaskToken), Output: aws.String(string(result)),
	})
	return err
}

func callbackFailureCause(executionID string) string {
	if executionID == "" {
		return "Agent workflow failed; inspect the service logs and workflow ledger."
	}
	return "Agent workflow failed; inspect workflow execution " + executionID + "."
}
