package eventauth

import (
	"crypto/ecdsa"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func newSigner(t *testing.T, service string, now time.Time) (*Signer, *ecdsa.PublicKey) {
	t.Helper()
	signer, key, err := NewLocalSigner(service, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	return signer, key
}

const (
	goodSource   = "oryxa.auth"
	goodDetail   = "auth.organization.deleted.v1"
	goodID       = "evt_1"
	goodVersion  = 1
	goodOccurred = "2026-09-21T00:00:00Z"
	goodData     = `{"organization_id":"org_1"}`
)

func signed(t *testing.T, signer *Signer) string {
	t.Helper()
	sig, err := signer.Sign(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData))
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func TestASignedEventVerifies(t *testing.T) {
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return now })
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), signed(t, signer)); err != nil {
		t.Fatalf("a signed event did not verify: %v", err)
	}
}

func TestASignatureCannotBeMovedBetweenServices(t *testing.T) {
	now := time.Now()
	authSigner, authKey := newSigner(t, "auth", now)
	teamsSigner, teamsKey := newSigner(t, "teams", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": authKey, "teams": teamsKey}, nil, func() time.Time { return now })
	sig := signed(t, authSigner)

	if err := verifier.Verify("oryxa.teams", "teams.team.deleted.v1", goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatalf("auth's signature verified for teams: %v", err)
	}
	// And the same content signed by the service it claims.
	teamsSig, err := teamsSigner.Sign("oryxa.teams", "teams.team.deleted.v1", goodID, goodVersion, goodOccurred, []byte(goodData))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.teams", "teams.team.deleted.v1", goodID, goodVersion, goodOccurred, []byte(goodData), teamsSig); err != nil {
		t.Fatalf("teams' own signature did not verify: %v", err)
	}
}

func TestEveryTamperedFormIsRefused(t *testing.T) {
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return now })
	sig := signed(t, signer)

	checks := map[string]struct {
		source, detail, id string
		version            int
		occurred           string
		data               string
		sig                string
	}{
		"no signature":          {goodSource, goodDetail, goodID, goodVersion, goodOccurred, goodData, ""},
		"other version":         {goodSource, goodDetail, goodID, goodVersion, goodOccurred, goodData, "v2" + sig[2:]},
		"another detail type":   {goodSource, "auth.organization.created.v1", goodID, goodVersion, goodOccurred, goodData, sig},
		"another event id":      {goodSource, goodDetail, "evt_2", goodVersion, goodOccurred, goodData, sig},
		"another payload":       {goodSource, goodDetail, goodID, goodVersion, goodOccurred, `{"organization_id":"org_attacker"}`, sig},
		"another occurred time": {goodSource, goodDetail, goodID, goodVersion, "2026-09-20T00:00:00Z", goodData, sig},
		"garbage signature":     {goodSource, goodDetail, goodID, goodVersion, goodOccurred, goodData, "v1.0.bbb"},
		"non-oryxa source":      {"evil.example", goodDetail, goodID, goodVersion, goodOccurred, goodData, sig},
		"unknown service":       {"oryxa.unknown", goodDetail, goodID, goodVersion, goodOccurred, goodData, sig},
	}
	for name, check := range checks {
		t.Run(name, func(t *testing.T) {
			err := verifier.Verify(check.source, check.detail, check.id, check.version, check.occurred, []byte(check.data), check.sig)
			if !errors.Is(err, ErrUnverified) {
				t.Fatalf("tampered form verified: %v", err)
			}
		})
	}
}

func TestAStaleSignatureIsRefused(t *testing.T) {
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	clock := now
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return clock })
	sig := signed(t, signer)

	clock = now.Add(maxAge + time.Minute)
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a stale signature verified: %v", err)
	}
}

