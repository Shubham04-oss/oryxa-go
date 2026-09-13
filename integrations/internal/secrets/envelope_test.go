package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"reflect"
	"testing"
)

type fakeDataKeys struct {
	key       []byte
	context   map[string]string
	encrypted []byte
}

func (f *fakeDataKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	f.context = cloneMap(encryptionContext)
	f.encrypted = []byte("kms-wrapped-key")
	return append([]byte(nil), f.key...), append([]byte(nil), f.encrypted...), nil
}

func (f *fakeDataKeys) DecryptDataKey(_ context.Context, encrypted []byte, encryptionContext map[string]string) ([]byte, error) {
	if !bytes.Equal(encrypted, f.encrypted) || !reflect.DeepEqual(encryptionContext, f.context) {
		return nil, errors.New("kms context mismatch")
	}
	return append([]byte(nil), f.key...), nil
}

func TestEnvelopeRoundTripAndBinding(t *testing.T) {
	t.Parallel()
	key := sha256.Sum256([]byte("test data key"))
	keys := &fakeDataKeys{key: key[:]}
	envelope := Envelope{Keys: keys, KeyID: "alias/oryxa-integrations", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{OrganizationID: "org-1", ResourceType: "connection", ResourceID: "con-1"}

	sealed, err := envelope.Seal(context.Background(), binding, []byte(`{"access_token":"not-logged"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("not-logged")) {
		t.Fatal("ciphertext contains plaintext")
	}
	plaintext, err := envelope.Open(context.Background(), binding, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, want := string(plaintext), `{"access_token":"not-logged"}`; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}

	wrongBinding := Binding{OrganizationID: "org-2", ResourceType: "connection", ResourceID: "con-1"}
	if _, err := envelope.Open(context.Background(), wrongBinding, sealed); err == nil {
		t.Fatal("opening with another organization unexpectedly succeeded")
	}
}

func TestEnvelopeRejectsTamperingAndMalformedValues(t *testing.T) {
	t.Parallel()
	key := sha256.Sum256([]byte("test data key"))
	keys := &fakeDataKeys{key: key[:]}
	envelope := Envelope{Keys: keys, KeyID: "key", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{OrganizationID: "org-1", ResourceType: "oauth-app", ResourceID: "app-1"}
	sealed, err := envelope.Seal(context.Background(), binding, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}
	sealed.Ciphertext[0] ^= 0xff
	if _, err := envelope.Open(context.Background(), binding, sealed); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("tamper error = %v, want ErrInvalidEnvelope", err)
	}
	sealed.Version = 99
	if _, err := envelope.Open(context.Background(), binding, sealed); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("version error = %v, want ErrInvalidEnvelope", err)
	}
}

func cloneMap(input map[string]string) map[string]string {
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
