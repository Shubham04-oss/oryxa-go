package hooks

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/github"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/google"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integrations/slack"
	"github.com/Shubham04-oss/oryxa-go/internal/composio"
)

// verifyScheme runs steps 5 and 6 as the ingress does, for one scheme and
// the secrets a delivery may be signed with.
func verifyScheme(scheme integration.Webhook, header http.Header, body string, keys []string, now time.Time) error {
	d := newDelivery(header, []byte(body))
	var opened [][]byte
	for _, key := range keys {
		opened = append(opened, []byte(key))
	}
	switch scheme.Verify.Type {
	case integration.VerifyHMACSHA256:
		check, err := prepareHMAC(scheme, d)
		if err != nil {
			return err
		}
		if err := check.match(opened); err != nil {
			return err
		}
	case integration.VerifyStaticToken:
		tokens, err := presentedTokens(scheme.Verify, d)
		if err != nil {
			return err
		}
		if err := matchTokens(tokens, opened); err != nil {
			return err
		}
	default:
		return fmt.Errorf("not a scheme verifyScheme runs: %s", scheme.Verify.Type)
	}
	return checkTolerance(scheme.Verify, d, now)
}

func headerOf(pairs ...string) http.Header {
	header := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		header.Add(pairs[i], pairs[i+1])
	}
	return header
}

// Slack's own example request, from "Verifying requests from Slack":
// https://docs.slack.dev/authentication/verifying-requests-from-slack
const (
	slackExampleSecret    = "8f742231b10e8888abcd99yyyzzz85a5"
	slackExampleTimestamp = "1531420618"
	slackExampleBody      = "token=xyzz0WbapA4vBCDEFasx0q6G&team_id=T1DC2JH3J&team_domain=testteamnow&channel_id=G8PSS9T3V&channel_name=foobar&user_id=U2CERLKJA&user_name=roadrunner&command=%2Fwebhook-collect&text=&response_url=https%3A%2F%2Fhooks.slack.com%2Fcommands%2FT1DC2JH3J%2F397700885554%2F96rGlfmibIGlgcZRskXaIFfN&trigger_id=398738663015.47445629121.803a0bc887a14d10d2c447fce8b6703c"
	slackExampleSignature = "v0=a2114d57b48eac39b9ad189dd8316235a7b4a8d21a10bd27519666489c69b503"
)

