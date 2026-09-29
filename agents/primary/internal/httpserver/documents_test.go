package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// The document server's own secret, which it holds, and Oryxa's, which it never
// does: each 32 bytes or more, and different.
const (
	docsSecret   = "the-document-servers-own-secret-of-32-bytes-plus"
	ticketSecret = "oryxas-ticket-secret-that-docs-never-holds-32-bytes-plus"
)

const draftPath = "/v1/organizations/org_1/threads/thread_1/artifacts/draft.docx"

func documentsAt(publicURL, internalURL string) DocumentsConfig {
	return DocumentsConfig{
		PublicURL: publicURL, InternalURL: internalURL, APIURL: "https://api.oryxa.test",
		JWTSecret: docsSecret, TicketSecret: ticketSecret,
	}
}

func fromNow(d time.Duration) *time.Time {
	at := time.Now().Add(d)
	return &at
}

// signedTicket is a ticket for org_1's thread_1 draft.docx, version 1, as the
// member user_1, signed as given: a nil expiry leaves it out, and mutate can
// change the claims before it is signed. The key is derived from the claims
// last, so a ticket changed with mutate still carries a key that matches unless
// the test breaks it on purpose.
func signedTicket(t *testing.T, purpose string, expires *time.Time, method jwt.SigningMethod, key any, mutate func(*documentTicket)) string {
	t.Helper()
	ticket := documentTicket{Purpose: purpose, Organization: "org_1", User: "user_1", Thread: "thread_1", Name: "draft.docx", Version: 1}
	if expires != nil {
		ticket.ExpiresAt = jwt.NewNumericDate(*expires)
	}
	if mutate != nil {
		mutate(&ticket)
	}
	if ticket.Key == "" {
		ticket.Key = documentKey(ticket.Organization, ticket.User, ticket.Thread, ticket.Name, ticket.Version)
	}
	signed, err := jwt.NewWithClaims(method, ticket).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func goodTicket(t *testing.T, purpose string) string {
	t.Helper()
	return signedTicket(t, purpose, fromNow(time.Hour), jwt.SigningMethodHS256, []byte(ticketSecret), nil)
}

// docsSignature is what Docs sends as the Bearer of a save callback.
func docsSignature(t *testing.T, method jwt.SigningMethod, key any, claims jwt.MapClaims) string {
	t.Helper()
	signed, err := jwt.NewWithClaims(method, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

// signedBy is a callback's claims as Docs makes them: the payload, and the
// five minutes it is good for.
func signedBy(payload any) jwt.MapClaims {
	return jwt.MapClaims{"payload": payload, "exp": time.Now().Add(5 * time.Minute).Unix()}
}

func editedKey() string { return documentKey("org_1", "user_1", "thread_1", "draft.docx", 1) }

func tokenValidUnder(raw, secret string, claims jwt.Claims) bool {
	parsed, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return []byte(secret), nil },
		jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired())
	return err == nil && parsed.Valid
}

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
	editedServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/changed.docx" {
			t.Errorf("unexpected download path %s", request.URL.Path)
		}
		_, _ = writer.Write([]byte("docx-edited"))
	}))
	defer editedServer.Close()
	files := &fakeThreads{}
	server := New(member(), files, Config{Documents: documentsAt(editedServer.URL, editedServer.URL)}, nil)
	base := draftPath
	if response := serve(server, http.MethodGet, base+"/editor", "", nil); response.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous editor = %d", response.Code)
	}
	before := time.Now()
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

	// The editor's configuration is Docs' to read, so Docs' secret signs it,
	// and it expires with the session it opens.
	configClaims := jwt.MapClaims{}
	if !tokenValidUnder(session.Config.Token, docsSecret, configClaims) {
		t.Fatal("the editor configuration is not signed with the document server's secret, with an expiry")
	}
	if tokenValidUnder(session.Config.Token, ticketSecret, jwt.MapClaims{}) {
		t.Fatal("the editor configuration is signed with the ticket secret")
	}
	if expiry, err := configClaims.GetExpirationTime(); err != nil || expiry.Before(before.Add(time.Hour)) || expiry.After(time.Now().Add(25*time.Hour)) {
		t.Fatalf("the editor configuration expires at %v (%v), want about a day from now", expiry, err)
	}

	sourceURL, err := url.Parse(session.Config.Document.URL)
	if err != nil {
		t.Fatal(err)
	}
	if sourceURL.Host != "api.oryxa.test" {
		t.Fatalf("source URL host = %s", sourceURL.Host)
	}
	callbackURL, err := url.Parse(session.Config.EditorConfig.CallbackURL)
	if err != nil {
		t.Fatal(err)
	}
	// What Docs may read and write a member's file with is signed by Oryxa
	// alone, never with a secret Docs holds, and each ticket has its own life.
	for name, test := range map[string]struct {
		raw      string
		lifetime time.Duration
	}{
		"source":   {sourceURL.Query().Get("ticket"), time.Hour},
		"callback": {callbackURL.Query().Get("ticket"), 24 * time.Hour},
	} {
		claims := &documentTicket{}
		if !tokenValidUnder(test.raw, ticketSecret, claims) || claims.Purpose != name {
			t.Fatalf("the %s ticket is not a %s ticket signed with the ticket secret with an expiry: %+v", name, name, claims)
		}
		if tokenValidUnder(test.raw, docsSecret, &documentTicket{}) {
			t.Fatalf("the %s ticket is signed with the document server's secret", name)
		}
		if lived := claims.ExpiresAt.Sub(before); lived < test.lifetime-time.Minute || lived > test.lifetime+time.Minute {
			t.Fatalf("the %s ticket lives %v, want about %v", name, lived, test.lifetime)
		}
	}

	source := serve(server, http.MethodGet, sourceURL.RequestURI(), "", nil)
	if source.Code != http.StatusOK || source.Body.String() != "docx-original" {
		t.Fatalf("signed source = %d %s", source.Code, source.Body.String())
	}
	if unsigned := serve(server, http.MethodGet, base+"/editor-source", "", nil); unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned source = %d", unsigned.Code)
	}
	callback := documentCallbackBody{Key: session.Config.Document.Key, Status: 2, URL: editedServer.URL + "/changed.docx", FileType: "docx"}
	body, _ := json.Marshal(callback)
	if unsigned := serve(server, http.MethodPost, callbackURL.RequestURI(), string(body), nil); unsigned.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned callback = %d", unsigned.Code)
	}
	signature := docsSignature(t, jwt.SigningMethodHS256, []byte(docsSecret), signedBy(callback))
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
	convertedSignature := docsSignature(t, jwt.SigningMethodHS256, []byte(docsSecret), signedBy(converted))
	refused := serve(server, http.MethodPost, callbackURL.RequestURI(), string(convertedBody), map[string]string{"Authorization": "Bearer " + convertedSignature})
	if refused.Code != http.StatusOK || !strings.Contains(refused.Body.String(), `"error":1`) {
		t.Fatalf("converted callback = %d %s", refused.Code, refused.Body.String())
	}
}

