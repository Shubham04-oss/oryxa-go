package providers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// runSlackAction runs a named action from provider with credential and input
// (raw JSON), the way a caller would.
func runSlackAction(t *testing.T, provider Provider, name string, credential domain.Credential, input string) (ActionResult, error) {
	t.Helper()
	action, ok := provider.Action(name)
	if !ok {
		t.Fatalf("provider has no action %q", name)
	}
	return action.Run(context.Background(), http.DefaultClient, ActionRequest{
		Credential: credential, Input: json.RawMessage(input), Now: fixedNow,
	})
}

// slackNoCallServer fails the test if Slack is ever called, for asserting an
// input refusal never reaches the network.
func slackNoCallServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Errorf("unexpected request to Slack")
	}))
}

var slackActionCredential = domain.Credential{AccessToken: "tok123", Prefix: "Bearer "}

func TestSlackListChannels(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		var gotMethod, gotPath, gotAuth string
		var gotQuery url.Values
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath, gotAuth = request.Method, request.URL.Path, request.Header.Get("Authorization")
			gotQuery = request.URL.Query()
			_, _ = io.WriteString(writer, `{"ok":true,"channels":[{"id":"C1","name":"general","is_private":false,"is_member":true}]}`)
		}))
		defer server.Close()

		result, err := runSlackAction(t, slackProvider(server.URL), "list_channels", slackActionCredential,
			`{"limit":50,"types":"public_channel,private_channel"}`)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if gotMethod != http.MethodGet || gotPath != "/conversations.list" {
			t.Fatalf("request = %s %s, want GET /conversations.list", gotMethod, gotPath)
		}
		if gotAuth != "Bearer tok123" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
		}
		if gotQuery.Get("limit") != "50" || gotQuery.Get("types") != "public_channel,private_channel" {
			t.Fatalf("query = %v, want limit=50 types=public_channel,private_channel", gotQuery)
		}
		var channels []slackChannelSummary
		if err := json.Unmarshal(result.Result, &channels); err != nil {
			t.Fatalf("Result is not a channel array: %v (%s)", err, result.Result)
		}
		want := []slackChannelSummary{{ID: "C1", Name: "general", IsPrivate: false, IsMember: true}}
		if !slices.Equal(channels, want) {
			t.Fatalf("channels = %+v, want %+v", channels, want)
		}
		if result.Resource != (integrationsv1.Resource{}) {
			t.Fatalf("Resource = %+v, want the zero value for a read", result.Resource)
		}
	})

	t.Run("defaults when limit and types are omitted", func(t *testing.T) {
		var gotQuery url.Values
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotQuery = request.URL.Query()
			_, _ = io.WriteString(writer, `{"ok":true,"channels":[]}`)
		}))
		defer server.Close()

		result, err := runSlackAction(t, slackProvider(server.URL), "list_channels", slackActionCredential, `{}`)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if gotQuery.Get("limit") != "100" || gotQuery.Get("types") != "public_channel" {
			t.Fatalf("query = %v, want limit=100 types=public_channel", gotQuery)
		}
		if string(result.Result) != "[]" {
			t.Fatalf("Result = %s, want [] for no channels", result.Result)
		}
	})

	for _, limit := range []int{0, 201, -1} {
		t.Run("out of range limit "+strconv.Itoa(limit), func(t *testing.T) {
			server := slackNoCallServer(t)
			defer server.Close()
			_, err := runSlackAction(t, slackProvider(server.URL), "list_channels", slackActionCredential,
				`{"limit":`+strconv.Itoa(limit)+`}`)
			if !errors.Is(err, domain.ErrInvalid) {
				t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
			}
		})
	}

	t.Run("unknown field", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "list_channels", slackActionCredential, `{"limit":10,"archived":true}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

func TestSlackPostMessage(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		var gotMethod, gotPath, gotAuth string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath, gotAuth = request.Method, request.URL.Path, request.Header.Get("Authorization")
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"ok":true,"channel":"C1","ts":"111.222"}`)
		}))
		defer server.Close()

		result, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential,
			`{"channel":"C1","text":"hello"}`)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if gotMethod != http.MethodPost || gotPath != "/chat.postMessage" {
			t.Fatalf("request = %s %s, want POST /chat.postMessage", gotMethod, gotPath)
		}
		if gotAuth != "Bearer tok123" {
			t.Fatalf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
		}
		if gotBody["channel"] != "C1" || gotBody["text"] != "hello" {
			t.Fatalf("request body = %v, want channel C1, text hello", gotBody)
		}
		wantResource := integrationsv1.Resource{Type: "message", ID: "111.222", Name: "C1"}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var got slackMessageResult
		if err := json.Unmarshal(result.Result, &got); err != nil {
			t.Fatalf("Result is not a slackMessageResult: %v (%s)", err, result.Result)
		}
		if want := (slackMessageResult{Channel: "C1", TS: "111.222"}); got != want {
			t.Fatalf("Result = %+v, want %+v", got, want)
		}
	})

	t.Run("missing text", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential, `{"channel":"C1"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("missing channel", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential, `{"text":"hi"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential,
			`{"channel":"C1","text":"hi","urgent":true}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("#channel name refused", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential,
			`{"channel":"#general","text":"hi"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

func TestSlackReplyInThread(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"ok":true,"channel":"C1","ts":"111.333"}`)
		}))
		defer server.Close()

		result, err := runSlackAction(t, slackProvider(server.URL), "reply_in_thread", slackActionCredential,
			`{"channel":"C1","thread_ts":"111.222","text":"reply"}`)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if gotBody["channel"] != "C1" || gotBody["thread_ts"] != "111.222" || gotBody["text"] != "reply" {
			t.Fatalf("request body = %v, want channel C1, thread_ts 111.222, text reply", gotBody)
		}
		wantResource := integrationsv1.Resource{Type: "message", ID: "111.333", Name: "C1"}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		var got slackThreadReplyResult
		if err := json.Unmarshal(result.Result, &got); err != nil {
			t.Fatalf("Result is not a slackThreadReplyResult: %v (%s)", err, result.Result)
		}
		if want := (slackThreadReplyResult{Channel: "C1", TS: "111.333", ThreadTS: "111.222"}); got != want {
			t.Fatalf("Result = %+v, want %+v", got, want)
		}
	})

	t.Run("missing thread_ts", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "reply_in_thread", slackActionCredential,
			`{"channel":"C1","text":"reply"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "reply_in_thread", slackActionCredential,
			`{"channel":"C1","thread_ts":"111.222","text":"reply","broadcast":true}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("#channel name refused", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "reply_in_thread", slackActionCredential,
			`{"channel":"#general","thread_ts":"111.222","text":"reply"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

func TestSlackAddReaction(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		var gotMethod, gotPath string
		var gotBody map[string]any
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			gotMethod, gotPath = request.Method, request.URL.Path
			if err := json.NewDecoder(request.Body).Decode(&gotBody); err != nil {
				t.Errorf("decode request body: %v", err)
			}
			_, _ = io.WriteString(writer, `{"ok":true}`)
		}))
		defer server.Close()

		result, err := runSlackAction(t, slackProvider(server.URL), "add_reaction", slackActionCredential,
			`{"channel":"C1","ts":"111.222","emoji":"tada"}`)
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
		if gotMethod != http.MethodPost || gotPath != "/reactions.add" {
			t.Fatalf("request = %s %s, want POST /reactions.add", gotMethod, gotPath)
		}
		// Slack's reactions.add takes timestamp and name, not ts and emoji.
		if gotBody["channel"] != "C1" || gotBody["timestamp"] != "111.222" || gotBody["name"] != "tada" {
			t.Fatalf("request body = %v, want channel C1, timestamp 111.222, name tada", gotBody)
		}
		wantResource := integrationsv1.Resource{Type: "reaction", ID: "111.222", Name: "tada"}
		if result.Resource != wantResource {
			t.Fatalf("Resource = %+v, want %+v", result.Resource, wantResource)
		}
		if string(result.Result) != `{"ok":true}` {
			t.Fatalf("Result = %s, want {\"ok\":true}", result.Result)
		}
	})

	t.Run("missing emoji", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "add_reaction", slackActionCredential,
			`{"channel":"C1","ts":"111.222"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("unknown field", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "add_reaction", slackActionCredential,
			`{"channel":"C1","ts":"111.222","emoji":"tada","remove":true}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})

	t.Run("#channel name refused", func(t *testing.T) {
		server := slackNoCallServer(t)
		defer server.Close()
		_, err := runSlackAction(t, slackProvider(server.URL), "add_reaction", slackActionCredential,
			`{"channel":"#general","ts":"111.222","emoji":"tada"}`)
		if !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("Run() error = %v, want %v", err, domain.ErrInvalid)
		}
	})
}

