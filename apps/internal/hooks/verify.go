package hooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
)

// refuse is a delivery that failed verification, for a reason that names
// what was wrong and never quotes the delivery, so it is safe to log.
func refuse(reason string) error { return fmt.Errorf("%w: %s", ErrUnverified, reason) }

// maxSignatures bounds how many signatures one delivery may offer. A
// rotation needs two; more only costs work.
const maxSignatures = 16

// hmacCheck is an hmac_sha256 delivery read far enough to be checked: the
// signatures it offers, and what they must be a MAC of. Reading it costs no
// secret, so a delivery that carries no signature is refused before any
// secret is opened.
type hmacCheck struct {
	candidates [][]byte
	pieces     [][]byte
}

// prepareHMAC reads an hmac_sha256 delivery: its signatures, and the signed
// template filled in with the raw body, the timestamp and the delivery id as
// the delivery carried them.
func prepareHMAC(scheme integration.Webhook, d *delivery) (hmacCheck, error) {
	verify := scheme.Verify
	if verify.Signature == nil {
		return hmacCheck{}, refuse("the scheme reads no signature")
	}
	candidates := signatures(d, *verify.Signature, verify.Encoding)
	if len(candidates) == 0 {
		return hmacCheck{}, refuse("no signature")
	}
	pieces, ok := signedPieces(verify.Signed, d, verify.Timestamp, scheme.DeliveryID)
	if !ok {
		return hmacCheck{}, refuse("what the signature covers is missing")
	}
	return hmacCheck{candidates: candidates, pieces: pieces}, nil
}

// match says whether any signature is the MAC of the signed content under any
// of keys. Every pairing is compared, in constant time, whatever matched
// before it.
func (check hmacCheck) match(keys [][]byte) error {
	if len(keys) == 0 {
		return refuse("no secret")
	}
	matched := 0
	for _, key := range keys {
		mac := hmac.New(sha256.New, key)
		for _, piece := range check.pieces {
			mac.Write(piece)
		}
		sum := mac.Sum(nil)
		for _, candidate := range check.candidates {
			matched |= subtle.ConstantTimeCompare(sum, candidate)
		}
	}
	if matched != 1 {
		return refuse("no signature matches")
	}
	return nil
}

// signatures reads every signature a delivery offers: each value the locator
// finds, split when the scheme says one holds several, trimmed, with the
// prefix taken off, and decoded. A part without the prefix, or that does not
// decode, is not a signature and is skipped, as is anything past
// maxSignatures.
func signatures(d *delivery, locator integration.Locator, encoding string) [][]byte {
	values, _ := d.values(locator)
	var decoded [][]byte
	for _, value := range values {
		parts := []string{value}
		if locator.Split != "" {
			parts = strings.Split(value, locator.Split)
		}
		for _, part := range parts {
			encoded, ok := strings.CutPrefix(strings.TrimSpace(part), locator.Prefix)
			if !ok || encoded == "" {
				continue
			}
			signature, ok := decode(encoded, encoding)
			if !ok {
				continue
			}
			decoded = append(decoded, signature)
			if len(decoded) == maxSignatures {
				return decoded
			}
		}
	}
	return decoded
}

// decode reads a signature in the scheme's encoding. Base64 is accepted with
// or without padding, in either alphabet: which one an app uses is not a
// secret, and only a matching MAC passes.
func decode(encoded, encoding string) ([]byte, bool) {
	switch encoding {
	case integration.EncodingHex:
		decoded, err := hex.DecodeString(encoded)
		return decoded, err == nil
	case integration.EncodingBase64:
		for _, alphabet := range []*base64.Encoding{
			base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding,
		} {
			if decoded, err := alphabet.DecodeString(encoded); err == nil {
				return decoded, true
			}
		}
	}
	return nil, false
}

