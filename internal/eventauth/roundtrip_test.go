package eventauth

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"
	"time"
)

// TestSignatureSurvivesTheEnvelopeRoundTrip is the property the whole
// mechanism rests on: what the publisher signs, after travelling through
// json.Marshal and json.Unmarshal in the envelope, is what the consumer
// holds when it verifies.
func TestSignatureSurvivesTheEnvelopeRoundTrip(t *testing.T) {
	signer, key, err := NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, time.Now)

	occurred := time.Date(2026, 9, 21, 3, 14, 15, 130000000, time.UTC).Format(time.RFC3339Nano)
	data := json.RawMessage(`{"user_id":"user-1","display_name":"Asha","registered_at":"2026-09-13T12:00:00Z"}`)
	sig, err := signer.Sign("oryxa.auth", "auth.user.registered.v1", "evt_1", 1, occurred, data)
	if err != nil {
		t.Fatal(err)
	}

	type envelope struct {
		EventID    string          `json:"event_id"`
		Version    int             `json:"version"`
		OccurredAt string          `json:"occurred_at"`
		Data       json.RawMessage `json:"data"`
		Signature  string          `json:"sig"`
	}
	encoded, err := json.Marshal(envelope{EventID: "evt_1", Version: 1, OccurredAt: occurred, Data: data, Signature: sig})
	if err != nil {
		t.Fatal(err)
	}
	var back envelope
	if err := json.Unmarshal(encoded, &back); err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.auth", "auth.user.registered.v1", back.EventID, back.Version, back.OccurredAt, back.Data, back.Signature); err != nil {
		t.Fatalf("a signature that survived the envelope did not verify: %v", err)
	}
}

// TestASignatureSurvivesABusThatReordersKeys is the LocalStack lesson: an
// event bus that decodes and re-encodes JSON reorders object keys, so a
// signature over the publisher's own byte order would not verify against
// what the consumer received. Both sides normalize the data to one
// encoding, so the same JSON verifies however it was serialized.
func TestASignatureSurvivesABusThatReordersKeys(t *testing.T) {
	signer, key, err := NewLocalSigner("teams", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"teams": key}, nil, time.Now)

	// What the publisher signs: struct order.
	published := []byte(`{"team_id":"team_1","team_name":"Platform","notify_email":true}`)
	// What the consumer receives: the same JSON, alphabetically sorted, the
	// way a re-encoding bus writes it.
	reordered := []byte(`{"notify_email":true,"team_id":"team_1","team_name":"Platform"}`)

	sig, err := signer.Sign("oryxa.teams", "teams.member.pinged.v1", "evt_1", 1, "2026-09-20T21:44:14Z", published)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.teams", "teams.member.pinged.v1", "evt_1", 1, "2026-09-20T21:44:14Z", reordered, sig); err != nil {
		t.Fatalf("a re-encoding bus broke the signature: %v", err)
	}
}
