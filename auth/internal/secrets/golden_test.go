package secrets

// This file pins the wire format of auth's sealed agent refresh tokens: the
// JSON auth stores in auth_agent_grants.encrypted_refresh_token, the KMS
// request that wraps and unwraps each data key, and the AES-GCM associated
// data. testdata/envelopes.golden.json was written by this test against
// auth's own envelope code, before that code moved to internal/secrets.
// Grants sealed in production since then must keep opening, so the file is
// never regenerated: a change that fails this test changes the format.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

var writeGolden = flag.Bool("write-golden", false, "write testdata/envelopes.golden.json, only if it does not exist yet")

const goldenPath = "testdata/envelopes.golden.json"

// goldenKeyID is the KMS key both sides of the golden test name; auth passes
// its one configured key as Envelope.KeyID and KMSDataKeys.KeyID alike.
const goldenKeyID = "arn:aws:kms:us-west-2:111122223333:key/0e2f0c8e-golden-test-key"

type goldenFile struct {
	KeyID     string           `json:"key_id"`
	Envelopes []goldenEnvelope `json:"envelopes"`
}

type goldenEnvelope struct {
	Name         string `json:"name"`
	ResourceType string `json:"resource_type"`
	ResourceID   string `json:"resource_id"`
	Plaintext    string `json:"plaintext"`
	// EncryptionContext is what KMS was asked to bind the data key to.
	EncryptionContext map[string]string `json:"encryption_context"`
	// Stored is the envelope exactly as auth's store encodes it for the
	// jsonb column: json.Marshal of a domain.SealedData.
	Stored json.RawMessage `json:"stored"`
}

// goldenInputs are the values the golden file was sealed from.
func goldenInputs() []goldenEnvelope {
	long := "golden-refresh-token."
	for block := sha256.Sum256([]byte("golden refresh token")); len(long) < 1733; block = sha256.Sum256(block[:]) {
		long += hex.EncodeToString(block[:])
	}
	return []goldenEnvelope{
		{Name: "a Cognito-sized refresh token", ResourceType: ResourceAgentGrant, ResourceID: "3f9c1a52-8e47-4b1d-9c6a-0d2e7b5f8a13", Plaintext: long[:1733]},
		{Name: "a short value", ResourceType: ResourceAgentGrant, ResourceID: "member-1", Plaintext: `{"refresh_token":"not-logged"}`},
		{Name: "an empty value", ResourceType: ResourceAgentGrant, ResourceID: "member-empty", Plaintext: ""},
		{Name: "a binding JSON escapes", ResourceType: ResourceAgentGrant, ResourceID: "member <&> \"quoted\" é  ", Plaintext: "escaped-binding-token"},
		{Name: "another resource type", ResourceType: "golden-resource", ResourceID: "resource-1", Plaintext: "another-resource-value"},
	}
}

// goldenNonce is the AES-GCM nonce an input was first sealed with.
func goldenNonce(name string) []byte {
	sum := sha256.Sum256([]byte("golden nonce: " + name))
	return sum[:12]
}

// TestGoldenEnvelopesOpen proves envelopes sealed by auth's original code
// still open, through the same KMS request.
func TestGoldenEnvelopesOpen(t *testing.T) {
	t.Parallel()
	golden := readGolden(t)
	for _, want := range golden.Envelopes {
		t.Run(want.Name, func(t *testing.T) {
			t.Parallel()
			// Decoded as auth's store decodes the column.
			var sealed domain.SealedData
			if err := json.Unmarshal(want.Stored, &sealed); err != nil {
				t.Fatalf("decode stored envelope: %v", err)
			}
			keys := &goldenKMS{}
			envelope := Envelope{Keys: KMSDataKeys{Client: keys, KeyID: golden.KeyID}, KeyID: golden.KeyID}
			plaintext, err := envelope.Open(context.Background(), Binding{ResourceType: want.ResourceType, ResourceID: want.ResourceID}, sealed)
			if err != nil {
				t.Fatalf("open golden envelope: %v", err)
			}
			if string(plaintext) != want.Plaintext {
				t.Fatalf("plaintext = %q, want %q", plaintext, want.Plaintext)
			}
			if len(keys.decrypted) != 1 {
				t.Fatalf("KMS Decrypt calls = %d, want 1", len(keys.decrypted))
			}
			if got := aws.ToString(keys.decrypted[0].KeyId); got != golden.KeyID {
				t.Fatalf("Decrypt key ID = %q, want %q", got, golden.KeyID)
			}
			if got := keys.decrypted[0].EncryptionContext; !maps.Equal(got, want.EncryptionContext) {
				t.Fatalf("Decrypt encryption context = %v, want %v", got, want.EncryptionContext)
			}

			other := Binding{ResourceType: want.ResourceType, ResourceID: want.ResourceID + "-other"}
			if _, err := envelope.Open(context.Background(), other, sealed); err == nil {
				t.Fatal("a golden envelope opened for another resource")
			}
		})
	}
}

