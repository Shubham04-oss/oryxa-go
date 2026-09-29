package httpserver

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/auth/internal/application"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/authn"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/clisession"
	"github.com/Shubham04-oss/oryxa-go/auth/internal/domain"
)

type fakeCLISessions struct {
	mu         sync.Mutex
	challenges map[string]clisession.Challenge
	sessions   map[string]clisession.Session
}

func newFakeCLISessions() *fakeCLISessions {
	return &fakeCLISessions{challenges: map[string]clisession.Challenge{"state": {State: "state", Nonce: "nonce", ExpiresAt: time.Now().Add(time.Minute)}}, sessions: map[string]clisession.Session{clisession.Hash("setup-token"): {ExpiresAt: time.Now().Add(time.Minute)}}}
}
func (s *fakeCLISessions) CreateCLIChallenge(_ context.Context, c clisession.Challenge) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.challenges[c.State] = c
	return nil
}
func (s *fakeCLISessions) CLIChallenge(_ context.Context, state string) (clisession.Challenge, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[state]
	if !ok || !c.ExpiresAt.After(time.Now()) {
		return clisession.Challenge{}, domain.ErrUnauthorized
	}
	return c, nil
}
func (s *fakeCLISessions) AdmitCLISession(_ context.Context, state, nonce, hash string, expires time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.challenges[state]
	_, admitted := s.sessions[hash]
	if !ok || c.Nonce != nonce || !c.ExpiresAt.After(time.Now()) || admitted {
		return domain.ErrUnauthorized
	}
	delete(s.challenges, state)
	s.sessions[hash] = clisession.Session{ExpiresAt: expires}
	return nil
}
func (s *fakeCLISessions) CLISession(_ context.Context, hash string) (clisession.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok || !session.ExpiresAt.After(time.Now()) {
		return clisession.Session{}, domain.ErrUnauthorized
	}
	return session, nil
}
func (s *fakeCLISessions) BindCLISession(_ context.Context, hash, org string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok || !session.ExpiresAt.After(time.Now()) || session.OrganizationID != "" && session.OrganizationID != org {
		return domain.ErrForbidden
	}
	session.OrganizationID = org
	s.sessions[hash] = session
	return nil
}
func (s *fakeCLISessions) ReserveCLIOrganization(_ context.Context, hash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok || !session.ExpiresAt.After(time.Now()) || session.OrganizationID != "" {
		return domain.ErrForbidden
	}
	session.OrganizationID = clisession.CreatingOrganization
	s.sessions[hash] = session
	return nil
}
func (s *fakeCLISessions) FinishCLIOrganization(_ context.Context, hash, org string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[hash]
	if !ok || session.OrganizationID != clisession.CreatingOrganization {
		return domain.ErrForbidden
	}
	session.OrganizationID = org
	s.sessions[hash] = session
	return nil
}

type setupAuthService struct {
	fakeAuthService
	mu      sync.Mutex
	created int
}

func (s *setupAuthService) CreateOrganization(context.Context, string, application.CreateOrganizationInput) (domain.Organization, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created++
	return domain.Organization{ID: "org_created"}, nil
}
func (s *setupAuthService) Organization(_ context.Context, _ string, id string) (domain.Organization, domain.Membership, error) {
	if id == "org_forbidden" {
		return domain.Organization{}, domain.Membership{}, domain.ErrForbidden
	}
	return domain.Organization{ID: id}, domain.Membership{Role: domain.RoleOwner}, nil
}
func setupServer(sessions *fakeCLISessions) (*Server, *setupAuthService) {
	service := &setupAuthService{fakeAuthService: fakeAuthService{user: domain.User{ID: "user-1"}, organizations: []domain.Organization{{ID: "org_1"}, {ID: "org_2"}}}}
	return New(fakeVerifier{claims: authn.Claims{Subject: "user-1", ClientID: "cli-client", ExpiresAt: time.Now().Add(time.Hour).Unix()}}, &fakeAuthFlow{}, &fakePasswordAuth{}, service, Config{CLIClientID: "cli-client", CLISessions: sessions}), service
}
func cliRequest(server http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	server.ServeHTTP(w, r)
	return w
}

func TestCLIRequiresAdmittedSessionBeforeAnySetupAccess(t *testing.T) {
	server, _ := setupServer(newFakeCLISessions())
	for _, path := range []string{"/v1/auth/me", "/v1/organizations", "/v1/organizations/org_1"} {
		if got := cliRequest(server, "GET", path, "raw-cognito-token", ""); got.Code != 401 {
			t.Errorf("unregistered raw CLI token at %s: %d", path, got.Code)
		}
	}
}
func TestCLISelectsOnlyOneAuthorizedOrganization(t *testing.T) {
	server, _ := setupServer(newFakeCLISessions())
	if got := cliRequest(server, "GET", "/v1/organizations/org_forbidden", "setup-token", ""); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got := cliRequest(server, "GET", "/v1/organizations/org_1", "setup-token", ""); got.Code != 200 || !strings.Contains(got.Body.String(), `"cli":true`) {
		t.Fatal(got.Code, got.Body)
	}
	for _, path := range []string{"/v1/organizations/org_2", "/v1/organizations/org_2/members"} {
		if got := cliRequest(server, "GET", path, "setup-token", ""); got.Code != 403 {
			t.Errorf("cross-org %s: %d", path, got.Code)
		}
	}
	if got := cliRequest(server, "POST", "/v1/organizations", "setup-token", `{"name":"Another","slug":"another"}`); got.Code != 403 {
		t.Fatal("created second org", got.Code)
	}
	for _, path := range []string{"/v1/auth/me", "/v1/organizations"} {
		got := cliRequest(server, "GET", path, "setup-token", "")
		if got.Code != 200 || strings.Contains(got.Body.String(), "org_2") || !strings.Contains(got.Body.String(), "org_1") {
			t.Fatal(got.Code, got.Body)
		}
	}
}
func TestConcurrentCLICreationCanCreateOnlyOneOrganization(t *testing.T) {
	server, service := setupServer(newFakeCLISessions())
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for range 2 {
		wg.Go(func() {
			codes <- cliRequest(server, "POST", "/v1/organizations", "setup-token", `{"name":"Example","slug":"example"}`).Code
		})
	}
	wg.Wait()
	close(codes)
	counts := map[int]int{}
	for code := range codes {
		counts[code]++
	}
	if counts[201] != 1 || counts[403] != 1 || service.created != 1 {
		t.Fatalf("codes=%v created=%d", counts, service.created)
	}
	if got := cliRequest(server, "GET", "/v1/organizations/org_1", "setup-token", ""); got.Code != 403 {
		t.Fatal("creation did not bind", got.Code)
	}
	if got := cliRequest(server, "GET", "/v1/organizations/org_created", "setup-token", ""); got.Code != 200 {
		t.Fatal(got.Code)
	}
}

