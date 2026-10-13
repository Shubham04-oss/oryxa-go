package pipedream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SignatureHeader carries a trigger delivery's signature.
const SignatureHeader = "x-pd-signature"

// WebhookTolerance is how far a delivery's timestamp may be from now: the
// five minutes Pipedream's docs suggest, so a captured delivery cannot be
// replayed later.
const WebhookTolerance = 5 * time.Minute

// ErrUnverified means a trigger delivery's signature does not prove it came
// from Pipedream. Nothing in such a delivery may be used.
var ErrUnverified = errors.New("the webhook delivery is not signed by pipedream")

// VerifyWebhook proves a trigger delivery came from Pipedream, as
// https://pipedream.com/docs/connect/webhooks#webhook-signatures documents
// it: an x-pd-signature header of the form "t=<unix seconds>,v1=<hex>",
// where v1 is an HMAC-SHA256, keyed with the trigger's signing key (the
// string as given, not decoded), over "<t>.<raw body>". Several v1 values
// are accepted, should Pipedream ever send one per key while a key rotates.
func VerifyWebhook(signingKey string, header http.Header, body []byte, now time.Time) error {
	if signingKey == "" {
		return fmt.Errorf("%w: no signing key is configured", ErrUnverified)
	}
	value := header.Get(SignatureHeader)
	if value == "" {
		return fmt.Errorf("%w: the signature header is missing", ErrUnverified)
	}
	var timestamp string
	var signatures [][]byte
	for _, part := range strings.Split(value, ",") {
		name, field, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			return fmt.Errorf("%w: the signature header is malformed", ErrUnverified)
		}
		switch name {
		case "t":
			timestamp = field
		case "v1":
			if decoded, err := hex.DecodeString(field); err == nil {
				signatures = append(signatures, decoded)
			}
		}
	}
	if timestamp == "" || len(signatures) == 0 {
		return fmt.Errorf("%w: the signature header has no timestamp or signature", ErrUnverified)
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return fmt.Errorf("%w: the timestamp is not a number", ErrUnverified)
	}
	if age := now.Sub(time.Unix(seconds, 0)); age > WebhookTolerance || age < -WebhookTolerance {
		return fmt.Errorf("%w: the timestamp is outside the tolerance", ErrUnverified)
	}

	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	expected := mac.Sum(nil)
	for _, signature := range signatures {
		if hmac.Equal(signature, expected) {
			return nil
		}
	}
	return fmt.Errorf("%w: no signature matches", ErrUnverified)
}

// SignWebhook makes the x-pd-signature value for body at now, the inverse
// of VerifyWebhook, for fakes and tests that deliver as Pipedream would.
func SignWebhook(signingKey string, body []byte, now time.Time) string {
	timestamp := strconv.FormatInt(now.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(signingKey))
	mac.Write([]byte(timestamp + "."))
	mac.Write(body)
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}
