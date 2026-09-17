package sources

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

func githubRequest(source domain.Source) Request {
	return Request{Source: source}
}

func githubSource(repository string) domain.Source {
	return domain.Source{Target: map[string]string{"repository": repository}}
}

func TestProviderIsGitHub(t *testing.T) {
	if got := NewGitHub(nil).Provider(); got != "github" {
		t.Fatalf("Provider() = %q, want %q", got, "github")
	}
}

func TestGitHubTargetRequiresOwnerSlashName(t *testing.T) {
	source := NewGitHub(nil)
	for target, wantErr := range map[string]bool{
		"":             true,
		"acme":         true,
		"acme/api/two": true,
		"acme/api":     false,
	} {
		err := source.Target(map[string]string{"repository": target})
		if wantErr && !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Target(%q) error = %v, want ErrInvalid", target, err)
		}
		if !wantErr && err != nil {
			t.Fatalf("Target(%q) error = %v, want nil", target, err)
		}
	}
}

func TestGitHubFetchWalksPagesByPageNumber(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusOK, `[{"number":1,"title":"first","state":"open","html_url":"https://github.test/1","updated_at":"2026-01-01T00:00:00Z"}]`, nil),
		answer(http.StatusOK, `[]`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)
	request := githubRequest(githubSource("acme/api"))
	request.Limit = 1

	first, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if first.Cursor != "2" {
		t.Fatalf("Fetch() cursor = %q, want %q: the page was full", first.Cursor, "2")
	}
	if len(first.Items) != 1 || first.Items[0].ExternalID != "1" {
		t.Fatalf("Fetch() items = %+v", first.Items)
	}
	if got := provider.calls[0].URL.Query().Get("page"); got != "1" {
		t.Fatalf("first call page = %q, want 1", got)
	}

	request.Cursor = domain.Cursor{Value: first.Cursor}
	second, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if second.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty: a short page is the last one", second.Cursor)
	}
	if got := provider.calls[1].URL.Query().Get("page"); got != "2" {
		t.Fatalf("second call page = %q, want 2", got)
	}
}

func TestGitHubFetchAsksOnlySinceTheWatermarkOnAnIncrementalPass(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, `[]`, nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)
	watermark := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	request := githubRequest(githubSource("acme/api"))
	request.Cursor = domain.Cursor{Watermark: &watermark}
	if _, err := source.Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := provider.calls[0].URL.Query().Get("since"); got != watermark.Format(time.RFC3339) {
		t.Fatalf("since = %q, want %q", got, watermark.Format(time.RFC3339))
	}
}

func TestGitHubFetchOmitsSinceOnAFullPass(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, `[]`, nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)
	watermark := time.Date(2026, time.September, 1, 0, 0, 0, 0, time.UTC)

	request := githubRequest(githubSource("acme/api"))
	request.Cursor = domain.Cursor{Watermark: &watermark}
	request.Full = true
	if _, err := source.Fetch(context.Background(), request); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := provider.calls[0].URL.Query().Get("since"); got != "" {
		t.Fatalf("since = %q, want empty on a full pass", got)
	}
}

func TestGitHubFetchSendsTheVersionedAcceptHeader(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, `[]`, nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)

	if _, err := source.Fetch(context.Background(), githubRequest(githubSource("acme/api"))); err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if got := provider.calls[0].Header.Get("X-GitHub-Api-Version"); got != githubAPIVersion {
		t.Fatalf("X-GitHub-Api-Version = %q, want %q", got, githubAPIVersion)
	}
	if got := provider.calls[0].Header.Get("Accept"); got != "application/vnd.github+json" {
		t.Fatalf("Accept = %q, want application/vnd.github+json", got)
	}
}

func TestGitHubFetchTellsIssuesAndPullRequestsApart(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, `[
		{"number":1,"title":"a bug","state":"open","html_url":"https://github.test/1","updated_at":"2026-01-01T00:00:00Z","user":{"login":"asha"},"comments":2},
		{"number":2,"title":"a change","state":"open","html_url":"https://github.test/2","updated_at":"2026-01-02T00:00:00Z","pull_request":{"url":"https://api.github.com/repos/acme/api/pulls/2"}}
	]`, nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)

	page, err := source.Fetch(context.Background(), githubRequest(githubSource("acme/api")))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 2 {
		t.Fatalf("Fetch() items = %d, want 2", len(page.Items))
	}
	if page.Items[0].Kind != knowledgev1.KindIssue {
		t.Fatalf("Items[0].Kind = %q, want %q", page.Items[0].Kind, knowledgev1.KindIssue)
	}
	if page.Items[0].Metadata["author"] != "asha" {
		t.Fatalf("Items[0] author = %q, want asha", page.Items[0].Metadata["author"])
	}
	if page.Items[1].Kind != knowledgev1.KindPullRequest {
		t.Fatalf("Items[1].Kind = %q, want %q", page.Items[1].Kind, knowledgev1.KindPullRequest)
	}
	if page.Items[1].MIME != transform.MIMEMarkdown {
		t.Fatalf("Items[1].MIME = %q, want %q", page.Items[1].MIME, transform.MIMEMarkdown)
	}
}

func TestGitHubFetchAdvancesTheWatermarkToTheNewestIssueSeen(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusOK, `[
		{"number":1,"title":"old","state":"open","html_url":"x","updated_at":"2026-01-01T00:00:00Z"},
		{"number":2,"title":"new","state":"open","html_url":"x","updated_at":"2026-03-01T00:00:00Z"}
	]`, nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)

	page, err := source.Fetch(context.Background(), githubRequest(githubSource("acme/api")))
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	want := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	if page.Watermark == nil || !page.Watermark.Equal(want) {
		t.Fatalf("Fetch() watermark = %v, want %v", page.Watermark, want)
	}
}

func TestGitHubFetchMapsARefusalToNeedsReconnect(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{answer(http.StatusUnauthorized, "", nil)}}
	client, _ := testClient(t, provider)
	source := NewGitHub(client)

	if _, err := source.Fetch(context.Background(), githubRequest(githubSource("acme/api"))); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("Fetch() error = %v, want ErrNeedsReconnect", err)
	}
}

func TestGitHubFetchRefusesATargetWithNoRepository(t *testing.T) {
	source := NewGitHub(NewClient(nil))
	if _, err := source.Fetch(context.Background(), githubRequest(domain.Source{})); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Fetch() error = %v, want ErrInvalid", err)
	}
}

func TestPageNumberStartsOverOnAnythingUnreadable(t *testing.T) {
	for cursor, want := range map[string]int{"": 1, "0": 1, "-1": 1, "abc": 1, "3": 3} {
		if got := pageNumber(cursor); got != want {
			t.Fatalf("pageNumber(%q) = %d, want %d", cursor, got, want)
		}
	}
}

func TestSplitRepositoryReadsOwnerAndName(t *testing.T) {
	owner, name, err := splitRepository("acme/api")
	if err != nil || owner != "acme" || name != "api" {
		t.Fatalf("splitRepository() = %q, %q, %v", owner, name, err)
	}
	if !strings.Contains("acme/api", "/") {
		t.Fatal("test fixture must contain a slash")
	}
}
