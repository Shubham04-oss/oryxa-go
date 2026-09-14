package filesclient

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCreatePostsMemberScopedPieceObject(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/organizations/org-1/files" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer user-token" {
			t.Fatalf("authorization = %q", request.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if payload["purpose"] != PurposePiece || payload["scope"] != "member" || payload["filename"] != "note.txt" ||
			payload["content_type"] != "application/octet-stream" || payload["content"] != "aGVsbG8=" {
			t.Fatalf("payload = %s", body)
		}
		writer.WriteHeader(http.StatusCreated)
		_, _ = writer.Write([]byte(`{"id":"file_abc","filename":"note.txt","content_type":"application/octet-stream","size_bytes":5}`))
	}))
	t.Cleanup(server.Close)

	object, err := New(server.URL).Create(t.Context(), Credentials{Authorization: "Bearer user-token"}, "org-1", CreateInput{
		Filename: "note.txt", Content: []byte("hello"),
	})
	if err != nil || object.ID != "file_abc" || object.Filename != "note.txt" {
		t.Fatalf("object/error = %+v/%v", object, err)
	}
}

func TestContentReadsBytesThroughFilesHTTP(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1/organizations/org-1/files/file_abc/content" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		writer.Header().Set("Content-Type", "text/plain")
		writer.Header().Set("Content-Disposition", `attachment; filename="note.txt"`)
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("hello"))
	}))
	t.Cleanup(server.Close)

	object, body, err := New(server.URL).Content(t.Context(), Credentials{Authorization: "Bearer user-token"}, "org-1", "file_abc")
	if err != nil || object.Filename != "note.txt" || string(body) != "hello" {
		t.Fatalf("content = %+v/%q/%v", object, body, err)
	}
}

func TestCreateMapsFilesStatusErrors(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)
	_, err := New(server.URL).Create(t.Context(), Credentials{Authorization: "Bearer user-token"}, "org-1", CreateInput{
		Filename: "note.txt", Content: []byte("hello"),
	})
	if err != ErrNotFound {
		t.Fatalf("error = %v", err)
	}
}
