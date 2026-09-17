package providers

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// fixedNow is shared by every test file in this package so deliveries and
// registrations verify against one deterministic clock.
var fixedNow = time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC)

// hmacHex is the hex HMAC-SHA256 of message under secret, for building valid
// signatures in tests.
func hmacHex(secret, message []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(message)
	return hex.EncodeToString(mac.Sum(nil))
}

func TestValidHMACSHA256(t *testing.T) {
	secret, message := []byte("top-secret"), []byte(`{"hello":"world"}`)
	valid := hmacHex(secret, message)

	if !ValidHMACSHA256(secret, message, valid) {
		t.Errorf("ValidHMACSHA256(valid) = false, want true")
	}
	if ValidHMACSHA256([]byte("wrong-secret"), message, valid) {
		t.Errorf("ValidHMACSHA256(wrong secret) = true, want false")
	}
	if ValidHMACSHA256(secret, message, "not-hex-zz") {
		t.Errorf("ValidHMACSHA256(non-hex) = true, want false")
	}
	if ValidHMACSHA256(secret, message, "ab") {
		t.Errorf("ValidHMACSHA256(wrong length) = true, want false")
	}
}

func TestDigest(t *testing.T) {
	first := Digest([]byte("a"), []byte("b"))
	second := Digest([]byte("a"), []byte("b"))
	if first != second {
		t.Errorf("Digest() is not deterministic: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "sha256:") {
		t.Errorf("Digest() = %q, want a sha256: prefix", first)
	}
	if got := Digest([]byte("ab"), []byte("c")); got == Digest([]byte("a"), []byte("bc")) {
		t.Errorf("Digest(\"ab\",\"c\") = Digest(\"a\",\"bc\") = %q, want them to differ", got)
	}
}

func TestWithoutFields(t *testing.T) {
	raw := []byte(`{"a":1,"b":"kept","secret":"shh","token":"also-shh"}`)
	out := withoutFields(raw, "secret", "token")
	var got map[string]json.RawMessage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("withoutFields() produced invalid JSON: %v (%s)", err, out)
	}
	if _, ok := got["secret"]; ok {
		t.Errorf("withoutFields() kept secret: %s", out)
	}
	if _, ok := got["token"]; ok {
		t.Errorf("withoutFields() kept token: %s", out)
	}
	if string(got["a"]) != "1" || string(got["b"]) != `"kept"` {
		t.Errorf("withoutFields() = %s, want a and b kept", out)
	}

	for _, notObject := range [][]byte{[]byte(`[1,2,3]`), []byte(`42`), []byte(`"a string"`), []byte(`not json`)} {
		if got := withoutFields(notObject, "a"); got != nil {
			t.Errorf("withoutFields(%s) = %s, want nil", notObject, got)
		}
	}
}

func TestStatusError(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusOK, nil},
		{http.StatusNoContent, nil},
		{http.StatusUnauthorized, domain.ErrCredentialRejected},
		{http.StatusForbidden, domain.ErrForbidden},
		{http.StatusNotFound, domain.ErrNotFound},
		{http.StatusGone, domain.ErrNotFound},
		{http.StatusRequestTimeout, domain.ErrProviderUnavailable},
		{http.StatusTooManyRequests, domain.ErrProviderUnavailable},
		{http.StatusInternalServerError, domain.ErrProviderUnavailable},
		{http.StatusServiceUnavailable, domain.ErrProviderUnavailable},
		{http.StatusBadRequest, domain.ErrInvalid},
		{http.StatusUnprocessableEntity, domain.ErrInvalid},
	}
	for _, tc := range tests {
		err := StatusError(tc.status)
		if tc.want == nil {
			if err != nil {
				t.Errorf("StatusError(%d) = %v, want nil", tc.status, err)
			}
			continue
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("StatusError(%d) = %v, want %v", tc.status, err, tc.want)
		}
	}
}

