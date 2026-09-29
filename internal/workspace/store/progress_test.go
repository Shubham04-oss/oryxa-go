package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestLatestApplyAttemptIsScopedAndBoundsOperationDetails(t *testing.T) {
	storage, org := newIntegrationStore(t)
	attempt := ApplyAttempt{ID: org + "_attempt", OrganizationID: org, ActorID: "owner", Status: "partial", StartedAt: time.Now(), UpdatedAt: time.Now(), Completed: []CompletedOperation{}}
	for i := 0; i < 150; i++ {
		attempt.Completed = append(attempt.Completed, CompletedOperation{Method: "POST", Path: fmt.Sprintf("/v1/organizations/%s/teams/team%d", org, i)})
	}
	if err := storage.SaveAttempt(context.Background(), attempt); err != nil {
		t.Fatal(err)
	}
	result, err := storage.LatestAttempt(context.Background(), org)
	if err != nil || result.CompletedCount != 150 || len(result.Completed) != 100 || result.Status != "partial" {
		t.Fatalf("latest progress: %+v %v", result, err)
	}
	if _, err := storage.LatestAttempt(context.Background(), org+"_other"); !errors.Is(err, ErrAttemptNotFound) {
		t.Fatalf("cross-org attempt leaked: %v", err)
	}
}
