package secretshares

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
)

// fakeKMS is KMS as far as an envelope needs it, and as strict as KMS is about
// the encryption context: a data key wrapped under one context opens only with
// exactly that one. It records the context of every call, so a test can say
// what was bound to what.
type fakeKMS struct {
	mu      sync.Mutex
	next    int
	wrapped map[string]wrappedKey

	// generated and opened are the encryption context of every call.
	generated []map[string]string
	opened    []map[string]string

	// delay holds every decrypt for a moment, so views made at once overlap.
	delay time.Duration
	// failDecrypt, when set, is what every decrypt fails with.
	failDecrypt error
}

type wrappedKey struct {
	key     []byte
	context map[string]string
}

func newFakeKMS() *fakeKMS { return &fakeKMS{wrapped: map[string]wrappedKey{}} }

func (fake *fakeKMS) GenerateDataKey(_ context.Context, keyID string, encryptionContext map[string]string) ([]byte, []byte, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	fake.next++
	blob := fmt.Sprintf("wrapped-%d-%s", fake.next, keyID)
	// What is kept is a copy: the envelope wipes the key it is handed.
	fake.wrapped[blob] = wrappedKey{key: bytes.Clone(key), context: maps.Clone(encryptionContext)}
	fake.generated = append(fake.generated, maps.Clone(encryptionContext))
	return key, []byte(blob), nil
}

func (fake *fakeKMS) DecryptDataKey(_ context.Context, blob []byte, encryptionContext map[string]string) ([]byte, error) {
	fake.mu.Lock()
	delay := fake.delay
	fake.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.opened = append(fake.opened, maps.Clone(encryptionContext))
	if fake.failDecrypt != nil {
		return nil, fake.failDecrypt
	}
	wrapped, ok := fake.wrapped[string(blob)]
	if !ok || !maps.Equal(wrapped.context, encryptionContext) {
		return nil, errors.New("InvalidCiphertextException: the encryption context does not match")
	}
	return bytes.Clone(wrapped.key), nil
}

// slowDecrypts holds every decrypt for delay from now on.
func (fake *fakeKMS) slowDecrypts(delay time.Duration) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.delay = delay
}

// failDecrypts makes every decrypt fail with err from now on; nil ends it.
func (fake *fakeKMS) failDecrypts(err error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.failDecrypt = err
}

func (fake *fakeKMS) generatedContexts() []map[string]string {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return append([]map[string]string(nil), fake.generated...)
}

func (fake *fakeKMS) openCount() int {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	return len(fake.opened)
}

// fakeKMSAPI is the KMS client itself, for NewSealer, which builds its own
// data keys on top of one.
type fakeKMSAPI struct {
	mu         sync.Mutex
	generate   []*kms.GenerateDataKeyInput
	decrypt    []*kms.DecryptInput
	dataKey    []byte
	wrappedKey []byte
}

func (fake *fakeKMSAPI) GenerateDataKey(_ context.Context, input *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.generate = append(fake.generate, input)
	return &kms.GenerateDataKeyOutput{Plaintext: bytes.Clone(fake.dataKey), CiphertextBlob: bytes.Clone(fake.wrappedKey)}, nil
}

func (fake *fakeKMSAPI) Decrypt(_ context.Context, input *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	fake.mu.Lock()
	defer fake.mu.Unlock()
	fake.decrypt = append(fake.decrypt, input)
	return &kms.DecryptOutput{Plaintext: bytes.Clone(fake.dataKey)}, nil
}

// fakeClock is a clock a test moves by hand.
type fakeClock struct {
	mu sync.Mutex
	at time.Time
}

func newFakeClock() *fakeClock {
	return &fakeClock{at: time.Now().UTC().Truncate(time.Microsecond)}
}

func (clock *fakeClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.at
}

func (clock *fakeClock) Advance(by time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.at = clock.at.Add(by)
}
