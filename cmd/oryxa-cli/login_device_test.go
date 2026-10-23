package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeDeviceAuth answers token polls from a script, and records the waits
// the CLI chose between them.
type fakeDeviceAuth struct {
	mu        sync.Mutex
	responses []func(http.ResponseWriter)
	polls     int
	started   string
	start     map[string]any
}

func newFakeDeviceAuth(t *testing.T, responses ...func(http.ResponseWriter)) (*httptest.Server, *fakeDeviceAuth) {
	t.Helper()
	fake := &fakeDeviceAuth{responses: responses}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		switch r.URL.Path {
		case "/v1/auth/cli/device":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			fake.started = body["client_name"]
			start := fake.start
			if start == nil {
				start = map[string]any{
					"device_code": "device-code", "user_code": "HJKM-PQRT",
					"verification_uri": "https://app.example.com/cli/device", "verification_uri_complete": "https://app.example.com/cli/device?code=HJKM-PQRT",
					"expires_in": 600, "interval": 5,
				}
			}
			_ = json.NewEncoder(w).Encode(start)
		case "/v1/auth/cli/device/token":
			var body map[string]string
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["device_code"] != "device-code" || body["grant_type"] != "urn:ietf:params:oauth:grant-type:device_code" {
				t.Errorf("poll body = %v", body)
			}
			if fake.polls >= len(fake.responses) {
				t.Errorf("unexpected poll %d", fake.polls)
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			fake.responses[fake.polls](w)
			fake.polls++
		case "/v1/auth/me":
			if r.Header.Get("Authorization") != "Bearer oryxa_cli_token" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"user":{"id":"member-1"},"cli":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return server, fake
}

func deviceError(code string, interval int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusBadRequest)
		body := map[string]any{"error": code}
		if interval > 0 {
			body["interval"] = interval
		}
		_ = json.NewEncoder(w).Encode(body)
	}
}

func deviceStatus(status int) func(http.ResponseWriter) {
	return func(w http.ResponseWriter) { w.WriteHeader(status) }
}

func deviceApproved(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "oryxa_cli_token", "token_type": "Bearer", "expires_in": 900, "expires_at": time.Now().Add(15 * time.Minute)})
}

func recordDeviceWaits(t *testing.T) *[]time.Duration {
	t.Helper()
	waits := &[]time.Duration{}
	previous := deviceSleep
	deviceSleep = func(ctx context.Context, wait time.Duration) error {
		*waits = append(*waits, wait)
		return ctx.Err()
	}
	t.Cleanup(func() { deviceSleep = previous })
	return waits
}

func TestDeviceLoginPollsAtIntervalAndBacksOffOnSlowDown(t *testing.T) {
	waits := recordDeviceWaits(t)
	api, fake := newFakeDeviceAuth(t,
		deviceError("authorization_pending", 5),
		deviceError("slow_down", 10),
		deviceError("authorization_pending", 10),
		deviceStatus(http.StatusTooManyRequests),
		deviceStatus(http.StatusBadGateway),
		deviceApproved,
	)
	var output bytes.Buffer
	session, err := deviceLogin(context.Background(), api.URL, false, &output)
	if err != nil {
		t.Fatal(err)
	}
	if session.AccessToken != "oryxa_cli_token" || !session.ExpiresAt.After(time.Now()) {
		t.Fatalf("session = %+v", session)
	}
	want := []time.Duration{5 * time.Second, 5 * time.Second, 10 * time.Second, 10 * time.Second, 15 * time.Second, 15 * time.Second}
	if len(*waits) != len(want) {
		t.Fatalf("waits = %v", *waits)
	}
	for i := range want {
		if (*waits)[i] != want[i] {
			t.Fatalf("waits = %v, want %v", *waits, want)
		}
	}
	text := output.String()
	for _, part := range []string{"https://app.example.com/cli/device", "HJKM-PQRT", "Signed in."} {
		if !strings.Contains(text, part) {
			t.Errorf("output lacks %q:\n%s", part, text)
		}
	}
	// The code is typed on the page, never carried in a link: a link with it
	// filled in is what a phisher sends.
	if strings.Contains(text, "?code=") {
		t.Errorf("output offers a link with the code filled in:\n%s", text)
	}
	if !strings.HasPrefix(fake.started, "oryxa-cli") {
		t.Errorf("client name = %q", fake.started)
	}
}

