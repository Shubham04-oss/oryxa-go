package httpserver

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// DocumentsConfig connects Oryxa's private artifacts to an ONLYOFFICE Docs
// server. PublicURL is loaded by the browser; InternalURL is used by Oryxa to
// retrieve a saved edit; APIURL is how Docs reaches Oryxa from its container.
type DocumentsConfig struct {
	PublicURL   string
	InternalURL string
	APIURL      string
	JWTSecret   string
}

func (config DocumentsConfig) ready() bool {
	return config.PublicURL != "" && config.InternalURL != "" && config.APIURL != "" && len(config.JWTSecret) >= 32
}

const (
	// Docs fetches the file once, when it opens a version; the tickets in an
	// editing session's URLs outlive that only as long as the session may.
	sourceTicketLifetime = time.Hour
	// The longest an editing session can go on saving, and so the longest the
	// callback ticket, and the editor configuration that carries it, live.
	editingSessionLifetime = 24 * time.Hour
)

type documentTicket struct {
	Purpose      string `json:"purpose"`
	Organization string `json:"organization"`
	User         string `json:"user"`
	Thread       string `json:"thread"`
	Name         string `json:"name"`
	Version      int64  `json:"version"`
	Key          string `json:"key"`
	jwt.RegisteredClaims
}

var documentTypes = map[string]string{
	"doc": "word", "docx": "word", "odt": "word", "rtf": "word", "txt": "word", "md": "word",
	"xls": "cell", "xlsx": "cell", "xlsm": "cell", "ods": "cell", "csv": "cell",
	"ppt": "slide", "pptx": "slide", "ppsx": "slide", "odp": "slide",
	"pdf": "pdf",
}

func documentType(name string) (string, string, bool) {
	extension := strings.TrimPrefix(strings.ToLower(path.Ext(name)), ".")
	kind, ok := documentTypes[extension]
	return extension, kind, ok
}

func documentKey(organization, user, thread, name string, version int64) string {
	sum := sha256.Sum256([]byte(strings.Join([]string{organization, user, thread, name, strconv.FormatInt(version, 10)}, "\x00")))
	return "oryxa-" + hex.EncodeToString(sum[:24])
}

func (server *Server) signDocumentTicket(ticket documentTicket) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, ticket).SignedString([]byte(server.documents.JWTSecret))
}

