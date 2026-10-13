package pipedream

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"
)

const testSigningKey = "whsk_test"

// sign is the docs' own example, written out apart from SignWebhook and
// VerifyWebhook, so a test proves both agree with Pipedream and not only with
// each other.
func sign(key, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(timestamp + "." + string(body)))
	return hex.EncodeToString(mac.Sum(nil))
}

func signed(value string) http.Header {
	header := http.Header{}
	header.Set(SignatureHeader, value)
	return header
}

func TestVerifyWebhook(t *testing.T) {
	now := time.Unix(1760000000, 0)
	body := []byte(`{"id":"evt_1","data":{"subject":"hello"}}`)
	stamp := strconv.FormatInt(now.Unix(), 10)
	valid := "t=" + stamp + ",v1=" + sign(testSigningKey, stamp, body)
	stale := strconv.FormatInt(now.Add(-6*time.Minute).Unix(), 10)
	future := strconv.FormatInt(now.Add(6*time.Minute).Unix(), 10)

	if err := VerifyWebhook(testSigningKey, signed(valid), body, now); err != nil {
		t.Fatalf("a valid delivery = %v", err)
	}
	if err := VerifyWebhook(testSigningKey, signed(SignWebhook(testSigningKey, body, now)), body, now.Add(4*time.Minute)); err != nil {
		t.Fatalf("SignWebhook's own, four minutes on = %v", err)
	}
	if err := VerifyWebhook(testSigningKey, signed("t="+stamp+",v1="+sign("old", stamp, body)+",v1="+sign(testSigningKey, stamp, body)), body, now); err != nil {
		t.Fatalf("a second v1 that matches = %v", err)
	}

	for name, attempt := range map[string]struct {
		key, header string
		body        []byte
	}{
		"the wrong key":       {"whsk_other", valid, body},
		"a changed body":      {testSigningKey, valid, []byte(`{"id":"evt_2"}`)},
		"a stale timestamp":   {testSigningKey, "t=" + stale + ",v1=" + sign(testSigningKey, stale, body), body},
		"a future timestamp":  {testSigningKey, "t=" + future + ",v1=" + sign(testSigningKey, future, body), body},
		"no header":           {testSigningKey, "", body},
		"no timestamp":        {testSigningKey, "v1=" + sign(testSigningKey, stamp, body), body},
		"no signature":        {testSigningKey, "t=" + stamp, body},
		"a non-hex signature": {testSigningKey, "t=" + stamp + ",v1=zz", body},
		"a non-numeric time":  {testSigningKey, "t=soon,v1=" + sign(testSigningKey, "soon", body), body},
		"no equals sign":      {testSigningKey, "t" + stamp + "," + sign(testSigningKey, stamp, body), body},
		"a v0 signature":      {testSigningKey, "t=" + stamp + ",v0=" + sign(testSigningKey, stamp, body), body},
		"no key configured":   {"", valid, body},
	} {
		if err := VerifyWebhook(attempt.key, signed(attempt.header), attempt.body, now); !errors.Is(err, ErrUnverified) {
			t.Errorf("%s = %v, want ErrUnverified", name, err)
		}
	}
}
