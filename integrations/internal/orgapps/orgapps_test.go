package orgapps

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/httpserver"
	"github.com/Shubham04-oss/oryxa-go/integrations/internal/vault"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// fakeKMS wraps a data key as a handle and unwraps it only for the encryption
// context it was generated for, as KMS does.
type fakeKMS struct {
	n    int
	keys map[string]fakeWrapped
}

type fakeWrapped struct {
	key     []byte
	context map[string]string
}

func (f *fakeKMS) GenerateDataKey(_ context.Context, _ string, ec map[string]string) ([]byte, []byte, error) {
	f.n++
	if f.keys == nil {
		f.keys = map[string]fakeWrapped{}
	}
	key := sha256.Sum256([]byte(fmt.Sprint("data key ", f.n)))
	handle := fmt.Sprint("wrapped-", f.n)
	f.keys[handle] = fakeWrapped{key: key[:], context: maps.Clone(ec)}
	return append([]byte(nil), key[:]...), []byte(handle), nil
}

func (f *fakeKMS) DecryptDataKey(_ context.Context, encrypted []byte, ec map[string]string) ([]byte, error) {
	w, ok := f.keys[string(encrypted)]
	if !ok || !maps.Equal(w.context, ec) {
		return nil, errors.New("kms context mismatch")
	}
	return append([]byte(nil), w.key...), nil
}