// TestSlackActionErrorMapping is the ok:false mapping table: Slack answers
// most failures with HTTP 200 and {"ok":false,"error":"..."}.
func TestSlackActionErrorMapping(t *testing.T) {
	tests := []struct {
		name string
		code string
		want error
	}{
		{"invalid_auth", "invalid_auth", domain.ErrCredentialRejected},
		{"token_revoked", "token_revoked", domain.ErrCredentialRejected},
		{"account_inactive", "account_inactive", domain.ErrCredentialRejected},
		{"missing_scope", "missing_scope", domain.ErrForbidden},
		{"not_allowed_token_type", "not_allowed_token_type", domain.ErrForbidden},
		{"channel_not_found", "channel_not_found", domain.ErrNotFound},
		{"message_not_found", "message_not_found", domain.ErrNotFound},
		{"thread_not_found", "thread_not_found", domain.ErrNotFound},
		{"ratelimited", "ratelimited", domain.ErrProviderUnavailable},
		{"unknown error string", "something_slack_added_later", domain.ErrInvalid},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(http.StatusOK)
				_, _ = io.WriteString(writer, `{"ok":false,"error":"`+tc.code+`"}`)
			}))
			defer server.Close()
			_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential,
				`{"channel":"C1","text":"hi"}`)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run() error = %v, want %v", err, tc.want)
			}
			if err != nil && strings.Contains(err.Error(), "{") {
				t.Errorf("error leaked Slack's raw response body: %v", err)
			}
		})
	}
}