// Nothing is opened, or read, for a deployment that has no working editor, and
// no ticket is honoured by one.
func TestAnUnconfiguredDocumentServerOpensNothing(t *testing.T) {
	ready := documentsAt("https://docs.oryxa.test", "https://docs.oryxa.test")
	broken := func(change func(*DocumentsConfig)) DocumentsConfig {
		config := ready
		change(&config)
		return config
	}
	for name, config := range map[string]DocumentsConfig{
		"nothing set":                        {},
		"secrets only":                       {JWTSecret: docsSecret, TicketSecret: ticketSecret},
		"no ticket secret":                   broken(func(c *DocumentsConfig) { c.TicketSecret = "" }),
		"no document server secret":          broken(func(c *DocumentsConfig) { c.JWTSecret = "" }),
		"a ticket secret of 31 bytes":        broken(func(c *DocumentsConfig) { c.TicketSecret = strings.Repeat("t", 31) }),
		"a document server secret of 31":     broken(func(c *DocumentsConfig) { c.JWTSecret = strings.Repeat("j", 31) }),
		"one secret for both":                broken(func(c *DocumentsConfig) { c.TicketSecret = c.JWTSecret }),
		"the published sample as the ticket": broken(func(c *DocumentsConfig) { c.TicketSecret = publishedDocumentSecret }),
		"the published sample as Docs'":      broken(func(c *DocumentsConfig) { c.JWTSecret = publishedDocumentSecret }),
		"no public address":                  broken(func(c *DocumentsConfig) { c.PublicURL = "" }),
		"no internal address":                broken(func(c *DocumentsConfig) { c.InternalURL = "" }),
		"no API address":                     broken(func(c *DocumentsConfig) { c.APIURL = "" }),
	} {
		t.Run(name, func(t *testing.T) {
			files := &fakeThreads{}
			server := New(member(), files, Config{Documents: config}, nil)
			for _, mode := range []string{"view", "edit"} {
				response := serve(server, http.MethodGet, draftPath+"/editor?mode="+mode, "", bearer)
				if response.Code != http.StatusServiceUnavailable || errorCode(t, response) != "document_editor_unavailable" {
					t.Fatalf("%s editor = %d %s, want 503", mode, response.Code, response.Body.String())
				}
			}
			if files.fileReads != 0 || files.versionReads != 0 {
				t.Fatalf("an editor that is not set up read %d files and %d versions, want nothing read", files.fileReads, files.versionReads)
			}
			// Tickets signed with any secret a deployment might hold, or none.
			for _, secret := range []string{"", docsSecret, ticketSecret, publishedDocumentSecret, strings.Repeat("a", 31)} {
				source := signedTicket(t, "source", fromNow(time.Hour), jwt.SigningMethodHS256, []byte(secret), nil)
				if response := serve(server, http.MethodGet, draftPath+"/editor-source?ticket="+url.QueryEscape(source), "", nil); response.Code != http.StatusUnauthorized {
					t.Fatalf("a source ticket signed with a %d-byte secret = %d, want 401", len(secret), response.Code)
				}
				callback := documentCallbackBody{Key: editedKey(), Status: 2, FileType: "docx", URL: "http://docs.invalid/x"}
				body, _ := json.Marshal(callback)
				ticket := signedTicket(t, "callback", fromNow(time.Hour), jwt.SigningMethodHS256, []byte(secret), nil)
				headers := map[string]string{"Authorization": "Bearer " + docsSignature(t, jwt.SigningMethodHS256, []byte(secret), signedBy(callback))}
				if response := serve(server, http.MethodPost, draftPath+"/editor-callback?ticket="+url.QueryEscape(ticket), string(body), headers); response.Code != http.StatusUnauthorized {
					t.Fatalf("a callback signed with a %d-byte secret = %d, want 401", len(secret), response.Code)
				}
			}
			if files.fileReads != 0 || len(files.edited) != 0 {
				t.Fatal("an editor that is not set up served or saved a file")
			}
		})
	}
}