func TestASignatureFromTheFutureIsRefused(t *testing.T) {
	now := time.Now()
	_, key := newSigner(t, "auth", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return now })
	future, _, err := NewLocalSigner("auth", func() time.Time { return now.Add(maxAge + time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	sig, err := future.Sign(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a signature from the future verified: %v", err)
	}
}

// A consumer whose events can wait longer than a day, and that is harmless to
// act on late, may accept older signatures. Every verifier not given MaxAge
// keeps the day.
func TestMaxAgeLengthensHowOldASignatureMayBe(t *testing.T) {
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	sig := signed(t, signer)
	clock := now
	keys := map[string]*ecdsa.PublicKey{"auth": key}
	longer := NewVerifier(keys, nil, func() time.Time { return clock }, MaxAge(21*24*time.Hour))
	usual := NewVerifier(keys, nil, func() time.Time { return clock })
	verify := func(verifier *Verifier) error {
		return verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig)
	}

	clock = now.Add(maxAge - time.Minute)
	if err := verify(usual); err != nil {
		t.Fatalf("by default, a signature within a day did not verify: %v", err)
	}
	clock = now.Add(10 * 24 * time.Hour)
	if err := verify(longer); err != nil {
		t.Fatalf("a ten-day-old signature under a 21-day bound did not verify: %v", err)
	}
	if err := verify(usual); !errors.Is(err, ErrUnverified) {
		t.Fatalf("by default, a ten-day-old signature verified: %v", err)
	}
	clock = now.Add(21*24*time.Hour + time.Minute)
	if err := verify(longer); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a signature older than the longer bound verified: %v", err)
	}
	// Nothing but a positive age changes the bound.
	clock = now.Add(maxAge + time.Minute)
	for _, age := range []time.Duration{0, -time.Hour} {
		if err := verify(NewVerifier(keys, nil, func() time.Time { return clock }, MaxAge(age))); !errors.Is(err, ErrUnverified) {
			t.Fatalf("MaxAge(%s) lifted the default bound: %v", age, err)
		}
	}
}

func TestARegistryVerifierTakesMaxAge(t *testing.T) {
	now := time.Now()
	clock := now
	registry := NewRegistry(nil, func() time.Time { return clock })
	signer, err := registry.Signer("auth")
	if err != nil {
		t.Fatal(err)
	}
	sig := signed(t, signer)
	clock = now.Add(10 * 24 * time.Hour)
	if err := registry.Verifier(MaxAge(21*24*time.Hour)).Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); err != nil {
		t.Fatalf("a registry verifier given MaxAge refused a ten-day-old signature: %v", err)
	}
	if err := registry.Verifier().Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a registry verifier without it accepted one: %v", err)
	}
}

// A longer bound is for events that waited, not for ones signed ahead of
// time: the future stays held to the default day.
func TestMaxAgeStillRefusesASignatureFromTheFuture(t *testing.T) {
	now := time.Now()
	future, key, err := NewLocalSigner("auth", func() time.Time { return now.Add(maxAge + time.Hour) })
	if err != nil {
		t.Fatal(err)
	}
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return now }, MaxAge(21*24*time.Hour))
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), signed(t, future)); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a signature from beyond the default day ahead verified: %v", err)
	}
}

