package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// binaryClient builds a client whose proxy answers a scripted sequence and
// whose binary fetch — the second hop composio.go follows for an export or a
// download, since Composio never returns a file's bytes inline — answers
// whatever binaries maps a URL to, so a test never makes a network call.
func binaryClient(t *testing.T, proxy *fakeProxier, binaries map[string][]byte) *Client {
	t.Helper()
	doer := &composioDoer{proxy: proxy, binaries: func(_ context.Context, url string) ([]byte, error) {
		content, ok := binaries[url]
		if !ok {
			return nil, fmt.Errorf("the test scripted no binary for %s", url)
		}
		return content, nil
	}}
	client, _ := testClient(t, doer)
	return client
}

func jsonResponse(body string) composio.ProxyResponse {
	return composio.ProxyResponse{Status: http.StatusOK, Data: json.RawMessage(body)}
}

func binaryResponse(url, contentType string) composio.ProxyResponse {
	return composio.ProxyResponse{Status: http.StatusOK, Binary: &composio.BinaryData{URL: url, ContentType: contentType}}
}

func driveRequest(target map[string]string, full bool, cursor domain.Cursor) Request {
	return Request{
		Source:     domain.Source{OrganizationID: "org_1", Target: target},
		Credential: composioCredential("ca_1"),
		Full:       full,
		Cursor:     cursor,
	}
}

func TestGoogleDriveTargetAcceptsNoFolderOrANamedOne(t *testing.T) {
	source := NewGoogleDrive(nil)
	if err := source.Target(map[string]string{}); err != nil {
		t.Fatalf("Target(nothing) error = %v, want nil: the whole Drive is the default", err)
	}
	if err := source.Target(map[string]string{"folder": "f1"}); err != nil {
		t.Fatalf("Target(folder) error = %v", err)
	}
	if err := source.Target(map[string]string{"folder": "  "}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target(blank folder) error = %v, want ErrInvalid", err)
	}
}

