package httpserver

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
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
	// refused is a caller the service turns away before it asks for the body.
	refused error
	created application.CreateInput
	filter  application.ListFilter
	object  domain.Object
	content []byte
	url     application.DownloadURL
	err     error
}

func (fake *fakeService) Create(_ context.Context, _ authclient.Credentials, _ string, read application.InputReader) (domain.Object, error) {
	// Like the real service, the fake asks for the body only of a caller it admits.
	if fake.refused != nil {
		return domain.Object{}, fake.refused
	}
	input, err := read()
	if err != nil {
		return domain.Object{}, err
	}
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
	body := `{"filename":"notes.txt","content_type":"text/plain","purpose":"upload","scope":"team","team_id":"team_eng","access":"members","content":"` + base64.StdEncoding.EncodeToString([]byte("hello")) + `"}`
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files", body)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.created.Filename != "notes.txt" || string(service.created.Content) != "hello" || service.created.Purpose != domain.PurposeUpload || service.created.Scope != domain.ScopeTeam || service.created.TeamID != "team_eng" || service.created.Access != domain.AccessMembers {
		t.Fatalf("created = %+v", service.created)
	}
}

func TestCreateMultipart(t *testing.T) {
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for name, value := range map[string]string{"scope": "team", "team_id": "team_eng", "retention_days": "7", "purpose": "upload"} {
		_ = form.WriteField(name, value)
	}
	part, _ := form.CreateFormFile("file", "notes.txt")
	_, _ = part.Write([]byte("hello"))
	_ = form.Close()

	service := &fakeService{object: domain.Object{ID: "file_1"}}
	request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/files", &body)
	request.Header.Set("Authorization", "Bearer token")
	request.Header.Set("Content-Type", form.FormDataContentType())
	response := httptest.NewRecorder()
	New(service, Config{}, nil).ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.Bytes())
	}
	if service.created.Filename != "notes.txt" || string(service.created.Content) != "hello" || service.created.Scope != domain.ScopeTeam ||
		service.created.TeamID != "team_eng" || service.created.RetentionDays != 7 {
		t.Fatalf("created = %+v", service.created)
	}
}

// countingBody counts what a handler takes from a request body.
type countingBody struct {
	io.Reader
	read int
}

func (body *countingBody) Read(buffer []byte) (int, error) {
	count, err := body.Reader.Read(buffer)
	body.read += count
	return count, err
}

func TestCreateReadsNoBodyForACallerTheServiceRefuses(t *testing.T) {
	for name, contentType := range map[string]string{"json": "application/json", "multipart": "multipart/form-data; boundary=x"} {
		t.Run(name, func(t *testing.T) {
			body := &countingBody{Reader: strings.NewReader(strings.Repeat("A", 4<<20))}
			request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/files", body)
			request.Header.Set("Authorization", "Bearer a-token-nobody-issued")
			request.Header.Set("Content-Type", contentType)
			response := httptest.NewRecorder()
			New(&fakeService{refused: domain.ErrNotFound}, Config{}, nil).ServeHTTP(response, request)
			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want not found", response.Code)
			}
			if body.read != 0 {
				t.Fatalf("read %d bytes of the body of a caller the service refused, want none", body.read)
			}
		})
	}
}

func TestCreateRefusesAnUploadPastTheLimit(t *testing.T) {
	json := `{"filename":"a.txt","content":"` + strings.Repeat("A", int(maxUploadBytes)) + `"}`

	var multipartBody bytes.Buffer
	form := multipart.NewWriter(&multipartBody)
	part, _ := form.CreateFormFile("file", "big.bin")
	_, _ = part.Write(bytes.Repeat([]byte("A"), int(maxUploadBytes)+1))
	_ = form.Close()

	for name, upload := range map[string]struct{ contentType, body string }{
		"json":      {"application/json", json},
		"multipart": {form.FormDataContentType(), multipartBody.String()},
	} {
		t.Run(name, func(t *testing.T) {
			service := &fakeService{}
			request := httptest.NewRequest(http.MethodPost, "/v1/organizations/org_1/files", strings.NewReader(upload.body))
			request.Header.Set("Authorization", "Bearer token")
			request.Header.Set("Content-Type", upload.contentType)
			response := httptest.NewRecorder()
			New(service, Config{}, nil).ServeHTTP(response, request)
			if response.Code != http.StatusRequestEntityTooLarge {
				t.Fatalf("status = %d body = %.200s, want 413", response.Code, response.Body.Bytes())
			}
			if !strings.Contains(response.Body.String(), "at most") {
				t.Fatalf("body = %s, want the limit named", response.Body.Bytes())
			}
			if service.created.Filename != "" {
				t.Fatalf("an oversize upload reached the service: %+v", service.created)
			}
		})
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
		domain.ErrTooLarge:  http.StatusRequestEntityTooLarge,
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

// jsonNUL is how a JSON document spells a NUL inside a string.
const jsonNUL = `\u` + `0000`

func TestCreateRefusesANulInAFieldButNotInTheFile(t *testing.T) {
	service := &fakeService{object: domain.Object{ID: "file_1"}}
	content := base64.StdEncoding.EncodeToString([]byte("a" + string(rune(0)) + "b")) // content is bytes: anything goes
	response := do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files",
		`{"filename":"notes.txt","scope":"team","team_id":"team`+jsonNUL+`_eng","content":"`+content+`"}`)
	if response.Code != http.StatusBadRequest || service.created.TeamID != "" {
		t.Fatalf("status = %d created = %+v, want a 400 and nothing created: %s", response.Code, service.created, response.Body.Bytes())
	}
	response = do(New(service, Config{}, nil), http.MethodPost, "/v1/organizations/org_1/files",
		`{"filename":"notes.txt","content":"`+content+`"}`)
	if response.Code != http.StatusCreated || string(service.created.Content) != "a"+string(rune(0))+"b" {
		t.Fatalf("a file holding a NUL: status = %d created = %q", response.Code, service.created.Content)
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
