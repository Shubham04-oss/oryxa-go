package store

import (
	"context"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"

	"github.com/Shubham04-oss/oryxa-go/email/internal/delivery"
)

// These tests exercise the conditional writes against a real DynamoDB, such as
// the LocalStack in docker-compose.yml:
//
//	EMAIL_DYNAMODB_TEST_ENDPOINT=http://localhost:4566 go test ./email/internal/store
//
// They create throwaway tables and delete them afterwards.
func TestDynamoDB(t *testing.T) {
	endpoint := os.Getenv("EMAIL_DYNAMODB_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("set EMAIL_DYNAMODB_TEST_ENDPOINT to run against DynamoDB")
	}

	ctx := context.Background()
	client := dynamodb.NewFromConfig(aws.Config{
		Region:      "ap-south-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}, func(options *dynamodb.Options) {
		options.BaseEndpoint = aws.String(endpoint)
	})
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	deliveries := createTable(t, client, "test-email-deliveries-"+suffix, "delivery_id")
	suppressions := createTable(t, client, "test-email-suppressions-"+suffix, "address")
	store := NewDynamoDB(client, deliveries, suppressions)

	now := time.Date(2026, time.September, 13, 12, 0, 0, 0, time.UTC)
	claim := delivery.Delivery{
		ID:             "delivery-1",
		Event:          delivery.EventRef{Source: "oryxa.auth", DetailType: "auth.user.registered.v1", ID: "event-1"},
		Template:       "welcome",
		Recipient:      "Asha@Example.com",
		Status:         delivery.StatusSending,
		LeaseExpiresAt: now.Add(delivery.ClaimLease),
		CreatedAt:      now,
		ExpiresAt:      now.Add(delivery.Retention),
	}

	t.Run("claims are exclusive until settled or expired", func(t *testing.T) {
		assertClaim(t, store, claim, now, true)
		assertClaim(t, store, claim, now.Add(time.Second), false)
		assertClaim(t, store, claim, claim.LeaseExpiresAt.Add(time.Millisecond), true)

		if err := store.MarkFailed(ctx, claim.ID, "throttled", now); err != nil {
			t.Fatalf("MarkFailed() error = %v", err)
		}
		retry := claim
		retry.LeaseExpiresAt = now.Add(time.Hour)
		assertClaim(t, store, retry, now, true)

		recorded, err := store.MarkSent(ctx, claim.ID, "ses-1", now)
		if err != nil || !recorded {
			t.Fatalf("MarkSent() = %v, %v; want recorded", recorded, err)
		}
		assertClaim(t, store, claim, now.Add(48*time.Hour), false)

		got, err := store.Get(ctx, claim.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if got.Status != delivery.StatusSent || got.SESMessageID != "ses-1" || !got.LeaseExpiresAt.IsZero() || !got.CreatedAt.Equal(now) {
			t.Fatalf("Get() = %+v", got)
		}
		if got.Event != claim.Event || got.Recipient != "Asha@Example.com" || !got.ExpiresAt.Equal(claim.ExpiresAt) {
			t.Fatalf("Get() lost the claim's details: %+v", got)
		}
	})

	t.Run("a settled claim cannot be marked sent again", func(t *testing.T) {
		recorded, err := store.MarkSent(ctx, claim.ID, "ses-2", now)
		if err != nil || recorded {
			t.Fatalf("MarkSent() on a sent delivery = %v, %v; want not recorded", recorded, err)
		}
	})

	t.Run("feedback only moves a delivery forward", func(t *testing.T) {
		assertFeedback(t, store, delivery.Feedback{DeliveryID: claim.ID, Status: delivery.StatusDelivered, OccurredAt: now}, true)
		assertFeedback(t, store, delivery.Feedback{DeliveryID: claim.ID, Status: delivery.StatusDelayed, OccurredAt: now}, false)
		assertFeedback(t, store, delivery.Feedback{DeliveryID: claim.ID, Status: delivery.StatusComplained, OccurredAt: now}, true)
		assertFeedback(t, store, delivery.Feedback{DeliveryID: "unknown", Status: delivery.StatusBounced, OccurredAt: now}, false)

		got, err := store.Get(ctx, claim.ID)
		if err != nil || got.Status != delivery.StatusComplained {
			t.Fatalf("Get() = %+v, %v; want complained", got, err)
		}
	})

	t.Run("an address keeps its first suppression", func(t *testing.T) {
		if _, found, err := store.Suppression(ctx, "asha@example.com"); err != nil || found {
			t.Fatalf("Suppression() before suppressing = %v, %v", found, err)
		}
		first := delivery.Suppression{Address: "asha@example.com", Reason: delivery.ReasonBounce, Detail: "Permanent/General", CreatedAt: now}
		if err := store.Suppress(ctx, first); err != nil {
			t.Fatalf("Suppress() error = %v", err)
		}
		if err := store.Suppress(ctx, delivery.Suppression{Address: "asha@example.com", Reason: delivery.ReasonComplaint, CreatedAt: now}); err != nil {
			t.Fatalf("second Suppress() error = %v", err)
		}

		got, found, err := store.Suppression(ctx, "asha@example.com")
		if err != nil || !found || got.Reason != delivery.ReasonBounce || !got.CreatedAt.Equal(now) {
			t.Fatalf("Suppression() = %+v, %v, %v; want the first bounce", got, found, err)
		}
	})
}

func createTable(t *testing.T, client *dynamodb.Client, name, key string) string {
	t.Helper()
	ctx := context.Background()
	_, err := client.CreateTable(ctx, &dynamodb.CreateTableInput{
		TableName:            aws.String(name),
		BillingMode:          types.BillingModePayPerRequest,
		AttributeDefinitions: []types.AttributeDefinition{{AttributeName: aws.String(key), AttributeType: types.ScalarAttributeTypeS}},
		KeySchema:            []types.KeySchemaElement{{AttributeName: aws.String(key), KeyType: types.KeyTypeHash}},
	})
	if err != nil {
		t.Fatalf("create table %s: %v", name, err)
	}
	t.Cleanup(func() {
		if _, err := client.DeleteTable(context.Background(), &dynamodb.DeleteTableInput{TableName: aws.String(name)}); err != nil {
			t.Errorf("delete table %s: %v", name, err)
		}
	})
	if err := dynamodb.NewTableExistsWaiter(client).Wait(ctx, &dynamodb.DescribeTableInput{TableName: aws.String(name)}, time.Minute); err != nil {
		t.Fatalf("wait for table %s: %v", name, err)
	}
	return name
}

func assertClaim(t *testing.T, store *DynamoDB, claim delivery.Delivery, now time.Time, want bool) {
	t.Helper()
	claimed, err := store.Claim(context.Background(), claim, now)
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	if claimed != want {
		t.Fatalf("Claim() at %s = %v, want %v", now.Format(time.RFC3339Nano), claimed, want)
	}
}

func assertFeedback(t *testing.T, store *DynamoDB, feedback delivery.Feedback, want bool) {
	t.Helper()
	applied, err := store.RecordFeedback(context.Background(), feedback, feedback.OccurredAt)
	if err != nil {
		t.Fatalf("RecordFeedback(%s) error = %v", feedback.Status, err)
	}
	if applied != want {
		t.Fatalf("RecordFeedback(%s) applied = %v, want %v", feedback.Status, applied, want)
	}
}