func TestADocumentServerThatCannotBeUsedSaysWhyWithoutItsSecrets(t *testing.T) {
	ready := documentsAt("https://docs.oryxa.test", "https://docs.oryxa.test")
	if ready.Problem() != "" || !ready.ready() {
		t.Fatalf("a complete configuration has a problem: %q", ready.Problem())
	}
	if (DocumentsConfig{}).Problem() != "" {
		t.Fatal("a deployment with no document server has nothing to report")
	}
	for name, test := range map[string]struct {
		change func(*DocumentsConfig)
		names  string
	}{
		"an address missing":     {func(c *DocumentsConfig) { c.APIURL = "" }, "AGENTS_PRIMARY_DOCUMENT_API_URL"},
		"a short Docs secret":    {func(c *DocumentsConfig) { c.JWTSecret = "short" }, "AGENTS_PRIMARY_DOCUMENT_JWT_SECRET"},
		"a short ticket secret":  {func(c *DocumentsConfig) { c.TicketSecret = "short" }, "AGENTS_PRIMARY_DOCUMENT_TICKET_SECRET"},
		"the sample as a secret": {func(c *DocumentsConfig) { c.JWTSecret = publishedDocumentSecret }, "published example"},
		"one secret for both":    {func(c *DocumentsConfig) { c.TicketSecret = c.JWTSecret }, "must differ"},
	} {
		t.Run(name, func(t *testing.T) {
			config := ready
			test.change(&config)
			problem := config.Problem()
			if !strings.Contains(problem, test.names) {
				t.Fatalf("Problem() = %q, want it to name %q", problem, test.names)
			}
			for _, secret := range []string{docsSecret, ticketSecret, config.JWTSecret, config.TicketSecret} {
				if len(secret) > 8 && strings.Contains(problem, secret) {
					t.Fatalf("Problem() = %q carries a secret", problem)
				}
			}
		})
	}
}

