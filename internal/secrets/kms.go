package secrets

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/aws/aws-sdk-go-v2/service/kms/types"

	"github.com/Shubham04-oss/oryxa-go/internal/kmscall"
)

type KMSAPI interface {
	GenerateDataKey(context.Context, *kms.GenerateDataKeyInput, ...func(*kms.Options)) (*kms.GenerateDataKeyOutput, error)
	Decrypt(context.Context, *kms.DecryptInput, ...func(*kms.Options)) (*kms.DecryptOutput, error)
}

// KMSDataKeys implements envelope data-key generation. KMS authenticates the
// encryption context, and decrypt must present exactly the same one.
type KMSDataKeys struct {
	Client KMSAPI
	KeyID  string
}

func (k KMSDataKeys) GenerateDataKey(ctx context.Context, keyID string, encryptionContext map[string]string) ([]byte, []byte, error) {
	if keyID == "" {
		keyID = k.KeyID
	}
	if k.Client == nil || keyID == "" {
		return nil, nil, fmt.Errorf("kms client and key ID are required")
	}
	ctx, cancel := kmscall.Context(ctx)
	defer cancel()
	output, err := k.Client.GenerateDataKey(ctx, &kms.GenerateDataKeyInput{
		KeyId:             &keyID,
		KeySpec:           types.DataKeySpecAes256,
		EncryptionContext: encryptionContext,
	})
	if err != nil {
		return nil, nil, err
	}
	return output.Plaintext, output.CiphertextBlob, nil
}

func (k KMSDataKeys) DecryptDataKey(ctx context.Context, ciphertext []byte, encryptionContext map[string]string) ([]byte, error) {
	if k.Client == nil || k.KeyID == "" {
		return nil, fmt.Errorf("kms client and key ID are required")
	}
	ctx, cancel := kmscall.Context(ctx)
	defer cancel()
	output, err := k.Client.Decrypt(ctx, &kms.DecryptInput{
		CiphertextBlob:    ciphertext,
		EncryptionContext: encryptionContext,
		KeyId:             &k.KeyID,
	})
	if err != nil {
		return nil, err
	}
	return output.Plaintext, nil
}
