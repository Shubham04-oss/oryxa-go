package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"maps"
	"testing"
)

type fakeDataKeys struct {
	key       []byte
	context   map[string]string
	encrypted []byte
}

func (f *fakeDataKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	f.context = maps.Clone(encryptionContext)
	f.encrypted = []byte("kms-wrapped-key")
	return append([]byte(nil), f.key...), append([]byte(nil), f.encrypted...), nil
}

func (f *fakeDataKeys) DecryptDataKey(_ context.Context, encrypted []byte, encryptionContext map[string]string) ([]byte, error) {
	if !bytes.Equal(encrypted, f.encrypted) || !maps.Equal(encryptionContext, f.context) {
		return nil, errors.New("kms context mismatch")
	}
	return append([]byte(nil), f.key...), nil
}

func TestEnvelopeRoundTripAndBinding(t *testing.T) {
	t.Parallel()
	key := sha256.Sum256([]byte("test data key"))
	keys := &fakeDataKeys{key: key[:]}
	envelope := Envelope{Keys: keys, KeyID: "alias/oryxa-connections", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{OrganizationID: "org-1", ResourceType: ResourceConnection, ResourceID: "con-1"}

	sealed, err := envelope.Seal(context.Background(), binding, []byte(`{"access_token":"not-logged"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("not-logged")) {
		t.Fatal("ciphertext contains plaintext")
	}
	if keys.context["service"] != "oryxa-connections" || keys.context["resource_type"] != ResourceConnection {
		t.Fatalf("encryption context = %v", keys.context)
	}
	plaintext, err := envelope.Open(context.Background(), binding, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, want := string(plaintext), `{"access_token":"not-logged"}`; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}

	for _, other := range []Binding{
		{OrganizationID: "org-2", ResourceType: ResourceConnection, ResourceID: "con-1"},
		{OrganizationID: "org-1", ResourceType: ResourceConnection, ResourceID: "con-2"},
		{OrganizationID: "org-1", ResourceType: ResourceAppSecret, ResourceID: "con-1"},
	} {
		if _, err := envelope.Open(context.Background(), other, sealed); err == nil {
			t.Fatalf("opening with %+v unexpectedly succeeded", other)
		}
	}
}

func TestEnvelopeRejectsTamperingAndMalformedValues(t *testing.T) {
	t.Parallel()
	key := sha256.Sum256([]byte("test data key"))
	envelope := Envelope{Keys: &fakeDataKeys{key: key[:]}, KeyID: "key", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{OrganizationID: "org-1", ResourceType: ResourceAppSecret, ResourceID: "app-1"}
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
	if _, err := envelope.Seal(context.Background(), Binding{OrganizationID: "org-1"}, []byte("secret")); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("incomplete binding error = %v, want ErrInvalidEnvelope", err)
	}
}

func TestWipe(t *testing.T) {
	t.Parallel()
	value := []byte("secret")
	Wipe(value)
	if !bytes.Equal(value, make([]byte, len(value))) {
		t.Fatalf("Wipe left %q", value)
	}
}