// The editor is told which version it opens from the file's version alone: a
// file can be tens of megabytes, and Docs fetches it itself, through a ticket.
func TestTheEditorNamesTheVersionItOpensWithoutReadingTheFile(t *testing.T) {
	files := &fakeThreads{}
	server := New(member(), files, Config{Documents: documentsAt("https://docs.oryxa.test", "https://docs.oryxa.test")}, nil)
	response := serve(server, http.MethodGet, draftPath+"/editor?mode=edit", "", bearer)
	if response.Code != http.StatusOK {
		t.Fatalf("editor = %d %s", response.Code, response.Body.String())
	}
	if files.fileReads != 0 || files.versionReads != 1 {
		t.Fatalf("the editor read %d files and %d versions, want one version and no file", files.fileReads, files.versionReads)
	}
	var session struct {
		Version int64 `json:"version"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || session.Version != 1 {
		t.Fatalf("version = %d, %v", session.Version, err)
	}
	// The next open is of the version an edit saved.
	files.edited = []byte("docx-edited")
	if next := serve(server, http.MethodGet, draftPath+"/editor", "", bearer); next.Code != http.StatusOK || !strings.Contains(next.Body.String(), `"version":2`) {
		t.Fatalf("editor after an edit = %d %s", next.Code, next.Body.String())
	}
	// A file that is not there, or not the member's, opens nothing.
	for _, path := range []string{
		"/v1/organizations/org_1/threads/thread_1/artifacts/missing.docx/editor",
		"/v1/organizations/org_1/threads/thread_2/artifacts/draft.docx/editor",
	} {
		if response := serve(server, http.MethodGet, path, "", bearer); response.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404", path, response.Code)
		}
	}
}

// What Docs is given is opaque bytes: a file named like an Office file and
// typed like a page must not be one if a browser is ever handed its URL.
func TestTheSourceIsServedAsBytesThatCannotRunAsAPage(t *testing.T) {
	server := New(member(), &fakeThreads{}, Config{Documents: documentsAt("https://docs.oryxa.test", "https://docs.oryxa.test")}, nil)
	for name, want := range map[string]string{"draft.docx": "docx-original", "trap.docx": "<script>alert(1)</script>"} {
		ticket := signedTicket(t, "source", fromNow(time.Hour), jwt.SigningMethodHS256, []byte(ticketSecret), func(ticket *documentTicket) { ticket.Name = name })
		response := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/thread_1/artifacts/"+name+"/editor-source?ticket="+url.QueryEscape(ticket), "", nil)
		if response.Code != http.StatusOK || response.Body.String() != want {
			t.Fatalf("%s = %d %q", name, response.Code, response.Body.String())
		}
		header := response.Header()
		if header.Get("Content-Type") != "application/octet-stream" {
			t.Fatalf("%s is served as %q, want opaque bytes whatever type the file carries", name, header.Get("Content-Type"))
		}
		if header.Get("Content-Security-Policy") != artifactContentPolicy || !strings.Contains(header.Get("Content-Security-Policy"), "sandbox") {
			t.Fatalf("%s carries the policy %q, want the one every file a run kept carries: %q", name, header.Get("Content-Security-Policy"), artifactContentPolicy)
		}
		if !strings.HasPrefix(header.Get("Content-Disposition"), "attachment") || header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Cache-Control") != "no-store" {
			t.Fatalf("%s headers = %v", name, header)
		}
	}
}

// An edit is saved as the file it edits: only the formats Docs returns as they
// were are ever opened for one, and no callback saves any other.
func TestOnlyFormatsThatRoundTripAreEditable(t *testing.T) {
	for extension, editable := range map[string]bool{
		"docx": true, "xlsx": true, "pptx": true,
		"doc": false, "odt": false, "rtf": false, "txt": false, "md": false,
		"xls": false, "xlsm": false, "ods": false, "csv": false,
		"ppt": false, "ppsx": false, "odp": false, "pdf": false,
	} {
		if editableDocuments[extension] != editable {
			t.Errorf("editable(%s) = %v, want %v", extension, editableDocuments[extension], editable)
		}
		if _, _, viewable := documentType("file." + extension); !viewable {
			t.Errorf("%s is not viewable", extension)
		}
	}

	files := &fakeThreads{}
	server := New(member(), files, Config{Documents: documentsAt("https://docs.oryxa.test", "https://docs.oryxa.test")}, nil)
	legacy := "/v1/organizations/org_1/threads/thread_1/artifacts/legacy.odt/editor"
	if response := serve(server, http.MethodGet, legacy+"?mode=edit", "", bearer); response.Code != http.StatusUnsupportedMediaType || errorCode(t, response) != "unsupported_document" {
		t.Fatalf("editing an ODT = %d %s, want 415", response.Code, response.Body.String())
	}
	if files.fileReads != 0 || files.versionReads != 0 {
		t.Fatal("a file that cannot be edited was read before it was refused")
	}
	// Viewing one is allowed: it gets as far as looking for the file, which
	// this fake does not have.
	if response := serve(server, http.MethodGet, legacy+"?mode=view", "", bearer); response.Code != http.StatusNotFound || files.versionReads != 1 {
		t.Fatalf("viewing an ODT = %d after %d version reads, want it to look for the file", response.Code, files.versionReads)
	}

	// Even a properly signed callback for one saves nothing.
	rig := newDocumentRig(t)
	odt := documentCallbackBody{Key: documentKey("org_1", "user_1", "thread_1", "legacy.odt", 1), Status: 2, FileType: "odt", URL: rig.internalURL + "/x"}
	body, _ := json.Marshal(odt)
	ticket := signedTicket(t, "callback", fromNow(time.Hour), jwt.SigningMethodHS256, []byte(ticketSecret), func(ticket *documentTicket) {
		ticket.Name = "legacy.odt"
		ticket.Key = documentKey(ticket.Organization, ticket.User, ticket.Thread, ticket.Name, ticket.Version)
	})
	response := serve(rig.server, http.MethodPost, "/v1/organizations/org_1/threads/thread_1/artifacts/legacy.odt/editor-callback?ticket="+url.QueryEscape(ticket), string(body),
		map[string]string{"Authorization": "Bearer " + docsSignature(t, jwt.SigningMethodHS256, []byte(docsSecret), signedBy(odt))})
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"error":1`) || len(rig.files.edited) != 0 || rig.internalHits.Load() != 0 {
		t.Fatalf("a callback for an ODT = %d %s; saved %q", response.Code, response.Body.String(), rig.files.edited)
	}
}

