package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"slices"
	"testing"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var microsoftActionCredential = domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}

// runMicrosoftAction marshals input and runs the named action against
// server, the way a caller with a connection to it would.
func runMicrosoftAction(t *testing.T, server *httptest.Server, name string, input map[string]any) (ActionResult, error) {
	t.Helper()
	provider := microsoftProvider(server.URL)
	action, ok := provider.Action(name)
	if !ok {
		t.Fatalf("provider has no action %q", name)
	}
	raw, err := json.Marshal(input)
	if err != nil {
		t.Fatalf("marshal input: %v", err)
	}
	return action.Run(context.Background(), server.Client(), ActionRequest{Credential: microsoftActionCredential, Input: raw, Now: fixedNow})
}

// microsoftFakeRequest is what reached microsoftFakeServer, for a happy-path
// test to assert against.
type microsoftFakeRequest struct {
	Method string
	Path   string
	Auth   string
	Query  url.Values
	Body   map[string]any
}

// microsoftFakeServer answers one request with status and body, both as
// JSON (body may be empty, the way Graph answers sendMail), and reports what
// it received.
func microsoftFakeServer(t *testing.T, status int, body string) (*httptest.Server, *microsoftFakeRequest) {
	t.Helper()
	got := &microsoftFakeRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		got.Method, got.Path, got.Auth, got.Query = request.Method, request.URL.Path, request.Header.Get("Authorization"), request.URL.Query()
		if request.ContentLength != 0 {
			if err := json.NewDecoder(request.Body).Decode(&got.Body); err != nil {
				t.Errorf("decode request body: %v", err)
			}
		}
		if body != "" {
			writer.Header().Set("Content-Type", "application/json")
		}
		writer.WriteHeader(status)
		if body != "" {
			_, _ = io.WriteString(writer, body)
		}
	}))
	return server, got
}

// microsoftNoCallServer fails the test if Graph is ever called, for
// asserting an input refusal never reaches the network.
func microsoftNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("unexpected request to Graph")
	}))
}

func TestMicrosoftListMessagesAction(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusOK, `{"value":[`+
		`{"id":"m1","subject":"Hello","from":{"emailAddress":{"name":"Bob","address":"bob@example.com"}},"receivedDateTime":"2026-09-15T09:00:00Z","isRead":true,"bodyPreview":"Hi there"},`+
		`{"id":"m2","subject":"Re: Hello","from":{"emailAddress":{"name":"Carol","address":"carol@example.com"}},"receivedDateTime":"2026-09-15T10:00:00Z","isRead":false,"bodyPreview":"Following up"}`+
		`]}`)
	defer server.Close()

	result, err := runMicrosoftAction(t, server, "list_messages", map[string]any{"folder": "Drafts", "limit": 5})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodGet || got.Path != "/me/mailFolders('Drafts')/messages" {
		t.Fatalf("request = %s %s, want GET /me/mailFolders('Drafts')/messages", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Query.Get("$top") != "5" || got.Query.Get("$select") != "id,subject,from,receivedDateTime,isRead,bodyPreview" {
		t.Fatalf("query = %v, want $top=5 $select=id,subject,from,receivedDateTime,isRead,bodyPreview", got.Query)
	}
	if result.Resource != (integrationsv1.Resource{}) {
		t.Fatalf("Resource = %+v, want the zero value for a read", result.Resource)
	}
	var summaries []microsoftMessageSummary
	if err := json.Unmarshal(result.Result, &summaries); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	want := []microsoftMessageSummary{
		{ID: "m1", Subject: "Hello", From: "bob@example.com", ReceivedAt: "2026-09-15T09:00:00Z", IsRead: true, Preview: "Hi there"},
		{ID: "m2", Subject: "Re: Hello", From: "carol@example.com", ReceivedAt: "2026-09-15T10:00:00Z", IsRead: false, Preview: "Following up"},
	}
	if !slices.Equal(summaries, want) {
		t.Fatalf("Result = %+v, want %+v", summaries, want)
	}
}

func TestMicrosoftListMessagesDefaults(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusOK, `{"value":[]}`)
	defer server.Close()

	result, err := runMicrosoftAction(t, server, "list_messages", map[string]any{})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Path != "/me/mailFolders('Inbox')/messages" {
		t.Fatalf("path = %s, want the default folder Inbox", got.Path)
	}
	if got.Query.Get("$top") != "20" {
		t.Fatalf("$top = %s, want the default 20", got.Query.Get("$top"))
	}
	if string(result.Result) != "[]" {
		t.Fatalf("Result = %s, want [] for no messages", result.Result)
	}
}

