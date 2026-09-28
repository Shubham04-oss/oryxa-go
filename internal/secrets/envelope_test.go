package secrets

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"maps"
	"testing"
)

// fakeDataKeys stands in for KMS: a wrapped key unwraps only with exactly
// the encryption context it was generated for.
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

func testKey() []byte {
	key := sha256.Sum256([]byte("test data key"))
	return key[:]
}

func TestEnvelopeRoundTripAndBinding(t *testing.T) {
	t.Parallel()
	keys := &fakeDataKeys{key: testKey()}
	envelope := Envelope{Keys: keys, KeyID: "alias/oryxa-apps", Service: "oryxa-apps", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{ResourceType: "credential", ResourceID: "connection-1"}

	sealed, err := envelope.Seal(context.Background(), binding, []byte(`{"refresh_token":"not-logged"}`))
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if bytes.Contains(sealed.Ciphertext, []byte("not-logged")) {
		t.Fatal("ciphertext contains plaintext")
	}
	want := map[string]string{"service": "oryxa-apps", "resource_type": "credential", "resource_id": "connection-1"}
	if !maps.Equal(keys.context, want) {
		t.Fatalf("encryption context = %v, want %v", keys.context, want)
	}
	plaintext, err := envelope.Open(context.Background(), binding, sealed)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got, want := string(plaintext), `{"refresh_token":"not-logged"}`; got != want {
		t.Fatalf("plaintext = %q, want %q", got, want)
	}

	for _, other := range []Binding{
		{ResourceType: "credential", ResourceID: "connection-2"},
		{ResourceType: "oauth-client", ResourceID: "connection-1"},
	} {
		if _, err := envelope.Open(context.Background(), other, sealed); err == nil {
			t.Fatalf("opening with %+v unexpectedly succeeded", other)
		}
	}
}

// TestEnvelopeServiceBindsCiphertext proves another service cannot open a
// value even with the same resource and data key: the associated data names
// the service too, so AES-GCM refuses it after KMS has answered.
func TestEnvelopeServiceBindsCiphertext(t *testing.T) {
	t.Parallel()
	binding := Binding{ResourceType: "credential", ResourceID: "connection-1"}
	apps := Envelope{Keys: &fakeDataKeys{key: testKey()}, Service: "oryxa-apps", Rand: bytes.NewReader(make([]byte, 64))}
	sealed, err := apps.Seal(context.Background(), binding, []byte("secret"))
	if err != nil {
		t.Fatal(err)
	}

	lenient := &lenientDataKeys{key: testKey()}
	auth := Envelope{Keys: lenient, Service: "oryxa-auth"}
	if _, err := auth.Open(context.Background(), binding, sealed); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("open under another service = %v, want ErrInvalidEnvelope", err)
	}
	if got := lenient.context["service"]; got != "oryxa-auth" {
		t.Fatalf("KMS was asked for service %q, want oryxa-auth", got)
	}
}

// lenientDataKeys unwraps any key under any context, as KMS never would, to
// show what the associated data alone refuses.
type lenientDataKeys struct {
	key     []byte
	context map[string]string
}

func (l *lenientDataKeys) GenerateDataKey(context.Context, string, map[string]string) ([]byte, []byte, error) {
	return append([]byte(nil), l.key...), []byte("wrapped"), nil
}

func (l *lenientDataKeys) DecryptDataKey(_ context.Context, _ []byte, encryptionContext map[string]string) ([]byte, error) {
	l.context = maps.Clone(encryptionContext)
	return append([]byte(nil), l.key...), nil
}

func TestEnvelopeRejectsTamperingAndMalformedValues(t *testing.T) {
	t.Parallel()
	envelope := Envelope{Keys: &fakeDataKeys{key: testKey()}, KeyID: "key", Service: "oryxa-apps", Rand: bytes.NewReader(make([]byte, 64))}
	binding := Binding{ResourceType: "credential", ResourceID: "connection-1"}
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
	for name, attempt := range map[string]struct {
		envelope Envelope
		binding  Binding
	}{
		"no resource id":   {envelope, Binding{ResourceType: "credential"}},
		"no resource type": {envelope, Binding{ResourceID: "connection-1"}},
		"no service":       {Envelope{Keys: envelope.Keys}, binding},
	} {
		if _, err := attempt.envelope.Seal(context.Background(), attempt.binding, []byte("secret")); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("%s: seal error = %v, want ErrInvalidEnvelope", name, err)
		}
		if _, err := attempt.envelope.Open(context.Background(), attempt.binding, sealed); !errors.Is(err, ErrInvalidEnvelope) {
			t.Fatalf("%s: open error = %v, want ErrInvalidEnvelope", name, err)
		}
	}
}

func TestEnvelopeRefusesShortDataKeys(t *testing.T) {
	t.Parallel()
	envelope := Envelope{Keys: &fakeDataKeys{key: make([]byte, 16)}, Service: "oryxa-apps"}
	if _, err := envelope.Seal(context.Background(), Binding{ResourceType: "credential", ResourceID: "connection-1"}, []byte("secret")); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("seal with a 128-bit data key = %v, want ErrInvalidEnvelope", err)
	}
}

// TestSealedDataJSON pins how a sealed value is stored: the same JSON as
// auth's domain.SealedData, whose golden test pins auth's stored grants.
func TestSealedDataJSON(t *testing.T) {
	t.Parallel()
	encoded, err := json.Marshal(SealedData{Version: 1, Nonce: []byte{1}, Ciphertext: []byte{2}, EncryptedDataKey: []byte{3}})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(encoded), `{"version":1,"nonce":"AQ==","ciphertext":"Ag==","encrypted_data_key":"Aw=="}`; got != want {
		t.Fatalf("stored form = %s, want %s", got, want)
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