// documentRig is a server set up with a document server whose public and
// internal addresses are two servers, and a third that is neither, each
// counting how often it is asked for a file.
type documentRig struct {
	t                                     *testing.T
	server                                *Server
	files                                 *fakeThreads
	publicHits, internalHits, foreignHits atomic.Int32
	lastInternal                          atomic.Value
	publicURL, internalURL, foreignURL    string
	publicHost, internalHost, foreignHost string
	ticket                                string
}

func newDocumentRig(t *testing.T) *documentRig {
	t.Helper()
	rig := &documentRig{t: t, files: &fakeThreads{}}
	public := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rig.publicHits.Add(1)
		_, _ = writer.Write([]byte("from-public"))
	}))
	internal := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rig.internalHits.Add(1)
		rig.lastInternal.Store(request.URL.RequestURI())
		_, _ = writer.Write([]byte("from-internal"))
	}))
	foreign := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		rig.foreignHits.Add(1)
		_, _ = writer.Write([]byte("from-foreign"))
	}))
	t.Cleanup(public.Close)
	t.Cleanup(internal.Close)
	t.Cleanup(foreign.Close)
	rig.publicURL, rig.internalURL, rig.foreignURL = public.URL, internal.URL, foreign.URL
	rig.publicHost = strings.TrimPrefix(public.URL, "http://")
	rig.internalHost = strings.TrimPrefix(internal.URL, "http://")
	rig.foreignHost = strings.TrimPrefix(foreign.URL, "http://")
	rig.server = New(member(), rig.files, Config{Documents: documentsAt(public.URL, internal.URL)}, nil)
	rig.ticket = goodTicket(t, "callback")
	return rig
}

// reset forgets what the servers were asked and what was saved.
func (rig *documentRig) reset() {
	rig.publicHits.Store(0)
	rig.internalHits.Store(0)
	rig.foreignHits.Store(0)
	rig.lastInternal = atomic.Value{}
	rig.files.edited = nil
}

// post sends Docs' callback for the edited draft, signed as Docs signs it.
func (rig *documentRig) post(callback documentCallbackBody) (int, string) {
	rig.t.Helper()
	body, _ := json.Marshal(callback)
	headers := map[string]string{"Authorization": "Bearer " + docsSignature(rig.t, jwt.SigningMethodHS256, []byte(docsSecret), signedBy(callback))}
	response := serve(rig.server, http.MethodPost, draftPath+"/editor-callback?ticket="+url.QueryEscape(rig.ticket), string(body), headers)
	return response.Code, strings.TrimSpace(response.Body.String())
}
