package projector

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"log/slog"
	"strconv"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	sqstypes "github.com/aws/aws-sdk-go-v2/service/sqs/types"

	ragv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/rag/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

type fakeQueue struct {
	batches  [][]sqstypes.Message
	receives int
	deleted  []string
	released []sqstypes.ChangeMessageVisibilityBatchRequestEntry
}

func (queue *fakeQueue) ReceiveMessage(ctx context.Context, _ *sqs.ReceiveMessageInput, _ ...func(*sqs.Options)) (*sqs.ReceiveMessageOutput, error) {
	queue.receives++
	if len(queue.batches) == 0 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	batch := queue.batches[0]
	queue.batches = queue.batches[1:]
	return &sqs.ReceiveMessageOutput{Messages: batch}, nil
}

func (queue *fakeQueue) DeleteMessageBatch(ctx context.Context, input *sqs.DeleteMessageBatchInput, _ ...func(*sqs.Options)) (*sqs.DeleteMessageBatchOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	for _, entry := range input.Entries {
		queue.deleted = append(queue.deleted, aws.ToString(entry.ReceiptHandle))
	}
	return &sqs.DeleteMessageBatchOutput{}, nil
}

func (queue *fakeQueue) ChangeMessageVisibilityBatch(ctx context.Context, input *sqs.ChangeMessageVisibilityBatchInput, _ ...func(*sqs.Options)) (*sqs.ChangeMessageVisibilityBatchOutput, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	queue.released = append(queue.released, input.Entries...)
	return &sqs.ChangeMessageVisibilityBatchOutput{}, nil
}

// stoppingApplier cancels the poll while it applies the first change, as a
// shutdown arriving mid-batch does, and fails if its context is cancelled.
type stoppingApplier struct {
	stop    context.CancelFunc
	applied int
}

func (applier *stoppingApplier) Apply(ctx context.Context, _ Change) (*v1.ZedToken, error) {
	applier.stop()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	applier.applied++
	return &v1.ZedToken{Token: "t"}, nil
}

// TestPollReturnsUnhandledMessagesAtShutdown: the message in hand when the
// process stops is finished and deleted; the rest of its batch is made visible
// at once rather than after the queue's visibility timeout (#449).
func TestPollReturnsUnhandledMessagesAtShutdown(t *testing.T) {
	now := time.Now().UTC()
	signer, key, err := eventauth.NewLocalSigner("rag", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	verifier := eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"rag": key}, nil, func() time.Time { return now })
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	applier := &stoppingApplier{stop: stop}
	consumer, err := NewConsumer(applier, &fakeGrants{}, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	var batch []sqstypes.Message
	for index := range 3 {
		id := "evt_" + strconv.Itoa(index)
		data := `{"corpus_id":"c` + strconv.Itoa(index) + `","organization_id":"acme","scope":"organization","created_by":"sam"}`
		occurred := now.Format(time.RFC3339Nano)
		signature, _ := signer.Sign(ragv1.Source, ragv1.CorpusCreatedDetailType, id, 1, occurred, []byte(data))
		detail, _ := json.Marshal(receivedEnvelope{EventID: id, Version: 1, OccurredAt: occurred, Data: json.RawMessage(data), Signature: signature})
		body, _ := json.Marshal(eventBridgeEvent{ID: id, Source: ragv1.Source, DetailType: ragv1.CorpusCreatedDetailType, Detail: detail})
		batch = append(batch, sqstypes.Message{Body: aws.String(string(body)), ReceiptHandle: aws.String("r" + strconv.Itoa(index))})
	}
	queue := &fakeQueue{batches: [][]sqstypes.Message{batch, batch}}

	done := make(chan struct{})
	go func() { consumer.Poll(ctx, queue, "https://sqs.example/1/spicedb-events"); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Poll did not stop")
	}
	if applier.applied != 1 || len(queue.deleted) != 1 || queue.deleted[0] != "r0" {
		t.Fatalf("applied %d, deleted %v: the message in hand must finish", applier.applied, queue.deleted)
	}
	if len(queue.released) != 2 || aws.ToString(queue.released[0].ReceiptHandle) != "r1" || aws.ToString(queue.released[1].ReceiptHandle) != "r2" || queue.released[0].VisibilityTimeout != 0 {
		t.Fatalf("released %+v, want r1 and r2 at visibility 0", queue.released)
	}
	if queue.receives != 1 {
		t.Fatalf("received %d times after shutdown began, want 1", queue.receives)
	}
}
