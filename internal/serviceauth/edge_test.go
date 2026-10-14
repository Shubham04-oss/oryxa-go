package serviceauth

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"flag"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

var updateEdgeFixture = flag.Bool("update", false, "rewrite contracts/edge/v1/testdata/tokens/edge_tokens.json")

const edgeTokenFixture = "../../contracts/edge/v1/testdata/tokens/edge_tokens.json"

func newEdgeSigner(t *testing.T, at time.Time) (Signer, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Signer{Client: &fakeSignAPI{key: key}, KeyID: "edge-test", Service: "agents", Now: func() time.Time { return at }}, key
}

func edgeClaims() edgev1.TokenClaims {
	return edgev1.TokenClaims{
		Org: "org_1", Room: edgev1.Room{Kind: edgev1.RoomThread, ID: "thr_1"},
		Member: "usr_1", Agent: "agt_1", Run: "run_1",
		Scopes: []edgev1.Scope{edgev1.ScopeSandboxExec, edgev1.ScopeSandboxFiles, edgev1.ScopeLogWrite},
	}
}

// decodeEdgeToken checks token's ES256 signature against key and returns its
// claims, the way the edge does.
func decodeEdgeToken(t *testing.T, token string, key *ecdsa.PublicKey) (edgev1.TokenClaims, bool) {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatalf("token has %d parts", len(parts))
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || len(signature) != 64 {
		return edgev1.TokenClaims{}, false
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	valid := ecdsa.Verify(key, digest[:], new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:]))
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Fatal(err)
	}
	var claims edgev1.TokenClaims
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&claims); err != nil {
		t.Fatalf("decode claims: %v", err)
	}
	return claims, valid
}

func TestEdgeTokenCarriesTheContractClaims(t *testing.T) {
	at := time.Date(2026, time.October, 14, 10, 0, 0, 500, time.UTC)
	signer, key := newEdgeSigner(t, at)
	token, err := signer.EdgeToken(context.Background(), edgeClaims())
	if err != nil {
		t.Fatal(err)
	}
	claims, valid := decodeEdgeToken(t, token, &key.PublicKey)
	if !valid {
		t.Fatal("signature does not verify")
	}
	if claims.Issuer != "agents" || claims.Audience != "edge" {
		t.Errorf("iss/aud = %q/%q", claims.Issuer, claims.Audience)
	}
	if claims.IssuedAt != at.Unix() || claims.Expiry-claims.IssuedAt != int64(EdgeTokenLifetime/time.Second) {
		t.Errorf("iat %d exp %d", claims.IssuedAt, claims.Expiry)
	}
	if time.Duration(claims.Expiry-claims.IssuedAt)*time.Second > edgev1.MaxTokenLifetime {
		t.Error("lifetime exceeds the contract's maximum")
	}
	if claims.Org != "org_1" || claims.Room.ID != "thr_1" || claims.Member != "usr_1" || claims.Run != "run_1" || len(claims.Scopes) != 3 {
		t.Errorf("claims = %+v", claims)
	}
	again, err := signer.EdgeToken(context.Background(), edgeClaims())
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := decodeEdgeToken(t, again, &key.PublicKey); second.ID == claims.ID || claims.ID == "" {
		t.Error("each token needs its own jti")
	}
}

func TestEdgeTokenIgnoresCallerTimingAndAudience(t *testing.T) {
	at := time.Date(2026, time.October, 14, 10, 0, 0, 0, time.UTC)
	signer, key := newEdgeSigner(t, at)
	claims := edgeClaims()
	claims.Audience, claims.Issuer, claims.Expiry, claims.ID = "agents", "edge", at.Add(24*time.Hour).Unix(), "fixed"
	token, err := signer.EdgeToken(context.Background(), claims)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := decodeEdgeToken(t, token, &key.PublicKey)
	if got.Audience != "edge" || got.Issuer != "agents" || got.Expiry != at.Add(EdgeTokenLifetime).Unix() || got.ID == "fixed" {
		t.Errorf("caller-supplied timing or identity leaked through: %+v", got)
	}
}

func TestEdgeTokenRefusesWhatTheEdgeWould(t *testing.T) {
	signer, _ := newEdgeSigner(t, time.Now())
	for name, mutate := range map[string]func(*edgev1.TokenClaims){
		"no org":        func(c *edgev1.TokenClaims) { c.Org = "" },
		"org with path": func(c *edgev1.TokenClaims) { c.Org = "org/2" },
		"no room":       func(c *edgev1.TokenClaims) { c.Room = edgev1.Room{} },
		"bad kind":      func(c *edgev1.TokenClaims) { c.Room.Kind = "lobby" },
		"no scopes":     func(c *edgev1.TokenClaims) { c.Scopes = nil },
		"unknown scope": func(c *edgev1.TokenClaims) { c.Scopes = []edgev1.Scope{"sandbox:root"} },
	} {
		claims := edgeClaims()
		mutate(&claims)
		if _, err := signer.EdgeToken(context.Background(), claims); err == nil {
			t.Errorf("%s: signed", name)
		}
	}
	if _, err := (Signer{Service: "agents"}).EdgeToken(context.Background(), edgeClaims()); err == nil {
		t.Error("signed without a KMS key")
	}
}