func TestAKeyOfAnotherServiceIsNotATrustedKey(t *testing.T) {
	// The verifier holds the real auth key; an attacker holds a different
	// one and signs as auth. The signature must not verify, because the
	// key it was made with is not the key the source resolves to.
	now := time.Now()
	_, authKey := newSigner(t, "auth", now)
	attacker, _ := newSigner(t, "attacker", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": authKey}, nil, func() time.Time { return now })
	sig, err := attacker.Sign(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatal("a signature made with a key the verifier does not hold for that service verified")
	}
}

func TestExternalSourcesAreExempt(t *testing.T) {
	now := time.Now()
	verifier := NewVerifier(nil, nil, func() time.Time { return now })
	if err := verifier.Verify("aws.ses", "delivery", "evt_ses", 1, goodOccurred, []byte("{}"), ""); err != nil {
		t.Fatalf("SES's own feedback was refused: %v", err)
	}
	if !IsExternal("aws.ses") || IsExternal("oryxa.auth") {
		t.Fatal("the external-source list is wrong")
	}
}

func TestRegistrySignsAndVerifiesAcrossServices(t *testing.T) {
	now := time.Now()
	registry := NewRegistry(nil, func() time.Time { return now })
	authSigner, err := registry.Signer("auth")
	if err != nil {
		t.Fatal(err)
	}
	teamsSigner, err := registry.Signer("teams")
	if err != nil {
		t.Fatal(err)
	}
	if authSigner == teamsSigner {
		t.Fatal("each service must have its own signer")
	}
	verifier := registry.Verifier()
	sig, err := authSigner.Sign("oryxa.auth", "auth.member.removed.v1", "evt_9", 1, goodOccurred, []byte(`{"user_id":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.auth", "auth.member.removed.v1", "evt_9", 1, goodOccurred, []byte(`{"user_id":"u"}`), sig); err != nil {
		t.Fatalf("registry event did not verify: %v", err)
	}
	// And a consumer that only knows the registry cannot be fooled by a
	// service that never signed, even with a real signature from another.
	teamsSig, err := teamsSigner.Sign("oryxa.auth", "auth.member.removed.v1", "evt_9", 1, goodOccurred, []byte(`{"user_id":"u"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.Verify("oryxa.auth", "auth.member.removed.v1", "evt_9", 1, goodOccurred, []byte(`{"user_id":"u"}`), teamsSig); !errors.Is(err, ErrUnverified) {
		t.Fatal("teams' signature verified for auth's source")
	}
}

// Services mount one after another, each taking a verifier as it goes, and
// auth — mounted first — hears billing, mounted third. A verifier that knew
// only the keys minted before it would refuse billing for as long as the
// process ran.
func TestARegistryVerifierTrustsServicesMountedAfterIt(t *testing.T) {
	now := time.Now()
	registry := NewRegistry(nil, func() time.Time { return now })
	if _, err := registry.Signer("auth"); err != nil {
		t.Fatal(err)
	}
	authConsumer := registry.Verifier()
	billingSigner, err := registry.Signer("billing")
	if err != nil {
		t.Fatal(err)
	}
	const source, detailType = "oryxa.billing", "billing.entitlements.changed.v1"
	sig, err := billingSigner.Sign(source, detailType, goodID, goodVersion, goodOccurred, []byte(goodData))
	if err != nil {
		t.Fatal(err)
	}
	if err := authConsumer.Verify(source, detailType, goodID, goodVersion, goodOccurred, []byte(goodData), sig); err != nil {
		t.Fatalf("a verifier taken before billing mounted refused billing's event: %v", err)
	}
}

func TestAReportOnlyRegistryLetsAFailureThroughAndSaysWhy(t *testing.T) {
	now := time.Now()
	registry := NewRegistry(nil, func() time.Time { return now })
	signer, err := registry.Signer("auth")
	if err != nil {
		t.Fatal(err)
	}
	// Taken while the registry still enforced: it follows the registry.
	verifier := registry.Verifier()
	unsigned := func() error {
		return verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), "")
	}
	if err := unsigned(); !errors.Is(err, ErrUnverified) {
		t.Fatalf("an enforcing registry's verifier let an unsigned event through: %v", err)
	}

	var reported []string
	registry.ReportOnly(func(source, detailType, eventID string, err error) {
		if !errors.Is(err, ErrUnverified) {
			t.Errorf("reported %v, want ErrUnverified", err)
		}
		reported = append(reported, source+" "+detailType+" "+eventID)
	})
	if err := unsigned(); err != nil {
		t.Fatalf("a report-only verifier refused an unsigned event: %v", err)
	}
	if want := []string{goodSource + " " + goodDetail + " " + goodID}; !slices.Equal(reported, want) {
		t.Fatalf("reported %v, want %v", reported, want)
	}
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), signed(t, signer)); err != nil || len(reported) != 1 {
		t.Fatalf("a good signature: err = %v, reported %v", err, reported)
	}

	// Without a registry there is nothing to report to, so nothing is let
	// through: a consumer built without identities still fails closed.
	var none *Registry
	none.ReportOnly(func(string, string, string, error) {})
	if err := none.Verifier().Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), ""); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a nil registry's verifier let an unsigned event through: %v", err)
	}
}

