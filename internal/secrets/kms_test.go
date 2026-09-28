package secrets

import (
	"context"
	"maps"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"
)

type fakeKMS struct {
	generate *kms.GenerateDataKeyInput
	decrypt  *kms.DecryptInput
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, input *kms.GenerateDataKeyInput, _ ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error) {
	f.generate = input
	return &kms.GenerateDataKeyOutput{Plaintext: make([]byte, 32), CiphertextBlob: []byte("wrapped")}, nil
}

func (f *fakeKMS) Decrypt(_ context.Context, input *kms.DecryptInput, _ ...func(*kms.Options)) (*kms.DecryptOutput, error) {
	f.decrypt = input
	return &kms.DecryptOutput{Plaintext: make([]byte, 32)}, nil
}

func TestKMSDataKeysUsesAES256AndSameContext(t *testing.T) {
	t.Parallel()
	client := &fakeKMS{}
	keys := KMSDataKeys{Client: client, KeyID: "key-id"}
	contextValues := Binding{ResourceType: "credential", ResourceID: "connection-1"}.Context("oryxa-apps")

	if _, _, err := keys.GenerateDataKey(context.Background(), "", contextValues); err != nil {
		t.Fatal(err)
	}
	if got := client.generate.KeySpec; got != types.DataKeySpecAes256 {
		t.Fatalf("key spec = %q", got)
	}
	if client.generate.KeyId == nil || *client.generate.KeyId != "key-id" {
		t.Fatalf("key ID = %v", client.generate.KeyId)
	}
	if _, err := keys.DecryptDataKey(context.Background(), []byte("wrapped"), contextValues); err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(client.generate.EncryptionContext, client.decrypt.EncryptionContext) {
		t.Fatal("decrypt did not use the generate encryption context")
	}
	if got := client.decrypt.EncryptionContext["service"]; got != "oryxa-apps" {
		t.Fatalf("encryption context service = %q, want oryxa-apps", got)
	}
}

func TestKMSDataKeysRequiresClientAndKey(t *testing.T) {
	t.Parallel()
	if _, _, err := (KMSDataKeys{KeyID: "key-id"}).GenerateDataKey(context.Background(), "", nil); err == nil {
		t.Fatal("generate without a client succeeded")
	}
	if _, err := (KMSDataKeys{Client: &fakeKMS{}}).DecryptDataKey(context.Background(), []byte("wrapped"), nil); err == nil {
		t.Fatal("decrypt without a key ID succeeded")
	}
}
