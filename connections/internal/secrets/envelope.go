// Package secrets envelope-encrypts client secrets, authorization sessions and
// grants. Each value gets a fresh KMS data key, and its binding is both the
// KMS encryption context and the AES-GCM associated data, so ciphertext cannot
// be moved between organizations or records.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/Shubham04-oss/oryxa-go/connections/internal/domain"
)

const envelopeVersion = 1

// Resource types a binding can name.
const (
	ResourceAppSecret  = "app-secret"
	ResourceConnection = "connection"
	ResourceSession    = "session"
)

var ErrInvalidEnvelope = errors.New("invalid encrypted envelope")

type Binding struct {
	OrganizationID string
	ResourceType   string
	ResourceID     string
}

func (b Binding) Context() map[string]string {
	return map[string]string{
		"service":         "oryxa-connections",
		"organization_id": b.OrganizationID,
		"resource_type":   b.ResourceType,
		"resource_id":     b.ResourceID,
	}
}

type DataKeys interface {
	GenerateDataKey(context.Context, string, map[string]string) (plaintext, encrypted []byte, err error)
	DecryptDataKey(context.Context, []byte, map[string]string) ([]byte, error)
}

type Envelope struct {
	Keys  DataKeys
	KeyID string
	Rand  io.Reader
}

func (e Envelope) Seal(ctx context.Context, binding Binding, plaintext []byte) (domain.SealedData, error) {
	if err := validateBinding(binding); err != nil {
		return domain.SealedData{}, err
	}
	dataKey, encryptedKey, err := e.Keys.GenerateDataKey(ctx, e.KeyID, binding.Context())
	if err != nil {
		return domain.SealedData{}, fmt.Errorf("generate data key: %w", err)
	}
	defer wipe(dataKey)
	if len(dataKey) != 32 || len(encryptedKey) == 0 {
		return domain.SealedData{}, fmt.Errorf("%w: data key must be 256 bits", ErrInvalidEnvelope)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return domain.SealedData{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	random := e.Rand
	if random == nil {
		random = rand.Reader
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return domain.SealedData{}, fmt.Errorf("generate nonce: %w", err)
	}
	aad, _ := json.Marshal(binding.Context())
	return domain.SealedData{
		Version:          envelopeVersion,
		Nonce:            nonce,
		Ciphertext:       gcm.Seal(nil, nonce, plaintext, aad),
		EncryptedDataKey: append([]byte(nil), encryptedKey...),
	}, nil
}

func (e Envelope) Open(ctx context.Context, binding Binding, sealed domain.SealedData) ([]byte, error) {
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	if sealed.Version != envelopeVersion || len(sealed.Nonce) == 0 || len(sealed.Ciphertext) == 0 || len(sealed.EncryptedDataKey) == 0 {
		return nil, ErrInvalidEnvelope
	}
	dataKey, err := e.Keys.DecryptDataKey(ctx, sealed.EncryptedDataKey, binding.Context())
	if err != nil {
		return nil, fmt.Errorf("decrypt data key: %w", err)
	}
	defer wipe(dataKey)
	if len(dataKey) != 32 {
		return nil, fmt.Errorf("%w: data key must be 256 bits", ErrInvalidEnvelope)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return nil, err
	}
	if len(sealed.Nonce) != gcm.NonceSize() {
		return nil, ErrInvalidEnvelope
	}
	aad, _ := json.Marshal(binding.Context())
	plaintext, err := gcm.Open(nil, sealed.Nonce, sealed.Ciphertext, aad)
	if err != nil {
		return nil, fmt.Errorf("%w: authentication failed", ErrInvalidEnvelope)
	}
	return plaintext, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create gcm: %w", err)
	}
	return gcm, nil
}

func validateBinding(binding Binding) error {
	if binding.OrganizationID == "" || binding.ResourceType == "" || binding.ResourceID == "" {
		return fmt.Errorf("%w: incomplete encryption binding", ErrInvalidEnvelope)
	}
	return nil
}

// Wipe overwrites plaintext once it is no longer needed.
func Wipe(value []byte) { wipe(value) }

func wipe(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