// signedPieces fills in a signed template, such as "v0:{timestamp}:{body}",
// as the pieces a MAC runs over in order: its literal text, the raw body, and
// the timestamp and delivery id as the delivery carried them. It answers
// false when the template names something the delivery lacks, or anything
// but those three.
func signedPieces(template string, d *delivery, timestamp *integration.Locator, id integration.Locator) ([][]byte, bool) {
	var pieces [][]byte
	for rest := template; rest != ""; {
		open := strings.IndexByte(rest, '{')
		if open < 0 {
			pieces = append(pieces, []byte(rest))
			break
		}
		if open > 0 {
			pieces = append(pieces, []byte(rest[:open]))
		}
		length := strings.IndexByte(rest[open:], '}')
		if length < 0 {
			return nil, false
		}
		switch rest[open+1 : open+length] {
		case "body":
			pieces = append(pieces, d.body)
		case "timestamp":
			if timestamp == nil {
				return nil, false
			}
			value, ok := d.one(*timestamp)
			if !ok {
				return nil, false
			}
			pieces = append(pieces, []byte(value))
		case "id":
			if id.Header == "" && id.JSON == "" {
				return nil, false
			}
			value, ok := d.one(id)
			if !ok {
				return nil, false
			}
			pieces = append(pieces, []byte(value))
		default:
			return nil, false
		}
		rest = rest[open+length+1:]
	}
	return pieces, true
}

// presentedTokens reads the static tokens a delivery presents: one header,
// or every value a JSON pointer reaches, as Microsoft Graph's
// /value/*/clientState reaches one per notification in a batch. Each must be
// there, with its prefix.
func presentedTokens(verify integration.Verify, d *delivery) ([]string, error) {
	if verify.Token == nil {
		return nil, refuse("the scheme reads no token")
	}
	locator := *verify.Token
	values, ok := d.values(locator)
	switch {
	case !ok || len(values) == 0:
		return nil, refuse("no token")
	case locator.Header != "" && len(values) != 1:
		return nil, refuse("more than one token header")
	}
	tokens := make([]string, 0, len(values))
	for _, value := range values {
		token, ok := strings.CutPrefix(value, locator.Prefix)
		if !ok || token == "" {
			return nil, refuse("a token is missing")
		}
		tokens = append(tokens, token)
	}
	return tokens, nil
}

// matchTokens says whether every token equals one of keys. Tokens and keys
// are compared by their SHA-256, in constant time, so neither a token's
// content nor its length shows in how long the answer takes.
func matchTokens(tokens []string, keys [][]byte) error {
	if len(keys) == 0 {
		return refuse("no secret")
	}
	digests := make([][sha256.Size]byte, len(keys))
	for i, key := range keys {
		digests[i] = sha256.Sum256(key)
	}
	every := 1
	for _, token := range tokens {
		digest := sha256.Sum256([]byte(token))
		one := 0
		for i := range digests {
			one |= subtle.ConstantTimeCompare(digest[:], digests[i][:])
		}
		every &= one
	}
	if every != 1 {
		return refuse("a token does not match")
	}
	return nil
}

// checkTolerance refuses a delivery whose timestamp is further from now than
// the scheme allows, so a captured delivery cannot be replayed later. A
// scheme that reads no timestamp checks nothing here, and relies on dedupe.
func checkTolerance(verify integration.Verify, d *delivery, now time.Time) error {
	if verify.Timestamp == nil {
		return nil
	}
	raw, ok := d.one(*verify.Timestamp)
	if !ok {
		return refuse("no timestamp")
	}
	at, ok := parseTimestamp(raw, verify.Timestamp.Unit)
	if !ok {
		return refuse("the timestamp is not a time")
	}
	tolerance := verify.EffectiveTolerance()
	if now.Sub(at) > tolerance || at.Sub(now) > tolerance {
		return refuse("the timestamp is outside the tolerance")
	}
	return nil
}

// parseTimestamp reads a Unix time, a whole number in the scheme's unit.
func parseTimestamp(raw, unit string) (time.Time, bool) {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || value <= 0 {
		return time.Time{}, false
	}
	switch unit {
	case integration.UnitSeconds:
		return time.Unix(value, 0), true
	case integration.UnitMilliseconds:
		return time.UnixMilli(value), true
	}
	return time.Time{}, false
}
