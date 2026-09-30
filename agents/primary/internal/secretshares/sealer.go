package secretshares

import (
	"context"
	"errors"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

const (
	// SealService is the service every secret is sealed for. It is the
	// "service" entry of the KMS encryption context, which the task role's
	// grant on the agents-secrets key is conditioned on
	// (kms:EncryptionContext:service, deploy/production/compute.tf): a call
	// that names another service has no permission on the key at all.
	SealService = "oryxa-agents"

	// SealResource is the resource type a secret is bound to, beside the id of
	// the request it was shared for. Together with SealService they are the
	// encryption context and the associated data of the value's encryption, so
	// a sealed value cannot be moved to another request's row and opened there.
	SealResource = "secret_share"
)

// Sealer seals and opens one value at a time. secrets.Envelope, bound to
// SealService, is one, and NewSealer makes it.
type Sealer interface {
	Seal(ctx context.Context, binding secrets.Binding, plaintext []byte) (secrets.SealedData, error)
	Open(ctx context.Context, binding secrets.Binding, sealed secrets.SealedData) ([]byte, error)
}

// NewSealer is the envelope one-time secrets are sealed with: a fresh data key
// for every secret from the KMS key keyID (AGENTS_PRIMARY_SECRETS_KMS_KEY_ID,
// the agents-secrets key), bound to SealService and to the request.
func NewSealer(client secrets.KMSAPI, keyID string) (secrets.Envelope, error) {
	if client == nil || strings.TrimSpace(keyID) == "" {
		return secrets.Envelope{}, errors.New("sealing one-time secrets needs a KMS client and the agents-secrets key, AGENTS_PRIMARY_SECRETS_KMS_KEY_ID")
	}
	keyID = strings.TrimSpace(keyID)
	return secrets.Envelope{
		Keys:    secrets.KMSDataKeys{Client: client, KeyID: keyID},
		KeyID:   keyID,
		Service: SealService,
	}, nil
}

// bindingOf is what a secret is sealed for: the request it belongs to.
func bindingOf(requestID string) secrets.Binding {
	return secrets.Binding{ResourceType: SealResource, ResourceID: requestID}
}