// edgeTokenCase is one token in the shared fixture, with when the edge
// checks it and what it must answer.
type edgeTokenCase struct {
	Name  string `json:"name"`
	Token string `json:"token"`
	Now   int64  `json:"now"`
	// Want is "ok" or the reason the edge refuses it.
	Want string `json:"want"`
}

type edgeTokenFixtureFile struct {
	Comment   string          `json:"_comment"`
	PublicKey string          `json:"publicKey"`
	Cases     []edgeTokenCase `json:"cases"`
}

// TestEdgeTokenFixture keeps contracts/edge/v1/testdata/tokens current: real
// tokens this package signed, which the edge's verifier test
// (edge/src/auth/token.test.ts) must accept or refuse as each case says.
// Run with -update to sign a fresh set.
func TestEdgeTokenFixture(t *testing.T) {
	if *updateEdgeFixture {
		writeEdgeTokenFixture(t)
	}
	raw, err := os.ReadFile(edgeTokenFixture)
	if err != nil {
		t.Fatalf("%v (go test ./internal/serviceauth -run TestEdgeTokenFixture -update writes it)", err)
	}
	var fixture edgeTokenFixtureFile
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	der, err := base64.StdEncoding.DecodeString(fixture.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		t.Fatal(err)
	}
	key := parsed.(*ecdsa.PublicKey)
	for _, test := range fixture.Cases {
		if test.Want != "ok" {
			continue
		}
		claims, valid := decodeEdgeToken(t, test.Token, key)
		if !valid || test.Now < claims.IssuedAt || test.Now >= claims.Expiry {
			t.Errorf("%s: the fixture's valid token is not valid at its own time", test.Name)
		}
	}
}

func writeEdgeTokenFixture(t *testing.T) {
	t.Helper()
	at := time.Date(2026, time.October, 14, 10, 0, 0, 0, time.UTC)
	signer, key := newEdgeSigner(t, at)
	other, _ := newEdgeSigner(t, at)
	ctx := context.Background()

	sign := func(s Signer, claims edgev1.TokenClaims) string {
		encoded, err := json.Marshal(claims)
		if err != nil {
			t.Fatal(err)
		}
		token, err := s.sign(ctx, encoded)
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	valid, err := signer.EdgeToken(ctx, edgeClaims())
	if err != nil {
		t.Fatal(err)
	}
	base := func() edgev1.TokenClaims {
		claims := edgeClaims()
		claims.Issuer, claims.Audience, claims.ID = "agents", "edge", "jti-fixture"
		claims.IssuedAt, claims.Expiry = at.Unix(), at.Add(EdgeTokenLifetime).Unix()
		return claims
	}
	wrongAudience := base()
	wrongAudience.Audience = "agents"
	wrongIssuer := base()
	wrongIssuer.Issuer = "worker"
	tooLong := base()
	tooLong.Expiry = at.Add(10 * time.Minute).Unix()
	privateBrowser := base()
	privateBrowser.Private = true
	privateBrowser.Scopes = []edgev1.Scope{edgev1.ScopeBrowserDrive}

	parts := strings.Split(valid, ".")
	tamperedClaims := base()
	tamperedClaims.Org = "org_2"
	tamperedPayload, _ := json.Marshal(tamperedClaims)
	tampered := parts[0] + "." + base64.RawURLEncoding.EncodeToString(tamperedPayload) + "." + parts[2]
	noneHeader := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	algNone := noneHeader + "." + parts[1] + "."

	now := at.Unix() + 5
	cases := []edgeTokenCase{
		{Name: "valid", Token: valid, Now: now, Want: "ok"},
		{Name: "expired", Token: valid, Now: at.Add(EdgeTokenLifetime).Unix() + 31, Want: "expired"},
		{Name: "issued in the future", Token: valid, Now: at.Unix() - 120, Want: "not_yet_valid"},
		{Name: "wrong audience", Token: sign(signer, wrongAudience), Now: now, Want: "wrong_audience"},
		{Name: "wrong issuer", Token: sign(signer, wrongIssuer), Now: now, Want: "wrong_issuer"},
		{Name: "lifetime over five minutes", Token: sign(signer, tooLong), Now: now, Want: "invalid_lifetime"},
		{Name: "tampered claims", Token: tampered, Now: now, Want: "bad_signature"},
		{Name: "signed by another key", Token: sign(other, base()), Now: now, Want: "bad_signature"},
		{Name: "alg none", Token: algNone, Now: now, Want: "malformed"},
		{Name: "not a jwt", Token: "abc.def", Now: now, Want: "malformed"},
		{Name: "private for a browser", Token: sign(signer, privateBrowser), Now: now, Want: "ok"},
	}

	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.MarshalIndent(edgeTokenFixtureFile{
		Comment:   "Generated by go test ./internal/serviceauth -run TestEdgeTokenFixture -update. publicKey is DER SPKI, base64.",
		PublicKey: base64.StdEncoding.EncodeToString(der),
		Cases:     cases,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(edgeTokenFixture), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(edgeTokenFixture, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}