func TestAuthorize(t *testing.T) {
	t.Run("default header", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/api", nil)
		Authorize(request, domain.Credential{AccessToken: "tok123", Prefix: "Bearer "})
		if got := request.Header.Get("Authorization"); got != "Bearer tok123" {
			t.Errorf("Authorize() Authorization = %q, want %q", got, "Bearer tok123")
		}
	})
	t.Run("custom header name", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/api", nil)
		Authorize(request, domain.Credential{In: "header", Name: "X-Api-Key", AccessToken: "tok123"})
		if got := request.Header.Get("X-Api-Key"); got != "tok123" {
			t.Errorf("Authorize() X-Api-Key = %q, want %q", got, "tok123")
		}
		if got := request.Header.Get("Authorization"); got != "" {
			t.Errorf("Authorize() also set Authorization = %q, want none", got)
		}
	})
	t.Run("query placement keeps existing query params", func(t *testing.T) {
		request, _ := http.NewRequest(http.MethodGet, "https://example.test/api?foo=bar", nil)
		Authorize(request, domain.Credential{In: "query", Name: "access_token", AccessToken: "tok123"})
		query := request.URL.Query()
		if query.Get("foo") != "bar" || query.Get("access_token") != "tok123" {
			t.Errorf("Authorize() query = %v, want foo=bar and access_token=tok123", query)
		}
	})
}

func TestSendJSON(t *testing.T) {
	t.Run("error does not leak the response body", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			writer.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(writer, `{"error":"secret-value"}`)
		}))
		defer server.Close()
		err := sendJSON(context.Background(), server.Client(), http.MethodGet, server.URL, domain.Credential{}, nil, nil)
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("sendJSON() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("sendJSON() error leaked the response body: %v", err)
		}
	})
	t.Run("oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write(bytes.Repeat([]byte("x"), maxResponseBytes+1))
		}))
		defer server.Close()
		err := sendJSON(context.Background(), server.Client(), http.MethodGet, server.URL, domain.Credential{}, nil, nil)
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("sendJSON() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
	})
	t.Run("invalid JSON answered with 200", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(writer, `not valid json`)
		}))
		defer server.Close()
		var target map[string]any
		err := sendJSON(context.Background(), server.Client(), http.MethodGet, server.URL, domain.Credential{}, nil, &target)
		if !errors.Is(err, domain.ErrProviderUnavailable) {
			t.Fatalf("sendJSON() error = %v, want %v", err, domain.ErrProviderUnavailable)
		}
	})
}

func TestBuiltin(t *testing.T) {
	registry := Builtin()
	names := []string{"github", "hubspot", "linear", "microsoft", "slack"}
	if len(registry) != len(names) {
		t.Fatalf("Builtin() has %d providers, want %d (%v)", len(registry), len(names), registry)
	}
	for _, name := range names {
		provider, ok := registry[name]
		if !ok || provider.Name != name {
			t.Fatalf("Builtin()[%q] = %+v, %v", name, provider, ok)
		}
		if provider.Target == nil {
			t.Errorf("%s Target is nil", name)
		}
		if provider.Receive == nil {
			t.Errorf("%s Receive is nil", name)
		}
		switch provider.Delivery {
		case DeliveryWebhook:
			if provider.Register == nil {
				t.Errorf("%s is a webhook provider with no Register", name)
			}
			if provider.Unregister == nil {
				t.Errorf("%s is a webhook provider with no Unregister", name)
			}
			if provider.AccountKey != nil {
				t.Errorf("%s is a webhook provider but has AccountKey", name)
			}
		case DeliveryApp:
			if provider.AccountKey == nil {
				t.Errorf("%s is an app provider with no AccountKey", name)
			}
			if provider.Register != nil || provider.Unregister != nil {
				t.Errorf("%s is an app provider but has Register/Unregister", name)
			}
		default:
			t.Errorf("%s has an unknown Delivery %q", name, provider.Delivery)
		}
		if name == "microsoft" {
			if provider.Renew == nil {
				t.Errorf("microsoft has no Renew")
			}
		} else if provider.Renew != nil {
			t.Errorf("%s has a Renew, want none", name)
		}
	}
}