func TestMicrosoftGetMessageAction(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusOK,
		`{"id":"msg1","subject":"Hello","from":{"emailAddress":{"name":"Bob","address":"bob@example.com"}},`+
			`"toRecipients":[{"emailAddress":{"name":"Carol","address":"carol@example.com"}},{"emailAddress":{"name":"","address":"dave@example.com"}}],`+
			`"receivedDateTime":"2026-09-15T09:00:00Z","body":{"contentType":"html","content":"<p>Hi there <b>Bob</b>.</p><p>Second line.</p>"},`+
			`"webLink":"https://outlook.office.com/mail/msg1"}`)
	defer server.Close()

	result, err := runMicrosoftAction(t, server, "get_message", map[string]any{"message": "msg1"})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodGet || got.Path != "/me/messages/msg1" {
		t.Fatalf("request = %s %s, want GET /me/messages/msg1", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Query.Get("$select") != "id,subject,from,toRecipients,receivedDateTime,body,webLink" {
		t.Fatalf("$select = %s, want id,subject,from,toRecipients,receivedDateTime,body,webLink", got.Query.Get("$select"))
	}
	wantResource := integrationsv1.Resource{Type: "message", ID: "msg1", Name: "Hello", URL: "https://outlook.office.com/mail/msg1"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var detail microsoftMessageDetail
	if err := json.Unmarshal(result.Result, &detail); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	want := microsoftMessageDetail{
		ID: "msg1", Subject: "Hello", From: "bob@example.com", To: []string{"carol@example.com", "dave@example.com"},
		ReceivedAt: "2026-09-15T09:00:00Z", Body: "Hi there Bob.\nSecond line.",
	}
	if !reflect.DeepEqual(detail, want) {
		t.Fatalf("Result = %+v, want %+v", detail, want)
	}
}

func TestMicrosoftSendMailAction(t *testing.T) {
	t.Run("happy path against a 202 with an empty body", func(t *testing.T) {
		server, got := microsoftFakeServer(t, http.StatusAccepted, "")
		defer server.Close()

		result, err := runMicrosoftAction(t, server, "send_mail", map[string]any{
			"to": []string{"carol@example.com"}, "subject": "Standup notes", "body": "See you at 10.",
		})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		if got.Method != http.MethodPost || got.Path != "/me/sendMail" {
			t.Fatalf("request = %s %s, want POST /me/sendMail", got.Method, got.Path)
		}
		if got.Auth != "Bearer tok123" {
			t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
		}
		if got.Body["saveToSentItems"] != true {
			t.Fatalf("saveToSentItems = %v, want true", got.Body["saveToSentItems"])
		}
		message, ok := got.Body["message"].(map[string]any)
		if !ok {
			t.Fatalf("body message = %v, want an object", got.Body["message"])
		}
		if message["subject"] != "Standup notes" {
			t.Fatalf("message.subject = %v, want \"Standup notes\"", message["subject"])
		}
		wantBody := map[string]any{"contentType": "Text", "content": "See you at 10."}
		if !reflect.DeepEqual(message["body"], wantBody) {
			t.Fatalf("message.body = %v, want %v", message["body"], wantBody)
		}
		wantTo := []any{map[string]any{"emailAddress": map[string]any{"address": "carol@example.com"}}}
		if !reflect.DeepEqual(message["toRecipients"], wantTo) {
			t.Fatalf("message.toRecipients = %v, want %v", message["toRecipients"], wantTo)
		}
		if _, has := message["ccRecipients"]; has {
			t.Fatalf("message.ccRecipients = %v, want none when cc is not given", message["ccRecipients"])
		}
		if result.Resource != (integrationsv1.Resource{Type: "message", Name: "Standup notes"}) {
			t.Fatalf("Resource = %+v, want Type message, Name \"Standup notes\"", result.Resource)
		}
		var sent microsoftMailSent
		if err := json.Unmarshal(result.Result, &sent); err != nil {
			t.Fatalf("unmarshal Result: %v", err)
		}
		if want := (microsoftMailSent{Sent: true, Subject: "Standup notes"}); sent != want {
			t.Fatalf("Result = %+v, want %+v", sent, want)
		}
	})

	t.Run("cc is carried as ccRecipients", func(t *testing.T) {
		server, got := microsoftFakeServer(t, http.StatusAccepted, "")
		defer server.Close()

		_, err := runMicrosoftAction(t, server, "send_mail", map[string]any{
			"to": []string{"carol@example.com"}, "cc": []string{"dave@example.com"}, "subject": "Hi", "body": "Hello",
		})
		if err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
		message := got.Body["message"].(map[string]any)
		wantCC := []any{map[string]any{"emailAddress": map[string]any{"address": "dave@example.com"}}}
		if !reflect.DeepEqual(message["ccRecipients"], wantCC) {
			t.Fatalf("message.ccRecipients = %v, want %v", message["ccRecipients"], wantCC)
		}
	})
}

func TestMicrosoftListEventsAction(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusOK,
		`{"value":[{"id":"evt1","subject":"Standup",`+
			`"start":{"dateTime":"2026-09-16T10:00:00.0000000","timeZone":"UTC"},`+
			`"end":{"dateTime":"2026-09-16T10:30:00.0000000","timeZone":"UTC"},`+
			`"organizer":{"emailAddress":{"name":"Alice","address":"alice@example.com"}},`+
			`"onlineMeeting":{"joinUrl":"https://teams.microsoft.com/l/meetup-join/abc"},`+
			`"webLink":"https://outlook.office.com/calendar/item/evt1"}]}`)
	defer server.Close()

	result, err := runMicrosoftAction(t, server, "list_events", map[string]any{
		"from": "2026-09-16T09:00:00Z", "to": "2026-09-16T18:00:00Z", "limit": 10,
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodGet || got.Path != "/me/calendarView" {
		t.Fatalf("request = %s %s, want GET /me/calendarView", got.Method, got.Path)
	}
	if got.Query.Get("startDateTime") != "2026-09-16T09:00:00Z" || got.Query.Get("endDateTime") != "2026-09-16T18:00:00Z" {
		t.Fatalf("window = %s to %s, want 2026-09-16T09:00:00Z to 2026-09-16T18:00:00Z", got.Query.Get("startDateTime"), got.Query.Get("endDateTime"))
	}
	if got.Query.Get("$top") != "10" || got.Query.Get("$select") != "id,subject,start,end,organizer,onlineMeeting,webLink" {
		t.Fatalf("query = %v, want $top=10 $select=id,subject,start,end,organizer,onlineMeeting,webLink", got.Query)
	}
	if result.Resource != (integrationsv1.Resource{}) {
		t.Fatalf("Resource = %+v, want the zero value for a read", result.Resource)
	}
	var summaries []microsoftEventSummary
	if err := json.Unmarshal(result.Result, &summaries); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	want := []microsoftEventSummary{
		{ID: "evt1", Subject: "Standup", Start: "2026-09-16T10:00:00Z", End: "2026-09-16T10:30:00Z",
			Organizer: "alice@example.com", OnlineMeetingURL: "https://teams.microsoft.com/l/meetup-join/abc"},
	}
	if !slices.Equal(summaries, want) {
		t.Fatalf("Result = %+v, want %+v", summaries, want)
	}
}

func TestMicrosoftListEventsDefaults(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusOK, `{"value":[]}`)
	defer server.Close()

	if _, err := runMicrosoftAction(t, server, "list_events", map[string]any{}); err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	wantFrom := fixedNow.Format(time.RFC3339)
	wantTo := fixedNow.Add(7 * 24 * time.Hour).Format(time.RFC3339)
	if got.Query.Get("startDateTime") != wantFrom || got.Query.Get("endDateTime") != wantTo {
		t.Fatalf("window = %s to %s, want %s to %s (now to seven days on)", got.Query.Get("startDateTime"), got.Query.Get("endDateTime"), wantFrom, wantTo)
	}
	if got.Query.Get("$top") != "20" {
		t.Fatalf("$top = %s, want the default 20", got.Query.Get("$top"))
	}
}

func TestMicrosoftCreateEventAction(t *testing.T) {
	server, got := microsoftFakeServer(t, http.StatusCreated,
		`{"id":"evt9","subject":"Planning",`+
			`"start":{"dateTime":"2026-09-20T10:00:00.0000000","timeZone":"UTC"},`+
			`"end":{"dateTime":"2026-09-20T11:00:00.0000000","timeZone":"UTC"},`+
			`"webLink":"https://outlook.office.com/calendar/item/evt9"}`)
	defer server.Close()

	result, err := runMicrosoftAction(t, server, "create_event", map[string]any{
		"subject": "Planning", "start": "2026-09-20T10:00:00Z", "end": "2026-09-20T11:00:00Z",
		"attendees": []string{"carol@example.com"}, "body": "Quarterly planning.",
	})
	if err != nil {
		t.Fatalf("Run() error = %v, want nil", err)
	}
	if got.Method != http.MethodPost || got.Path != "/me/events" {
		t.Fatalf("request = %s %s, want POST /me/events", got.Method, got.Path)
	}
	if got.Auth != "Bearer tok123" {
		t.Fatalf("Authorization = %q, want %q", got.Auth, "Bearer tok123")
	}
	if got.Body["subject"] != "Planning" {
		t.Fatalf("body.subject = %v, want \"Planning\"", got.Body["subject"])
	}
	wantStart := map[string]any{"dateTime": "2026-09-20T10:00:00", "timeZone": "UTC"}
	if !reflect.DeepEqual(got.Body["start"], wantStart) {
		t.Fatalf("body.start = %v, want %v", got.Body["start"], wantStart)
	}
	wantEnd := map[string]any{"dateTime": "2026-09-20T11:00:00", "timeZone": "UTC"}
	if !reflect.DeepEqual(got.Body["end"], wantEnd) {
		t.Fatalf("body.end = %v, want %v", got.Body["end"], wantEnd)
	}
	wantAttendees := []any{map[string]any{"emailAddress": map[string]any{"address": "carol@example.com"}, "type": "required"}}
	if !reflect.DeepEqual(got.Body["attendees"], wantAttendees) {
		t.Fatalf("body.attendees = %v, want %v", got.Body["attendees"], wantAttendees)
	}
	wantResource := integrationsv1.Resource{Type: "event", ID: "evt9", Name: "Planning", URL: "https://outlook.office.com/calendar/item/evt9"}
	if result.Resource != wantResource {
		t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
	}
	var created microsoftEventCreated
	if err := json.Unmarshal(result.Result, &created); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	want := microsoftEventCreated{
		ID: "evt9", Subject: "Planning", Start: "2026-09-20T10:00:00Z", End: "2026-09-20T11:00:00Z",
		URL: "https://outlook.office.com/calendar/item/evt9",
	}
	if created != want {
		t.Fatalf("Result = %+v, want %+v", created, want)
	}
}

func TestMicrosoftActionsInputRefusals(t *testing.T) {
	tests := []struct {
		name   string
		action string
		input  map[string]any
	}{
		{"list_messages unknown field", "list_messages", map[string]any{"folder": "Inbox", "extra": "nope"}},
		{"list_messages limit too high", "list_messages", map[string]any{"limit": 51}},
		{"list_messages limit negative", "list_messages", map[string]any{"limit": -1}},

		{"get_message missing message", "get_message", map[string]any{}},
		{"get_message unknown field", "get_message", map[string]any{"message": "m1", "extra": "nope"}},

		{"send_mail missing to", "send_mail", map[string]any{"subject": "Hi", "body": "Hello"}},
		{"send_mail empty to", "send_mail", map[string]any{"to": []string{}, "subject": "Hi", "body": "Hello"}},
		{"send_mail missing subject", "send_mail", map[string]any{"to": []string{"a@b.com"}, "body": "Hello"}},
		{"send_mail missing body", "send_mail", map[string]any{"to": []string{"a@b.com"}, "subject": "Hi"}},
		{"send_mail to is not an address", "send_mail", map[string]any{"to": []string{"not-an-address"}, "subject": "Hi", "body": "Hello"}},
		{"send_mail cc is not an address", "send_mail", map[string]any{"to": []string{"a@b.com"}, "cc": []string{"nope"}, "subject": "Hi", "body": "Hello"}},
		{"send_mail unknown field", "send_mail", map[string]any{"to": []string{"a@b.com"}, "subject": "Hi", "body": "Hello", "urgent": true}},

		{"list_events unknown field", "list_events", map[string]any{"extra": "nope"}},
		{"list_events from not RFC 3339", "list_events", map[string]any{"from": "not-a-date"}},
		{"list_events to before from", "list_events", map[string]any{"from": "2026-09-20T10:00:00Z", "to": "2026-09-19T10:00:00Z"}},
		{"list_events limit too high", "list_events", map[string]any{"limit": 51}},

		{"create_event missing subject", "create_event", map[string]any{"start": "2026-09-20T10:00:00Z", "end": "2026-09-20T10:30:00Z"}},
		{"create_event missing start", "create_event", map[string]any{"subject": "Standup", "end": "2026-09-20T10:30:00Z"}},
		{"create_event missing end", "create_event", map[string]any{"subject": "Standup", "start": "2026-09-20T10:00:00Z"}},
		{"create_event start not RFC 3339", "create_event", map[string]any{"subject": "Standup", "start": "not-a-date", "end": "2026-09-20T10:30:00Z"}},
		{"create_event end before start", "create_event", map[string]any{"subject": "Standup", "start": "2026-09-20T10:30:00Z", "end": "2026-09-20T10:00:00Z"}},
		{"create_event attendee is not an address", "create_event", map[string]any{"subject": "Standup", "start": "2026-09-20T10:00:00Z", "end": "2026-09-20T10:30:00Z", "attendees": []string{"nope"}}},
		{"create_event unknown field", "create_event", map[string]any{"subject": "Standup", "start": "2026-09-20T10:00:00Z", "end": "2026-09-20T10:30:00Z", "extra": "nope"}},
	}
	server := microsoftNoCallServer(t)
	defer server.Close()
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := runMicrosoftAction(t, server, tc.action, tc.input)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}
}

func TestMicrosoftActionsStatusMapping(t *testing.T) {
	actions := []struct {
		name  string
		input map[string]any
	}{
		{"list_messages", map[string]any{}},
		{"get_message", map[string]any{"message": "msg1"}},
		{"send_mail", map[string]any{"to": []string{"a@b.com"}, "subject": "Hi", "body": "Hello"}},
		{"list_events", map[string]any{}},
		{"create_event", map[string]any{"subject": "Standup", "start": "2026-09-20T10:00:00Z", "end": "2026-09-20T10:30:00Z"}},
	}
	statuses := []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, domain.ErrInvalid},
		{http.StatusUnauthorized, domain.ErrCredentialRejected},
		{http.StatusForbidden, domain.ErrForbidden},
		{http.StatusNotFound, domain.ErrNotFound},
		{http.StatusTooManyRequests, domain.ErrProviderUnavailable},
		{http.StatusServiceUnavailable, domain.ErrProviderUnavailable},
	}
	for _, action := range actions {
		for _, tc := range statuses {
			t.Run(action.name+"/"+http.StatusText(tc.status), func(t *testing.T) {
				server, _ := microsoftFakeServer(t, tc.status, `{}`)
				defer server.Close()
				_, err := runMicrosoftAction(t, server, action.name, action.input)
				if !errors.Is(err, tc.want) {
					t.Fatalf("Run() error = %v, want %v", err, tc.want)
				}
			})
		}
	}
}

