package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeDevice struct {
	clisession.Device
	status     clisession.DeviceStatus
	subject    string
	lastPolled time.Time
}

// fakeCLIDevices keeps the Postgres store's rules, writing sessions into the
// fake CLI session store the middleware reads.
type fakeCLIDevices struct {
	sessions *fakeCLISessions
	devices  map[string]*fakeDevice
}

func (s *fakeCLIDevices) byUserCode(hash string) *fakeDevice {
	for _, device := range s.devices {
		if device.UserCodeHash == hash && device.status == clisession.DevicePending && device.ExpiresAt.After(time.Now()) {
			return device
		}
	}
	return nil
}
func (s *fakeCLIDevices) CreateCLIDevice(_ context.Context, device clisession.Device) error {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	device.CreatedAt = time.Now()
	s.devices[device.DeviceCodeHash] = &fakeDevice{Device: device, status: clisession.DevicePending}
	return nil
}
func (s *fakeCLIDevices) CLIDevice(_ context.Context, hash string) (clisession.Device, error) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	if device := s.byUserCode(hash); device != nil {
		return device.Device, nil
	}
	return clisession.Device{}, domain.ErrNotFound
}
func (s *fakeCLIDevices) DecideCLIDevice(_ context.Context, hash, subject string, approve bool) error {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	device := s.byUserCode(hash)
	if device == nil {
		return domain.ErrNotFound
	}
	device.status, device.subject = clisession.DeviceDenied, subject
	if approve {
		device.status = clisession.DeviceApproved
	}
	return nil
}
func (s *fakeCLIDevices) PollCLIDevice(_ context.Context, hash, tokenHash string, expires time.Time) (clisession.Poll, error) {
	s.sessions.mu.Lock()
	defer s.sessions.mu.Unlock()
	device, ok := s.devices[hash]
	if !ok {
		return clisession.Poll{Status: clisession.PollExpired}, nil
	}
	poll := clisession.Poll{Interval: device.Interval}
	switch {
	case !device.ExpiresAt.After(time.Now()):
		delete(s.devices, hash)
		poll.Status = clisession.PollExpired
	case device.status == clisession.DeviceDenied:
		delete(s.devices, hash)
		poll.Status = clisession.PollDenied
	case device.status == clisession.DeviceApproved:
		delete(s.devices, hash)
		s.sessions.sessions[tokenHash] = clisession.Session{Subject: device.subject, ExpiresAt: expires}
		poll.Subject = device.subject
	case !device.lastPolled.IsZero() && time.Since(device.lastPolled) < device.Interval:
		device.Interval += clisession.SlowDownStep
		device.lastPolled = time.Now()
		poll.Status, poll.Interval = clisession.PollSlowDown, device.Interval
	default:
		device.lastPolled = time.Now()
		poll.Status = clisession.PollPending
	}
	return poll, nil
}

// tokenVerifier answers per token, as Cognito would for different clients.
type tokenVerifier map[string]authn.Claims

func (verifier tokenVerifier) Verify(_ context.Context, token string) (authn.Claims, error) {
	claims, ok := verifier[token]
	if !ok {
		return authn.Claims{}, domain.ErrUnauthorized
	}
	return claims, nil
}

type subjectAuthService struct {
	setupAuthService
	subjects []string
}

func (s *subjectAuthService) Organization(ctx context.Context, subject, id string) (domain.Organization, domain.Membership, error) {
	s.mu.Lock()
	s.subjects = append(s.subjects, subject)
	s.mu.Unlock()
	return s.setupAuthService.Organization(ctx, subject, id)
}

const webOrigin = "https://app.example.com"

func deviceServer(t *testing.T) (*Server, *fakeCLIDevices, *subjectAuthService) {
	t.Helper()
	sessions := newFakeCLISessions()
	devices := &fakeCLIDevices{sessions: sessions, devices: map[string]*fakeDevice{}}
	hour := time.Now().Add(time.Hour).Unix()
	verifier := tokenVerifier{
		"web-token":   {Subject: "member-1", ClientID: "web-client", ExpiresAt: hour},
		"agent-token": {Subject: "member-1", ClientID: "agent-client", ExpiresAt: hour},
		"cli-token":   {Subject: "member-1", ClientID: "cli-client", ExpiresAt: hour},
	}
	service := &subjectAuthService{setupAuthService: setupAuthService{fakeAuthService: fakeAuthService{user: domain.User{ID: "member-1"}, organizations: []domain.Organization{{ID: "org_1"}, {ID: "org_2"}}}}}
	server := New(verifier, &fakeAuthFlow{}, &fakePasswordAuth{}, service, Config{
		AllowedOrigins: []string{webOrigin},
		CLIClientID:    "cli-client", AgentClientID: "agent-client",
		CLISessions: sessions, CLIDevices: devices,
		CLIDeviceURL: "https://app.example.com/cli/device",
	})
	return server, devices, service
}

