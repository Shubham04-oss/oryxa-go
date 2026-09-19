package sources

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

func b64(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func gmailRequest(target map[string]string, full bool, cursor domain.Cursor, fullSweptBefore bool) Request {
	source := domain.Source{OrganizationID: "org_1", Target: target}
	if fullSweptBefore {
		swept := time.Date(2026, time.September, 10, 12, 0, 0, 0, time.UTC)
		source.FullSweepAt = &swept
	}
	return Request{Source: source, Credential: composioCredential("ca_1"), Full: full, Cursor: cursor}
}

func TestGmailTargetAcceptsNoLabelOrANamedOne(t *testing.T) {
	source := NewGmail(nil)
	if err := source.Target(map[string]string{}); err != nil {
		t.Fatalf("Target(nothing) error = %v, want nil: INBOX is the default", err)
	}
	if err := source.Target(map[string]string{"label": "SENT"}); err != nil {
		t.Fatalf("Target(label) error = %v", err)
	}
	if err := source.Target(map[string]string{"label": " "}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target(blank label) error = %v, want ErrInvalid", err)
	}
}

// gmailMessageJSON builds one message.get(format=full) answer: a single
// text/plain part unless html is given, in which case the message carries
// only an HTML part the way a marketing email or a client that never sends
// plain text would.
func gmailMessageJSON(id, subject, from, plain, html string) string {
	part := `{"mimeType":"text/plain","body":{"data":"` + b64(plain) + `"}}`
	if html != "" {
		part = `{"mimeType":"multipart/alternative","parts":[{"mimeType":"text/html","body":{"data":"` + b64(html) + `"}}]}`
	}
	return `{"id":"` + id + `","threadId":"thread_` + id + `","internalDate":"1757000000000","payload":{"headers":[` +
		`{"name":"Subject","value":"` + subject + `"},{"name":"From","value":"` + from + `"},` +
		`{"name":"To","value":"me@example.test"},{"name":"Date","value":"Tue, 1 Sep 2026 00:00:00 +0000"}` +
		`],"mimeType":"multipart/mixed","parts":[` + part + `]}}`
}

// TestGmailFullPassBoundsAFirstSweepToTheLookbackWindow covers the very
// first pass over a source (FullSweepAt unset): it asks for recent mail only,
// reads each message, and ends by asking Gmail's own profile for a watermark
// to resume incrementally from.
func TestGmailFullPassBoundsAFirstSweepToTheLookbackWindow(t *testing.T) {
	list := `{"messages":[{"id":"m1"},{"id":"m2"}],"nextPageToken":""}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(list),
		jsonResponse(gmailMessageJSON("m1", "Hello", "a@example.test", "Plain body one", "")),
		jsonResponse(gmailMessageJSON("m2", "Re: Hello", "b@example.test", "Plain body two", "")),
		jsonResponse(`{"historyId":"h100"}`),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	page, err := source.Fetch(context.Background(), gmailRequest(nil, true, domain.Cursor{}, false))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Fetch() = %d items, want 2", len(page.Items))
	}
	if page.Items[0].Title != "Hello" || string(page.Items[0].Content) != "Plain body one" {
		t.Fatalf("first item = %+v", page.Items[0])
	}
	if page.Items[0].Metadata["from"] != "a@example.test" || page.Items[0].Metadata["thread_id"] != "thread_m1" {
		t.Fatalf("metadata = %+v", page.Items[0].Metadata)
	}
	if page.Cursor != "" || page.Token != "h100" {
		t.Fatalf("Fetch() cursor=%q token=%q, want drained with the profile's historyId", page.Cursor, page.Token)
	}
	// The very first call must bound the search window, or a heavy mailbox's
	// first connection would try to read years of mail before anything is
	// searchable.
	if got := proxy.calls[0].Endpoint; !containsAll(got, "labelIds=INBOX", "q=newer_than") {
		t.Fatalf("messages.list endpoint = %s, want it scoped to INBOX and bounded by newer_than", got)
	}
}

// TestGmailPeriodicFullSweepIsNotBoundedByTheLookback is the correctness
// reason the lookback exists only for a source's very first pass: a message
// that has simply aged past ninety days between one full sweep and the next
// must not look deleted just because a bounded re-sweep stopped seeing it.
func TestGmailPeriodicFullSweepIsNotBoundedByTheLookback(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(`{"messages":[],"nextPageToken":""}`),
		jsonResponse(`{"historyId":"h200"}`),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	if _, err := source.Fetch(context.Background(), gmailRequest(nil, true, domain.Cursor{}, true)); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := proxy.calls[0].Endpoint; containsAll(got, "q=newer_than") {
		t.Fatalf("messages.list endpoint = %s, want no lookback filter on a source already swept once", got)
	}
}

// TestGmailBodyFallsBackToHTMLWhenThereIsNoPlainPart covers a message with
// only an HTML part: the markup is stripped through the same transform an
// HTML page from any other source reads through, not indexed as markup.
func TestGmailBodyFallsBackToHTMLWhenThereIsNoPlainPart(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(`{"messages":[{"id":"m1"}],"nextPageToken":""}`),
		jsonResponse(gmailMessageJSON("m1", "Only HTML", "a@example.test", "", "<html><body><p>Hello <b>world</b></p></body></html>")),
		jsonResponse(`{"historyId":"h1"}`),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	page, err := source.Fetch(context.Background(), gmailRequest(nil, true, domain.Cursor{}, false))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Fetch() = %d items, want 1", len(page.Items))
	}
	if page.Items[0].MIME != transform.MIMEPlain {
		t.Fatalf("MIME = %q, want plain text: HTML is stripped, not stored as markup", page.Items[0].MIME)
	}
	if got := string(page.Items[0].Content); got != "Hello world" {
		t.Fatalf("content = %q, want the stripped text", got)
	}
}

// Gmail sends a body padded or not from one message to the next. A padded one
// must read the same, and one that cannot be read at all must cost only its
// own text, never the rest of the mailbox.
func TestGmailReadsPaddedBodiesAndPassesOverOneItCannotDecode(t *testing.T) {
	padded := strings.Replace(gmailMessageJSON("m1", "Padded", "a@example.test", "Hi", ""),
		`"data":"`+b64("Hi")+`"`, `"data":"`+base64.URLEncoding.EncodeToString([]byte("Hi"))+`"`, 1)
	broken := strings.Replace(gmailMessageJSON("m2", "Broken", "b@example.test", "x", ""),
		`"data":"`+b64("x")+`"`, `"data":"not*base64!"`, 1)
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(`{"messages":[{"id":"m1"},{"id":"m2"}],"nextPageToken":""}`),
		jsonResponse(padded),
		jsonResponse(broken),
		jsonResponse(`{"historyId":"h1"}`),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})

	page, err := NewGmail(client).Fetch(context.Background(), gmailRequest(nil, true, domain.Cursor{}, false))
	if err != nil {
		t.Fatalf("Fetch() error = %v, want the pass to go on past a body it cannot decode", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Fetch() = %d items, want 2", len(page.Items))
	}
	if got := string(page.Items[0].Content); got != "Hi" {
		t.Fatalf("padded body = %q, want %q", got, "Hi")
	}
	if got := page.Items[1]; len(got.Content) != 0 || got.Title != "Broken" {
		t.Fatalf("undecodable message = %+v, want it kept by its subject with no body", got)
	}
}

// TestGmailIncrementalReadsAddedAndReportsDeletedAndUnlabeled covers the
// three history types this reads: a message added to the label is fetched
// in full, one deleted outright is retracted, and one that merely left this
// label — archived out of INBOX — is retracted the same way, since this
// source is exactly that label.
func TestGmailIncrementalReadsAddedAndReportsDeletedAndUnlabeled(t *testing.T) {
	history := `{"history":[
		{"messagesAdded":[{"message":{"id":"new1"}}]},
		{"messagesDeleted":[{"message":{"id":"gone1"}}]},
		{"labelsRemoved":[{"message":{"id":"archived1"},"labelIds":["INBOX"]}]},
		{"labelsRemoved":[{"message":{"id":"still_here"},"labelIds":["UNREAD"]}]}
	],"nextPageToken":"","historyId":"h300"}`
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		jsonResponse(history),
		jsonResponse(gmailMessageJSON("new1", "New", "a@example.test", "Body", "")),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	page, err := source.Fetch(context.Background(), gmailRequest(nil, false, domain.Cursor{Token: "h200"}, false))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 || page.Items[0].ExternalID != "new1" {
		t.Fatalf("Items = %+v, want new1 read in full", page.Items)
	}
	removed := map[string]bool{}
	for _, id := range page.Removed {
		removed[id] = true
	}
	if !removed["gone1"] || !removed["archived1"] {
		t.Fatalf("Removed = %v, want gone1 and archived1", page.Removed)
	}
	if removed["still_here"] {
		t.Fatal("a message that only lost an unrelated label was reported removed")
	}
	if page.Token != "h300" {
		t.Fatalf("Token = %q, want the history feed's own historyId", page.Token)
	}
}

// TestGmailIncrementalWithExpiredHistoryStartsOverWithAFreshWatermark covers
// Gmail's own retention on history: a 404 for a startHistoryId too old to
// resume from reads as a fresh watermark rather than a failure a member
// would see as their source being broken. The periodic full sweep is what
// actually recovers the source; this only keeps the incremental leg from
// erroring out forever in the meantime.
func TestGmailIncrementalWithExpiredHistoryStartsOverWithAFreshWatermark(t *testing.T) {
	// Gmail itself answers 404 for a startHistoryId it no longer has — a
	// toolkit status, not Composio failing to reach the account, so this is
	// scripted as an ordinary (if unsuccessful) response rather than a proxy
	// error.
	proxy := &fakeProxier{responses: []composio.ProxyResponse{
		{Status: http.StatusNotFound},
		jsonResponse(`{"historyId":"h999"}`),
	}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	page, err := source.Fetch(context.Background(), gmailRequest(nil, false, domain.Cursor{Token: "way-too-old"}, true))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 0 || page.Token != "h999" {
		t.Fatalf("Fetch() = %+v, want no items and a fresh watermark", page)
	}
}

// TestGmailIncrementalWithNoWatermarkAsksWhereNowIs mirrors Drive's own
// fallback: a source somehow reaching an incremental pass with nothing to
// resume from asks where "now" is rather than failing the run.
func TestGmailIncrementalWithNoWatermarkAsksWhereNowIs(t *testing.T) {
	proxy := &fakeProxier{responses: []composio.ProxyResponse{jsonResponse(`{"historyId":"fresh"}`)}}
	client, _ := testClient(t, &composioDoer{proxy: proxy})
	source := NewGmail(client)

	page, err := source.Fetch(context.Background(), gmailRequest(nil, false, domain.Cursor{}, false))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 0 || page.Token != "fresh" {
		t.Fatalf("Fetch() = %+v, want no items and a fresh historyId", page)
	}
}

func containsAll(s string, substrs ...string) bool {
	for _, substr := range substrs {
		if !strings.Contains(s, substr) {
			return false
		}
	}
	return true
}
