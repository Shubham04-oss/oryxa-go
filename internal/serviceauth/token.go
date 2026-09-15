package serviceauth

import (
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
)

// headerJSON is the fixed JOSE header for every token this package issues.
// Verify accepts only a header that decodes to this algorithm.
const headerJSON = `{"alg":"ES256","typ":"JWT"}`

// headerSegment is headerJSON, base64url-encoded once at init since it never
// changes.
var headerSegment = base64.RawURLEncoding.EncodeToString([]byte(headerJSON))

// joseHeader is what Verify decodes a token's header into. Crit exists only
// to detect a "crit" parameter: RFC 7515 requires rejecting a critical
// extension the verifier does not implement, and this package implements
// none.
type joseHeader struct {
	Alg  string          `json:"alg"`
	Crit json.RawMessage `json:"crit,omitempty"`
}

// wireClaims is the JSON a Signer writes.
type wireClaims struct {
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	Purpose  string `json:"purpose"`
	IssuedAt int64  `json:"iat"`
	Expiry   int64  `json:"exp"`
	ID       string `json:"jti"`
}

// wireClaimsIn is the JSON a Verifier reads. Audience stays raw so Verify can
// reject a JSON array instead of silently coercing it, and the timestamps
// stay pointers so a missing iat/exp is distinguishable from an explicit
// zero.
type wireClaimsIn struct {
	Issuer   string          `json:"iss"`
	Audience json.RawMessage `json:"aud"`
	Purpose  string          `json:"purpose"`
	IssuedAt *int64          `json:"iat"`
	Expiry   *int64          `json:"exp"`
	ID       string          `json:"jti"`
}

// errMalformedSignature means KMS (or a hand-crafted test) returned bytes
// that are not a well-formed ECDSA-Sig-Value DER signature.
var errMalformedSignature = errors.New("malformed kms signature")

// joseSignature converts an ASN.1 DER ECDSA signature, as KMS's Sign
// operation returns it, into the 64-byte JOSE r||s form, left-padding each
// component to 32 bytes. It rejects anything that cannot be a P-256
// signature: r or s that is missing, non-positive, larger than 32 bytes, or
// followed by trailing bytes.
func joseSignature(der []byte) ([]byte, error) {
	var parsed struct {
		R, S *big.Int
	}
	rest, err := asn1.Unmarshal(der, &parsed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errMalformedSignature, err)
	}
	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: trailing data", errMalformedSignature)
	}
	if parsed.R == nil || parsed.S == nil || parsed.R.Sign() <= 0 || parsed.S.Sign() <= 0 {
		return nil, fmt.Errorf("%w: non-positive component", errMalformedSignature)
	}
	if parsed.R.BitLen() > 256 || parsed.S.BitLen() > 256 {
		return nil, fmt.Errorf("%w: component exceeds 32 bytes", errMalformedSignature)
	}

	signature := make([]byte, 64)
	parsed.R.FillBytes(signature[:32])
	parsed.S.FillBytes(signature[32:])
	return signature, nil
}
