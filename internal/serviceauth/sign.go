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

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/Shubham04-oss/oryxa-go/internal/kmscall"
)

// SignAPI is the KMS surface a Signer needs.
type SignAPI interface {
	Sign(context.Context, *kms.SignInput, ...func(*kms.Options)) (*kms.SignOutput, error)
}

// Signer issues tokens for one service with an ECC_NIST_P256 KMS key.
type Signer struct {
	Client  SignAPI
	KeyID   string
	Service string
	Now     func() time.Time
	Random  io.Reader
}

// Token returns a token for audience and purpose that lives MaxLifetime.
func (signer Signer) Token(ctx context.Context, audience, purpose string) (string, error) {
	if err := validateName(signer.Service); err != nil {
		return "", fmt.Errorf("signer service: %w", err)
	}
	if err := validateName(audience); err != nil {
		return "", fmt.Errorf("audience: %w", err)
	}
	if err := validateName(purpose); err != nil {
		return "", fmt.Errorf("purpose: %w", err)
	}
	if signer.Client == nil || signer.KeyID == "" {
		return "", errors.New("signer requires a KMS client and key ID")
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

	claims, err := json.Marshal(wireClaims{
		Issuer:   signer.Service,
		Audience: audience,
		Purpose:  purpose,
		IssuedAt: issuedAt.Unix(),
		Expiry:   issuedAt.Add(MaxLifetime).Unix(),
		ID:       base64.RawURLEncoding.EncodeToString(id),
	})
	if err != nil {
		return "", fmt.Errorf("encode claims: %w", err)
	}

	return signer.sign(ctx, claims)
}

// sign signs claims, already JSON, with the signer's KMS key and returns the
// compact JWT.
func (signer Signer) sign(ctx context.Context, claims []byte) (string, error) {
	signingInput := headerSegment + "." + base64.RawURLEncoding.EncodeToString(claims)
	ctx, cancel := kmscall.Context(ctx)
	defer cancel()
	output, err := signer.Client.Sign(ctx, &kms.SignInput{
		KeyId:            &signer.KeyID,
		Message:          []byte(signingInput),
		MessageType:      types.MessageTypeRaw,
		SigningAlgorithm: types.SigningAlgorithmSpecEcdsaSha256,
	})
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}

	signature, err := joseSignature(output.Signature)
	if err != nil {
		return "", fmt.Errorf("sign token: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}
