package google

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestWatchCalendarUsesGoogleContract(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/calendar/v3/calendars/primary/events/watch" ||
			request.Header.Get("authorization") != "Bearer token" {
			t.Fatalf("request = %s %s auth=%q", request.Method, request.URL.Path, request.Header.Get("authorization"))
		}
		body, err := io.ReadAll(request.Body)
		if err != nil || !strings.Contains(string(body), `"id":"c-channel"`) ||
			!strings.Contains(string(body), `"type":"web_hook"`) ||
			!strings.Contains(string(body), `"address":"https://connect.example.com/v1/webhooks/opaque"`) ||
			!strings.Contains(string(body), `"token":"abcdefghijklmnopqrstuvwxyz012345"`) {
			t.Fatalf("body/error = %s / %v", body, err)
		}
		writer.Header().Set("content-type", "application/json")
		_, _ = writer.Write([]byte(`{"id":"c-channel","resourceId":"resource-1","expiration":"1893456000000"}`))
	}))
	defer server.Close()
	channel, err := (Client{HTTPClient: server.Client(), BaseURL: server.URL}).WatchCalendar(context.Background(), "token", CalendarWatchRequest{
		CalendarID: primaryCalendarID, ChannelID: "c-channel",
		Address: "https://connect.example.com/v1/webhooks/opaque", Token: "abcdefghijklmnopqrstuvwxyz012345",
		Expiration: time.UnixMilli(1893456000000).UTC(),
	})
	if err != nil || channel.ID != "c-channel" || channel.ResourceID != "resource-1" || channel.CalendarID != "primary" {
		t.Fatalf("channel/error = %+v / %v", channel, err)
	}
}

func TestWatchGmailAndStopFollowGoogleContract(t *testing.T) {
	t.Parallel()
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		switch request.URL.Path {
		case "/gmail/v1/users/me/watch":
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"topicName":"projects/oryxa/topics/gmail"`) ||
				!strings.Contains(string(body), `"INBOX"`) {
				t.Fatalf("gmail watch body = %s", body)
			}
			writer.Header().Set("content-type", "application/json")
			_, _ = writer.Write([]byte(`{"historyId":"12345","expiration":"1893456000000"}`))
		case "/gmail/v1/users/me/stop", "/calendar/v3/channels/stop":
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("path = %s", request.URL.Path)
		}
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	watch, err := client.WatchGmail(context.Background(), "token", GmailWatchRequest{TopicName: "projects/oryxa/topics/gmail"})
	if err != nil || watch.HistoryID != "12345" {
		t.Fatalf("gmail watch/error = %+v / %v", watch, err)
	}
	if err := client.StopGmail(context.Background(), "token"); err != nil {
		t.Fatal(err)
	}
	if err := client.StopChannel(context.Background(), "token", WatchState{
		Kind: WatchKindCalendar, ChannelID: "c-channel", ResourceID: "resource-1", CalendarID: "primary",
		ExpiresAt: time.UnixMilli(1893456000000).UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "POST /gmail/v1/users/me/watch,POST /gmail/v1/users/me/stop,POST /calendar/v3/channels/stop" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestWatchDriveUsesStartPageTokenAndDriveStop(t *testing.T) {
	t.Parallel()
	var calls []string
	expires := time.Now().UTC().Add(12 * time.Hour)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls = append(calls, request.Method+" "+request.URL.Path)
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/drive/v3/changes/startPageToken":
			writer.Header().Set("content-type", "application/json")
			_, _ = writer.Write([]byte(`{"startPageToken":"token-1"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/drive/v3/changes/watch":
			if request.URL.Query().Get("pageToken") != "token-1" || request.Header.Get("authorization") != "Bearer token" {
				t.Fatalf("watch query/auth = %s %s", request.URL.RawQuery, request.Header.Get("authorization"))
			}
			body, _ := io.ReadAll(request.Body)
			if !strings.Contains(string(body), `"id":"d-channel"`) || !strings.Contains(string(body), `"type":"web_hook"`) {
				t.Fatalf("watch body = %s", body)
			}
			writer.Header().Set("content-type", "application/json")
			_, _ = writer.Write([]byte(`{"id":"d-channel","resourceId":"resource-1","expiration":"` +
				strconv.FormatInt(expires.UnixMilli(), 10) + `"}`))
		case request.Method == http.MethodPost && request.URL.Path == "/drive/v3/channels/stop":
			writer.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("path = %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := Client{HTTPClient: server.Client(), BaseURL: server.URL}
	channel, err := client.WatchDrive(context.Background(), "token", DriveWatchRequest{
		ChannelID: "d-channel", Address: "https://connect.example.com/v1/webhooks/opaque",
		Token: "abcdefghijklmnopqrstuvwxyz012345", Expiration: expires,
	})
	if err != nil || channel.ID != "d-channel" || channel.ResourceID != "resource-1" {
		t.Fatalf("channel/error = %+v / %v", channel, err)
	}
	if err := client.StopChannel(context.Background(), "token", WatchState{
		Kind: WatchKindDrive, ChannelID: "d-channel", ResourceID: "resource-1", ExpiresAt: expires,
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "GET /drive/v3/changes/startPageToken,POST /drive/v3/changes/watch,POST /drive/v3/channels/stop" {
		t.Fatalf("calls = %v", calls)
	}
}

func TestWatchRejectsUnsafeInputsAndClassifiesAuthorization(t *testing.T) {
	t.Parallel()
	client := Client{}
	if _, err := client.WatchCalendar(context.Background(), "token", CalendarWatchRequest{
		CalendarID: "../bad", ChannelID: "c1", Address: "http://insecure", Token: "short", Expiration: time.Now().Add(time.Hour),
	}); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("unsafe calendar watch error = %v", err)
	}
	if _, err := client.WatchGmail(context.Background(), "token", GmailWatchRequest{TopicName: "not-a-topic"}); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("unsafe gmail topic error = %v", err)
	}
	if _, err := client.WatchDrive(context.Background(), "token", DriveWatchRequest{
		ChannelID: "d-channel", Address: "https://connect.example.com/v1/webhooks/opaque",
		Token: "abcdefghijklmnopqrstuvwxyz012345", Expiration: time.Now().Add(48 * time.Hour),
	}); !errors.Is(err, ErrInvalidWatch) {
		t.Fatalf("overlong Drive watch error = %v", err)
	}
	if !IsAuthorizationRejection(RejectionError{StatusCode: http.StatusUnauthorized}) ||
		IsAuthorizationRejection(RejectionError{StatusCode: http.StatusBadRequest}) {
		t.Fatal("authorization classification")
	}
}

func TestWatchStateOmitsSecrets(t *testing.T) {
	t.Parallel()
	state := WatchState{Kind: WatchKindCalendar, ChannelID: "c-channel", ResourceID: "resource-1",
		CalendarID: "primary", ExpiresAt: time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)}
	mutations := state.StoreMutations()
	encoded := ""
	for _, mutation := range mutations {
		encoded += mutation.Key + "=" + mutation.Value.(string) + ";"
	}
	if strings.Contains(encoded, "token") || strings.Contains(encoded, "secret") {
		t.Fatalf("watch state leaked secret: %s", encoded)
	}
	store := map[string]any{}
	for _, mutation := range mutations {
		store[mutation.Key] = mutation.Value
	}
	loaded, err := WatchStateFromStore(store)
	if err != nil || loaded.ChannelID != "c-channel" || loaded.Kind != WatchKindCalendar {
		t.Fatalf("loaded/error = %+v / %v", loaded, err)
	}
}