func TestCanonicalSeparatesEveryField(t *testing.T) {
	// Two events that differ in exactly one place must canonicalize
	// differently, whatever the differing field is.
	base := canonical("oryxa.a", "x.v1", "e1", 1, "t1", 100, []byte("d"))
	variants := [][]byte{
		canonical("oryxa.b", "x.v1", "e1", 1, "t1", 100, []byte("d")),
		canonical("oryxa.a", "y.v1", "e1", 1, "t1", 100, []byte("d")),
		canonical("oryxa.a", "x.v1", "e2", 1, "t1", 100, []byte("d")),
		canonical("oryxa.a", "x.v1", "e1", 2, "t1", 100, []byte("d")),
		canonical("oryxa.a", "x.v1", "e1", 1, "t2", 100, []byte("d")),
		canonical("oryxa.a", "x.v1", "e1", 1, "t1", 101, []byte("d")),
		canonical("oryxa.a", "x.v1", "e1", 1, "t1", 100, []byte("e")),
	}
	for _, variant := range variants {
		if string(base) == string(variant) {
			t.Fatal("two different events canonicalized the same")
		}
	}
	// And a crafted value cannot swallow a separator: the fields are
	// length-delimited by NUL, which no JSON string can contain raw.
	crafted := canonical("oryxa.a\x00x.v1", "", "", 0, "", 0, nil)
	if string(crafted) == string(canonical("oryxa.a", "x.v1", "", 0, "", 0, nil)) {
		t.Fatal("a NUL in a source crossed a field boundary")
	}
}

// TestTheSigningTimeCannotBeRewritten is the attack the age bound exists to
// stop. A captured envelope goes stale on its own, but if the timestamp a
// verifier ages it by were left outside the signature, an attacker could edit
// that one field — touching nothing the signature covered — and hand the same
// event back fresh, for ever. Signing over it makes the edit a mismatch.
func TestTheSigningTimeCannotBeRewritten(t *testing.T) {
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	clock := now
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return clock })
	sig := signed(t, signer)

	// Two days on, the captured event is stale, as it should be.
	clock = now.Add(2 * maxAge)
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), sig); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a captured event did not go stale: %v", err)
	}

	// The attacker rewrites only the timestamp, leaving the signature bytes
	// and every other field exactly as the publisher made them.
	parts := strings.SplitN(sig, ".", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected signature form %q", sig)
	}
	refreshed := fmt.Sprintf("%s.%d.%s", parts[0], clock.Unix(), parts[2])
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), refreshed); !errors.Is(err, ErrUnverified) {
		t.Fatalf("rewriting the signing time revived a stale event: %v", err)
	}
}

func TestANonOryxaKeyCannotSign(t *testing.T) {
	// A corrupted base64 signature is simply not the verifier's key's
	// signature; the verifier answers ErrUnverified, never an error that
	// says anything else about the event.
	now := time.Now()
	signer, key := newSigner(t, "auth", now)
	verifier := NewVerifier(map[string]*ecdsa.PublicKey{"auth": key}, nil, func() time.Time { return now })
	sig := signed(t, signer)
	flipped := []byte(sig)
	// Flip one character deep inside the base64 payload.
	for i := len(flipped) - 3; i > len(flipped)-40; i-- {
		if flipped[i] != '.' && flipped[i] != 'A' {
			flipped[i] = 'A'
			break
		}
	}
	if err := verifier.Verify(goodSource, goodDetail, goodID, goodVersion, goodOccurred, []byte(goodData), string(flipped)); !errors.Is(err, ErrUnverified) {
		t.Fatalf("a corrupted signature verified: %v", err)
	}
}
