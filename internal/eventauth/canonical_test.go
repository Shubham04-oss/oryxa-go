package eventauth

import (
	"crypto/ecdsa"
	"encoding/json"
	"testing"
	"time"
)

// TestNumberPrecisionCannotCollideSignatures is the sharpest attack on the
// normalization: a naive decode turns every JSON number into a float64,
// which cannot tell integers above 2^53 apart. Two different large numbers
// would then canonicalize the same, and a tampered payload would verify
// against the signature of the original. A signature scheme must never let
// different content hash the same, however unlikely the shape.
func TestNumberPrecisionCannotCollideSignatures(t *testing.T) {
	signer, key, err := NewLocalSigner("auth", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, time.Now)

	original := []byte(`{"revision":9007199254740993}`)
	tampered := []byte(`{"revision":9007199254740992}`)

	sig, err := signer.Sign("oryxa.auth", "auth.entitlements.changed.v1", "evt_1", 1, "2026-09-21T00:00:00Z", original)
	if err != nil {
		t.Fatal(err)
	}
	err = verifier.Verify("oryxa.auth", "auth.entitlements.changed.v1", "evt_1", 1, "2026-09-21T00:00:00Z", tampered, sig)
	if err == nil {
		t.Fatal("two different large integers collided in the canonical form; a tampered payload verified against the original's signature")
	}
}

// TestSemanticallyEqualEncodingsVerifyTheSame: the bus may re-encode, and
// numbers may travel as 1, 1.0 or 1e0 — all the same JSON value. A signature
// must verify across those encodings, or the bus breaks it itself.
func TestSemanticallyEqualEncodingsVerifyTheSame(t *testing.T) {
	signer, key, err := NewLocalSigner("teams", time.Now)
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"teams": key}, nil, time.Now)

	published := []byte(`{"member_count":1,"ratio":1.5,"name":"a<b"}`)
	reencoded := []byte("{\n  \"name\":\"a\\u003cb\",\n  \"ratio\":1.5e0,\n  \"member_count\":1.0\n}")

	if string(canonicalJSON(published)) != string(canonicalJSON(reencoded)) {
		t.Fatalf("canonical forms differ:\n  %s\n  %s", canonicalJSON(published), canonicalJSON(reencoded))
	}

	sig, err := signer.Sign("oryxa.teams", "teams.team.called.v1", "evt_1", 1, "2026-09-21T00:00:00Z", published)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.teams", "teams.team.called.v1", "evt_1", 1, "2026-09-21T00:00:00Z", reencoded, sig); err != nil {
		t.Fatalf("a re-encoded but semantically equal payload did not verify: %v", err)
	}
}

// TestDuplicateKeysCannotDisguiseATamperedValue: JSON allows a repeated
// object key, and decoders keep the last. The canonical form and the
// consumer's typed decode must agree on that rule, or an attacker could
// smuggle a second value past the signature. Whatever wins the duplicate
// must be the same value the signature covers — and the canonical form and
// a Go decoder must agree on which one won.
func TestDuplicateKeysCannotDisguiseATamperedValue(t *testing.T) {
	// The tampered body claims 999 first and 10 last: a last-wins reader
	// sees 10, but a first-wins reader would see 999. Every consumer decodes
	// with encoding/json (last-wins); the canonical form must see the same.
	tampered := []byte(`{"member_limit":999,"member_limit":10}`)

	var decoded map[string]int
	if err := json.Unmarshal(tampered, &decoded); err != nil {
		t.Fatal(err)
	}
	var canonical any
	if err := json.Unmarshal(canonicalJSON(tampered), &canonical); err != nil {
		t.Fatal(err)
	}
	view := canonical.(map[string]any)["member_limit"].(float64)
	if int(view) != decoded["member_limit"] {
		t.Fatalf("canonical sees %v but a decoder sees %d; a duplicate key smuggles a value past the signature", view, decoded["member_limit"])
	}
}