// TestGoogleDriveFullPassReadsExportsDownloadsAndSkips covers the whole shape
// of a document Fetch has an opinion about: a Doc and a Slide exported as
// text, a Sheet exported as CSV, a PDF downloaded whole, an image skipped
// outright, and a huge PDF skipped for its size without ever being
// downloaded at all.
func TestGoogleDriveFullPassReadsExportsDownloadsAndSkips(t *testing.T) {
	list := `{"files":[
		{"id":"doc1","name":"Notes","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-09-01T00:00:00Z","webViewLink":"https://docs/doc1"},
		{"id":"sheet1","name":"Budget","mimeType":"application/vnd.google-apps.spreadsheet","modifiedTime":"2026-09-02T00:00:00Z","webViewLink":"https://docs/sheet1"},
		{"id":"pdf1","name":"Report.pdf","mimeType":"application/pdf","modifiedTime":"2026-09-03T00:00:00Z","webViewLink":"https://docs/pdf1","size":"13","md5Checksum":"abc123"},
		{"id":"img1","name":"Photo.png","mimeType":"image/png","modifiedTime":"2026-09-04T00:00:00Z"},
		{"id":"huge1","name":"Huge.pdf","mimeType":"application/pdf","modifiedTime":"2026-09-05T00:00:00Z","size":"99999999999"}
	],"nextPageToken":""}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(list),
		binaryResponse("https://fake/doc1.txt", "text/plain"),
		binaryResponse("https://fake/sheet1.csv", "text/csv"),
		binaryResponse("https://fake/pdf1.bin", "application/pdf"),
		jsonResponse(`{"startPageToken":"tok1"}`),
	}}
	client := binaryClient(t, proxy, map[string][]byte{
		"https://fake/doc1.txt":   []byte("Doc one content"),
		"https://fake/sheet1.csv": []byte("a,b,c\n1,2,3"),
		"https://fake/pdf1.bin":   []byte("%PDF-1.4 fake bytes"),
	})
	source := NewGoogleDrive(client)

	page, err := source.Fetch(context.Background(), driveRequest(nil, true, domain.Cursor{}))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 3 {
		t.Fatalf("Fetch() = %d items, want 3 (doc, sheet, pdf); got %+v", len(page.Items), page.Items)
	}
	byID := map[string]Item{}
	for _, item := range page.Items {
		byID[item.ExternalID] = item
	}
	if got := byID["doc1"]; got.MIME != transform.MIMEPlain || string(got.Content) != "Doc one content" {
		t.Fatalf("doc1 = %+v, want plain text export", got)
	}
	if got := byID["sheet1"]; got.MIME != transform.MIMECSV || string(got.Content) != "a,b,c\n1,2,3" {
		t.Fatalf("sheet1 = %+v, want CSV export", got)
	}
	if got := byID["pdf1"]; got.MIME != "application/pdf" || string(got.Content) != "%PDF-1.4 fake bytes" {
		t.Fatalf("pdf1 = %+v, want the raw PDF bytes with its real MIME", got)
	}
	if got, ok := byID["huge1"]; ok {
		t.Fatalf("a file over the size ceiling was downloaded and kept: %+v", got)
	}
	if page.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty: the whole Drive was read in one page", page.Cursor)
	}
	if page.Token != "tok1" {
		t.Fatalf("Fetch() token = %q, want the fresh start page token", page.Token)
	}
	// The huge file's size was known from the listing alone: nothing here
	// ever asked Drive to download it.
	if len(proxy.calls) != 5 {
		t.Fatalf("made %d proxy calls, want 5 (list, 2 exports, 1 download, startPageToken); calls = %+v", len(proxy.calls), proxy.calls)
	}
}

// TestGoogleDriveFolderTargetWalksSubfoldersBreadthFirst is the one thing
// Drive's own query language cannot do in one call: "under this folder,
// however deep." Fetch queues a subfolder it finds rather than descending
// immediately, and resumes the queue across calls the same way a walk that
// outlives one run resumes anything else.
func TestGoogleDriveFolderTargetWalksSubfoldersBreadthFirst(t *testing.T) {
	rootListing := `{"files":[
		{"id":"sub1","name":"Sub","mimeType":"application/vnd.google-apps.folder","modifiedTime":"2026-09-01T00:00:00Z"},
		{"id":"pdf_a","name":"A.pdf","mimeType":"application/pdf","modifiedTime":"2026-09-01T00:00:00Z","size":"5"}
	],"nextPageToken":""}`
	subListing := `{"files":[
		{"id":"doc_b","name":"B","mimeType":"application/vnd.google-apps.document","modifiedTime":"2026-09-02T00:00:00Z"}
	],"nextPageToken":""}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(rootListing),
		binaryResponse("https://fake/a.bin", "application/pdf"),
		jsonResponse(subListing),
		binaryResponse("https://fake/b.txt", "text/plain"),
		jsonResponse(`{"startPageToken":"tok2"}`),
	}}
	client := binaryClient(t, proxy, map[string][]byte{
		"https://fake/a.bin": []byte("aaaaa"),
		"https://fake/b.txt": []byte("doc b text"),
	})
	source := NewGoogleDrive(client)
	target := map[string]string{"folder": "root_folder"}

	first, err := source.Fetch(context.Background(), driveRequest(target, true, domain.Cursor{}))
	if err != nil {
		t.Fatalf("Fetch() [first] error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].ExternalID != "pdf_a" {
		t.Fatalf("first page items = %+v, want only pdf_a: the folder found is queued, not read as a document", first.Items)
	}
	if first.Cursor == "" {
		t.Fatal("first page cursor is empty, want the walk to continue into the queued subfolder")
	}
	// The query for the root folder must name it, not something else.
	if call := proxy.calls[0]; !strings.Contains(call.Endpoint, "root_folder") {
		t.Fatalf("the first list call = %s, want it scoped to root_folder", call.Endpoint)
	}

	second, err := source.Fetch(context.Background(), driveRequest(target, true, domain.Cursor{Value: first.Cursor}))
	if err != nil {
		t.Fatalf("Fetch() [second] error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ExternalID != "doc_b" {
		t.Fatalf("second page items = %+v, want the subfolder's own document", second.Items)
	}
	if second.Cursor != "" {
		t.Fatalf("second page cursor = %q, want empty: the whole tree is drained", second.Cursor)
	}
	if second.Token != "tok2" {
		t.Fatalf("second page token = %q, want the fresh start page token", second.Token)
	}
	// The subfolder's own query must name sub1, taken from what the first
	// page discovered — not the root folder again.
	if call := proxy.calls[2]; !strings.Contains(call.Endpoint, "sub1") {
		t.Fatalf("the second list call = %s, want it scoped to the discovered subfolder sub1", call.Endpoint)
	}
}

// TestGoogleDriveIncrementalReadsChangesAndReportsRemovals covers the
// changes feed doing three things at once: a change to a file still in
// scope is read in full, a file removed from the caller's view at all is
// reported removed, and one merely trashed is too.
func TestGoogleDriveIncrementalReadsChangesAndReportsRemovals(t *testing.T) {
	changes := `{"changes":[
		{"fileId":"gone1","removed":true},
		{"fileId":"trashed1","file":{"id":"trashed1","name":"Old","mimeType":"application/pdf","trashed":true}},
		{"fileId":"changed1","file":{"id":"changed1","name":"Changed","mimeType":"application/pdf","modifiedTime":"2026-09-10T00:00:00Z","size":"9"}}
	],"nextPageToken":"","newStartPageToken":"newtok"}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(changes),
		binaryResponse("https://fake/changed1.bin", "application/pdf"),
	}}
	client := binaryClient(t, proxy, map[string][]byte{"https://fake/changed1.bin": []byte("new bytes")})
	source := NewGoogleDrive(client)

	page, err := source.Fetch(context.Background(), driveRequest(nil, false, domain.Cursor{Token: "starttok"}))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Removed) != 2 {
		t.Fatalf("Removed = %v, want gone1 and trashed1", page.Removed)
	}
	removedSet := map[string]bool{page.Removed[0]: true}
	if len(page.Removed) > 1 {
		removedSet[page.Removed[1]] = true
	}
	if !removedSet["gone1"] || !removedSet["trashed1"] {
		t.Fatalf("Removed = %v, want gone1 and trashed1 specifically", page.Removed)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "changed1" || string(page.Items[0].Content) != "new bytes" {
		t.Fatalf("Items = %+v, want changed1 read in full", page.Items)
	}
	if page.Token != "newtok" {
		t.Fatalf("Token = %q, want the feed's own newStartPageToken", page.Token)
	}
}

// TestGoogleDriveIncrementalFolderScopeIgnoresChangesOutsideTheFolder is the
// documented limit: a change is matched against the target folder's direct
// parent alone, since only a full pass has walked the whole subtree. A file
// nested in a subfolder is caught at the source's next full sweep instead.
func TestGoogleDriveIncrementalFolderScopeIgnoresChangesOutsideTheFolder(t *testing.T) {
	changes := `{"changes":[
		{"fileId":"in_scope","file":{"id":"in_scope","name":"In","mimeType":"application/pdf","parents":["root_folder"],"size":"3"}},
		{"fileId":"out_of_scope","file":{"id":"out_of_scope","name":"Out","mimeType":"application/pdf","parents":["some_other_folder"],"size":"3"}}
	],"nextPageToken":"","newStartPageToken":"tok3"}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(changes),
		binaryResponse("https://fake/in.bin", "application/pdf"),
	}}
	client := binaryClient(t, proxy, map[string][]byte{"https://fake/in.bin": []byte("abc")})
	source := NewGoogleDrive(client)
	target := map[string]string{"folder": "root_folder"}

	page, err := source.Fetch(context.Background(), driveRequest(target, false, domain.Cursor{Token: "starttok"}))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "in_scope" {
		t.Fatalf("Items = %+v, want only the change whose parents name the target folder", page.Items)
	}
}

// TestGoogleDriveIncrementalWithNoWatermarkAsksWhereNowIs covers a source
// that somehow reaches an incremental pass with neither a token nor a value
// to resume from: rather than fail the run, it asks Drive for a fresh
// starting point.
func TestGoogleDriveIncrementalWithNoWatermarkAsksWhereNowIs(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{jsonResponse(`{"startPageToken":"fresh"}`)}}
	client := binaryClient(t, proxy, nil)
	source := NewGoogleDrive(client)

	page, err := source.Fetch(context.Background(), driveRequest(nil, false, domain.Cursor{}))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 0 || page.Token != "fresh" {
		t.Fatalf("Fetch() = %+v, want no items and the fresh start token", page)
	}
}
