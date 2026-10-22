package egress

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
)

func TestServeHTTPForwardsToTheAppHost(t *testing.T) {
	var gotURL, gotAuth string
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL, gotAuth = r.Host+r.URL.RequestURI(), r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer app.Close()
	broker := httptest.NewServer(proxyTo(app, &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-1"}}))
	defer broker.Close()

	send := func(handle, host string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest(http.MethodPost, broker.URL+"/v1/notes?x=1", strings.NewReader(`{"a":1}`))
		req.Header.Set("Authorization", "Bearer "+handle)
		req.Header.Set(HostHeader, host)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}

	resp := send(handle(t), "api.example.test")
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusCreated || string(body) != `{"ok":true}` {
		t.Fatalf("got %d %s", resp.StatusCode, body)
	}
	if gotURL != "api.example.test/v1/notes?x=1" || gotAuth != "Bearer tok-1" {
		t.Fatalf("app saw %q with %q", gotURL, gotAuth)
	}

	for name, c := range map[string]struct {
		handle, host string
		want         int
	}{
		"forged handle":      {"eh1.k1.e30.AAAA", "api.example.test", http.StatusUnauthorized},
		"host not allowed":   {handle(t), "evil.example.test", http.StatusForbidden},
		"no host":            {handle(t), "", http.StatusBadRequest},
		"host with a path":   {handle(t), "api.example.test/x", http.StatusBadRequest},
		"host with userinfo": {handle(t), "u@api.example.test", http.StatusBadRequest},
	} {
		if got := send(c.handle, c.host).StatusCode; got != c.want {
			t.Errorf("%s: status %d, want %d", name, got, c.want)
		}
	}
}

// The proxy's redaction must see the token: in headers, and in a body the
// connector can't ask to have compressed or cut into ranges.
func TestTheTokenNeverReachesTheConnector(t *testing.T) {
	var gotEncoding, gotRange string
	app := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotEncoding, gotRange = r.Header.Get("Accept-Encoding"), r.Header.Get("Range")
		w.Header().Set("Location", "https://api.example.test/next?token="+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		_, _ = w.Write([]byte(`{"echo":"` + r.Header.Get("Authorization") + `"}`))
	}))
	defer app.Close()
	broker := httptest.NewServer(proxyTo(app, &fakeConns{m: testManifest(), tokens: []secrets.Secret{"tok-secret"}}))
	defer broker.Close()

	req, _ := http.NewRequest(http.MethodGet, broker.URL+"/v1/x", nil)
	req.Header.Set("Authorization", "Bearer "+handle(t))
	req.Header.Set(HostHeader, "api.example.test")
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("Range", "bytes=0-3")
	req.Header.Set("If-Range", `"etag"`)
	resp, err := (&http.Client{Transport: &http.Transport{DisableCompression: true}}).Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if strings.Contains(string(body), "tok-secret") || strings.Contains(resp.Header.Get("Location"), "tok-secret") {
		t.Fatalf("token reached the connector: Location %q body %s", resp.Header.Get("Location"), body)
	}
	if gotRange != "" || strings.Contains(gotEncoding, "gzip") && resp.Header.Get("Content-Encoding") != "" {
		t.Fatalf("app saw Range %q, Accept-Encoding %q; connector got Content-Encoding %q", gotRange, gotEncoding, resp.Header.Get("Content-Encoding"))
	}
}
