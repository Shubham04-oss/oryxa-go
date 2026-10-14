package edgedispatch

import (
	"crypto/ecdsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"sync"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// The edge→agents token (contract §4) is the edge token with iss and aud
// swapped, signed by the edge's own ES256 key, whose private half is a
// Worker secret. Agents hold the public half(s) in the same form the edge
// holds agents' key: a JSON array of base64 DER SPKI, more than one while
// the key rotates. This file is edge/src/auth/token.ts's twin.

// clockSkew tolerates the edge's and agents' clocks disagreeing, as the
// edge's own verifier does.
const clockSkew = 30 * time.Second

// maxTokenBytes bounds what Verify parses.
const maxTokenBytes = 4096

// maxSeen bounds the replay cache. Tokens live at most five minutes, and a
// dispatch is one spoken request, so this is far more than one process sees.
const maxSeen = 100_000

// ErrUnauthorized means the token is not a valid, unused edge→agents token.
var ErrUnauthorized = errors.New("invalid edge token")

// ParsePublicKeys reads a JSON array of base64 DER SPKI P-256 keys. An empty
// value is no keys.
func ParsePublicKeys(raw string) ([]*ecdsa.PublicKey, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var encoded []string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("edge public keys must be a JSON array of base64 strings: %w", err)
	}
	keys := make([]*ecdsa.PublicKey, 0, len(encoded))
	for i, value := range encoded {
		der, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("edge public key %d: %w", i, err)
		}
		parsed, err := x509.ParsePKIXPublicKey(der)
		if err != nil {
			return nil, fmt.Errorf("edge public key %d: %w", i, err)
		}
		key, ok := parsed.(*ecdsa.PublicKey)
		if !ok || key.Curve.Params().Name != "P-256" {
			return nil, fmt.Errorf("edge public key %d is not a P-256 key", i)
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// Verifier checks edge→agents tokens, and refuses one it has seen.
type Verifier struct {
	keys []*ecdsa.PublicKey
	now  func() time.Time

	mu   sync.Mutex
	seen map[string]int64
}

// NewVerifier returns a Verifier for keys. now defaults to the clock.
func NewVerifier(keys []*ecdsa.PublicKey, now func() time.Time) (*Verifier, error) {
	if len(keys) == 0 {
		return nil, errors.New("edgedispatch: no edge public keys")
	}
	if now == nil {
		now = time.Now
	}
	return &Verifier{keys: keys, now: now, seen: map[string]int64{}}, nil
}

// Verify returns a valid token's claims and spends its jti.
func (verifier *Verifier) Verify(token string) (edgev1.TokenClaims, error) {
	refuse := func(reason string) (edgev1.TokenClaims, error) {
		return edgev1.TokenClaims{}, fmt.Errorf("%w: %s", ErrUnauthorized, reason)
	}
	if len(token) > maxTokenBytes {
		return refuse("too large")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return refuse("malformed")
	}
	headerBytes, err1 := base64.RawURLEncoding.DecodeString(parts[0])
	claimsBytes, err2 := base64.RawURLEncoding.DecodeString(parts[1])
	signature, err3 := base64.RawURLEncoding.DecodeString(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return refuse("malformed")
	}
	var header struct {
		Alg  string          `json:"alg"`
		Crit json.RawMessage `json:"crit"`
	}
	if json.Unmarshal(headerBytes, &header) != nil || header.Alg != "ES256" || header.Crit != nil || len(signature) != 64 {
		return refuse("malformed")
	}

	// The signature is checked before any claim is believed.
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r, s := new(big.Int).SetBytes(signature[:32]), new(big.Int).SetBytes(signature[32:])
	verified := false
	for _, key := range verifier.keys {
		if ecdsa.Verify(key, digest[:], r, s) {
			verified = true
			break
		}
	}
	if !verified {
		return refuse("bad signature")
	}

	var claims edgev1.TokenClaims
	if json.Unmarshal(claimsBytes, &claims) != nil {
		return refuse("malformed claims")
	}
	if claims.Issuer != edgev1.IssuerEdge {
		return refuse("wrong issuer")
	}
	if claims.Audience != edgev1.AudienceAgents {
		return refuse("wrong audience")
	}
	lifetime := time.Duration(claims.Expiry-claims.IssuedAt) * time.Second
	if lifetime <= 0 || lifetime > edgev1.MaxTokenLifetime {
		return refuse("invalid lifetime")
	}
	now := verifier.now()
	if time.Unix(claims.IssuedAt, 0).After(now.Add(clockSkew)) {
		return refuse("not yet valid")
	}
	if !time.Unix(claims.Expiry, 0).After(now.Add(-clockSkew)) {
		return refuse("expired")
	}
	if !edgev1.ValidID(claims.Org) || claims.Room.Validate() != nil || !edgev1.ValidID(claims.Member) || claims.ID == "" || len(claims.ID) > 128 {
		return refuse("incomplete claims")
	}
	if !verifier.spend(claims.ID, claims.Expiry, now) {
		return refuse("token already used")
	}
	return claims, nil
}

// spend records jti until its token could no longer pass, and reports
// whether it was unused.
func (verifier *Verifier) spend(jti string, expiry int64, now time.Time) bool {
	verifier.mu.Lock()
	defer verifier.mu.Unlock()
	if _, used := verifier.seen[jti]; used {
		return false
	}
	if len(verifier.seen) >= maxSeen {
		cutoff := now.Add(-clockSkew).Unix()
		for id, exp := range verifier.seen {
			if exp < cutoff {
				delete(verifier.seen, id)
			}
		}
		if len(verifier.seen) >= maxSeen {
			// Full of live tokens: refusing is safer than forgetting one.
			return false
		}
	}
	verifier.seen[jti] = expiry
	return true
}