func TestServerChallengeIsRequiredExpiresAndCannotReplay(t *testing.T) {
	sessions := newFakeCLISessions()
	server, _ := setupServer(sessions)
	server.config.CLIVerifier = fakeCLIIdentity{claims: authn.IdentityClaims{Subject: "user-1", Email: "person@example.com"}}
	server.config.CLICompleteLogin = func(context.Context, application.FederatedIdentity) (domain.User, bool, error) {
		return domain.User{ID: "user-1"}, false, nil
	}
	for _, body := range []string{`{"id_token":"identity","state":"fabricated"}`, `{"id_token":"identity","nonce":"nonce"}`} {
		got := cliRequest(server, "POST", "/v1/auth/cli/session", "access", body)
		if got.Code != 401 && got.Code != 400 {
			t.Fatal("fabricated state or nonce admitted", got.Code)
		}
	}
	sessions.challenges["expired"] = clisession.Challenge{State: "expired", Nonce: "nonce", ExpiresAt: time.Now().Add(-time.Second)}
	if got := cliRequest(server, "POST", "/v1/auth/cli/session", "access", `{"id_token":"identity","state":"expired"}`); got.Code != 401 {
		t.Fatal("expired challenge accepted", got.Code)
	}
	// Stored nonce, not a nonce chosen beside the ID token, reaches verification.
	sessions.challenges["wrong"] = clisession.Challenge{State: "wrong", Nonce: "different-server-nonce", ExpiresAt: time.Now().Add(time.Minute)}
	if got := cliRequest(server, "POST", "/v1/auth/cli/session", "access", `{"id_token":"identity","state":"wrong"}`); got.Code != 401 {
		t.Fatal("nonce mismatch accepted", got.Code)
	}
	body := `{"id_token":"identity","state":"state"}`
	first := cliRequest(server, "POST", "/v1/auth/cli/session", "access", body)
	if first.Code != 200 {
		t.Fatal(first.Code, first.Body)
	}
	var response struct {
		ExpiresAt time.Time `json:"expires_at"`
	}
	if err := json.Unmarshal(first.Body.Bytes(), &response); err != nil || !response.ExpiresAt.After(time.Now()) || response.ExpiresAt.After(time.Now().Add(clisession.SessionTTL)) {
		t.Fatal("invalid TTL", response, err)
	}
	if got := cliRequest(server, "POST", "/v1/auth/cli/session", "access", body); got.Code != 401 {
		t.Fatal("challenge replay accepted", got.Code)
	}
	if got := cliRequest(server, "GET", "/v1/auth/me", "access", ""); got.Code != 200 {
		t.Fatal("admitted session refused", got.Code)
	}
}
func TestServerIssuesUniqueShortLivedChallenges(t *testing.T) {
	server, _ := setupServer(newFakeCLISessions())
	seen := map[string]bool{}
	for range 2 {
		got := cliRequest(server, "POST", "/v1/auth/cli/challenge", "unused", "")
		var c clisession.Challenge
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &c) != nil {
			t.Fatal(got.Code, got.Body)
		}
		if len(c.State) < 40 || len(c.Nonce) < 40 || seen[c.State] || seen[c.Nonce] || !c.ExpiresAt.After(time.Now()) || c.ExpiresAt.After(time.Now().Add(clisession.ChallengeTTL)) {
			t.Fatal("invalid challenge", c)
		}
		seen[c.State] = true
		seen[c.Nonce] = true
		if got.Header().Get("Cache-Control") != "no-store" {
			t.Fatal("challenge is cacheable")
		}
	}
}

func TestBoundCLISessionStopsWhenAdministratorRoleIsLost(t *testing.T) {
	server, _ := setupServer(newFakeCLISessions())
	if got := cliRequest(server, "GET", "/v1/organizations/org_1", "setup-token", ""); got.Code != 200 {
		t.Fatal(got.Code)
	}
	server.service = &fakeAuthService{organization: domain.Organization{ID: "org_1"}, membership: domain.Membership{Role: domain.RoleMember}}
	for _, path := range []string{"/v1/auth/me", "/v1/organizations", "/v1/organizations/org_1", "/v1/organizations/org_1/members"} {
		if got := cliRequest(server, "GET", path, "setup-token", ""); got.Code != 403 {
			t.Errorf("demoted CLI at %s=%d", path, got.Code)
		}
	}
}
