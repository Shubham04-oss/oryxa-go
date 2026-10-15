// Package vault seals the secrets the integrations service stores: app
// secrets, tokens and PKCE verifiers. Each value is an internal/secrets
// envelope (AES-256-GCM under a fresh KMS data key) bound to the table, org
// and row it is stored in, so a value copied to another org, table or row
// does not open.
package vault

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// Service is the name every value is bound to.
const Service = "integrations"

// Where names the row a sealed value sits in. Org is "" only for Oryxa's own
// app (org_apps with org_id NULL).
type Where struct {
	Table string
	Org   string
	Row   string
}

// Sealer seals and opens values for the service.
type Sealer struct {
	envelope secrets.Envelope
}

// New returns a Sealer over keys, wrapping data keys under keyID.
func New(keys secrets.DataKeys, keyID string) (Sealer, error) {
	if keys == nil || keyID == "" {
		return Sealer{}, errors.New("vault: data keys and a key id are required")
	}
	return Sealer{envelope: secrets.Envelope{Keys: keys, KeyID: keyID, Service: Service}}, nil
}

// Seal encrypts plaintext for where.
func (s Sealer) Seal(ctx context.Context, where Where, plaintext []byte) (secrets.SealedData, error) {
	binding, err := where.binding()
	if err != nil {
		return secrets.SealedData{}, err
	}
	return s.envelope.Seal(ctx, binding, plaintext)
}

// Open decrypts sealed, which must have been sealed for where.
func (s Sealer) Open(ctx context.Context, where Where, sealed secrets.SealedData) (secrets.Secret, error) {
	binding, err := where.binding()
	if err != nil {
		return "", err
	}
	plaintext, err := s.envelope.Open(ctx, binding, sealed)
	if err != nil {
		return "", err
	}
	defer secrets.Wipe(plaintext)
	return secrets.Secret(plaintext), nil
}

// binding folds the org into the resource id, which secrets binds to the KMS
// encryption context and the GCM associated data. An org id holding "/" is
// refused so "a/b"+"c" and "a"+"b/c" can't collide.
func (w Where) binding() (secrets.Binding, error) {
	if w.Table == "" || w.Row == "" {
		return secrets.Binding{}, fmt.Errorf("%w: a table and a row are required", secrets.ErrInvalidEnvelope)
	}
	if strings.ContainsRune(w.Org, '/') {
		return secrets.Binding{}, fmt.Errorf("%w: org id %q", secrets.ErrInvalidEnvelope, w.Org)
	}
	return secrets.Binding{ResourceType: w.Table, ResourceID: w.Org + "/" + w.Row}, nil
}