func TestHMACVerifiesSlacksPublishedExample(t *testing.T) {
	scheme := *slack.New().Manifest().Webhook
	sent := time.Unix(1531420618, 0)
	header := headerOf("X-Slack-Request-Timestamp", slackExampleTimestamp, "X-Slack-Signature", slackExampleSignature)

	if err := verifyScheme(scheme, header, slackExampleBody, []string{slackExampleSecret}, sent.Add(3*time.Second)); err != nil {
		t.Fatalf("Slack's own example = %v, want verified", err)
	}
	// Five minutes either way is the edge of Slack's tolerance.
	for _, at := range []time.Time{sent.Add(300 * time.Second), sent.Add(-300 * time.Second)} {
		if err := verifyScheme(scheme, header, slackExampleBody, []string{slackExampleSecret}, at); err != nil {
			t.Fatalf("at %s from the timestamp = %v, want verified", at.Sub(sent), err)
		}
	}

	for name, check := range map[string]func() error{
		"a tampered body": func() error {
			return verifyScheme(scheme, header, strings.Replace(slackExampleBody, "text=", "text=hi", 1), []string{slackExampleSecret}, sent)
		},
		"another secret": func() error {
			return verifyScheme(scheme, header, slackExampleBody, []string{"8f742231b10e8888abcd99yyyzzz85a6"}, sent)
		},
		"another timestamp, the signature unchanged": func() error {
			moved := headerOf("X-Slack-Request-Timestamp", "1531420619", "X-Slack-Signature", slackExampleSignature)
			return verifyScheme(scheme, moved, slackExampleBody, []string{slackExampleSecret}, sent)
		},
		"an expired timestamp": func() error {
			return verifyScheme(scheme, header, slackExampleBody, []string{slackExampleSecret}, sent.Add(301*time.Second))
		},
		"a timestamp from the future": func() error {
			return verifyScheme(scheme, header, slackExampleBody, []string{slackExampleSecret}, sent.Add(-301*time.Second))
		},
		"no signature": func() error {
			return verifyScheme(scheme, headerOf("X-Slack-Request-Timestamp", slackExampleTimestamp), slackExampleBody, []string{slackExampleSecret}, sent)
		},
		"no timestamp": func() error {
			return verifyScheme(scheme, headerOf("X-Slack-Signature", slackExampleSignature), slackExampleBody, []string{slackExampleSecret}, sent)
		},
		"the signature without its v0= prefix": func() error {
			bare := headerOf("X-Slack-Request-Timestamp", slackExampleTimestamp, "X-Slack-Signature", strings.TrimPrefix(slackExampleSignature, "v0="))
			return verifyScheme(scheme, bare, slackExampleBody, []string{slackExampleSecret}, sent)
		},
		"no secret configured": func() error {
			return verifyScheme(scheme, header, slackExampleBody, nil, sent)
		},
	} {
		if err := check(); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
}

// GitHub's own example, from "Validating webhook deliveries":
// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
const (
	githubExampleSecret    = "It's a Secret to Everybody"
	githubExampleBody      = "Hello, World!"
	githubExampleSignature = "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
)

func TestHMACVerifiesGitHubsPublishedExample(t *testing.T) {
	scheme := *github.New().Manifest().Webhook
	header := headerOf("X-Hub-Signature-256", githubExampleSignature, "X-GitHub-Delivery", "72d3162e-cc78-11e3-81ab-4c9367dc0958")
	if err := verifyScheme(scheme, header, githubExampleBody, []string{githubExampleSecret}, testStart); err != nil {
		t.Fatalf("GitHub's own example = %v, want verified", err)
	}
	// Hex is read in either case.
	upper := headerOf("X-Hub-Signature-256", "sha256="+strings.ToUpper(strings.TrimPrefix(githubExampleSignature, "sha256=")))
	if err := verifyScheme(scheme, upper, githubExampleBody, []string{githubExampleSecret}, testStart); err != nil {
		t.Fatalf("the example in upper-case hex = %v, want verified", err)
	}

	for name, check := range map[string]func() error{
		"a tampered body": func() error {
			return verifyScheme(scheme, header, "Hello, World?", []string{githubExampleSecret}, testStart)
		},
		"another secret": func() error {
			return verifyScheme(scheme, header, githubExampleBody, []string{"It's a Secret to Nobody"}, testStart)
		},
		"the old sha1 header's prefix": func() error {
			sha1 := headerOf("X-Hub-Signature-256", "sha1="+strings.TrimPrefix(githubExampleSignature, "sha256="))
			return verifyScheme(scheme, sha1, githubExampleBody, []string{githubExampleSecret}, testStart)
		},
		"a signature that is not hex": func() error {
			return verifyScheme(scheme, headerOf("X-Hub-Signature-256", "sha256=zz"), githubExampleBody, []string{githubExampleSecret}, testStart)
		},
		"a truncated signature": func() error {
			return verifyScheme(scheme, headerOf("X-Hub-Signature-256", githubExampleSignature[:len(githubExampleSignature)-2]), githubExampleBody, []string{githubExampleSecret}, testStart)
		},
	} {
		if err := check(); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
}

// Linear publishes no example with values, so this one was computed apart
// from hooks, with openssl:
//
//	printf '%s' "$body" | openssl dgst -sha256 -hmac "$secret"
//
// over Linear's documented shape: a hex HMAC-SHA256 of the raw body in
// Linear-Signature, and webhookTimestamp in milliseconds inside it.
const (
	linearSecret    = "lin_wh_7d0c6f2a9b8e4d1c3f5a6b7c8d9e0f1a"
	linearBody      = `{"action":"create","type":"Issue","data":{"id":"2174add1-f7c8-44e3-bbf3-2d60b5ea8bc9","title":"Flaky test"},"webhookTimestamp":1790596800000,"webhookId":"4e3c1a9f-linear"}`
	linearSignature = "f6c7c36a28517d9e2ea2ae93ed2f31f1bc64ffee10ff59a18c26f1d5b2c3bf08"
)

func TestHMACVerifiesALinearDeliveryWithItsTimestampInTheBody(t *testing.T) {
	scheme := *linearWebhook()
	header := headerOf("Linear-Signature", linearSignature, "Linear-Delivery", "d8c1a2b3")
	if err := verifyScheme(scheme, header, linearBody, []string{linearSecret}, testStart.Add(59*time.Second)); err != nil {
		t.Fatalf("a Linear delivery 59s old = %v, want verified", err)
	}
	for name, check := range map[string]func() error{
		"61 seconds old, past Linear's minute": func() error {
			return verifyScheme(scheme, header, linearBody, []string{linearSecret}, testStart.Add(61*time.Second))
		},
		"61 seconds early": func() error {
			return verifyScheme(scheme, header, linearBody, []string{linearSecret}, testStart.Add(-61*time.Second))
		},
		"a moved timestamp, the signature unchanged": func() error {
			moved := strings.Replace(linearBody, "1790596800000", "1790596860000", 1)
			return verifyScheme(scheme, header, moved, []string{linearSecret}, testStart.Add(61*time.Second))
		},
		"another secret": func() error {
			return verifyScheme(scheme, header, linearBody, []string{"lin_wh_other"}, testStart)
		},
		"no timestamp in the body": func() error {
			body := `{"action":"create","type":"Issue"}`
			return verifyScheme(scheme, headerOf("Linear-Signature", hmacHex(linearSecret, body)), body, []string{linearSecret}, testStart)
		},
		"a timestamp that is a string of words": func() error {
			body := `{"action":"create","webhookTimestamp":"soon"}`
			return verifyScheme(scheme, headerOf("Linear-Signature", hmacHex(linearSecret, body)), body, []string{linearSecret}, testStart)
		},
	} {
		if err := check(); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
}

// A Standard Webhooks delivery, computed with openssl:
//
//	printf '%s' "$id.$timestamp.$body" | openssl dgst -sha256 -hmac "$secret" -binary | base64
const (
	standardSecret    = "whsec_test_standard_webhooks"
	standardID        = "msg_2f1c"
	standardTimestamp = "1790596800"
	standardBody      = `{"test": 2432232314}`
	standardSignature = "v1,bgYrDOR/8KL6cZV6K/4RFHVztlq0F/PtINNZYtWn4Ys="
)

func TestHMACSplitSignaturesAcceptAnyOneDuringARotation(t *testing.T) {
	scheme := *standardWebhook()
	sign := func(signatures string) http.Header {
		return headerOf("webhook-id", standardID, "webhook-timestamp", standardTimestamp, "webhook-signature", signatures)
	}
	old := "v1," + hmacBase64("whsec_rotated_out", standardID, ".", standardTimestamp, ".", standardBody)

	for name, signatures := range map[string]string{
		"the computed vector alone":                       standardSignature,
		"the old secret's signature, then the new one's":  old + " " + standardSignature,
		"the new one's, then the old one's":               standardSignature + " " + old,
		"a signature of another version, then a good one": "v2,AAAA " + standardSignature,
		"extra spaces between them":                       old + "   " + standardSignature,
	} {
		if err := verifyScheme(scheme, sign(signatures), standardBody, []string{standardSecret}, testStart); err != nil {
			t.Errorf("%s = %v, want verified", name, err)
		}
	}
	// Both secrets verify while a rotation overlaps.
	if err := verifyScheme(scheme, sign(old), standardBody, []string{standardSecret, "whsec_rotated_out"}, testStart); err != nil {
		t.Errorf("the previous secret's signature, with both secrets = %v, want verified", err)
	}
	for name, signatures := range map[string]string{
		"only the old secret's signature":           old,
		"the right signature under another version": strings.Replace(standardSignature, "v1,", "v2,", 1),
		"only signatures that do not decode":        "v1,*** v1,@@@",
		"the right signature split in two":          standardSignature[:20] + " " + standardSignature[20:],
	} {
		if err := verifyScheme(scheme, sign(signatures), standardBody, []string{standardSecret}, testStart); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
	// A header holding more than maxSignatures is read only that far.
	crowded := strings.Repeat(old+" ", maxSignatures) + standardSignature
	if err := verifyScheme(scheme, sign(crowded), standardBody, []string{standardSecret}, testStart); !errors.Is(err, ErrUnverified) {
		t.Errorf("a good signature after %d others = %v, want unverified", maxSignatures, err)
	}
}

// TestHMACAgreesWithComposiosVerifier checks the generic scheme against the
// verifier apps already trusts for Composio, which signs the same way: both
// must accept and refuse exactly the same deliveries.
func TestHMACAgreesWithComposiosVerifier(t *testing.T) {
	scheme := *standardWebhook()
	now := time.Unix(1790596800, 0)
	body := `{"id":"msg_1","type":"composio.trigger.message"}`
	signed := func(secret, id string, at time.Time, body string) http.Header {
		timestamp := fmt.Sprint(at.Unix())
		return headerOf("webhook-id", id, "webhook-timestamp", timestamp,
			"webhook-signature", "v1,"+hmacBase64(secret, id, ".", timestamp, ".", body))
	}
	cases := map[string]struct {
		header http.Header
		body   string
	}{
		"genuine":                {signed(standardSecret, "msg_1", now, body), body},
		"another secret":         {signed("whsec_attacker", "msg_1", now, body), body},
		"a changed body":         {signed(standardSecret, "msg_1", now, body), body + " "},
		"four minutes old":       {signed(standardSecret, "msg_1", now.Add(-4*time.Minute), body), body},
		"six minutes old":        {signed(standardSecret, "msg_1", now.Add(-6*time.Minute), body), body},
		"six minutes early":      {signed(standardSecret, "msg_1", now.Add(6*time.Minute), body), body},
		"no signature":           {headerOf("webhook-id", "msg_1", "webhook-timestamp", fmt.Sprint(now.Unix())), body},
		"no id":                  {headerOf("webhook-timestamp", fmt.Sprint(now.Unix()), "webhook-signature", signed(standardSecret, "msg_1", now, body).Get("webhook-signature")), body},
		"no timestamp":           {headerOf("webhook-id", "msg_1", "webhook-signature", signed(standardSecret, "msg_1", now, body).Get("webhook-signature")), body},
		"a timestamp of letters": {headerOf("webhook-id", "msg_1", "webhook-timestamp", "noon", "webhook-signature", "v1,AAAA"), body},
	}
	moved := signed(standardSecret, "msg_1", now, body)
	moved.Set("webhook-id", "msg_2")
	cases["another id, the signature unchanged"] = struct {
		header http.Header
		body   string
	}{moved, body}
	rotated := signed(standardSecret, "msg_1", now, body)
	rotated.Set("webhook-signature", signed("whsec_old", "msg_1", now, body).Get("webhook-signature")+" "+rotated.Get("webhook-signature"))
	cases["one of two signatures"] = struct {
		header http.Header
		body   string
	}{rotated, body}

	for name, c := range cases {
		composioErr := composio.VerifyWebhook(standardSecret, c.header, []byte(c.body), now)
		hooksErr := verifyScheme(scheme, c.header, c.body, []string{standardSecret}, now)
		if (composioErr == nil) != (hooksErr == nil) {
			t.Errorf("%s: composio = %v, hooks = %v; they must agree", name, composioErr, hooksErr)
		}
	}
}

func TestStaticTokenInAHeaderMustBeTheOneToken(t *testing.T) {
	scheme := *google.ChannelWebhook()
	const token = "c2VjcmV0LWNoYW5uZWwtdG9rZW4tZm9yLWEtdGVzdA"
	if err := verifyScheme(scheme, headerOf("X-Goog-Channel-Token", token), "", []string{token}, testStart); err != nil {
		t.Fatalf("the channel's own token = %v, want verified", err)
	}
	if err := verifyScheme(scheme, headerOf("X-Goog-Channel-Token", "old-token"), "", []string{token, "old-token"}, testStart); err != nil {
		t.Fatalf("the previous token during a rotation = %v, want verified", err)
	}
	for name, header := range map[string]http.Header{
		"another token":         headerOf("X-Goog-Channel-Token", token+"x"),
		"a prefix of the token": headerOf("X-Goog-Channel-Token", token[:10]),
		"no token":              headerOf("X-Goog-Resource-State", "change"),
		"an empty token":        headerOf("X-Goog-Channel-Token", ""),
		"two tokens":            headerOf("X-Goog-Channel-Token", token, "X-Goog-Channel-Token", token),
	} {
		if err := verifyScheme(scheme, header, "", []string{token}, testStart); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
}

func TestStaticTokenGraphPointerNeedsEveryElementToMatch(t *testing.T) {
	scheme := *graphWebhook()
	const state = "graph-client-state-0123456789"
	for name, body := range map[string]string{
		"one notification":     `{"value":[{"clientState":"graph-client-state-0123456789","changeType":"created"}]}`,
		"a batch of three":     `{"value":[{"clientState":"graph-client-state-0123456789"},{"clientState":"graph-client-state-0123456789"},{"clientState":"graph-client-state-0123456789"}]}`,
		"an escaped character": `{"value":[{"clientState":"graph-client-state-0123456789","resource":"Users/a\/b"}]}`,
	} {
		if err := verifyScheme(scheme, nil, body, []string{state}, testStart); err != nil {
			t.Errorf("%s = %v, want verified", name, err)
		}
	}
	// During a rotation each notification may carry either secret.
	mixed := `{"value":[{"clientState":"graph-client-state-0123456789"},{"clientState":"previous-client-state"}]}`
	if err := verifyScheme(scheme, nil, mixed, []string{state, "previous-client-state"}, testStart); err != nil {
		t.Errorf("a batch across a rotation = %v, want verified", err)
	}

	for name, body := range map[string]string{
		"one of two forged":         `{"value":[{"clientState":"graph-client-state-0123456789"},{"clientState":"forged"}]}`,
		"the forged one first":      `{"value":[{"clientState":"forged"},{"clientState":"graph-client-state-0123456789"}]}`,
		"one without a clientState": `{"value":[{"clientState":"graph-client-state-0123456789"},{"changeType":"created"}]}`,
		"an empty batch":            `{"value":[]}`,
		"no value":                  `{"notvalue":[{"clientState":"graph-client-state-0123456789"}]}`,
		"value an object":           `{"value":{"a":{"clientState":"graph-client-state-0123456789"}}}`,
		"a clientState number":      `{"value":[{"clientState":12345}]}`,
		"a clientState object":      `{"value":[{"clientState":{"value":"graph-client-state-0123456789"}}]}`,
		"not JSON":                  `value=graph-client-state-0123456789`,
		"JSON with trailing text":   `{"value":[{"clientState":"graph-client-state-0123456789"}]} trailing`,
	} {
		if err := verifyScheme(scheme, nil, body, []string{state}, testStart); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want unverified", name, err)
		}
	}
}

func TestTimestampsInSecondsOrMillisecondsAndTheDefaultTolerance(t *testing.T) {
	seconds := integration.Verify{Timestamp: &integration.Locator{Header: "T", Unit: integration.UnitSeconds}}
	milliseconds := integration.Verify{Timestamp: &integration.Locator{JSON: "/at", Unit: integration.UnitMilliseconds}, Tolerance: 60}
	at := testStart

	check := func(verify integration.Verify, header http.Header, body string, now time.Time) error {
		return checkTolerance(verify, newDelivery(header, []byte(body)), now)
	}
	if err := check(seconds, headerOf("T", fmt.Sprint(at.Unix())), "", at.Add(299*time.Second)); err != nil {
		t.Errorf("seconds, within the default 300 = %v", err)
	}
	if err := check(seconds, headerOf("T", fmt.Sprint(at.Unix())), "", at.Add(301*time.Second)); !errors.Is(err, ErrUnverified) {
		t.Errorf("seconds, past the default 300 = %v, want unverified", err)
	}
	if err := check(milliseconds, nil, fmt.Sprintf(`{"at":%d}`, at.UnixMilli()), at.Add(60*time.Second)); err != nil {
		t.Errorf("milliseconds in the body, at the tolerance = %v", err)
	}
	if err := check(milliseconds, nil, fmt.Sprintf(`{"at":%d}`, at.UnixMilli()), at.Add(60*time.Second+time.Millisecond)); !errors.Is(err, ErrUnverified) {
		t.Errorf("milliseconds, a millisecond past the tolerance = %v, want unverified", err)
	}
	// Read as seconds, a millisecond timestamp is far in the future.
	if err := check(seconds, headerOf("T", fmt.Sprint(at.UnixMilli())), "", at); !errors.Is(err, ErrUnverified) {
		t.Errorf("milliseconds read as seconds = %v, want unverified", err)
	}
	for _, raw := range []string{"", "0", "-5", "1.5e9", "12abc", "99999999999999999999999"} {
		if err := check(seconds, headerOf("T", raw), "", at); !errors.Is(err, ErrUnverified) {
			t.Errorf("timestamp %q = %v, want unverified", raw, err)
		}
	}
	if err := check(integration.Verify{}, nil, "", at); err != nil {
		t.Errorf("a scheme without a timestamp = %v, want nothing checked", err)
	}
}

func TestJSONPointersFollowRFC6901(t *testing.T) {
	d := newDelivery(nil, []byte(`{"a/b":{"m~n":[10,{"x":"y"}]},"n":1790596800000,"f":1.5e3,"t":true,"":"empty key"}`))
	document, ok := d.json()
	if !ok {
		t.Fatal("the document did not parse")
	}
	found := map[string]string{
		"/a~1b/m~0n/1/x": "y",
		"/a~1b/m~0n/0":   "10",
		"/n":             "1790596800000",
		"/f":             "1.5e3",
		"/":              "empty key",
	}
	for pointer, want := range found {
		values, ok := resolve(document, pointer)
		if !ok || len(values) != 1 {
			t.Errorf("resolve(%q) = %v, %v", pointer, values, ok)
			continue
		}
		if got, _ := scalar(values[0]); got != want {
			t.Errorf("resolve(%q) = %q, want %q", pointer, got, want)
		}
	}
	for _, pointer := range []string{"", "a", "/missing", "/a~1b/m~0n/01", "/a~1b/m~0n/-", "/a~1b/m~0n/2", "/a~1b/m~0n/x", "/n/0", "/a~1b/*"} {
		if values, ok := resolve(document, pointer); ok {
			t.Errorf("resolve(%q) = %v, want nothing", pointer, values)
		}
	}
	if values, ok := d.values(integration.Locator{JSON: "/t"}); ok {
		t.Errorf("a boolean read as text = %v, want nothing", values)
	}
}

func TestDedupeKeysKeepPlainIdsAndHashTheRest(t *testing.T) {
	if got := keyPart("72d3162e-cc78-11e3-81ab-4c9367dc0958"); got != "72d3162e-cc78-11e3-81ab-4c9367dc0958" {
		t.Errorf("a plain id = %q", got)
	}
	for _, id := range []string{strings.Repeat("a", maxKeyPart+1), "tab\there", "bad\xffutf8", "nul\x00"} {
		if got := keyPart(id); !strings.HasPrefix(got, "sha256:") || len(got) != len("sha256:")+64 {
			t.Errorf("keyPart(%q) = %q, want a hash", id, got)
		}
	}
	if keyPart("a\tb") == keyPart("a\tc") {
		t.Error("two ids hash alike")
	}
}

func TestBase64SignaturesDecodeInEitherAlphabet(t *testing.T) {
	raw := []byte{0xfb, 0xff, 0xbf, 0x01}
	for _, encoded := range []string{"+/+/AQ==", "+/+/AQ", "-_-_AQ==", "-_-_AQ"} {
		decoded, ok := decode(encoded, integration.EncodingBase64)
		if !ok || string(decoded) != string(raw) {
			t.Errorf("decode(%q) = %x, %v", encoded, decoded, ok)
		}
	}
	if _, ok := decode("not base64!", integration.EncodingBase64); ok {
		t.Error("decoded something that is not base64")
	}
	if _, ok := decode("abcd", "rot13"); ok {
		t.Error("decoded an encoding hooks does not know")
	}
}