type deviceStart struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

func startDevice(t *testing.T, server http.Handler, peer string) deviceStart {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device", strings.NewReader(`{"client_name":"oryxa-cli on build-box (linux)"}`))
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	var start deviceStart
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &start) != nil {
		t.Fatalf("start device: %d %s", w.Code, w.Body)
	}
	return start
}

func pollDevice(server http.Handler, deviceCode, peer string) (*httptest.ResponseRecorder, map[string]any) {
	body, _ := json.Marshal(map[string]string{"grant_type": deviceGrantType, "device_code": deviceCode})
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device/token", strings.NewReader(string(body)))
	r.RemoteAddr = peer
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	decoded := map[string]any{}
	_ = json.Unmarshal(w.Body.Bytes(), &decoded)
	return w, decoded
}

// memberRequest is the approve page: the member's web session cookie, from
// the web app's origin.
func memberRequest(server http.Handler, path, code string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"user_code": code})
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	r.AddCookie(&http.Cookie{Name: accessCookieName, Value: "web-token"})
	r.Header.Set("Origin", webOrigin)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestDeviceSignInApprovedIssuesASetupOnlySessionForTheApprover(t *testing.T) {
	server, _, service := deviceServer(t)
	start := startDevice(t, server, "192.0.2.10:5000")
	if len(start.DeviceCode) < 40 || clisession.NormalizeUserCode(start.UserCode) != start.UserCode ||
		start.VerificationURI != "https://app.example.com/cli/device" || start.VerificationURIComplete != start.VerificationURI+"?code="+start.UserCode ||
		start.ExpiresIn != 600 || start.Interval != 5 {
		t.Fatalf("start = %+v", start)
	}

	w, body := pollDevice(server, start.DeviceCode, "192.0.2.10:5000")
	if w.Code != http.StatusBadRequest || body["error"] != clisession.PollPending {
		t.Fatalf("first poll: %d %v", w.Code, body)
	}
	w, body = pollDevice(server, start.DeviceCode, "192.0.2.10:5000")
	if w.Code != http.StatusBadRequest || body["error"] != clisession.PollSlowDown || body["interval"] != float64(10) {
		t.Fatalf("fast poll: %d %v", w.Code, body)
	}

	// The approve page sees who asks and what they get, typed loosely.
	lookup := memberRequest(server, "/v1/auth/cli/device/lookup", strings.ToLower(strings.ReplaceAll(start.UserCode, "-", " ")))
	if lookup.Code != http.StatusOK || !strings.Contains(lookup.Body.String(), `"client_name":"oryxa-cli on build-box (linux)"`) ||
		!strings.Contains(lookup.Body.String(), `"client_address":"192.0.2.10"`) || !strings.Contains(lookup.Body.String(), "15 minutes") {
		t.Fatalf("lookup: %d %s", lookup.Code, lookup.Body)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/approve", start.UserCode); got.Code != http.StatusOK {
		t.Fatalf("approve: %d %s", got.Code, got.Body)
	}
	// Approved once: the code cannot be decided again.
	if got := memberRequest(server, "/v1/auth/cli/device/deny", start.UserCode); got.Code != http.StatusNotFound {
		t.Fatalf("second decision: %d", got.Code)
	}

	w, body = pollDevice(server, start.DeviceCode, "192.0.2.10:5000")
	token, _ := body["access_token"].(string)
	if w.Code != http.StatusOK || !clisession.IsDeviceToken(token) || body["token_type"] != "Bearer" || body["expires_in"] != float64(900) {
		t.Fatalf("approved poll: %d %v", w.Code, body)
	}
	// Single use: the device code is gone.
	if w, body := pollDevice(server, start.DeviceCode, "192.0.2.10:5000"); w.Code != http.StatusBadRequest || body["error"] != clisession.PollExpired {
		t.Fatalf("reused device code: %d %v", w.Code, body)
	}

	if got := cliRequest(server, "GET", "/v1/auth/me", token, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"cli":true`) {
		t.Fatalf("me: %d %s", got.Code, got.Body)
	}
	if got := cliRequest(server, "GET", "/v1/organizations/org_1", token, ""); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), `"cli":true`) {
		t.Fatalf("org_1: %d %s", got.Code, got.Body)
	}
	if service.subjects[len(service.subjects)-1] != "member-1" {
		t.Fatalf("checked authority for %v", service.subjects)
	}
	// Bound to org_1, as a browser-flow session is.
	if got := cliRequest(server, "GET", "/v1/organizations/org_2", token, ""); got.Code != http.StatusForbidden {
		t.Fatalf("wrong organization: %d", got.Code)
	}
	// cliscope still holds.
	for _, route := range []struct{ method, path string }{
		{http.MethodDelete, "/v1/organizations/org_1"},
		{http.MethodPost, "/v1/agent-grant/authorize"},
		{http.MethodPost, "/v1/auth/cli/device/approve"},
	} {
		if got := cliRequest(server, route.method, route.path, token, `{"user_code":"HJKM-PQRT"}`); got.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", route.method, route.path, got.Code)
		}
	}
}

func TestDeviceSignInDeniedAnswersAccessDeniedOnce(t *testing.T) {
	server, _, _ := deviceServer(t)
	start := startDevice(t, server, "192.0.2.11:5000")
	if got := memberRequest(server, "/v1/auth/cli/device/deny", start.UserCode); got.Code != http.StatusOK || !strings.Contains(got.Body.String(), "denied") {
		t.Fatalf("deny: %d %s", got.Code, got.Body)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/approve", start.UserCode); got.Code != http.StatusNotFound {
		t.Fatalf("approve after deny: %d", got.Code)
	}
	if w, body := pollDevice(server, start.DeviceCode, "192.0.2.11:5000"); w.Code != http.StatusBadRequest || body["error"] != clisession.PollDenied {
		t.Fatalf("denied poll: %d %v", w.Code, body)
	}
	if w, body := pollDevice(server, start.DeviceCode, "192.0.2.11:5000"); body["error"] != clisession.PollExpired || body["access_token"] != nil {
		t.Fatalf("poll after deny: %d %v", w.Code, body)
	}
}

func TestDeviceSignInExpires(t *testing.T) {
	server, devices, _ := deviceServer(t)
	start := startDevice(t, server, "192.0.2.12:5000")
	for _, device := range devices.devices {
		device.ExpiresAt = time.Now().Add(-time.Second)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/lookup", start.UserCode); got.Code != http.StatusNotFound {
		t.Fatalf("expired lookup: %d", got.Code)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/approve", start.UserCode); got.Code != http.StatusNotFound {
		t.Fatalf("expired approve: %d", got.Code)
	}
	if w, body := pollDevice(server, start.DeviceCode, "192.0.2.12:5000"); w.Code != http.StatusBadRequest || body["error"] != clisession.PollExpired {
		t.Fatalf("expired poll: %d %v", w.Code, body)
	}
}

func TestDeviceApprovalNeedsTheMembersOwnWebSession(t *testing.T) {
	server, _, _ := deviceServer(t)
	start := startDevice(t, server, "192.0.2.13:5000")
	body := `{"user_code":"` + start.UserCode + `"}`
	// Neither a CLI token nor an agent token may approve another CLI.
	for _, token := range []string{"cli-token", "agent-token", "unknown"} {
		if got := cliRequest(server, "POST", "/v1/auth/cli/device/approve", token, body); got.Code != http.StatusForbidden && got.Code != http.StatusUnauthorized {
			t.Errorf("%s approved: %d", token, got.Code)
		}
	}
	// A cookie without the web app's origin is a cross-site form post.
	r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device/approve", strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: accessCookieName, Value: "web-token"})
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cookie without origin: %d", w.Code)
	}
	// A device-token-shaped cookie is never looked up as one.
	r = httptest.NewRequest(http.MethodGet, "/v1/auth/me", nil)
	r.AddCookie(&http.Cookie{Name: accessCookieName, Value: clisession.NewDeviceToken()})
	w = httptest.NewRecorder()
	server.ServeHTTP(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("device token cookie: %d", w.Code)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/lookup", "HJKM-PQRT"); got.Code != http.StatusNotFound {
		t.Fatalf("unknown code: %d", got.Code)
	}
	if got := memberRequest(server, "/v1/auth/cli/device/lookup", "not a code"); got.Code != http.StatusNotFound {
		t.Fatalf("malformed code: %d", got.Code)
	}
	if got := cliRequest(server, "GET", "/v1/auth/me", clisession.NewDeviceToken(), ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("unissued device token: %d", got.Code)
	}
}

func TestDeviceEndpointsAreRateLimitedPerPeer(t *testing.T) {
	server, _, _ := deviceServer(t)
	for i := 0; i < 11; i++ {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device", nil)
		r.RemoteAddr = "192.0.2.20:1"
		r.Header.Set("X-Forwarded-For", "198.51.100.1")
		w := httptest.NewRecorder()
		server.ServeHTTP(w, r)
		if i < 10 && w.Code != http.StatusOK {
			t.Fatal(i, w.Code, w.Body)
		}
		if i == 10 && (w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") == "") {
			t.Fatal("unbounded device starts", w.Code)
		}
	}
	start := startDevice(t, server, "192.0.2.21:1")
	for i := 0; i < 61; i++ {
		w, _ := pollDevice(server, start.DeviceCode, "192.0.2.22:1")
		if i < 60 && w.Code != http.StatusBadRequest {
			t.Fatal(i, w.Code)
		}
		if i == 60 && w.Code != http.StatusTooManyRequests {
			t.Fatal("unbounded polling", w.Code)
		}
	}
	for i := 0; i < 11; i++ {
		got := memberRequest(server, "/v1/auth/cli/device/lookup", "HJKM-PQRT")
		if i < 10 && got.Code != http.StatusNotFound {
			t.Fatal(i, got.Code)
		}
		if i == 10 && got.Code != http.StatusTooManyRequests {
			t.Fatal("user codes can be enumerated", got.Code)
		}
	}
}

func TestDeviceSignInIsOffUnlessConfigured(t *testing.T) {
	server := New(fakeVerifier{}, &fakeAuthFlow{}, &fakePasswordAuth{}, &fakeAuthService{}, Config{CLIClientID: "cli-client", CLISessions: newFakeCLISessions()})
	for _, path := range []string{"/v1/auth/cli/device", "/v1/auth/cli/device/token"} {
		w := httptest.NewRecorder()
		server.ServeHTTP(w, httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"device_code":"x"}`)))
		if w.Code != http.StatusNotFound {
			t.Errorf("%s = %d", path, w.Code)
		}
	}
	if got := cliRequest(server, "GET", "/v1/auth/me", clisession.NewDeviceToken(), ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("device token accepted while off: %d", got.Code)
	}
}

