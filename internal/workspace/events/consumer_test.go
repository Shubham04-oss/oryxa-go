package events

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/store"
)

type fakeCleanup struct {
	calls []store.Departure
	err   error
}

func (fake *fakeCleanup) Cleanup(_ context.Context, event store.Departure) error {
	fake.calls = append(fake.calls, event)
	return fake.err
}

func TestLifecycleDispatchRequiresTheCorrectPublisherSignature(t *testing.T) {
	now := time.Now().UTC()
	auth, authKey, err := eventauth.NewLocalSigner("auth", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	teams, teamsKey, err := eventauth.NewLocalSigner("teams", func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	verifier := eventauth.NewVerifier(map[string]*ecdsa.PublicKey{"auth": authKey, "teams": teamsKey}, nil, func() time.Time { return now })
	for _, tc := range []struct {
		source, kind, data, want string
		signer                   *eventauth.Signer
	}{
		{"oryxa.auth", "auth.member.removed.v1", `{"organization_id":"org_1","user_id":"user_1","email":"left@example.com"}`, "user", auth},
		{"oryxa.auth", "auth.organization.deleted.v1", `{"organization_id":"org_1"}`, "organization", auth},
		{"oryxa.teams", "teams.team.deleted.v1", `{"organization_id":"org_1","team_id":"team_1","name":"Old Team"}`, "team", teams},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			app := &fakeCleanup{}
			consumer, err := NewConsumer(app, verifier, slog.Default())
			if err != nil {
				t.Fatal(err)
			}
			occurred := now.Format(time.RFC3339Nano)
			signature, err := tc.signer.Sign(tc.source, tc.kind, "evt_1", 1, occurred, []byte(tc.data))
			if err != nil {
				t.Fatal(err)
			}
			envelope := receivedEnvelope{EventID: "evt_1", Version: 1, OccurredAt: occurred, Data: json.RawMessage(tc.data), Signature: signature}
			body := func(source string, envelope receivedEnvelope) string {
				detail, _ := json.Marshal(envelope)
				value, _ := json.Marshal(eventBridgeEvent{Source: source, DetailType: tc.kind, Detail: detail})
				return string(value)
			}
			if err := consumer.Handle(context.Background(), body(tc.source, envelope)); err != nil {
				t.Fatal(err)
			}
			if len(app.calls) != 1 || app.calls[0].Kind != tc.want || app.calls[0].EventID != "evt_1" || app.calls[0].OrganizationID != "org_1" {
				t.Fatalf("calls=%+v", app.calls)
			}
			for _, change := range []func(*receivedEnvelope){func(e *receivedEnvelope) { e.Signature = "" }, func(e *receivedEnvelope) { e.Data = json.RawMessage(`{"organization_id":"victim"}`) }} {
				altered := envelope
				change(&altered)
				if err := consumer.Handle(context.Background(), body(tc.source, altered)); !errors.Is(err, eventauth.ErrUnverified) {
					t.Fatalf("unverified event accepted: %v", err)
				}
			}
			if err := consumer.Handle(context.Background(), body("oryxa.apps", envelope)); err == nil {
				t.Fatal("wrong source accepted")
			}
			if len(app.calls) != 1 {
				t.Fatal("unverified event called cleanup")
			}
			app.err = store.ErrApplyBusy
			if err := consumer.Handle(context.Background(), body(tc.source, envelope)); !errors.Is(err, store.ErrApplyBusy) {
				t.Fatalf("busy event must remain retryable: %v", err)
			}
		})
	}
}

func TestLifecycleConsumerRequiresVerifier(t *testing.T) {
	if _, err := NewConsumer(&fakeCleanup{}, nil, slog.Default()); err == nil {
		t.Fatal("accepted no verifier")
	}
}
