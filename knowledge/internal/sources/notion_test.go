package sources

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

func notionRequestBody(t *testing.T, request *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	return string(body)
}

// notionJSON answers as Notion does: a JSON body with the content type the
// generated client requires before it will decode one.
func notionJSON(status int, body string) *http.Response {
	return answer(status, body, http.Header{"Content-Type": {"application/json"}})
}

func TestFetchWalksEveryPageWhenNoDatabaseIsNamed(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "page-1", "url": "https://notion.so/page-1",
				 "last_edited_time": "2026-01-01T00:00:00.000Z",
				 "properties": {"Name": {"type": "title", "title": [{"plain_text": "First"}]}}}
			],
			"has_more": true,
			"next_cursor": "cursor-1"
		}`),
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "page-2", "url": "https://notion.so/page-2",
				 "last_edited_time": "2026-01-02T00:00:00.000Z",
				 "properties": {"Name": {"type": "title", "title": [{"plain_text": "Second"}]}}}
			],
			"has_more": false,
			"next_cursor": null
		}`),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)

	first, err := source.Fetch(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if first.Cursor != "cursor-1" {
		t.Fatalf("Fetch() cursor = %q, want %q", first.Cursor, "cursor-1")
	}
	if len(first.Items) != 1 || first.Items[0].ExternalID != "page-1" {
		t.Fatalf("Fetch() items = %+v, want one item for page-1", first.Items)
	}

	second, err := source.Fetch(context.Background(), Request{Cursor: domain.Cursor{Value: first.Cursor}})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if second.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty once has_more is false", second.Cursor)
	}
	if len(second.Items) != 1 || second.Items[0].ExternalID != "page-2" {
		t.Fatalf("Fetch() items = %+v, want one item for page-2", second.Items)
	}

	if len(provider.calls) != 2 {
		t.Fatalf("made %d calls, want 2", len(provider.calls))
	}
	if body := notionRequestBody(t, provider.calls[1]); !strings.Contains(body, `"start_cursor":"cursor-1"`) {
		t.Fatalf("second call body = %s, want it to carry the cursor the first page returned", body)
	}
}

func TestFetchResolvesADatabaseToItsDataSourceBeforeQuerying(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		notionJSON(http.StatusOK, `{"object": "database", "id": "db-1", "data_sources": [{"id": "ds-1", "name": "Tasks"}]}`),
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "page-1", "url": "https://notion.so/page-1",
				 "last_edited_time": "2026-01-01T00:00:00.000Z",
				 "properties": {"Name": {"type": "title", "title": [{"plain_text": "Task One"}]}}}
			],
			"has_more": false,
			"next_cursor": null
		}`),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)
	request := Request{Source: domain.Source{Target: map[string]string{"database": "db-1"}}}

	page, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Fetch() items = %d, want 1", len(page.Items))
	}
	if len(provider.calls) != 2 {
		t.Fatalf("made %d calls, want 2: the database lookup and the data source query", len(provider.calls))
	}
	if got := provider.calls[0].URL.String(); !strings.Contains(got, "/v1/databases/db-1") {
		t.Fatalf("first call URL = %s, want the database lookup for db-1", got)
	}
	if got := provider.calls[1].URL.String(); !strings.Contains(got, "/v1/data_sources/ds-1/query") {
		t.Fatalf("second call URL = %s, want the resolved data source's query endpoint", got)
	}
}

func TestFetchReadsEveryDataSourceOfADatabase(t *testing.T) {
	// A database split since the 2025-09-03 API version can hold several data
	// sources, and rows live under all of them. Reading only the first left
	// the rest silently unindexed.
	provider := &fakeProvider{answers: []*http.Response{
		notionJSON(http.StatusOK, `{"object": "database", "id": "db-1", "data_sources": [{"id": "ds-1", "name": "Tasks"}, {"id": "ds-2", "name": "Archive"}]}`),
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "task-1", "url": "https://notion.so/task-1",
				 "last_edited_time": "2026-01-01T00:00:00.000Z",
				 "properties": {"Name": {"type": "title", "title": [{"plain_text": "Task one"}]}}}
			],
			"has_more": false,
			"next_cursor": null
		}`),
		notionJSON(http.StatusOK, `{"object": "database", "id": "db-1", "data_sources": [{"id": "ds-1", "name": "Tasks"}, {"id": "ds-2", "name": "Archive"}]}`),
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "task-2", "url": "https://notion.so/task-2",
				 "last_edited_time": "2026-01-02T00:00:00.000Z",
				 "properties": {"Name": {"type": "title", "title": [{"plain_text": "Task two"}]}}}
			],
			"has_more": false,
			"next_cursor": null
		}`),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)
	request := Request{Source: domain.Source{Target: map[string]string{"database": "db-1"}}}

	first, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(first.Items) != 1 || first.Items[0].ExternalID != "task-1" {
		t.Fatalf("Fetch() items = %+v, want one item from the first data source", first.Items)
	}
	if first.Cursor == "" {
		t.Fatal("Fetch() cursor = \"\", want more to read: the second data source has not been queried yet")
	}
	if got := provider.calls[1].URL.String(); !strings.Contains(got, "/v1/data_sources/ds-1/query") {
		t.Fatalf("second call URL = %s, want the first data source's query endpoint", got)
	}

	request.Cursor = domain.Cursor{Value: first.Cursor}
	second, err := source.Fetch(context.Background(), request)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(second.Items) != 1 || second.Items[0].ExternalID != "task-2" {
		t.Fatalf("Fetch() items = %+v, want one item from the second data source", second.Items)
	}
	if second.Cursor != "" {
		t.Fatalf("Fetch() cursor = %q, want empty: both data sources are drained", second.Cursor)
	}
	if got := provider.calls[3].URL.String(); !strings.Contains(got, "/v1/data_sources/ds-2/query") {
		t.Fatalf("fourth call URL = %s, want the second data source's query endpoint", got)
	}
}

func TestFetchReturnsNotFoundWhenTheDatabaseIsGone(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusNotFound, `{"object":"error","status":404,"code":"object_not_found","message":"nope"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)
	request := Request{Source: domain.Source{Target: map[string]string{"database": "missing"}}}

	if _, err := source.Fetch(context.Background(), request); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("Fetch() error = %v, want ErrNotFound", err)
	}
	if len(provider.calls) != 1 {
		t.Fatalf("made %d calls, want 1: querying should not happen once the database lookup fails", len(provider.calls))
	}
}