// The tests need a Postgres with the schema's migrations applied; see
// integrations/vault_integration_test.go. The rows they make are removed after.
func newService(t *testing.T) *Service {
	t.Helper()
	url := os.Getenv("INTEGRATIONS_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("INTEGRATIONS_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	database, err := postgreskit.Open(ctx, postgreskit.Config{URL: url, ApplicationName: "oryxa-orgapps-test", Schema: "public"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	sealer, err := vault.New(&fakeKMS{}, "alias/test")
	if err != nil {
		t.Fatal(err)
	}
	return New(database.Pool, sealer)
}

// org gives a test an org of its own and removes what it saved after.
func org(t *testing.T, s *Service) string {
	t.Helper()
	id := fmt.Sprintf("test_org_%d", time.Now().UnixNano())
	t.Cleanup(func() {
		ctx := context.Background()
		s.pool.Exec(ctx, `DELETE FROM integrations.connections WHERE org_id = $1`, id)
		s.pool.Exec(ctx, `DELETE FROM integrations.org_apps WHERE org_id = $1`, id)
	})
	return id
}

const (
	longSecret = "gho_0123456789abcdefSECRETTAIL"
	hookSecret = "whsec_0123456789abcdefHOOKTAIL"
)

func githubInput() Input {
	return Input{ClientID: "Iv1.first", Secret: longSecret, WebhookSecret: hookSecret, Extra: map[string]string{"app_id": "7"}, ActorID: "admin_1"}
}

func TestSetSavesAnAppAndTheViewHidesItsSecrets(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	view, changed, err := s.Put(ctx, orgID, "github", githubInput())
	if err != nil || !changed {
		t.Fatalf("Put = %+v, %v, %v", view, changed, err)
	}
	if view.Kind != "github_app" || view.Status != "ok" || !view.SecretSet || view.SecretHint != "TAIL" ||
		!view.WebhookSecretSet || view.WebhookSecretHint != "TAIL" || view.Extra["app_id"] != "7" || view.CreatedBy != "admin_1" {
		t.Fatalf("view = %+v", view)
	}
	got, err := s.Get(ctx, orgID, "github")
	if err != nil || got.ID != view.ID {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	for _, v := range []View{view, got} {
		encoded, _ := json.Marshal(v)
		if bytes.Contains(encoded, []byte("0123456789abcdef")) {
			t.Fatalf("a view holds a secret: %s", encoded)
		}
	}
	// The row at rest holds sealed values only.
	var stored string
	if err := s.pool.QueryRow(ctx, `SELECT secret::text || webhook_secret::text FROM integrations.org_apps WHERE id = $1`, view.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "0123456789abcdef") {
		t.Fatal("the stored row holds a secret in the clear")
	}
	// A secret under 16 characters gives no hint.
	short, _, err := s.Put(ctx, orgID, "github", Input{ClientID: "Iv1.short", Secret: "tiny"})
	if err != nil || !short.SecretSet || short.SecretHint != "" {
		t.Fatalf("short secret view = %+v, %v", short, err)
	}
}

func TestSettingTheSameAppAgainChangesNothingAndKeepsItsSecret(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	first, _, err := s.Put(ctx, orgID, "github", githubInput())
	if err != nil {
		t.Fatal(err)
	}
	again, changed, err := s.Put(ctx, orgID, "github", githubInput())
	if err != nil || changed || again.ID != first.ID || !again.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("same input: %+v, changed %v, %v", again, changed, err)
	}
	// Left out, the secret stays; an extra changes in place on the same row.
	updated, changed, err := s.Put(ctx, orgID, "github", Input{ClientID: "Iv1.first", Extra: map[string]string{"app_id": "8"}})
	if err != nil || !changed || updated.ID != first.ID || updated.Extra["app_id"] != "8" || updated.SecretHint != "TAIL" {
		t.Fatalf("update: %+v, changed %v, %v", updated, changed, err)
	}
	// A new secret for the same client replaces it in place.
	rotated, changed, err := s.Put(ctx, orgID, "github", Input{ClientID: "Iv1.first", Secret: "gho_rotatedrotated-NEW1"})
	if err != nil || !changed || rotated.ID != first.ID || rotated.SecretHint != "NEW1" {
		t.Fatalf("rotate: %+v, changed %v, %v", rotated, changed, err)
	}
}

func TestReplacingAnAppRetiresTheOldRowAndItsConnectionsStayPinned(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	old, _, err := s.Put(ctx, orgID, "github", githubInput())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `INSERT INTO integrations.connections (id, org_id, member_id, level, app, org_app_id, mode, status, created_at, updated_at)
		VALUES ($1, $2, 'm_1', 'personal', 'github', $3, 'byo', 'ok', now(), now())`, orgID+"_conn", orgID, old.ID); err != nil {
		t.Fatal(err)
	}
	replaced, changed, err := s.Put(ctx, orgID, "github", Input{ClientID: "Iv1.second", Secret: "gho_secondsecondsecond-2222"})
	if err != nil || !changed || replaced.ID == old.ID {
		t.Fatalf("replace: %+v, changed %v, %v", replaced, changed, err)
	}
	chosen, err := s.Choose(ctx, orgID, "github")
	if err != nil || chosen.ID != replaced.ID || chosen.ClientID != "Iv1.second" {
		t.Fatalf("a new connect chose %+v, %v; want the replacement", chosen, err)
	}
	var status, pinned string
	if err := s.pool.QueryRow(ctx, `SELECT a.status, c.org_app_id FROM integrations.org_apps a JOIN integrations.connections c ON c.org_app_id = a.id WHERE a.id = $1`, old.ID).
		Scan(&status, &pinned); err != nil || status != "retired" || pinned != old.ID {
		t.Fatalf("old row: status %q pinned %q, %v; want it retired and still pinned", status, pinned, err)
	}
	// The retired row still opens, for the pinned connection to refresh through.
	var sealed secrets.SealedData
	if err := s.pool.QueryRow(ctx, `SELECT secret FROM integrations.org_apps WHERE id = $1`, old.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	opened, err := s.sealer.Open(ctx, vault.Where{Table: "org_apps", Org: orgID, Row: old.ID + "/secret"}, sealed)
	if err != nil || opened.Reveal() != longSecret {
		t.Fatalf("the retired app's secret: %v", err)
	}
}

func TestRetireRemovesTheLiveAppOnce(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	if retired, err := s.Retire(ctx, orgID, "github"); err != nil || retired {
		t.Fatalf("retiring nothing: %v, %v", retired, err)
	}
	if _, _, err := s.Put(ctx, orgID, "github", githubInput()); err != nil {
		t.Fatal(err)
	}
	if retired, err := s.Retire(ctx, orgID, "github"); err != nil || !retired {
		t.Fatalf("retire: %v, %v", retired, err)
	}
	if _, err := s.Get(ctx, orgID, "github"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after retire: %v", err)
	}
	if retired, _ := s.Retire(ctx, orgID, "github"); retired {
		t.Fatal("a retired app was retired again")
	}
	// A new app can be saved in its place.
	if _, changed, err := s.Put(ctx, orgID, "github", githubInput()); err != nil || !changed {
		t.Fatalf("Put after retire: %v, %v", changed, err)
	}
}

func TestChoiceOrderIsTheOrgsAppThenOryxasThenRefusal(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	other := org(t, s)
	const app = "notion"
	if _, err := live(ctx, s.pool, "", app, false); err == nil {
		t.Skip("this database already has a live Oryxa notion app")
	}
	if _, err := s.Choose(ctx, orgID, app); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("nothing set up: %v", err)
	}
	oryxa, _, err := s.Load(ctx, app, Input{Secret: "secret_oryxa_notion_token"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.pool.Exec(context.Background(), `DELETE FROM integrations.org_apps WHERE id = $1`, oryxa.ID) })
	if got, err := s.Choose(ctx, orgID, app); err != nil || got.ID != oryxa.ID || got.Mode != "oryxa" || got.OrgID != "" {
		t.Fatalf("with only Oryxa's: %+v, %v", got, err)
	}
	own, _, err := s.Put(ctx, orgID, app, Input{Secret: "secret_org_notion_token"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := s.Choose(ctx, orgID, app); err != nil || got.ID != own.ID || got.Mode != "byo" {
		t.Fatalf("with its own: %+v, %v", got, err)
	}
	if got, err := s.Choose(ctx, other, app); err != nil || got.ID != oryxa.ID {
		t.Fatalf("another org must not see the first org's app: %+v, %v", got, err)
	}
	// A broken app blocks the connect; it does not fall through to Oryxa's.
	if _, err := s.pool.Exec(ctx, `UPDATE integrations.org_apps SET status = 'broken' WHERE id = $1`, own.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Choose(ctx, orgID, app); !errors.Is(err, ErrBroken) {
		t.Fatalf("a broken org app: %v", err)
	}
	// Retired, the org is back on Oryxa's.
	if _, err := s.Retire(ctx, orgID, app); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Choose(ctx, orgID, app); err != nil || got.ID != oryxa.ID {
		t.Fatalf("after retiring its own: %+v, %v", got, err)
	}
	// Oryxa's own app is one per app.
	if _, _, err := s.Load(ctx, app, Input{Secret: "secret_oryxa_notion_token"}); err != nil {
		t.Fatalf("loading Oryxa's app again: %v", err)
	}
}

func TestInputIsChecked(t *testing.T) {
	s := newService(t)
	ctx := context.Background()
	orgID := org(t, s)
	past := time.Now().Add(-time.Hour)
	tests := []struct {
		name string
		org  string
		app  string
		in   Input
	}{
		{"unknown app", orgID, "salesforce", githubInput()},
		{"org with a slash", "a/b", "github", githubInput()},
		{"empty org", "", "github", githubInput()},
		{"no client id", orgID, "github", Input{Secret: longSecret}},
		{"client id with a space", orgID, "github", Input{ClientID: "a b", Secret: longSecret}},
		{"a key-based app with a client id", orgID, "notion", Input{ClientID: "x", Secret: longSecret}},
		{"no secret on a new app", orgID, "github", Input{ClientID: "Iv1.x"}},
		{"secret too long", orgID, "github", Input{ClientID: "Iv1.x", Secret: secrets.Secret(strings.Repeat("a", maxSecretLength+1))}},
		{"expiry in the past", orgID, "google", Input{ClientID: "x", Secret: longSecret, ExpiresAt: &past}},
		{"bad extra key", orgID, "github", Input{ClientID: "Iv1.x", Secret: longSecret, Extra: map[string]string{"App Id": "1"}}},
	}
	for _, tt := range tests {
		if _, _, err := s.Put(ctx, tt.org, tt.app, tt.in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v; want ErrInvalid", tt.name, err)
		}
	}
	if _, err := s.Get(ctx, orgID, "github"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a refused input left an app behind: %v", err)
	}
}

type allowAll struct{}

func (allowAll) Verify(context.Context, string) (serviceauth.Claims, error) {
	return serviceauth.Claims{Service: "apps"}, nil
}

func TestTheRoutesNeedAServiceTokenAndNeverReturnASecret(t *testing.T) {
	s := newService(t)
	orgID := org(t, s)
	server := httpserver.New(allowAll{}, nil)
	Routes(server, s, nil)
	do := func(method, path, body string, token bool) *httptest.ResponseRecorder {
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		if token {
			request.Header.Set(serviceauth.Header, "t")
		}
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		return recorder
	}
	path := "/v1/orgs/" + orgID + "/apps/github"
	body := `{"client_id":"Iv1.first","secret":"` + longSecret + `","webhook_secret":"` + hookSecret + `","extra":{"app_id":"7"}}`

	if got := do(http.MethodPut, path, body, false); got.Code != http.StatusUnauthorized {
		t.Fatalf("PUT without a token answered %d", got.Code)
	}
	put := do(http.MethodPut, path, body, true)
	if put.Code != http.StatusOK || !strings.Contains(put.Body.String(), `"changed":true`) {
		t.Fatalf("PUT answered %d: %s", put.Code, put.Body)
	}
	get := do(http.MethodGet, path, "", true)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"secret_hint":"TAIL"`) {
		t.Fatalf("GET answered %d: %s", get.Code, get.Body)
	}
	for _, response := range []*httptest.ResponseRecorder{put, get} {
		if strings.Contains(response.Body.String(), "0123456789abcdef") {
			t.Fatalf("a response holds a secret: %s", response.Body)
		}
	}
	if got := do(http.MethodPut, path, `{"client_id":"x","secret":"y","surprise":1}`, true); got.Code != http.StatusBadRequest {
		t.Fatalf("PUT with an unknown field answered %d", got.Code)
	}
	if got := do(http.MethodPut, "/v1/orgs/"+orgID+"/apps/salesforce", body, true); got.Code != http.StatusBadRequest {
		t.Fatalf("PUT for an unknown app answered %d", got.Code)
	}
	if got := do(http.MethodDelete, path, "", true); got.Code != http.StatusNoContent {
		t.Fatalf("DELETE answered %d", got.Code)
	}
	if got := do(http.MethodGet, path, "", true); got.Code != http.StatusNotFound {
		t.Fatalf("GET after DELETE answered %d", got.Code)
	}
	if got := do(http.MethodDelete, path, "", true); got.Code != http.StatusNotFound {
		t.Fatalf("a second DELETE answered %d", got.Code)
	}
}
