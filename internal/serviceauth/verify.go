package serviceauth

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
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

	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/Shubham04-oss/oryxa-go/internal/kmscall"
)

// clockSkewLeeway tolerates modest disagreement between the signer's and the
// verifier's clocks.
const clockSkewLeeway = 30 * time.Second

// maxTokenBytes bounds how much of a request Verify parses before rejecting
// it as too large to be a token.
const maxTokenBytes = 4096

// PublicKeyAPI is the KMS surface a Verifier needs.
type PublicKeyAPI interface {
	GetPublicKey(context.Context, *kms.GetPublicKeyInput, ...func(*kms.Options)) (*kms.GetPublicKeyOutput, error)
}

type VerifierConfig struct {
	Keys     PublicKeyAPI
	Audience string
	Callers  []Caller
	Now      func() time.Time
}

// Verifier checks tokens for one audience.
type Verifier struct {
	keys     PublicKeyAPI
	audience string
	callers  map[string]Caller
	now      func() time.Time

	// publicKeys caches each caller's KMS public key so a hot path never
	// waits on KMS after its first token. Only successful fetches are
	// cached; a KMS outage must not wedge the cache with an error.
	publicKeysMu sync.RWMutex
	publicKeys   map[string]*ecdsa.PublicKey
}

func NewVerifier(config VerifierConfig) (*Verifier, error) {
	if config.Audience == "" {
		return nil, errors.New("verifier requires an audience")
	}

	callers := make(map[string]Caller, len(config.Callers))
	for _, caller := range config.Callers {
		if _, exists := callers[caller.Service]; exists {
			return nil, fmt.Errorf("duplicate caller service %q", caller.Service)
		}
		callers[caller.Service] = caller
	}
	if len(callers) > 0 && config.Keys == nil {
		return nil, errors.New("verifier requires a KMS client when callers are configured")
	}

	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Verifier{
		keys:       config.Keys,
		audience:   config.Audience,
		callers:    callers,
		now:        now,
		publicKeys: make(map[string]*ecdsa.PublicKey),
	}, nil
}

// Verify returns a valid token's claims.
func (verifier *Verifier) Verify(ctx context.Context, token string) (Claims, error) {
	// 1. Token shape: size, three non-empty base64url parts, and a header
	// naming an algorithm and extensions this package understands.
	if len(token) > maxTokenBytes {
		return Claims{}, fmt.Errorf("%w: token too large", ErrInvalidToken)
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return Claims{}, fmt.Errorf("%w: malformed token", ErrInvalidToken)
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: malformed header encoding", ErrInvalidToken)
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: malformed claims encoding", ErrInvalidToken)
	}
	signatureBytes, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, fmt.Errorf("%w: malformed signature encoding", ErrInvalidToken)
	}

	var header joseHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return Claims{}, fmt.Errorf("%w: malformed header", ErrInvalidToken)
	}
	if header.Alg != "ES256" {
		return Claims{}, fmt.Errorf("%w: unsupported algorithm", ErrInvalidToken)
	}
	if header.Crit != nil {
		return Claims{}, fmt.Errorf("%w: unsupported critical header", ErrInvalidToken)
	}

	// 2. Claims: a configured issuer, our audience as a plain string, and a
	// lifetime that fits inside MaxLifetime with a little clock-skew room.
	var wire wireClaimsIn
	if err := json.Unmarshal(claimsBytes, &wire); err != nil {
		return Claims{}, fmt.Errorf("%w: malformed claims", ErrInvalidToken)
	}
	caller, ok := verifier.callers[wire.Issuer]
	if !ok {
		return Claims{}, fmt.Errorf("%w: unknown caller", ErrInvalidToken)
	}
	var audience string
	if err := json.Unmarshal(wire.Audience, &audience); err != nil {
		return Claims{}, fmt.Errorf("%w: malformed audience", ErrInvalidToken)
	}
	if audience != verifier.audience {
		return Claims{}, fmt.Errorf("%w: wrong audience", ErrInvalidToken)
	}
	if wire.IssuedAt == nil || wire.Expiry == nil {
		return Claims{}, fmt.Errorf("%w: missing timestamps", ErrInvalidToken)
	}
	issuedAt := time.Unix(*wire.IssuedAt, 0)
	expiry := time.Unix(*wire.Expiry, 0)
	if lifetime := expiry.Sub(issuedAt); lifetime <= 0 || lifetime > MaxLifetime {
		return Claims{}, fmt.Errorf("%w: invalid lifetime", ErrInvalidToken)
	}
	now := verifier.now()
	if issuedAt.After(now.Add(clockSkewLeeway)) {
		return Claims{}, fmt.Errorf("%w: issued in the future", ErrInvalidToken)
	}
	if !expiry.After(now.Add(-clockSkewLeeway)) {
		return Claims{}, fmt.Errorf("%w: expired", ErrInvalidToken)
	}

	// 3. The caller's public key. A KMS problem here is the callee's or
	// KMS's fault, not the caller's, so it must not look like an
	// unauthorized token to callers of Verify.
	publicKey, err := verifier.publicKey(ctx, caller.KeyID)
	if err != nil {
		return Claims{}, fmt.Errorf("fetch public key: %w", err)
	}

	// 4. The signature itself.
	if len(signatureBytes) != 64 {
		return Claims{}, fmt.Errorf("%w: malformed signature", ErrInvalidToken)
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	r := new(big.Int).SetBytes(signatureBytes[:32])
	s := new(big.Int).SetBytes(signatureBytes[32:])
	if !ecdsa.Verify(publicKey, digest[:], r, s) {
		return Claims{}, fmt.Errorf("%w: signature mismatch", ErrInvalidToken)
	}

	// 5. Only now, with a genuine token in hand, does the caller's allowed
	// purpose list get consulted.
	allowed := false
	for _, purpose := range caller.Purposes {
		if purpose == wire.Purpose {
			allowed = true
			break
		}
	}
	if !allowed {
		return Claims{}, ErrPurposeNotAllowed
	}

	return Claims{
		Service:  wire.Issuer,
		Audience: audience,
		Purpose:  wire.Purpose,
		ID:       wire.ID,
		IssuedAt: issuedAt,
		Expiry:   expiry,
	}, nil
}

// publicKey returns the ECDSA P-256 public key for keyID, fetching it from
// KMS and caching it on the first call.
func (verifier *Verifier) publicKey(ctx context.Context, keyID string) (*ecdsa.PublicKey, error) {
	verifier.publicKeysMu.RLock()
	key, ok := verifier.publicKeys[keyID]
	verifier.publicKeysMu.RUnlock()
	if ok {
		return key, nil
	}

	verifier.publicKeysMu.Lock()
	defer verifier.publicKeysMu.Unlock()
	if key, ok := verifier.publicKeys[keyID]; ok {
		return key, nil
	}

	// The lock is held across this call, so a stuck one would hold up every
	// verification: it is bounded by kmscall.Timeout even when the caller's
	// own deadline is later.
	ctx, cancel := context.WithTimeout(ctx, kmscall.Timeout)
	defer cancel()
	output, err := verifier.keys.GetPublicKey(ctx, &kms.GetPublicKeyInput{KeyId: &keyID})
	if err != nil {
		return nil, err
	}
	parsed, err := x509.ParsePKIXPublicKey(output.PublicKey)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	ecdsaKey, ok := parsed.(*ecdsa.PublicKey)
	if !ok || ecdsaKey.Curve != elliptic.P256() {
		return nil, errors.New("public key is not P-256 ECDSA")
	}

	verifier.publicKeys[keyID] = ecdsaKey
	return ecdsaKey, nil
}