// TestGoldenEnvelopesSealIdentically proves sealing is unchanged byte for
// byte: given the nonce a golden envelope drew and the data key the fake KMS
// derives, sealing the same value again stores exactly the golden bytes.
func TestGoldenEnvelopesSealIdentically(t *testing.T) {
	t.Parallel()
	golden := readGolden(t)
	for _, want := range golden.Envelopes {
		t.Run(want.Name, func(t *testing.T) {
			t.Parallel()
			var stored domain.SealedData
			if err := json.Unmarshal(want.Stored, &stored); err != nil {
				t.Fatalf("decode stored envelope: %v", err)
			}
			keys := &goldenKMS{}
			got := sealGolden(t, keys, golden.KeyID, want, stored.Nonce)

			var compact bytes.Buffer
			if err := json.Compact(&compact, want.Stored); err != nil {
				t.Fatalf("compact golden envelope: %v", err)
			}
			if !bytes.Equal(got, compact.Bytes()) {
				t.Fatalf("sealed envelope differs from the golden one\n got: %s\nwant: %s", got, compact.Bytes())
			}
			if len(keys.generated) != 1 {
				t.Fatalf("KMS GenerateDataKey calls = %d, want 1", len(keys.generated))
			}
			if got := aws.ToString(keys.generated[0].KeyId); got != golden.KeyID {
				t.Fatalf("GenerateDataKey key ID = %q, want %q", got, golden.KeyID)
			}
			if got := keys.generated[0].EncryptionContext; !maps.Equal(got, want.EncryptionContext) {
				t.Fatalf("GenerateDataKey encryption context = %v, want %v", got, want.EncryptionContext)
			}
		})
	}
}

// sealGolden seals an input as auth's service wiring does, with nonce as the
// only randomness, and encodes it as auth's store does.
func sealGolden(t *testing.T, keys *goldenKMS, keyID string, input goldenEnvelope, nonce []byte) []byte {
	t.Helper()
	envelope := Envelope{Keys: KMSDataKeys{Client: keys, KeyID: keyID}, KeyID: keyID, Rand: bytes.NewReader(nonce)}
	sealed, err := envelope.Seal(context.Background(), Binding{ResourceType: input.ResourceType, ResourceID: input.ResourceID}, []byte(input.Plaintext))
	if err != nil {
		t.Fatalf("seal %q: %v", input.Name, err)
	}
	stored, err := json.Marshal(sealed)
	if err != nil {
		t.Fatalf("encode %q: %v", input.Name, err)
	}
	return stored
}

func readGolden(t *testing.T) goldenFile {
	t.Helper()
	if *writeGolden {
		goldenWriting.Lock()
		createGolden(t)
		goldenWriting.Unlock()
	}
	data, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden envelopes: %v", err)
	}
	var golden goldenFile
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode golden envelopes: %v", err)
	}
	if golden.KeyID == "" || len(golden.Envelopes) == 0 {
		t.Fatal("the golden file holds no envelopes")
	}
	return golden
}

// goldenWriting keeps parallel tests from writing the golden file at once.
var goldenWriting sync.Mutex

