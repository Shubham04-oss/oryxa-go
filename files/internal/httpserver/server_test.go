package httpserver

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/files/internal/application"
	"github.com/Shubham04-oss/oryxa-go/files/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/files/internal/domain"
)

type fakeService struct {
	created application.CreateInput
	filter  application.ListFilter
	object  domain.Object
	content []byte
	url     application.DownloadURL
	err     error
}

func (fake *fakeService) Create(_ context.Context, _ authclient.Credentials, _ string, input application.CreateInput) (domain.Object, error) {
	fake.created = input
	if fake.err != nil {
		return domain.Object{}, fake.err
	}
	object := fake.object
	if object.ID == "" {
		object.ID = "file_1"
	}
	object.Filename = input.Filename
	return object, nil
}

func (fake *fakeService) List(_ context.Context, _ authclient.Credentials, _ string, filter application.ListFilter) ([]domain.Object, error) {
	fake.filter = filter
	if fake.err != nil {
		return nil, fake.err
	}
	return []domain.Object{fake.object}, nil
}

func (fake *fakeService) Get(context.Context, authclient.Credentials, string, string) (domain.Object, error) {
	return fake.object, fake.err
}

func (fake *fakeService) Content(context.Context, authclient.Credentials, string, string) (domain.Object, []byte, error) {
	return fake.object, fake.content, fake.err
}

func (fake *fakeService) DownloadURL(context.Context, authclient.Credentials, string, string) (application.DownloadURL, error) {
	return fake.url, fake.err
}

func (fake *fakeService) Delete(context.Context, authclient.Credentials, string, string) error {
	return fake.err
}

func TestPatternsNameEveryRoute(t *testing.T) {
	server := New(&fakeService{}, Config{}, nil)
	patterns := server.Patterns()
	if len(patterns) < 6 {
		t.Fatalf("patterns = %v", patterns)
	}
	wildcards := regexp.MustCompile(`\{[^}]+\}`)
	for _, pattern := range patterns {
		method, path, _ := strings.Cut(pattern, " ")
		request := httptest.NewRequest(method, wildcards.ReplaceAllString(path, "x"), nil)
		if _, matched := server.mux.Handler(request); matched != pattern {
			t.Errorf("%s routes to %q", pattern, matched)
		}
	}
}

func TestHealthz(t *testing.T) {
	response := httptest.NewRecorder()
	New(&fakeService{}, Config{}, slog.New(slog.DiscardHandler)).ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if response.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
	}
}

func TestCreateJSON(t *testing.T) {
	service := &fakeService{object: domain.Object{ID: "file_1", Filename: "notes.txt"}}
	body := `{"filename":"notes.txt","content_type":"text/plain","purpose":"upload","scope":"team","team_id":"team_eng","content":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}`
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.created.Filename != "notes.txt" || string(service.created.Content) != "hello" || service.created.Purpose != domain.PurposeUpload || service.created.Scope != domain.ScopeTeam || service.created.TeamID != "team_eng" {
		t.Fatalf("created = %+v", service.created)
	}
}

func TestListQuery(t *testing.T) {
	service := &fakeService{object: domain.Object{ID: "file_1"}}
	response := do(New(service, Config{}, nil), http.MethodGet, "/v1/organizations/org_1/files?scope=team&team_id=team_eng&purpose=upload&year=2026&month=9&day=14", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.filter.Scope != domain.ScopeTeam || service.filter.TeamID != "team_eng" || service.filter.Purpose != domain.PurposeUpload || service.filter.Date != (domain.DateFilter{Year: 2026, Month: 9, Day: 14}) {
		t.Fatalf("filter = %+v", service.filter)
	}
}

func TestContent(t *testing.T) {
	service := &fakeService{object: domain.Object{Filename: "notes.txt", ContentType: "text/plain", SHA256: "abc"}, content: []byte("hello")}
	response := do(New(service, Config{}, nil), http.MethodGet, "/v1/organizations/org_1/files/file_1/content", "")
	if response.Code != http.StatusOK || response.Body.String() != "hello" {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if response.Header().Get("Content-Type") != "text/plain" || response.Header().Get("X-Oryxa-SHA256") != "abc" {
		t.Fatalf("headers = %v", response.Header())
	}
}

func TestMapsDomainErrors(t *testing.T) {
	tests := map[error]int{
		domain.ErrInvalid:   http.StatusBadRequest,
		domain.ErrForbidden: http.StatusForbidden,
		domain.ErrNotFound:  http.StatusNotFound,
		domain.ErrConflict:  http.StatusConflict,
	}
	for err, status := range tests {
		service := &fakeService{err: err}
		response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files", `{"filename":"a.txt","content":"YQ=="}`)
		if response.Code != status {
			t.Fatalf("%v: status = %d, want %d", err, response.Code, status)
		}
	}
}

func TestCookieMutationRequiresOrigin(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/files", strings.NewReader(`{"filename":"a.txt","content":"YQ=="}`))
	request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: "cookie"})
	response := httptest.NewRecorder()
	New(&fakeService{}, Config{AllowedOrigins: []string{"http://localhost:3000"}}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want forbidden", response.Code)
	}
}

func TestDownloadURL(t *testing.T) {
	service := &fakeService{url: application.DownloadURL{URL: "https://s3.example/obj"}}
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files/file_1/download-url", "")
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	var body application.DownloadURL
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.URL != "https://s3.example/obj" {
		t.Fatalf("body = %s", response.Body.Bytes())
	}
}

func do(handler http.Handler, method, path, body string) *httptest.ResponseRecorder {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	request.Header.Set("Authorization", "Bearer token")
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
