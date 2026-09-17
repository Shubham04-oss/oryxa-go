package sources

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

func TestProviderIsSlack(t *testing.T) {
	if got := NewSlack(nil).Provider(); got != "slack" {
		t.Fatalf("Provider() = %q, want %q", got, "slack")
	}
}

func TestTargetRequiresAChannel(t *testing.T) {
	source := NewSlack(nil)
	if err := source.Target(map[string]string{}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target(no channel) error = %v, want ErrInvalid", err)
	}
	if err := source.Target(map[string]string{"channel": "C0123ABC"}); err != nil {
		t.Fatalf("Target(channel) error = %v, want nil", err)
	}
}

func TestFetchWalksSlacksCursorAcrossPagesAndStops(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":true,"messages":[{"user":"U1","text":"first page","ts":"1700000000.000100"}],"response_metadata":{"next_cursor":"dXNlcjpVMDYxTkZUVDI="}}`, nil),
		answer(http.StatusOK, `{"ok":true,"messages":[{"user":"U2","text":"second page","ts":"1700000100.000200"}],"response_metadata":{"next_cursor":""}}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)
	request := Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}}

	first, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].ExternalID != "1700000000.000100" {
		t.Fatalf("Fetch() items = %+v, want one item at ts 1700000000.000100", first.Items)
	}
	if first.Cursor != "dXNlcjpVMDYxTkZUVDI=" {
		t.Fatalf("Fetch() cursor = %q, want %q", first.Cursor, "dXNlcjpVMDYxTkZUVDI=")
	}

	request.Cursor = domain.Cursor{Value: first.Cursor}
	second, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ExternalID != "1700000100.000200" {
		t.Fatalf("Fetch() items = %+v, want one item at ts 1700000100.000200", second.Items)
	}
	if second.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty: the source is drained", second.Cursor)
	}
	if got := provider.calls[1].URL.Query().Get("cursor"); got != "dXNlcjpVMDYxTkZUVDI=" {
		t.Fatalf("second call's cursor param = %q, want %q", got, "dXNlcjpVMDYxTkZUVDI=")
	}
}

func TestFetchMapsRateLimitedToErrRateLimitedDespiteTheHTTPTwoHundred(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":false,"error":"ratelimited"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	_, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if !errors.Is(err, domain.ErrRateLimited) {
		t.Fatalf("Fetch() error = %v, want ErrRateLimited", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("made %d calls, want 1: an HTTP 200 gives the retrying transport nothing to act on", len(provider.calls))
	}
}

func TestFetchStopsWhenSlackSaysTheTokenIsRevoked(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":false,"error":"token_revoked"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	_, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("Fetch() error = %v, want ErrNeedsReconnect", err)
	}
}

func TestFetchMapsMissingScopeToErrScopeRequired(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":false,"error":"missing_scope"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	_, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if !errors.Is(err, domain.ErrScopeRequired) {
		t.Fatalf("Fetch() error = %v, want ErrScopeRequired", err)
	}
}

func TestFetchMapsChannelNotFoundToErrNotFound(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":false,"error":"channel_not_found"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	_, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Fetch() error = %v, want ErrNotFound", err)
	}
}

func TestFetchMapsAnUnrecognizedErrorToProviderUnavailable(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":false,"error":"fatal_error"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	_, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if !errors.Is(err, domain.ErrProviderUnavailable) {
		t.Fatalf("Fetch() error = %v, want ErrProviderUnavailable", err)
	}
}

func TestFetchParsesTsIntoUpdatedAt(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":true,"messages":[{"user":"U1","text":"hello","ts":"1700000000.000100","thread_ts":"1699999999.000000"}],"response_metadata":{"next_cursor":""}}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)

	page, err := source.Fetch(context.Background(), Request{Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}}})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Fetch() items = %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	want := time.Date(2023, time.November, 14, 22, 13, 20, 100000, time.UTC)
	if item.ExternalID != "1700000000.000100" {
		t.Fatalf("Fetch() ExternalID = %q, want %q", item.ExternalID, "1700000000.000100")
	}
	if item.Kind != knowledgev1.KindMessage {
		t.Fatalf("Fetch() Kind = %q, want %q", item.Kind, knowledgev1.KindMessage)
	}
	if item.MIME != transform.MIMEPlain {
		t.Fatalf("Fetch() MIME = %q, want %q", item.MIME, transform.MIMEPlain)
	}
	if string(item.Content) != "hello" {
		t.Fatalf("Fetch() Content = %q, want %q", item.Content, "hello")
	}
	if item.UpdatedAt == nil || !item.UpdatedAt.Equal(want) {
		t.Fatalf("Fetch() UpdatedAt = %v, want %v", item.UpdatedAt, want)
	}
	if item.Metadata["user"] != "U1" {
		t.Fatalf("Fetch() user metadata = %q, want %q", item.Metadata["user"], "U1")
	}
	if item.Metadata["thread_ts"] != "1699999999.000000" {
		t.Fatalf("Fetch() thread_ts metadata = %q, want %q", item.Metadata["thread_ts"], "1699999999.000000")
	}
	if page.Watermark == nil || !page.Watermark.Equal(want) {
		t.Fatalf("Fetch() Watermark = %v, want %v", page.Watermark, want)
	}
}

func TestFetchPassesOldestWhenResumingIncrementally(t *testing.T) {
	// A second before the watermark, not the watermark itself: Slack's
	// default treats oldest as exclusive, so asking for the watermark would
	// rely on that never changing. Asking a second early instead means the
	// idempotent upsert absorbs the overlap whichever way Slack treats it.
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":""}}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)
	watermark := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	request := Request{
		Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}},
		Cursor: domain.Cursor{Watermark: &watermark},
	}
	if _, err := source.Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got, want := provider.calls[0].URL.Query().Get("oldest"), "1788220799.000000"; got != want {
		t.Fatalf("oldest param = %q, want %q (one second before the watermark)", got, want)
	}
}

func TestFetchOmitsOldestOnAFullPass(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":""}}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewSlack(client)
	watermark := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	request := Request{
		Source: domain.Source{Target: map[string]string{"channel": "C0123ABC"}},
		Cursor: domain.Cursor{Watermark: &watermark},
		Full:   true,
	}
	if _, err := source.Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := provider.calls[0].URL.Query().Get("oldest"); got != "" {
		t.Fatalf("oldest param = %q, want empty on a full pass", got)
	}
}

func TestSlackTimeKeepsEveryMicrosecondATsCarries(t *testing.T) {
	// A ts spends sixteen significant digits and a float64 holds about
	// sixteen, so parsing one as a number rounds. A rounded watermark handed
	// back as oldest is how a message quietly stops being read.
	for _, moment := range []struct {
		ts   string
		want time.Time
	}{
		{"1609459200.000400", time.UnixMicro(1609459200000400).UTC()},
		{"1758110400.123456", time.UnixMicro(1758110400123456).UTC()},
		{"1758110400.999999", time.UnixMicro(1758110400999999).UTC()},
		{"1758110400.5", time.UnixMicro(1758110400500000).UTC()},
		{"1758110400", time.UnixMicro(1758110400000000).UTC()},
	} {
		got, err := slackTime(moment.ts)
		if err != nil {
			t.Fatalf("slackTime(%q) error = %v", moment.ts, err)
		}
		if !got.Equal(moment.want) {
			t.Fatalf("slackTime(%q) = %s, want %s", moment.ts, got, moment.want)
		}
	}
}

func TestSlackTimeRefusesWhatIsNotATimestamp(t *testing.T) {
	for _, ts := range []string{"", "not-a-time", "1609459200.abc"} {
		if _, err := slackTime(ts); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("slackTime(%q) error = %v, want ErrInvalid", ts, err)
		}
	}
}

func TestSlackTimeSurvivesARoundTripThroughOldest(t *testing.T) {
	// What Fetch sends as oldest must name the same moment the ts did, or a
	// resumed walk moves the window.
	const ts = "1758110400.123456"
	moment, err := slackTime(ts)
	if err != nil {
		t.Fatalf("slackTime() error = %v", err)
	}
	if got := slackOldest(moment); got != ts {
		t.Fatalf("slackOldest(slackTime(%q)) = %q, want %q", ts, got, ts)
	}
}

func TestSlackPermalinkPointsAtTheWorkspaceTheCredentialNamed(t *testing.T) {
	got := slackPermalink("acme.slack.com", "C0123ABC", "1758110400.123456")
	want := "https://acme.slack.com/archives/C0123ABC/p1758110400123456"
	if got != want {
		t.Fatalf("slackPermalink() = %q, want %q", got, want)
	}
	// Without a workspace the archive link still resolves for whoever follows it.
	if got := slackPermalink("", "C0123ABC", "1758110400.123456"); got != "https://slack.com/archives/C0123ABC/p1758110400123456" {
		t.Fatalf("slackPermalink(no workspace) = %q", got)
	}
}
