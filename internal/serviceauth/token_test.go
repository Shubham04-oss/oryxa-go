package serviceauth

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/asn1"
	"math/big"
	"testing"
)

// ecdsaSigValue mirrors the ECDSA-Sig-Value ASN.1 type KMS returns from
// Sign, letting tests build DER signatures without a KMS client.
type ecdsaSigValue struct {
	R, S *big.Int
}

func TestJOSESignatureRoundTrip(t *testing.T) {
	t.Parallel()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := bytes.Repeat([]byte{0xAB}, 32)
	der, err := ecdsa.SignASN1(rand.Reader, key, digest)
	if err != nil {
		t.Fatal(err)
	}

	signature, err := joseSignature(der)
	if err != nil {
		t.Fatalf("joseSignature: %v", err)
	}
	if len(signature) != 64 {
		t.Fatalf("len(signature) = %d, want 64", len(signature))
	}
	r := new(big.Int).SetBytes(signature[:32])
	s := new(big.Int).SetBytes(signature[32:])
	if !ecdsa.Verify(&key.PublicKey, digest, r, s) {
		t.Fatal("converted signature does not verify")
	}
}

func TestJOSESignaturePadsShortComponents(t *testing.T) {
	t.Parallel()
	der, err := asn1.Marshal(ecdsaSigValue{R: big.NewInt(5), S: big.NewInt(300)})
	if err != nil {
		t.Fatal(err)
	}

	signature, err := joseSignature(der)
	if err != nil {
		t.Fatalf("joseSignature: %v", err)
	}
	if len(signature) != 64 {
		t.Fatalf("len(signature) = %d, want 64", len(signature))
	}

	wantR := make([]byte, 32)
	big.NewInt(5).FillBytes(wantR)
	wantS := make([]byte, 32)
	big.NewInt(300).FillBytes(wantS)
	if !bytes.Equal(signature[:32], wantR) {
		t.Fatalf("r = %x, want %x", signature[:32], wantR)
	}
	if !bytes.Equal(signature[32:], wantS) {
		t.Fatalf("s = %x, want %x", signature[32:], wantS)
	}
}

func TestJOSESignatureRejectsMalformedDER(t *testing.T) {
	t.Parallel()
	validDER := mustMarshalASN1(t, ecdsaSigValue{R: big.NewInt(1), S: big.NewInt(2)})
	oversized := new(big.Int).Lsh(big.NewInt(1), 300) // 301 bits, more than 32 bytes

	cases := []struct {
		name string
		der  []byte
	}{
		{"garbage bytes", []byte{0x01, 0x02, 0x03, 0x04}},
		{"empty", nil},
		{"trailing data", append(append([]byte(nil), validDER...), 0x00)},
		{"not a sequence", mustMarshalASN1(t, big.NewInt(5))},
		{"sequence missing s", mustMarshalASN1(t, struct{ R *big.Int }{big.NewInt(5)})},
		{"negative r", mustMarshalASN1(t, ecdsaSigValue{R: big.NewInt(-5), S: big.NewInt(2)})},
		{"negative s", mustMarshalASN1(t, ecdsaSigValue{R: big.NewInt(2), S: big.NewInt(-5)})},
		{"r exceeds 32 bytes", mustMarshalASN1(t, ecdsaSigValue{R: oversized, S: big.NewInt(2)})},
		{"s exceeds 32 bytes", mustMarshalASN1(t, ecdsaSigValue{R: big.NewInt(2), S: oversized})},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := joseSignature(tc.der); err == nil {
				t.Fatal("expected error, got nil")
			}
		})
	}
}

func mustMarshalASN1(t *testing.T, v any) []byte {
	t.Helper()
	der, err := asn1.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return der
}
