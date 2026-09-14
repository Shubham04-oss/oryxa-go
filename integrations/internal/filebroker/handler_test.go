package filebroker

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filesclient"
)

type fakeFiles struct {
	created filesclient.CreateInput
	object  filesclient.Object
	body    []byte
	err     error
}

func (f *fakeFiles) Create(_ context.Context, credentials filesclient.Credentials, organizationID string, input filesclient.CreateInput) (filesclient.Object, error) {
	if credentials.Authorization != "Bearer user-token" || organizationID != "org-1" {
		return filesclient.Object{}, filesclient.ErrForbidden
	}
	f.created = input
	if f.err != nil {
		return filesclient.Object{}, f.err
	}
	return f.object, nil
}

func (f *fakeFiles) Content(_ context.Context, credentials filesclient.Credentials, organizationID, fileID string) (filesclient.Object, []byte, error) {
	if credentials.Authorization != "Bearer user-token" || organizationID != "org-1" || fileID != "file_abc" {
		return filesclient.Object{}, nil, filesclient.ErrNotFound
	}
	if f.err != nil {
		return filesclient.Object{}, nil, f.err
	}
	return f.object, f.body, nil
}

func TestWriteStoresPieceBytesThroughFiles(t *testing.T) {
	t.Parallel()
	sessions := NewMemory()
	_ = sessions.Put(context.Background(), Session{
		Token: "fbk_1", OrganizationID: "org-1", MemberID: "member-1",
		Authorization: "Bearer user-token", ExpiresAt: time.Now().Add(time.Minute),
	})
	files := &fakeFiles{object: filesclient.Object{ID: "file_xyz", Filename: "note.txt", SizeBytes: 5, SHA256: "abc"}}
	handler := Handler{Sessions: sessions, Files: files}

	body, _ := json.Marshal(map[string]string{"filename": "note.txt", "content": base64.StdEncoding.EncodeToString([]byte("hello"))})
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/piece-files", bytes.NewReader(body))
	request.Header.Set("Authorization", "Bearer fbk_1")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.Write(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d/%s", response.Code, response.Body.String())
	}
	if files.created.Purpose != filesclient.PurposePiece || files.created.ContentType != "application/octet-stream" ||
		string(files.created.Content) != "hello" {
		t.Fatalf("created = %+v", files.created)
	}
	if !strings.Contains(response.Body.String(), `"id":"file_xyz"`) {
		t.Fatalf("body = %s", response.Body.String())
	}
}

func TestReadReturnsBytesForHydration(t *testing.T) {
	t.Parallel()
	sessions := NewMemory()
	_ = sessions.Put(context.Background(), Session{
		Token: "fbk_1", OrganizationID: "org-1", MemberID: "member-1",
		Authorization: "Bearer user-token", ExpiresAt: time.Now().Add(time.Minute),
	})
	handler := Handler{Sessions: sessions, Files: &fakeFiles{
		object: filesclient.Object{ID: "file_abc", Filename: "note.txt"}, body: []byte("hello"),
	}}
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/piece-files/file_abc", nil)
	request.SetPathValue("fileID", "file_abc")
	request.Header.Set("Authorization", "Bearer fbk_1")
	response := httptest.NewRecorder()
	handler.Read(response, request)
	if response.Code != http.StatusOK || response.Body.String() != "hello" {
		t.Fatalf("status/body = %d/%q", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Header().Get("Content-Disposition"), "note.txt") {
		t.Fatalf("disposition = %q", response.Header().Get("Content-Disposition"))
	}
}

func TestExpiredBrokerTokenIsRejected(t *testing.T) {
	t.Parallel()
	sessions := NewMemory()
	sessions.now = func() time.Time { return time.Date(2026, 9, 14, 12, 0, 0, 0, time.UTC) }
	_ = sessions.Put(context.Background(), Session{
		Token: "fbk_old", OrganizationID: "org-1", MemberID: "member-1",
		Authorization: "Bearer user-token", ExpiresAt: time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC),
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/piece-files", strings.NewReader(`{}`))
	request.Header.Set("Authorization", "Bearer fbk_old")
	response := httptest.NewRecorder()
	Handler{Sessions: sessions, Files: &fakeFiles{}}.Write(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}
}

func TestUnknownJSONFieldIsRejected(t *testing.T) {
	t.Parallel()
	sessions := NewMemory()
	_ = sessions.Put(context.Background(), Session{
		Token: "fbk_1", OrganizationID: "org-1", MemberID: "member-1",
		Authorization: "Bearer user-token", ExpiresAt: time.Now().Add(time.Minute),
	})
	request := httptest.NewRequest(http.MethodPost, "/v1/internal/piece-files", strings.NewReader(`{"filename":"a.txt","content":"YQ==","extra":true}`))
	request.Header.Set("Authorization", "Bearer fbk_1")
	response := httptest.NewRecorder()
	Handler{Sessions: sessions, Files: &fakeFiles{}}.Write(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d/%s", response.Code, response.Body.String())
	}
}

func TestReadDoesNotTrustPathTraversal(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/v1/internal/piece-files/../secret", nil)
	request.Header.Set("Authorization", "Bearer fbk_1")
	response := httptest.NewRecorder()
	Handler{Sessions: NewMemory(), Files: &fakeFiles{}}.Read(response, request)
	if response.Code == http.StatusOK {
		t.Fatal("traversal was accepted")
	}
	_, _ = io.ReadAll(response.Body)
}
