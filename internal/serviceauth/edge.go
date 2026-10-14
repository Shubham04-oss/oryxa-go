package serviceauth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// EdgeTokenLifetime is how long an edge token lives. A token is issued for
// each call, and the edge refuses one it has already seen, so it only needs
// to outlive the request reaching the edge; contract §2 allows up to
// edgev1.MaxTokenLifetime.
const EdgeTokenLifetime = 60 * time.Second

// EdgeToken signs an edge token (docs/design/live-layer/01-contracts.md §2)
// for the room, member, agent, run and scopes claims names. The signer's
// service becomes the issuer, so agents' signer, Service "agents", issues
// what the edge expects; the audience, issue time, expiry and a fresh jti
// are filled in here, and whatever claims carried for them is ignored.
func (signer Signer) EdgeToken(ctx context.Context, claims edgev1.TokenClaims) (string, error) {
	if err := validateName(signer.Service); err != nil {
		return "", fmt.Errorf("signer service: %w", err)
	}
	if signer.Client == nil || signer.KeyID == "" {
		return "", errors.New("signer requires a KMS client and key ID")
	}
	if !edgev1.ValidID(claims.Org) {
		return "", errors.New("edge token: invalid organization id")
	}
	if err := claims.Room.Validate(); err != nil {
		return "", fmt.Errorf("edge token: %w", err)
	}
	if len(claims.Scopes) == 0 {
		return "", errors.New("edge token: no scopes")
	}
	for _, scope := range claims.Scopes {
		known := false
		for _, candidate := range edgev1.KnownScopes {
			known = known || scope == candidate
		}
		if !known {
			return "", fmt.Errorf("edge token: unknown scope %q", scope)
		}
	}

	now := signer.Now
	if now == nil {
		now = time.Now
	}
	random := signer.Random
	if random == nil {
		random = rand.Reader
	}
	id := make([]byte, 16)
	if _, err := io.ReadFull(random, id); err != nil {
		return "", fmt.Errorf("generate token id: %w", err)
	}
	issuedAt := now().Truncate(time.Second)

	claims.Issuer = signer.Service
	claims.Audience = edgev1.AudienceEdge
	claims.IssuedAt = issuedAt.Unix()
	claims.Expiry = issuedAt.Add(EdgeTokenLifetime).Unix()
	claims.ID = base64.RawURLEncoding.EncodeToString(id)
	if claims.Scopes == nil {
		claims.Scopes = []edgev1.Scope{}
	}

	encoded, err := json.Marshal(claims)
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}
	return signer.sign(ctx, encoded)
}
