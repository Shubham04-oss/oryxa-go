// Package secrets envelope-encrypts a member's stored agent refresh token.
// It is internal/secrets bound to auth's own name: each value's KMS encryption
// context and AES-GCM associated data carry service=oryxa-auth, as they did
// before that package existed. Grants sealed before still open, and sealing is
// unchanged byte for byte: testdata/envelopes.golden.json pins both. Nothing
// sealed here opens in another service.
package secrets

import (
	"context"
	"io"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
	// Aliased: this package is also named secrets, and wraps that one.
	sharedsecrets "github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// service is the name every value auth seals is bound to. Changing it would
// leave every stored grant unopenable.
const service = "oryxa-auth"

// ResourceAgentGrant is the resource type auth's envelope encryption binds:
// a member's stored agent refresh token.
const ResourceAgentGrant = "agent-grant"

var ErrInvalidEnvelope = sharedsecrets.ErrInvalidEnvelope

// Binding ties ciphertext to exactly the resource it was sealed for, so a
// value cannot be moved to a different resource and cannot be opened by
// another service. Service is always "oryxa-auth": auth binds and opens its
// own values only.
type Binding struct {
	ResourceType string
	ResourceID   string
}

func (b Binding) Context() map[string]string {
	return sharedsecrets.Binding(b).Context(service)
}

type DataKeys = sharedsecrets.DataKeys

type Envelope struct {
	Keys  DataKeys
	KeyID string
	Rand  io.Reader
}

func (e Envelope) Seal(ctx context.Context, binding Binding, plaintext []byte) (domain.SealedData, error) {
	sealed, err := e.shared().Seal(ctx, sharedsecrets.Binding(binding), plaintext)
	return domain.SealedData(sealed), err
}

func (e Envelope) Open(ctx context.Context, binding Binding, sealed domain.SealedData) ([]byte, error) {
	return e.shared().Open(ctx, sharedsecrets.Binding(binding), sharedsecrets.SealedData(sealed))
}

func (e Envelope) shared() sharedsecrets.Envelope {
	return sharedsecrets.Envelope{Keys: e.Keys, KeyID: e.KeyID, Service: service, Rand: e.Rand}
}

// Wipe overwrites plaintext once it is no longer needed.
func Wipe(value []byte) { sharedsecrets.Wipe(value) }