func TestFetchMapsAProviderRefusalToNeedsReconnect(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		answer(http.StatusUnauthorized, `{"object":"error","status":401,"code":"unauthorized","message":"nope"}`, nil),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)

	if _, err := source.Fetch(context.Background(), Request{}); !errors.Is(err, domain.ErrNeedsReconnect) {
		t.Fatalf("Fetch() error = %v, want ErrNeedsReconnect", err)
	}
}

func TestFetchAdvancesTheWatermarkToTheNewestPageSeen(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "page-1", "url": "https://notion.so/page-1",
				 "last_edited_time": "2026-01-01T00:00:00.000Z", "properties": {}},
				{"object": "page", "id": "page-2", "url": "https://notion.so/page-2",
				 "last_edited_time": "2026-03-01T00:00:00.000Z", "properties": {}},
				{"object": "page", "id": "page-3", "url": "https://notion.so/page-3",
				 "last_edited_time": "2026-02-01T00:00:00.000Z", "properties": {}}
			],
			"has_more": false,
			"next_cursor": null
		}`),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)

	page, err := source.Fetch(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	want := time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC)
	if page.Watermark == nil || !page.Watermark.Equal(want) {
		t.Fatalf("Fetch() watermark = %v, want %v", page.Watermark, want)
	}
}

func TestFetchExtractsTitleAndPropertiesAsTheBody(t *testing.T) {
	provider := &fakeProvider{answers: []*http.Response{
		notionJSON(http.StatusOK, `{
			"results": [
				{"object": "page", "id": "page-1", "url": "https://notion.so/page-1",
				 "last_edited_time": "2026-01-01T00:00:00.000Z",
				 "parent": {"type": "data_source_id"},
				 "properties": {
					"Name": {"type": "title", "title": [{"plain_text": "Launch plan"}]},
					"Done": {"type": "checkbox", "checkbox": true},
					"Owner": {"type": "select", "select": {"name": "Ari"}}
				 }}
			],
			"has_more": false,
			"next_cursor": null
		}`),
	}}
	client, _ := testClient(t, provider)
	source := NewNotion(client)

	page, err := source.Fetch(context.Background(), Request{})
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("Fetch() items = %d, want 1", len(page.Items))
	}
	item := page.Items[0]
	if item.ExternalID != "page-1" {
		t.Fatalf("Item.ExternalID = %q, want %q", item.ExternalID, "page-1")
	}
	if item.Kind != knowledgev1.KindPage {
		t.Fatalf("Item.Kind = %q, want %q", item.Kind, knowledgev1.KindPage)
	}
	if item.URL != "https://notion.so/page-1" {
		t.Fatalf("Item.URL = %q, want %q", item.URL, "https://notion.so/page-1")
	}
	if item.MIME != transform.MIMEMarkdown {
		t.Fatalf("Item.MIME = %q, want %q", item.MIME, transform.MIMEMarkdown)
	}
	if item.Title != "Launch plan" {
		t.Fatalf("Item.Title = %q, want %q", item.Title, "Launch plan")
	}
	wantUpdated := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	if item.UpdatedAt == nil || !item.UpdatedAt.Equal(wantUpdated) {
		t.Fatalf("Item.UpdatedAt = %v, want %v", item.UpdatedAt, wantUpdated)
	}
	body := string(item.Content)
	if !strings.Contains(body, "# Launch plan") {
		t.Fatalf("Item.Content = %q, want the title as a heading", body)
	}
	if !strings.Contains(body, "- Done: true") {
		t.Fatalf("Item.Content = %q, want the checkbox property", body)
	}
	if !strings.Contains(body, "- Owner: Ari") {
		t.Fatalf("Item.Content = %q, want the select property", body)
	}
}

func TestTargetAcceptsNoDatabaseOrRejectsABlankOne(t *testing.T) {
	source := NewNotion(NewClient())
	if err := source.Target(nil); err != nil {
		t.Fatalf("Target(nil) error = %v, want nil: no target indexes every page", err)
	}
	if err := source.Target(map[string]string{"database": "db-1"}); err != nil {
		t.Fatalf("Target() error = %v, want nil", err)
	}
	if err := source.Target(map[string]string{"database": "  "}); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("Target() error = %v, want ErrInvalid", err)
	}
}
