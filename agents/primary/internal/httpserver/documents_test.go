package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/golang-jwt/jwt/v5"
)

func TestDocumentKindsCoverMajorOfficeFormats(t *testing.T) {
	for name, want := range map[string]string{
		"report.docx": "word", "deck.pptx": "slide", "budget.xlsx": "cell", "invoice.pdf": "pdf",
		"notes.odt": "word", "table.ods": "cell", "talk.odp": "slide", "data.csv": "cell",
	} {
		_, got, ok := documentType(name)
		if !ok || got != want {
			t.Errorf("%s = %q, %v; want %q", name, got, ok, want)
		}
	}
	if _, _, ok := documentType("index.html"); ok {
		t.Fatal("HTML must not be passed to the document editor")
	}
	if _, _, ok := documentType("data.tsv"); ok {
		t.Fatal("TSV is not a Docs API file type")
	}
}

func TestDocumentEditorUsesSignedScopedSourceAndCallback(t *testing.T) {
	const secret = "an-example-secret-with-more-than-32-characters"
	editedServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/changed.docx" {
			t.Errorf("unexpected download path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte("docx-edited"))
	}))
	defer editedServer.Close()
	files := &fakeThreads{}
	server := New(member(), files, Config{Documents: DocumentsConfig{
		PublicURL: editedServer.URL, InternalURL: editedServer.URL,
		APIURL: "https://api.oryxa.test", JWTSecret: secret,
	}}, nil)
	base := "/v1/organizations/org_1/threads/thread_1/artifacts/draft.docx"
	if response := serve(server, http.MethodGet, base+"/editor", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous editor = %d", response.Code)
	}
	response := serve(server, http.MethodGet, base+"/editor", "", bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("editor = %d %s", response.Code, response.Body.String())
	}
	var session struct {
		ServerURL string `json:"server_url"`
		Version   int64  `json:"version"`
		Config    struct {
			Token    string `json:"token"`
			Document struct {
				Key         string `json:"key"`
				URL         string `json:"url"`
				Permissions struct {
					Edit bool `json:"edit"`
				} `json:"permissions"`
			} `json:"document"`
			EditorConfig struct {
				CallbackURL string `json:"callbackUrl"`
				Mode        string `json:"mode"`
			} `json:"editorConfig"`
		} `json:"config"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Version != 1 || session.ServerURL != editedServer.URL || session.Config.Token == "" || session.Config.Document.Key == "" {
		t.Fatalf("incomplete editor session: %+v", session)
	}
	if session.Config.EditorConfig.Mode != "view" || session.Config.Document.Permissions.Edit {
		t.Fatalf("editor should open read-only: %+v", session.Config)
	}
	if invalid := serve(server, http.MethodGet, base+"/editor?mode=other", "", bearer); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid editor mode = %d", invalid.Code)
	}
	edit := serve(server, http.MethodGet, base+"/editor?mode=edit", "", bearer)
	if edit.Code != http.StatusOK {
		t.Fatalf("edit mode = %d %s", edit.Code, edit.Body.String())
	}
	var editSession struct {
		Config struct {
			Document struct {
				Permissions struct {
					Edit bool `json:"edit"`
				} `json:"permissions"`
			} `json:"document"`
			EditorConfig struct {
				Mode string `json:"mode"`
			} `json:"editorConfig"`
		} `json:"config"`
	}
	if err := json.Unmarshal(edit.Body.Bytes(), &editSession); err != nil {
		t.Fatal(err)
	}
	if editSession.Config.EditorConfig.Mode != "edit" || !editSession.Config.Document.Permissions.Edit {
		t.Fatalf("edit button should permit editing: %+v", editSession.Config)
	}
	sourceURL, err := url.Parse(session.Config.Document.URL)
	if err != nil {
		t.Fatal(err)
	}
	if sourceURL.Host != "api.oryxa.test" {
		t.Fatalf("source URL host = %s", sourceURL.Host)
	}
	source := serve(server, http.MethodGet, sourceURL.RequestURI(), "", nil)
	if source.Code != http.StatusOK || source.Body.String() != "docx-original" {
		t.Fatalf("signed source = %d %s", source.Code, source.Body.String())
	}
	if unsigned := serve(server, http.MethodGet, base+"/editor-source", "", nil); unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned source = %d", unsigned.Code)
	}
	callbackURL, err := url.Parse(session.Config.EditorConfig.CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	callback := documentCallbackBody{Key: session.Config.Document.Key, Status: 2, URL: editedServer.URL + "/changed.docx", FileType: "docx"}
	body, _ := json.Marshal(callback)
	if unsigned := serve(server, http.MethodPost, callbackURL.RequestURI(), string(body), nil); unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned callback = %d", unsigned.Code)
	}
	signature, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"payload": callback}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	saved := serve(server, http.MethodPost, callbackURL.RequestURI(), string(body), map[string]string{"Authorization": "Bearer " + signature})
	if saved.Code != http.StatusOK || !strings.Contains(saved.Body.String(), `"error":0`) || string(files.edited) != "docx-edited" {
		t.Fatalf("save callback = %d %s; file = %q", saved.Code, saved.Body.String(), files.edited)
	}
	wrong := serve(server, http.MethodPost, callbackURL.RequestURI(), `{"key":"another-file","status":2}`, map[string]string{"Authorization": "Bearer " + signature})
	if wrong.Code != http.StatusBadRequest {
		t.Fatalf("mismatched callback body = %d", wrong.Code)
	}
	converted := callback
	converted.FileType = "odt"
	convertedBody, _ := json.Marshal(converted)
	convertedSignature, err := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"payload": converted}).SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	refused := serve(server, http.MethodPost, callbackURL.RequestURI(), string(convertedBody), map[string]string{"Authorization": "Bearer " + convertedSignature})
	if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), `"error":1`) {
		t.Fatalf("converted callback = %d %s", refused.Code, refused.Body.String())
	}
}