func TestDeviceLoginStopsOnDenialExpiryAndBadAnswers(t *testing.T) {
	recordDeviceWaits(t)
	for _, test := range []struct {
		name     string
		response func(http.ResponseWriter)
		want     string
	}{
		{"denied", deviceError("access_denied", 0), "denied"},
		{"expired", deviceError("expired_token", 0), "expired"},
		{"unknown error", deviceError("invalid_grant", 0), "invalid_grant"},
		{"unauthorized", deviceStatus(http.StatusUnauthorized), "401"},
		{"long session", func(w http.ResponseWriter) {
			_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "t", "token_type": "Bearer", "expires_at": time.Now().Add(time.Hour)})
		}, "invalid session"},
	} {
		t.Run(test.name, func(t *testing.T) {
			api, _ := newFakeDeviceAuth(t, test.response)
			_, err := deviceLogin(context.Background(), api.URL, false, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("err = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDeviceLoginTimesOutAtExpiry(t *testing.T) {
	previous := deviceSleep
	deviceSleep = func(ctx context.Context, _ time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { deviceSleep = previous })
	api, fake := newFakeDeviceAuth(t)
	fake.start = map[string]any{"device_code": "device-code", "user_code": "HJKM-PQRT", "verification_uri": "https://app.example.com/cli/device", "expires_in": 1, "interval": 5}
	_, err := deviceLogin(context.Background(), api.URL, false, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
}

func TestDeviceLoginRefusesAnUnsafeVerificationURL(t *testing.T) {
	recordDeviceWaits(t)
	for _, uri := range []string{"http://app.example.com/cli/device", "javascript:alert(1)", "https://user@app.example.com/cli/device", ""} {
		api, fake := newFakeDeviceAuth(t)
		fake.start = map[string]any{"device_code": "device-code", "user_code": "HJKM-PQRT", "verification_uri": uri, "expires_in": 600, "interval": 5}
		if _, err := deviceLogin(context.Background(), api.URL, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "verification URL") {
			t.Errorf("%q: err = %v", uri, err)
		}
	}
	// A complete URL pointing elsewhere is dropped for the plain one.
	start := deviceAuthorization{DeviceCode: "d", UserCode: "HJKM-PQRT", VerificationURI: "https://app.example.com/cli/device", VerificationURIComplete: "https://evil.example.com/cli/device?code=HJKM-PQRT", ExpiresIn: 600}
	if err := validateDeviceAuthorization(&start); err != nil || start.VerificationURIComplete != start.VerificationURI || start.Interval != 5 {
		t.Fatal(start, err)
	}
}

func TestDeviceLoginExplainsAServerWithoutIt(t *testing.T) {
	api := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(api.Close)
	if _, err := deviceLogin(context.Background(), api.URL, false, &bytes.Buffer{}); err == nil || !strings.Contains(err.Error(), "does not offer device sign-in") {
		t.Fatal(err)
	}
}

func TestDeviceLoginJSONGivesTheAgentTheCodeToRelay(t *testing.T) {
	recordDeviceWaits(t)
	api, _ := newFakeDeviceAuth(t, deviceApproved)
	var output bytes.Buffer
	if _, err := deviceLogin(context.Background(), api.URL, true, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(&output)
	var first struct {
		Status string            `json:"status"`
		Next   map[string]string `json:"next"`
	}
	if err := decoder.Decode(&first); err != nil || first.Status != "needs_human" || first.Next["action"] != "enter_code" || first.Next["code"] != "HJKM-PQRT" || first.Next["url"] != "https://app.example.com/cli/device" {
		t.Fatal(first, err)
	}
	var done map[string]string
	if err := decoder.Decode(&done); err != nil || done["status"] != "done" {
		t.Fatal(done, err)
	}
}

func TestUseDeviceLoginWhenNoBrowserCanBeOpened(t *testing.T) {
	previous := lookPath
	t.Cleanup(func() { lookPath = previous })
	found := func(string) (string, error) { return "/usr/bin/xdg-open", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }
	env := func(values map[string]string) func(string) string {
		return func(key string) string { return values[key] }
	}
	desktop := runtime.GOOS == "darwin" || runtime.GOOS == "windows"
	for _, test := range []struct {
		name string
		opts options
		env  map[string]string
		look func(string) (string, error)
		want bool
	}{
		{"flag", options{device: true}, map[string]string{"DISPLAY": ":0"}, found, true},
		{"ssh", options{}, map[string]string{"SSH_CONNECTION": "192.0.2.1 5000 192.0.2.2 22", "DISPLAY": ":0"}, found, true},
		{"ssh tty", options{noBrowser: true}, map[string]string{"SSH_TTY": "/dev/pts/1"}, found, true},
		{"display", options{}, map[string]string{"DISPLAY": ":0"}, found, false},
		{"wayland", options{}, map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, found, false},
		{"no display", options{}, map[string]string{}, found, !desktop},
		{"no opener", options{}, map[string]string{"DISPLAY": ":0"}, missing, !desktop},
		{"no-browser keeps loopback", options{noBrowser: true}, map[string]string{}, missing, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			lookPath = test.look
			if got := useDeviceLogin(test.opts, env(test.env)); got != test.want {
				t.Fatalf("useDeviceLogin = %t, want %t", got, test.want)
			}
		})
	}
}

func TestLoginDeviceFlagSignsInAndCachesTheSession(t *testing.T) {
	recordDeviceWaits(t)
	store := testSessionStore(t)
	api, _ := newFakeDeviceAuth(t, deviceError("authorization_pending", 5), deviceApproved)
	var stdout, stderr bytes.Buffer
	getenv := func(key string) string {
		if key == "ORYXA_SESSION_DIR" {
			return t.TempDir()
		}
		return ""
	}
	if code := run(context.Background(), []string{"login", "--device", "--api", api.URL}, &stdout, &stderr, getenv); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"cli":true`) || !strings.Contains(stderr.String(), "HJKM-PQRT") {
		t.Fatalf("stdout=%s stderr=%s", stdout.String(), stderr.String())
	}
	if len(store.values) != 1 {
		t.Fatalf("cached sessions = %d", len(store.values))
	}
}
