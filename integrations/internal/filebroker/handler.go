// Package filebroker lets the piece runner read and write objects through
// /files without S3 IAM or the caller's long-lived bearer in the Lambda payload.
package filebroker

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/filesclient"
)

const (
	maxWriteJSON   = (8 << 20) + (1 << 20)
	sessionTTL     = 5 * time.Minute
	maxFilenameLen = 255
)

type Session struct {
	Token          string
	OrganizationID string
	MemberID       string
	Authorization  string
	ExpiresAt      time.Time
}

type Sessions interface {
	Put(context.Context, Session) error
	Get(context.Context, string) (Session, error)
	Delete(context.Context, string) error
}

type Files interface {
	Create(context.Context, filesclient.Credentials, string, filesclient.CreateInput) (filesclient.Object, error)
	Content(context.Context, filesclient.Credentials, string, string) (filesclient.Object, []byte, error)
}

type Memory struct {
	mu      sync.Mutex
	entries map[string]Session
	now     func() time.Time
}

func NewMemory() *Memory {
	return &Memory{entries: map[string]Session{}, now: func() time.Time { return time.Now().UTC() }}
}

func (store *Memory) Put(_ context.Context, session Session) error {
	if session.Token == "" || session.OrganizationID == "" || session.MemberID == "" || session.Authorization == "" {
		return errors.New("file broker session is incomplete")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.entries[session.Token] = session
	return nil
}

func (store *Memory) Get(_ context.Context, token string) (Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session, ok := store.entries[token]
	if !ok || (!session.ExpiresAt.IsZero() && !store.now().Before(session.ExpiresAt)) {
		return Session{}, filesclient.ErrNotFound
	}
	return session, nil
}

func (store *Memory) Delete(_ context.Context, token string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.entries, token)
	return nil
}

type Handler struct {
	Sessions Sessions
	Files    Files
}

func (handler Handler) Write(writer http.ResponseWriter, request *http.Request) {
	session, ok := handler.session(writer, request)
	if !ok {
		return
	}
	handler.write(writer, request, session)
}

func (handler Handler) Read(writer http.ResponseWriter, request *http.Request) {
	session, ok := handler.session(writer, request)
	if !ok {
		return
	}
	handler.read(writer, request, session)
}

func (handler Handler) session(writer http.ResponseWriter, request *http.Request) (Session, bool) {
	if handler.Sessions == nil {
		writeError(writer, http.StatusServiceUnavailable, "file storage is not configured")
		return Session{}, false
	}
	parts := strings.Fields(request.Header.Get("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		writeError(writer, http.StatusUnauthorized, "file broker token is required")
		return Session{}, false
	}
	session, err := handler.Sessions.Get(request.Context(), parts[1])
	if err != nil || session.OrganizationID == "" || session.Authorization == "" {
		writeError(writer, http.StatusUnauthorized, "file broker token is invalid")
		return Session{}, false
	}
	return session, true
}

func (handler Handler) write(writer http.ResponseWriter, request *http.Request, session Session) {
	if handler.Files == nil {
		writeError(writer, http.StatusServiceUnavailable, "file storage is not configured")
		return
	}
	var body struct {
		Filename string `json:"filename"`
		Content  string `json:"content"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, maxWriteJSON+1))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(writer, http.StatusBadRequest, "request body is invalid")
		return
	}
	content, err := base64.StdEncoding.DecodeString(strings.TrimSpace(body.Content))
	if err != nil {
		content, err = base64.RawStdEncoding.DecodeString(strings.TrimSpace(body.Content))
	}
	if err != nil {
		writeError(writer, http.StatusBadRequest, "content must be base64")
		return
	}
	filename := sanitizeFilename(body.Filename)
	object, err := handler.Files.Create(request.Context(), filesclient.Credentials{Authorization: session.Authorization},
		session.OrganizationID, filesclient.CreateInput{
			Filename: filename, ContentType: "application/octet-stream",
			Purpose: filesclient.PurposePiece, Content: content,
		})
	if err != nil {
		writeFilesError(writer, err)
		return
	}
	writeJSON(writer, http.StatusCreated, map[string]any{
		"id": object.ID, "filename": object.Filename, "sha256": object.SHA256,
		"size_bytes": object.SizeBytes,
	})
}

func (handler Handler) read(writer http.ResponseWriter, request *http.Request, session Session) {
	if handler.Files == nil {
		writeError(writer, http.StatusServiceUnavailable, "file storage is not configured")
		return
	}
	fileID := request.PathValue("fileID")
	if fileID == "" {
		fileID = strings.TrimPrefix(request.URL.Path, "/v1/internal/piece-files/")
	}
	if fileID == "" || strings.Contains(fileID, "/") {
		writeError(writer, http.StatusBadRequest, "file id is invalid")
		return
	}
	object, body, err := handler.Files.Content(request.Context(), filesclient.Credentials{Authorization: session.Authorization},
		session.OrganizationID, fileID)
	if err != nil {
		writeFilesError(writer, err)
		return
	}
	filename := object.Filename
	if filename == "" {
		filename = "file"
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename=%q`, filename))
	if object.SHA256 != "" {
		writer.Header().Set("X-Oryxa-SHA256", object.SHA256)
	}
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(body)
}

func sanitizeFilename(raw string) string {
	name := strings.TrimSpace(raw)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, `/\:`) || strings.ContainsRune(name, 0) {
		return "piece.bin"
	}
	if utf8.RuneCountInString(name) > maxFilenameLen {
		runes := []rune(name)
		name = string(runes[:maxFilenameLen])
	}
	return name
}

func writeFilesError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, filesclient.ErrUnauthorized):
		writeError(writer, http.StatusUnauthorized, "files authentication failed")
	case errors.Is(err, filesclient.ErrForbidden):
		writeError(writer, http.StatusForbidden, "file operation is not allowed")
	case errors.Is(err, filesclient.ErrNotFound):
		writeError(writer, http.StatusNotFound, "file was not found")
	case errors.Is(err, filesclient.ErrInvalid):
		writeError(writer, http.StatusBadRequest, "file request is invalid")
	default:
		writeError(writer, http.StatusBadGateway, "files request failed")
	}
}

func writeError(writer http.ResponseWriter, status int, message string) {
	writeJSON(writer, status, map[string]string{"error": message})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func SessionTTL() time.Duration { return sessionTTL }
