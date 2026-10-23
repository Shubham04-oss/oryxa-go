package projector

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	v1 "github.com/authzed/authzed-go/proto/authzed/api/v1"

	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

type fakeApplier struct{ changes []Change }

func (fake *fakeApplier) Apply(_ context.Context, change Change) (*v1.ZedToken, error) {
	fake.changes = append(fake.changes, change)
	return &v1.ZedToken{Token: "t"}, nil
}

type fakeGrants struct{ synced []string }

func (fake *fakeGrants) SyncGrants(_ context.Context, org, agent string) error {
	fake.synced = append(fake.synced, org+"/"+agent)
	return nil
}

func TestConsumerAppliesSignedEventsAndRereadsGrants(t *testing.T) {
	now := time.Now().UTC()
	signer, key, err := eventauth.NewLocalSigner("agents", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	verifier := eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"agents": key}, nil, func() time.Time { return now })
	writer, grants := &fakeApplier{}, &fakeGrants{}
	consumer, err := NewConsumer(writer, grants, verifier, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	body := func(detailType, data, signature string) string {
		occurred := now.Format(time.RFC3339Nano)
		if signature == "" {
			signature, _ = signer.Sign(agentsv1.Source, detailType, "evt_1", 1, occurred, []byte(data))
		}
		detail, _ := json.Marshal(receivedEnvelope{EventID: "evt_1", Version: 1, OccurredAt: occurred, Data: json.RawMessage(data), Signature: signature})
		value, _ := json.Marshal(eventBridgeEvent{Source: agentsv1.Source, DetailType: detailType, Detail: detail})
		return string(value)
	}

	updated := `{"agent_id":"agt_r","user_id":"sam","organization_id":"acme","kind":"specialist"}`
	if err := consumer.Handle(context.Background(), body(agentsv1.AgentUpdatedDetailType, updated, "")); err != nil {
		t.Fatal(err)
	}
	if len(writer.changes) != 1 || len(grants.synced) != 1 || grants.synced[0] != "acme/agt_r" {
		t.Fatalf("changes = %+v, synced = %v", writer.changes, grants.synced)
	}

	deleted := `{"agent_id":"agt_r","organization_id":"acme"}`
	if err := consumer.Handle(context.Background(), body(agentsv1.AgentDeletedDetailType, deleted, "")); err != nil {
		t.Fatal(err)
	}
	if len(grants.synced) != 1 {
		t.Fatal("a deletion re-read grants")
	}

	if err := consumer.Handle(context.Background(), body(agentsv1.AgentUpdatedDetailType, updated, "forged")); !errors.Is(err, eventauth.ErrUnverified) {
		t.Fatalf("forged event: %v", err)
	}
	if err := consumer.Handle(context.Background(), body(agentsv1.RunCompletedDetailType, `{}`, "")); err == nil {
		t.Fatal("an event the projector does not consume was accepted")
	}
	if len(writer.changes) != 2 {
		t.Fatalf("changes = %d, want 2", len(writer.changes))
	}
}