func (server *Server) readDocumentTicket(request *http.Request, purpose string) (*documentTicket, bool) {
	raw := request.URL.Query().Get("ticket")
	if raw == "" || !server.documents.ready() {
		return nil, false
	}
	claims := &documentTicket{}
	parsed, err := jwt.ParseWithClaims(raw, claims, func(token *jwt.Token) (any, error) {
		return []byte(server.documents.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid || claims.Purpose != purpose || claims.Organization != request.PathValue("organizationID") ||
		claims.Thread != request.PathValue("threadID") || claims.Name != request.PathValue("name") || claims.User == "" || claims.Version < 1 ||
		claims.Key != documentKey(claims.Organization, claims.User, claims.Thread, claims.Name, claims.Version) {
		return nil, false
	}
	return claims, true
}

func (server *Server) documentEditor(writer http.ResponseWriter, request *http.Request) {
	member, ok := server.member(writer, request)
	if !ok {
		return
	}
	mode := request.URL.Query().Get("mode")
	if mode == "" {
		mode = "view"
	}
	if mode != "view" && mode != "edit" {
		writeError(writer, http.StatusBadRequest, "invalid_request", "mode must be view or edit")
		return
	}
	editing := mode == "edit"
	name := request.PathValue("name")
	extension, kind, supported := documentType(name)
	if !supported {
		writeError(writer, http.StatusUnsupportedMediaType, "unsupported_document", "This file type cannot be edited here.")
		return
	}
	// Nothing is read for a deployment that has no editor to hand it to.
	if !server.documents.ready() {
		writeError(writer, http.StatusServiceUnavailable, "document_editor_unavailable", "Document editing is not configured.")
		return
	}
	// The editor opens the latest version, and only its number is needed to
	// say which: the file itself can be tens of megabytes, and Docs fetches it
	// on its own, through a ticket.
	version, err := server.threads.ArtifactVersion(request.Context(), member, request.PathValue("threadID"), name)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	if version < 1 {
		writeError(writer, http.StatusInternalServerError, "document_version_missing", "Could not identify this file version.")
		return
	}
	key := documentKey(member.OrganizationID, member.UserID, request.PathValue("threadID"), name, version)
	baseTicket := documentTicket{
		Organization: member.OrganizationID, User: member.UserID, Thread: request.PathValue("threadID"),
		Name: name, Version: version, Key: key,
	}
	now := time.Now()
	sourceTicket := baseTicket
	sourceTicket.Purpose = "source"
	sourceTicket.ExpiresAt = jwt.NewNumericDate(now.Add(sourceTicketLifetime))
	signedSource, err := server.signDocumentTicket(sourceTicket)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	callbackTicket := baseTicket
	callbackTicket.Purpose = "callback"
	callbackTicket.ExpiresAt = jwt.NewNumericDate(now.Add(editingSessionLifetime))
	signedCallback, err := server.signDocumentTicket(callbackTicket)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	basePath := "/v1/organizations/" + url.PathEscape(member.OrganizationID) + "/threads/" + url.PathEscape(baseTicket.Thread) + "/artifacts/" + url.PathEscape(name)
	sourceURL := strings.TrimRight(server.documents.APIURL, "/") + basePath + "/editor-source?ticket=" + url.QueryEscape(signedSource)
	callbackURL := strings.TrimRight(server.documents.APIURL, "/") + basePath + "/editor-callback?ticket=" + url.QueryEscape(signedCallback)
	config := map[string]any{
		"documentType": kind,
		"type":         "desktop",
		"width":        "100%", "height": "100%",
		"document": map[string]any{
			"fileType": extension, "key": key, "title": path.Base(strings.TrimPrefix(name, "user:")), "url": sourceURL,
			"permissions": map[string]any{"edit": editing, "download": true, "print": true, "comment": editing},
		},
		"editorConfig": map[string]any{
			"mode": mode, "callbackUrl": callbackURL,
			"user":          map[string]any{"id": member.UserID, "name": "Member"},
			"customization": map[string]any{"autosave": true, "forcesave": false, "theme": "theme-dark"},
		},
	}
	// Docs reads its configuration from this token, so it expires with the
	// session it opens: no longer than the callback ticket it carries.
	claims := jwt.MapClaims{"exp": now.Add(editingSessionLifetime).Unix()}
	for field, value := range config {
		claims[field] = value
	}
	signature, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(server.documents.JWTSecret))
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	config["token"] = signature
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Referrer-Policy", "no-referrer")
	writeJSON(writer, http.StatusOK, map[string]any{
		"server_url": strings.TrimRight(server.documents.PublicURL, "/"),
		"config":     config,
		"version":    version,
	})
}

// The editor server fetches the exact member-authorized file version through a
// short-lived, scoped URL. It never receives the member's session cookie.
func (server *Server) documentSource(writer http.ResponseWriter, request *http.Request) {
	ticket, ok := server.readDocumentTicket(request, "source")
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_document_ticket", "Document access has expired.")
		return
	}
	file, err := server.threads.Artifact(request.Context(), threads.Member{UserID: ticket.User, OrganizationID: ticket.Organization}, ticket.Thread, ticket.Name, ticket.Version)
	if err != nil {
		server.handleError(writer, request, err)
		return
	}
	header := writer.Header()
	// Docs reads these bytes as the file type its configuration names, so what
	// the file says it is matters to no one, and a browser handed the ticket's
	// URL must never take it for a page: whatever a run kept is opaque bytes.
	header.Set("Content-Type", "application/octet-stream")
	header.Set("Content-Security-Policy", artifactContentPolicy)
	header.Set("Cache-Control", "no-store")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": path.Base(file.Name)}))
	header.Set("Content-Length", strconv.Itoa(len(file.Data)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(file.Data)
}

type documentCallbackBody struct {
	Key      string `json:"key"`
	Status   int    `json:"status"`
	URL      string `json:"url"`
	FileType string `json:"filetype"`
}

func (server *Server) signedDocumentCallback(request *http.Request) (documentCallbackBody, bool) {
	authorization := strings.TrimSpace(request.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		return documentCallbackBody{}, false
	}
	claims := jwt.MapClaims{}
	// Docs signs each callback with an expiry (five minutes by default), so a
	// signature that never expires is not one of its own and could be replayed.
	parsed, err := jwt.ParseWithClaims(strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer ")), claims, func(token *jwt.Token) (any, error) {
		return []byte(server.documents.JWTSecret), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return documentCallbackBody{}, false
	}
	payload, ok := claims["payload"]
	if !ok {
		return documentCallbackBody{}, false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return documentCallbackBody{}, false
	}
	var callback documentCallbackBody
	if json.Unmarshal(encoded, &callback) != nil || callback.Key == "" {
		return documentCallbackBody{}, false
	}
	return callback, true
}

func (server *Server) documentCallback(writer http.ResponseWriter, request *http.Request) {
	ticket, ok := server.readDocumentTicket(request, "callback")
	if !ok {
		writeError(writer, http.StatusUnauthorized, "invalid_document_ticket", "Document callback has expired.")
		return
	}
	callback, ok := server.signedDocumentCallback(request)
	if !ok || callback.Key != ticket.Key {
		writeError(writer, http.StatusUnauthorized, "invalid_document_signature", "Document callback is not signed for this file.")
		return
	}
	var body documentCallbackBody
	if json.NewDecoder(io.LimitReader(request.Body, 64<<10)).Decode(&body) != nil || body != callback {
		writeError(writer, http.StatusBadRequest, "invalid_document_callback", "Document callback body does not match its signature.")
		return
	}
	if callback.Status != 2 { // 2 is the completed edit; other statuses do not contain a final file.
		writeJSON(writer, http.StatusOK, map[string]int{"error": 0})
		return
	}
	// Docs may return an OOXML conversion for a legacy source when it cannot
	// assemble the original format. Never write those bytes under the source
	// extension: the next open would misidentify the file.
	extension, _, supported := documentType(ticket.Name)
	if !supported || !strings.EqualFold(callback.FileType, extension) {
		server.logger.ErrorContext(request.Context(), "edited document changed format", "name", ticket.Name, "returned_type", callback.FileType)
		writeJSON(writer, http.StatusOK, map[string]int{"error": 1})
		return
	}
	content, err := server.downloadDocumentEdit(request.Context(), callback.URL)
	if err != nil {
		server.logger.ErrorContext(request.Context(), "download edited document", "error", err)
		writeJSON(writer, http.StatusOK, map[string]int{"error": 1})
		return
	}
	// The new version is the file it replaces, edited: it keeps that version's
	// media type, which SaveArtifact reads from the file itself.
	_, err = server.threads.SaveArtifact(request.Context(), threads.Member{UserID: ticket.User, OrganizationID: ticket.Organization}, ticket.Thread, ticket.Name, ticket.Version, content)
	if err != nil {
		server.logger.ErrorContext(request.Context(), "save edited document", "error", err)
		writeJSON(writer, http.StatusOK, map[string]int{"error": 1})
		return
	}
	writeJSON(writer, http.StatusOK, map[string]int{"error": 0})
}

func (server *Server) downloadDocumentEdit(ctx context.Context, raw string) ([]byte, error) {
	public, publicErr := url.Parse(server.documents.PublicURL)
	internal, internalErr := url.Parse(server.documents.InternalURL)
	requested, requestErr := url.Parse(raw)
	if publicErr != nil || internalErr != nil || requestErr != nil || requested.User != nil || requested.Fragment != "" ||
		(requested.Host != public.Host && requested.Host != internal.Host) ||
		(requested.Scheme != "http" && requested.Scheme != "https") {
		return nil, errors.New("edited file URL is not from the configured document server")
	}
	internal.Path = requested.Path
	internal.RawQuery = requested.RawQuery
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, internal.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("document server returned %d", response.StatusCode)
	}
	const maxDocumentBytes = 50 << 20
	content, err := io.ReadAll(io.LimitReader(response.Body, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(content) == 0 || len(content) > maxDocumentBytes || bytes.Equal(content, []byte("null")) {
		return nil, errors.New("edited file is empty or too large")
	}
	return content, nil
}
