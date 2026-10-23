package clisession

import (
	"context"
	"crypto/rand"
	"strings"
	"time"
)

// A device sign-in (RFC 8628) lets a CLI without a local browser get the
// same setup session: it shows a short user code, a member signed in on the
// web approves it, and the CLI collects an opaque token by polling.
const (
	DeviceTTL      = 10 * time.Minute
	DeviceInterval = 5 * time.Second
	// SlowDownStep is what RFC 8628 adds to the interval on each slow_down.
	SlowDownStep = 5 * time.Second
	// DeviceTokenPrefix marks a token auth issued itself, never a Cognito JWT.
	DeviceTokenPrefix = "oryxa_cli_"
)

// UserCodeAlphabet has only consonants: no digits, no vowels (so no words),
// and nothing that reads like another character (0/O, 1/I/L, 2/Z, 5/S, 8/B).
const UserCodeAlphabet = "CDFGHJKMNPQRTVWX"

// UserCodeLength gives 16^8 (about 4.3 billion) codes, against at most a few
// thousand live ones and a rate-limited, ten-minute guessing window.
const UserCodeLength = 8

type DeviceStatus string

const (
	DevicePending  DeviceStatus = "pending"
	DeviceApproved DeviceStatus = "approved"
	DeviceDenied   DeviceStatus = "denied"
)

// Device is a pending request as the approve page shows it. Only hashes of
// the two codes are stored.
type Device struct {
	DeviceCodeHash string
	UserCodeHash   string
	ClientName     string
	ClientAddress  string
	CreatedAt      time.Time
	ExpiresAt      time.Time
	Interval       time.Duration
}

// Poll is a token request's outcome. Status is one of RFC 8628's error codes
// (authorization_pending, slow_down, expired_token, access_denied) or "" when
// the token whose hash was offered is now an admitted session for Subject.
type Poll struct {
	Status   string
	Subject  string
	Interval time.Duration
}

const (
	PollPending  = "authorization_pending"
	PollSlowDown = "slow_down"
	PollExpired  = "expired_token"
	PollDenied   = "access_denied"
)

type DeviceStore interface {
	CreateCLIDevice(context.Context, Device) error
	CLIDevice(ctx context.Context, userCodeHash string) (Device, error)
	// DecideCLIDevice moves a pending, unexpired request to approved (for
	// subject) or denied, once.
	DecideCLIDevice(ctx context.Context, userCodeHash, subject string, approve bool) error
	// PollCLIDevice answers a token request. On approval it consumes the
	// request and admits tokenHash as a session for its approver, expiring
	// at sessionExpires, in one step.
	PollCLIDevice(ctx context.Context, deviceCodeHash, tokenHash string, sessionExpires time.Time) (Poll, error)
}

// NewUserCode returns a code like "HJKM-PQRT".
func NewUserCode() string {
	var raw [UserCodeLength]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic(err)
	}
	code := make([]byte, 0, UserCodeLength+1)
	for i, b := range raw {
		if i == UserCodeLength/2 {
			code = append(code, '-')
		}
		// 256 is a multiple of 16, so this is unbiased.
		code = append(code, UserCodeAlphabet[int(b)%len(UserCodeAlphabet)])
	}
	return string(code)
}

// NormalizeUserCode accepts what a member types: any case, with or without
// the hyphen or spaces. It returns "" for anything that is not a code.
func NormalizeUserCode(input string) string {
	var code strings.Builder
	for _, r := range strings.ToUpper(input) {
		switch {
		case r == '-' || r == ' ':
			continue
		case strings.ContainsRune(UserCodeAlphabet, r):
			code.WriteRune(r)
		default:
			return ""
		}
	}
	if code.Len() != UserCodeLength {
		return ""
	}
	value := code.String()
	return value[:UserCodeLength/2] + "-" + value[UserCodeLength/2:]
}

func NewDeviceCode() string { return random() }

// NewDeviceToken is the opaque setup token a device sign-in issues.
func NewDeviceToken() string { return DeviceTokenPrefix + random() }

func IsDeviceToken(raw string) bool { return strings.HasPrefix(raw, DeviceTokenPrefix) }