func TestMicrosoftActionsCatalog(t *testing.T) {
	actions := microsoftActions("https://graph.microsoft.test")
	want := []struct {
		name   string
		writes bool
	}{
		{"list_messages", false},
		{"get_message", false},
		{"send_mail", true},
		{"list_events", false},
		{"create_event", true},
	}
	if len(actions) != len(want) {
		t.Fatalf("microsoftActions() has %d actions, want %d", len(actions), len(want))
	}
	for index, action := range actions {
		if action.Name != want[index].name || action.Writes != want[index].writes {
			t.Errorf("actions[%d] = %q, writes %v; want %q, writes %v", index, action.Name, action.Writes, want[index].name, want[index].writes)
		}
		if !ActionNamePattern.MatchString(action.Name) {
			t.Errorf("action name %q does not match ActionNamePattern", action.Name)
		}
		if action.Summary == "" {
			t.Errorf("actions[%d] %q has no Summary", index, action.Name)
		}
		if action.Run == nil {
			t.Errorf("actions[%d] %q has no Run", index, action.Name)
		}
	}
}

func TestMicrosoftActionSchemas(t *testing.T) {
	for _, action := range microsoftActions("https://graph.microsoft.test") {
		t.Run(action.Name, func(t *testing.T) {
			var schema struct {
				Type                 string                    `json:"type"`
				Properties           map[string]map[string]any `json:"properties"`
				Required             []string                  `json:"required"`
				AdditionalProperties *bool                     `json:"additionalProperties"`
			}
			if err := json.Unmarshal(action.Input, &schema); err != nil {
				t.Fatalf("Input is not valid JSON: %v (%s)", err, action.Input)
			}
			if schema.Type != "object" {
				t.Errorf("type = %q, want object", schema.Type)
			}
			if schema.AdditionalProperties == nil || *schema.AdditionalProperties {
				t.Errorf("additionalProperties = %v, want false", schema.AdditionalProperties)
			}
			if len(schema.Properties) == 0 {
				t.Errorf("properties is empty")
			}
			for name, property := range schema.Properties {
				propType, _ := property["type"].(string)
				if propType == "" {
					t.Errorf("property %s has no type", name)
				}
				description, _ := property["description"].(string)
				if description == "" {
					t.Errorf("property %s has no description", name)
				}
			}
			for _, name := range schema.Required {
				if _, ok := schema.Properties[name]; !ok {
					t.Errorf("required field %q is not a declared property", name)
				}
			}
		})
	}
}
