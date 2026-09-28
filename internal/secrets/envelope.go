// Package secrets envelope-encrypts the credentials a service stores, such as
// auth's agent refresh tokens and the tokens and client secrets apps holds for
// connected apps. Each value gets a fresh KMS data key. Its binding (the
// service, the resource type and the resource id) is both the KMS encryption
// context and the AES-GCM associated data, so ciphertext cannot be moved to
// another resource or opened by another service.
//
// Secret carries a plaintext credential through a service without it reaching
// a log line, a trace or an API answer by accident.
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
)

const envelopeVersion = 1

var ErrInvalidEnvelope = errors.New("invalid encrypted envelope")

// Binding ties ciphertext to exactly the resource it was sealed for, so a
// value cannot be moved to a different resource. The service it is bound to
// is the Envelope's own.
type Binding struct {
	ResourceType string
	ResourceID   string
}

// Context is the binding's encryption context under service. KMS
// authenticates the data key against it, a key policy can condition on its
// entries (kms:EncryptionContext:service), and its JSON is the AES-GCM
// associated data.
func (b Binding) Context(service string) map[string]string {
	return map[string]string{
		"service":       service,
		"resource_type": b.ResourceType,
		"resource_id":   b.ResourceID,
	}
}

type DataKeys interface {
	GenerateDataKey(context.Context, string, map[string]string) (plaintext, encrypted []byte, err error)
	DecryptDataKey(context.Context, []byte, map[string]string) ([]byte, error)
}

// SealedData is an envelope-encrypted value. The plaintext data key is never
// stored; EncryptedDataKey opens only with the same KMS encryption context.
// Its JSON is how a value is stored, the same as auth's domain.SealedData.
type SealedData struct {
	Version          int    `json:"version"`
	Nonce            []byte `json:"nonce"`
	Ciphertext       []byte `json:"ciphertext"`
	EncryptedDataKey []byte `json:"encrypted_data_key"`
}

// Envelope seals and opens one service's values.
type Envelope struct {
	Keys  DataKeys
	KeyID string
	// Service is the name every value is bound to, such as "oryxa-apps". A
	// service binds and opens its own values only.
	Service string
	Rand    io.Reader
}

func (e Envelope) Seal(ctx context.Context, binding Binding, plaintext []byte) (SealedData, error) {
	if err := e.validateBinding(binding); err != nil {
		return SealedData{}, err
	}
	encryptionContext := binding.Context(e.Service)
	dataKey, encryptedKey, err := e.Keys.GenerateDataKey(ctx, e.KeyID, encryptionContext)
	if err != nil {
		return SealedData{}, fmt.Errorf("generate data key: %w", err)
	}
	defer Wipe(dataKey)
	if len(dataKey) != 32 || len(encryptedKey) == 0 {
		return SealedData{}, fmt.Errorf("%w: data key must be 256 bits", ErrInvalidEnvelope)
	}
	gcm, err := newGCM(dataKey)
	if err != nil {
		return SealedData{}, err
	}
	nonce := make([]byte, gcm.NonceSize())
	random := e.Rand
	if random == nil {
		random = rand.Reader
	}
	if _, err := io.ReadFull(random, nonce); err != nil {
		return SealedData{}, fmt.Errorf("generate nonce: %w", err)
	}
	aad, _ := json.Marshal(encryptionContext)
	return SealedData{
		Version:          envelopeVersion,
		Nonce:            nonce,
		Ciphertext:       gcm.Seal(nil, nonce, plaintext, aad),
		EncryptedDataKey: append([]byte(nil), encryptedKey...),
	}, nil
}

func (e Envelope) Open(ctx context.Context, binding Binding, sealed SealedData) ([]byte, error) {
	if err := e.validateBinding(binding); err != nil {
		return nil, err
	}
	if sealed.Version != envelopeVersion || len(sealed.Nonce) == 0 || len(sealed.Ciphertext) == 0 || len(sealed.EncryptedDataKey) == 0 {
		return nil, ErrInvalidEnvelope
	}
	encryptionContext := binding.Context(e.Service)
	dataKey, err := e.Keys.DecryptDataKey(ctx, sealed.EncryptedDataKey, encryptionContext)
	if err != nil {
		return nil, fmt.Errorf("decrypt data key: %w", err)
	}
	defer Wipe(dataKey)
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
	aad, _ := json.Marshal(encryptionContext)
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

func (e Envelope) validateBinding(binding Binding) error {
	if e.Service == "" || binding.ResourceType == "" || binding.ResourceID == "" {
		return fmt.Errorf("%w: incomplete encryption binding", ErrInvalidEnvelope)
	}
	return nil
}

// Wipe overwrites plaintext once it is no longer needed.
func Wipe(value []byte) { clear(value) }