func TestDeviceClientNameIsBounded(t *testing.T) {
	if got := deviceClientName("  "); got != "Oryxa CLI" {
		t.Fatal(got)
	}
	if got := deviceClientName("a\tb   c"); got != "a b c" {
		t.Fatal(got)
	}
	if got := deviceClientName(strings.Repeat("é", 100)); got != strings.Repeat("é", 80) {
		t.Fatal(len(got))
	}
}

func TestDevicePeerBelievesCloudflareOnlyThroughTheLocalTunnel(t *testing.T) {
	for _, test := range []struct{ remote, header, want string }{
		{"127.0.0.1:40000", "198.51.100.7", "198.51.100.7"},
		{"[::1]:40000", "2001:db8::1", "2001:db8::1"},
		{"127.0.0.1:40000", "not an address", "127.0.0.1"},
		{"127.0.0.1:40000", "", "127.0.0.1"},
		{"192.0.2.1:40000", "198.51.100.7", "192.0.2.1"},
	} {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device", nil)
		r.RemoteAddr = test.remote
		if test.header != "" {
			r.Header.Set("CF-Connecting-IP", test.header)
		}
		if got := devicePeer(r); got != test.want {
			t.Errorf("devicePeer(%s, %q) = %q, want %q", test.remote, test.header, got, test.want)
		}
	}
	// Through the tunnel, two clients have two budgets and see their own address.
	server, _, _ := deviceServer(t)
	for _, client := range []string{"198.51.100.1", "198.51.100.2"} {
		for i := 0; i < 10; i++ {
			r := httptest.NewRequest(http.MethodPost, "/v1/auth/cli/device", nil)
			r.RemoteAddr = "127.0.0.1:40000"
			r.Header.Set("CF-Connecting-IP", client)
			w := httptest.NewRecorder()
			server.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatal(client, i, w.Code)
			}
		}
	}
}
