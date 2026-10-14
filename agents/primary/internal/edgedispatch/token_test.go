package edgedispatch

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

var testNow = time.Unix(1_800_000_000, 0)

func newKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func sign(t *testing.T, key *ecdsa.PrivateKey, header string, claims any) string {
	t.Helper()
	payload, _ := json.Marshal(claims)
	signingInput := base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString(payload)
	digest := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, key, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature)
}

func edgeClaims() edgev1.TokenClaims {
	return edgev1.TokenClaims{
		Issuer: edgev1.IssuerEdge, Audience: edgev1.AudienceAgents, Org: "org_1",
		Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "thr_1"}, Member: "usr_1",
		IssuedAt: testNow.Unix(), Expiry: testNow.Add(time.Minute).Unix(), ID: "jti_1",
	}
}

const es256 = `{"alg":"ES256","typ":"JWT"}`

func spki(t *testing.T, keys ...*ecdsa.PrivateKey) string {
	t.Helper()
	encoded := make([]string, 0, len(keys))
	for _, key := range keys {
		der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		encoded = append(encoded, base64.StdEncoding.EncodeToString(der))
	}
	raw, _ := json.Marshal(encoded)
	return string(raw)
}

func newVerifier(t *testing.T, keys ...*ecdsa.PrivateKey) *Verifier {
	t.Helper()
	parsed, err := ParsePublicKeys(spki(t, keys...))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := NewVerifier(parsed, func() time.Time { return testNow })
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func TestVerifyAcceptsAnEdgeTokenOnceUnderEitherRotatingKey(t *testing.T) {
	old, current := newKey(t), newKey(t)
	verifier := newVerifier(t, old, current)
	claims, err := verifier.Verify(sign(t, current, es256, edgeClaims()))
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if claims.Org != "org_1" || claims.Member != "usr_1" || claims.Room.ID != "thr_1" {
		t.Fatalf("claims = %+v", claims)
	}
	if _, err := verifier.Verify(sign(t, current, es256, edgeClaims())); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("a replayed jti = %v", err)
	}
	second := edgeClaims()
	second.ID = "jti_2"
	if _, err := verifier.Verify(sign(t, old, es256, second)); err != nil {
		t.Fatalf("the rotating key's token: %v", err)
	}
}

func TestVerifyRefuses(t *testing.T) {
	key, other := newKey(t), newKey(t)
	cases := map[string]func() string{
		"other key":   func() string { return sign(t, other, es256, edgeClaims()) },
		"alg none":    func() string { return sign(t, key, `{"alg":"none"}`, edgeClaims()) },
		"crit header": func() string { return sign(t, key, `{"alg":"ES256","crit":["x"]}`, edgeClaims()) },
		"agents' own token": func() string {
			c := edgeClaims()
			c.Issuer, c.Audience = edgev1.IssuerAgents, edgev1.AudienceEdge
			return sign(t, key, es256, c)
		},
		"wrong audience": func() string { c := edgeClaims(); c.Audience = "knowledge"; return sign(t, key, es256, c) },
		"expired": func() string {
			c := edgeClaims()
			c.IssuedAt, c.Expiry = testNow.Add(-3*time.Minute).Unix(), testNow.Add(-time.Minute).Unix()
			return sign(t, key, es256, c)
		},
		"future": func() string {
			c := edgeClaims()
			c.IssuedAt, c.Expiry = testNow.Add(2*time.Minute).Unix(), testNow.Add(3*time.Minute).Unix()
			return sign(t, key, es256, c)
		},
		"too long": func() string {
			c := edgeClaims()
			c.Expiry = testNow.Add(6 * time.Minute).Unix()
			return sign(t, key, es256, c)
		},
		"no member": func() string { c := edgeClaims(); c.Member = ""; return sign(t, key, es256, c) },
		"bad room":  func() string { c := edgeClaims(); c.Room.Kind = "channel"; return sign(t, key, es256, c) },
		"no jti":    func() string { c := edgeClaims(); c.ID = ""; return sign(t, key, es256, c) },
		"tampered": func() string {
			token := sign(t, key, es256, edgeClaims())
			parts := strings.Split(token, ".")
			c := edgeClaims()
			c.Member = "usr_2"
			payload, _ := json.Marshal(c)
			return parts[0] + "." + base64.RawURLEncoding.EncodeToString(payload) + "." + parts[2]
		},
		"two parts": func() string { return "a.b" },
		"huge":      func() string { return strings.Repeat("a", maxTokenBytes+1) },
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := newVerifier(t, key).Verify(token()); !errors.Is(err, ErrUnauthorized) {
				t.Fatalf("Verify = %v, want ErrUnauthorized", err)
			}
		})
	}
}

func TestReplayCacheForgetsOnlyExpiredTokens(t *testing.T) {
	verifier := newVerifier(t, newKey(t))
	for i := range maxSeen - 1 {
		verifier.seen[fmt.Sprintf("old_%d", i)] = testNow.Add(-time.Hour).Unix()
	}
	verifier.seen["live"] = testNow.Add(time.Minute).Unix()
	if !verifier.spend("new", testNow.Add(time.Minute).Unix(), testNow) {
		t.Fatal("a full cache of expired tokens refused a new one")
	}
	if verifier.spend("live", testNow.Add(time.Minute).Unix(), testNow) {
		t.Fatal("pruning forgot a live token")
	}
}

func TestParsePublicKeys(t *testing.T) {
	if keys, err := ParsePublicKeys(" "); err != nil || keys != nil {
		t.Fatalf("empty = %v, %v", keys, err)
	}
	for _, raw := range []string{`"abc"`, `["not base64!"]`, `["aGVsbG8="]`} {
		if _, err := ParsePublicKeys(raw); err == nil {
			t.Errorf("ParsePublicKeys(%s) accepted it", raw)
		}
	}
	if _, err := NewVerifier(nil, nil); err == nil {
		t.Error("a verifier with no keys was made")
	}
}
