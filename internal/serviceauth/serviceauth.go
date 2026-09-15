// Package serviceauth proves which Oryxa service is calling another one. A
// caller signs a short-lived JWT with its own KMS asymmetric key, naming
// itself, the audience and the purpose; the callee verifies it with the
// caller's public key and a per-caller allowlist of purposes. IAM decides
// which runtime may sign with a key.
package serviceauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// Header carries a service token on a request.
const Header = "X-Oryxa-Service-Token"

// MaxLifetime is the longest a service token may live.
const MaxLifetime = 60 * time.Second

var (
	// ErrInvalidToken means the token is malformed, unsigned by its caller,
	// expired, or meant for another audience.
	ErrInvalidToken = errors.New("invalid service token")
	// ErrPurposeNotAllowed means the caller may not use the purpose it named.
	ErrPurposeNotAllowed = errors.New("service purpose not allowed")
)

// namePattern bounds service, audience and purpose names so they are safe to
// use as map keys and to write to logs.
var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// validateName reports whether name is a service, audience or purpose
// identifier this package will accept.
func validateName(name string) error {
	if !namePattern.MatchString(name) {
		return fmt.Errorf("invalid name %q", name)
	}
	return nil
}

// Claims are what a verified token says.
type Claims struct {
	Service  string
	Audience string
	Purpose  string
	ID       string
	IssuedAt time.Time
	Expiry   time.Time
}

// Caller is one service allowed to call, its KMS key, and its purposes.
type Caller struct {
	Service  string   `json:"service"`
	KeyID    string   `json:"key_id"`
	Purposes []string `json:"purposes"`
}

// ParseCallers reads a JSON list of callers, such as
// [{"service":"worker","key_id":"arn:aws:kms:…","purposes":["action","trigger","cleanup"]}].
func ParseCallers(raw string) ([]Caller, error) {
	if strings.TrimSpace(raw) == "" {
		return []Caller{}, nil
	}

	var callers []Caller
	if err := json.Unmarshal([]byte(raw), &callers); err != nil {
		return nil, fmt.Errorf("parse callers: %w", err)
	}

	seenServices := make(map[string]bool, len(callers))
	for _, caller := range callers {
		if err := validateName(caller.Service); err != nil {
			return nil, fmt.Errorf("caller service: %w", err)
		}
		if seenServices[caller.Service] {
			return nil, fmt.Errorf("duplicate caller service %q", caller.Service)
		}
		seenServices[caller.Service] = true

		if caller.KeyID == "" {
			return nil, fmt.Errorf("caller %q has no key_id", caller.Service)
		}
		if len(caller.Purposes) == 0 {
			return nil, fmt.Errorf("caller %q has no purposes", caller.Service)
		}

		seenPurposes := make(map[string]bool, len(caller.Purposes))
		for _, purpose := range caller.Purposes {
			if err := validateName(purpose); err != nil {
				return nil, fmt.Errorf("caller %q purpose: %w", caller.Service, err)
			}
			if seenPurposes[purpose] {
				return nil, fmt.Errorf("caller %q has duplicate purpose %q", caller.Service, purpose)
			}
			seenPurposes[purpose] = true
		}
	}
	return callers, nil
}
