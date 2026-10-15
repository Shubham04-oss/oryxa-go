// Package egress is how a connector reaches an app: with an egress handle
// that names one connection and token kind for a few minutes, through a proxy
// that checks the handle and the manifest's host allowlist, adds the token the
// connector never sees, and sends through safehttp (design §9.5).
package egress

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

// MaxTTL is the longest a handle may live. Mint refuses more and Verify
// refuses a handle that claims more, whoever signed it.
const MaxTTL = 5 * time.Minute

const (
	handlePrefix = "eh1"
	minKeyBytes  = 32
)

var (
	// ErrHandle means a handle is malformed, signed with an unknown key or
	// forged. The three look the same to the caller on purpose.
	ErrHandle = errors.New("egress: invalid handle")
	// ErrExpired means a genuine handle is past its expiry.
	ErrExpired = errors.New("egress: handle expired")
)

// Claims is what a handle grants: one connection's token of one kind, for a
// member and agent, until Expires.
type Claims struct {
	Connection string    `json:"c"`
	TokenKind  string    `json:"k"`
	Member     string    `json:"m,omitempty"`
	Agent      string    `json:"a,omitempty"`
	Expires    time.Time `json:"-"`
	Exp        int64     `json:"e"`
}

// Keys signs handles with Current and verifies with any key it holds, so a
// key can rotate: add the new one, make it current, and drop the old one once
// its last handle has expired.
type Keys struct {
	Current string
	Keys    map[string]secrets.Secret
}

func (k Keys) key(kid string) ([]byte, bool) {
	s, ok := k.Keys[kid]
	if !ok || len(s) < minKeyBytes {
		return nil, false
	}
	return []byte(s.Reveal()), true
}

// Validate checks the key set can mint: a current key of at least 32 bytes,
// and every kid free of the "." the handle format splits on.
func (k Keys) Validate() error {
	for kid, key := range k.Keys {
		if kid == "" || strings.Contains(kid, ".") {
			return fmt.Errorf("egress: key id %q is empty or holds a dot", kid)
		}
		if len(key) < minKeyBytes {
			return fmt.Errorf("egress: key %q is shorter than %d bytes", kid, minKeyBytes)
		}
	}
	if _, ok := k.key(k.Current); !ok {
		return errors.New("egress: no usable current key")
	}
	return nil
}

func mac(key []byte, signed string) []byte {
	h := hmac.New(sha256.New, key)
	h.Write([]byte(signed))
	return h.Sum(nil)
}

// Mint returns a handle for claims, which expire at claims.Expires, at most
// MaxTTL from now.
func (k Keys) Mint(claims Claims, now time.Time) (string, error) {
	if claims.Connection == "" || claims.TokenKind == "" {
		return "", errors.New("egress: a handle names a connection and a token kind")
	}
	if !claims.Expires.After(now) || claims.Expires.Sub(now) > MaxTTL {
		return "", fmt.Errorf("egress: a handle lasts up to %s", MaxTTL)
	}
	key, ok := k.key(k.Current)
	if !ok {
		return "", errors.New("egress: no usable current key")
	}
	claims.Exp = claims.Expires.Unix()
	raw, err := json.Marshal(claims)
	if err != nil {
		return "", err
	}
	signed := handlePrefix + "." + k.Current + "." + base64.RawURLEncoding.EncodeToString(raw)
	return signed + "." + base64.RawURLEncoding.EncodeToString(mac(key, signed)), nil
}

// Verify checks handle's signature, then its expiry, and returns its claims.
func (k Keys) Verify(handle string, now time.Time) (Claims, error) {
	parts := strings.Split(handle, ".")
	if len(parts) != 4 || parts[0] != handlePrefix {
		return Claims{}, ErrHandle
	}
	key, ok := k.key(parts[1])
	if !ok {
		return Claims{}, ErrHandle
	}
	got, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil || !hmac.Equal(got, mac(key, strings.Join(parts[:3], "."))) {
		return Claims{}, ErrHandle
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Claims{}, ErrHandle
	}
	var claims Claims
	if err := json.Unmarshal(raw, &claims); err != nil || claims.Connection == "" || claims.TokenKind == "" {
		return Claims{}, ErrHandle
	}
	claims.Expires = time.Unix(claims.Exp, 0)
	if !claims.Expires.After(now) {
		return Claims{}, ErrExpired
	}
	if claims.Expires.Sub(now) > MaxTTL+time.Second {
		return Claims{}, ErrHandle
	}
	return claims, nil
}