// createGolden writes the golden file from goldenInputs. It refuses to
// replace one: the file records what production already holds.
func createGolden(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(goldenPath); err == nil {
		return
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stat golden file: %v", err)
	}
	golden := goldenFile{KeyID: goldenKeyID}
	for _, input := range goldenInputs() {
		keys := &goldenKMS{}
		input.Stored = sealGolden(t, keys, goldenKeyID, input, goldenNonce(input.Name))
		input.EncryptionContext = keys.generated[0].EncryptionContext
		golden.Envelopes = append(golden.Envelopes, input)
	}
	data, err := json.MarshalIndent(golden, "", "  ")
	if err != nil {
		t.Fatalf("encode golden file: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
		t.Fatalf("create testdata: %v", err)
	}
	if err := os.WriteFile(goldenPath, append(data, '\n'), 0o644); err != nil {
		t.Fatalf("write golden file: %v", err)
	}
}

// goldenMasterKey is the fake KMS key's own material. It is test data, not a
// secret.
var goldenMasterKey = sha256.Sum256([]byte("oryxa golden test master key, not a real key"))

// goldenKMS stands in for AWS KMS, deterministically. It derives each data
// key from the key ID and encryption context it is asked for, and wraps it
// under goldenMasterKey with those as associated data. As with KMS, a
// wrapped key opens only when Decrypt names the same key and presents
// exactly the same encryption context; unlike KMS, the same request always
// answers the same bytes, so a seal can be repeated byte for byte.
type goldenKMS struct {
	generated []*kms.GenerateDataKeyInput
	decrypted []*kms.DecryptInput
}

func (k *goldenKMS) GenerateDataKey(_ context.Context, input *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	k.generated = append(k.generated, input)
	if input.KeySpec != types.DataKeySpecAes256 {
		return nil, fmt.Errorf("golden KMS: key spec %q, want AES_256", input.KeySpec)
	}
	bound := goldenBound(aws.ToString(input.KeyId), input.EncryptionContext)
	dataKey := goldenDerive("data key", bound)
	nonce := goldenDerive("wrapping nonce", bound)[:12]
	wrapped := goldenWrapper().Seal(append([]byte(nil), nonce...), nonce, dataKey, bound)
	return &kms.GenerateDataKeyOutput{KeyId: input.KeyId, Plaintext: dataKey, CiphertextBlob: wrapped}, nil
}

func (k *goldenKMS) Decrypt(_ context.Context, input *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	k.decrypted = append(k.decrypted, input)
	wrapped := input.CiphertextBlob
	if len(wrapped) < 12 {
		return nil, &types.InvalidCiphertextException{Message: aws.String("golden KMS: ciphertext too short")}
	}
	bound := goldenBound(aws.ToString(input.KeyId), input.EncryptionContext)
	dataKey, err := goldenWrapper().Open(nil, wrapped[:12], wrapped[12:], bound)
	if err != nil {
		return nil, &types.InvalidCiphertextException{Message: aws.String("golden KMS: another key or encryption context")}
	}
	return &kms.DecryptOutput{KeyId: input.KeyId, Plaintext: dataKey}, nil
}

// goldenBound is what a wrapped key is bound to: the key and the whole
// encryption context. json.Marshal sorts map keys, so it is canonical.
func goldenBound(keyID string, encryptionContext map[string]string) []byte {
	bound, err := json.Marshal(struct {
		KeyID   string            `json:"key_id"`
		Context map[string]string `json:"context"`
	}{keyID, encryptionContext})
	if err != nil {
		panic(err)
	}
	return bound
}

func goldenDerive(purpose string, bound []byte) []byte {
	mac := hmac.New(sha256.New, goldenMasterKey[:])
	mac.Write([]byte(purpose + "\x00"))
	mac.Write(bound)
	return mac.Sum(nil)
}

func goldenWrapper() cipher.AEAD {
	block, err := aes.NewCipher(goldenMasterKey[:])
	if err != nil {
		panic(err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		panic(err)
	}
	return gcm
}