// TestSlackActionHTTPStatus checks that a non-2xx status still maps through
// the shared StatusError, the way every other provider's calls do.
func TestSlackActionHTTPStatus(t *testing.T) {
	tests := []struct {
		status int
		want   error
	}{
		{http.StatusUnauthorized, domain.ErrCredentialRejected},
		{http.StatusInternalServerError, domain.ErrProviderUnavailable},
	}
	for _, tc := range tests {
		t.Run(http.StatusText(tc.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.WriteHeader(tc.status)
			}))
			defer server.Close()
			_, err := runSlackAction(t, slackProvider(server.URL), "post_message", slackActionCredential,
				`{"channel":"C1","text":"hi"}`)
			if !errors.Is(err, tc.want) {
				t.Fatalf("Run() error = %v, want %v", err, tc.want)
			}
		})
	}
}

// TestSlackActionSchemas checks the shape every action's Input must have to
// be usable as a tool definition: a JSON object schema that refuses unknown
// properties, where every declared property tells a model both its type and
// what it is for.
func TestSlackActionSchemas(t *testing.T) {
	for _, action := range slackActions("https://slack.example.test/api") {
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

// TestSlackActionsRegistered checks slackActions' own contract: the four
// actions, in order, with the Writes the task spec calls for, wired into
// slackProvider so a caller building the provider gets them too.
func TestSlackActionsRegistered(t *testing.T) {
	want := []struct {
		name   string
		writes bool
	}{
		{"list_channels", false},
		{"post_message", true},
		{"reply_in_thread", true},
		{"add_reaction", true},
	}
	provider := slackProvider("https://slack.example.test/api")
	if len(provider.Actions) != len(want) {
		t.Fatalf("Actions = %d, want %d (%+v)", len(provider.Actions), len(want), provider.Actions)
	}
	for index, tc := range want {
		action := provider.Actions[index]
		if action.Name != tc.name {
			t.Errorf("Actions[%d].Name = %q, want %q", index, action.Name, tc.name)
		}
		if action.Writes != tc.writes {
			t.Errorf("Actions[%d].Writes = %v, want %v", index, action.Writes, tc.writes)
		}
		if !ActionNamePattern.MatchString(action.Name) {
			t.Errorf("action name %q does not match ActionNamePattern", action.Name)
		}
		if action.Summary == "" {
			t.Errorf("action %q has no Summary", action.Name)
		}
		if action.Run == nil {
			t.Errorf("action %q has no Run", action.Name)
		}
	}
}
