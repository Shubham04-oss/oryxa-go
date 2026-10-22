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
