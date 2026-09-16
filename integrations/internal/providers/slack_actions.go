package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

// slackActions are the things a caller may do with a Slack connection: read
// channels, and speak in them. Every Slack Web API method lives at
// apiURL/<method>: a read like conversations.list takes a query string, a
// write like chat.postMessage takes a JSON body; both take the connection's
// token as a bearer Authorization header, which sendJSON applies through
// Authorize.
func slackActions(apiURL string) []Action {
	return []Action{
		slackListChannels(apiURL),
		slackPostMessage(apiURL),
		slackReplyInThread(apiURL),
		slackAddReaction(apiURL),
	}
}

// slackAnswer is the envelope every Slack Web API method answers: ok true
// alongside the method's own fields, or ok false naming what went wrong.
// Slack reports most failures this way on an HTTP 200, so a status check
// alone would miss them.
type slackAnswer struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
}

// slackCall sends a Slack Web API request authorized with credential and, on
// success, decodes its answer into target (nil when the caller needs nothing
// back beyond success). A non-2xx status is already mapped by sendJSON's
// StatusError; an ok:false answer, which Slack sends on a 200, is mapped by
// slackAPIError instead. The body is decoded twice: once to read ok/error,
// and again into target, since the two live in the same JSON object.
func slackCall(ctx context.Context, client *http.Client, method, endpoint string, credential domain.Credential, body, target any) error {
	var raw json.RawMessage
	if err := sendJSON(ctx, client, method, endpoint, credential, body, &raw); err != nil {
		return err
	}
	var answer slackAnswer
	if err := json.Unmarshal(raw, &answer); err != nil {
		return fmt.Errorf("%w: Slack response is not valid", domain.ErrProviderUnavailable)
	}
	if !answer.OK {
		return slackAPIError(answer.Error)
	}
	if target == nil {
		return nil
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("%w: Slack response is not valid", domain.ErrProviderUnavailable)
	}
	return nil
}

// slackAPIError maps one of Slack's ok:false error codes to the domain error
// a caller acts on. Slack documents far more codes than these; one this does
// not recognize is treated as a bad request rather than guessed at, and only
// the short code, never Slack's fuller message, reaches the caller.
func slackAPIError(code string) error {
	switch code {
	case "invalid_auth", "token_revoked", "account_inactive":
		return fmt.Errorf("%w: Slack %s", domain.ErrCredentialRejected, code)
	case "missing_scope", "not_allowed_token_type":
		return fmt.Errorf("%w: Slack %s", domain.ErrForbidden, code)
	case "channel_not_found", "message_not_found", "thread_not_found":
		return fmt.Errorf("%w: Slack %s", domain.ErrNotFound, code)
	case "ratelimited":
		return fmt.Errorf("%w: Slack %s", domain.ErrProviderUnavailable, code)
	default:
		return fmt.Errorf("%w: Slack %s", domain.ErrInvalid, code)
	}
}

// slackChannelID refuses a channel given as a name, such as #general: every
// action here takes the ID Slack's own APIs use (C0123ABC), and guessing the
// ID from a name the member typed risks acting on the wrong channel.
func slackChannelID(channel string) error {
	if strings.HasPrefix(channel, "#") {
		return fmt.Errorf("%w: channel must be an ID such as C0123ABC, not a #name", domain.ErrInvalid)
	}
	return nil
}

// --- list_channels ---

type slackListChannelsInput struct {
	Limit *int   `json:"limit"`
	Types string `json:"types"`
}

// slackChannelSummary is one channel as list_channels answers it, and also
// how Slack's own conversations.list names the same fields, so it doubles as
// the decode target.
type slackChannelSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
	IsMember  bool   `json:"is_member"`
}

const slackListChannelsSchema = `{
	"type": "object",
	"properties": {
		"limit": {"type": "integer", "description": "Most channels to return, from 1 to 200. Defaults to 100."},
		"types": {"type": "string", "description": "Comma-separated Slack channel types to list, such as public_channel or public_channel,private_channel. Defaults to public_channel."}
	},
	"required": [],
	"additionalProperties": false
}`

func slackListChannels(apiURL string) Action {
	return Action{
		Name:    "list_channels",
		Summary: "List the workspace's channels the connection can see.",
		Input:   json.RawMessage(slackListChannelsSchema),
		Writes:  false,
		Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
			var input slackListChannelsInput
			if err := DecodeInput(request.Input, &input); err != nil {
				return ActionResult{}, err
			}
			limit := 100
			if input.Limit != nil {
				if *input.Limit < 1 || *input.Limit > 200 {
					return ActionResult{}, fmt.Errorf("%w: limit must be between 1 and 200", domain.ErrInvalid)
				}
				limit = *input.Limit
			}
			types := input.Types
			if types == "" {
				types = "public_channel"
			}
			query := url.Values{"limit": {strconv.Itoa(limit)}, "types": {types}}
			var answer struct {
				Channels []slackChannelSummary `json:"channels"`
			}
			endpoint := apiURL + "/conversations.list?" + query.Encode()
			if err := slackCall(ctx, client, http.MethodGet, endpoint, request.Credential, nil, &answer); err != nil {
				return ActionResult{}, err
			}
			if answer.Channels == nil {
				// Answer a real array, never JSON null, when Slack lists nothing.
				answer.Channels = []slackChannelSummary{}
			}
			// Read-only: this names no single resource the action changed.
			return ActionResult{Result: Answered(answer.Channels)}, nil
		},
	}
}

// --- post_message ---

type slackPostMessageInput struct {
	Channel string `json:"channel"`
	Text    string `json:"text"`
}

// slackMessageResult is what post_message answers, and what chat.postMessage
// itself answers, so it doubles as the decode target. Permalink is left out
// unless Slack sends one.
type slackMessageResult struct {
	Channel   string `json:"channel"`
	TS        string `json:"ts"`
	Permalink string `json:"permalink,omitempty"`
}

const slackPostMessageSchema = `{
	"type": "object",
	"properties": {
		"channel": {"type": "string", "description": "The channel to post to, by ID such as C0123ABC, not by #name."},
		"text": {"type": "string", "description": "The message text to post."}
	},
	"required": ["channel", "text"],
	"additionalProperties": false
}`

func slackPostMessage(apiURL string) Action {
	return Action{
		Name:    "post_message",
		Summary: "Post a message to a Slack channel.",
		Input:   json.RawMessage(slackPostMessageSchema),
		Writes:  true,
		Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
			var input slackPostMessageInput
			if err := DecodeInput(request.Input, &input); err != nil {
				return ActionResult{}, err
			}
			if err := Required("channel", input.Channel); err != nil {
				return ActionResult{}, err
			}
			if err := Required("text", input.Text); err != nil {
				return ActionResult{}, err
			}
			if err := slackChannelID(input.Channel); err != nil {
				return ActionResult{}, err
			}
			var answer slackMessageResult
			body := map[string]string{"channel": input.Channel, "text": input.Text}
			if err := slackCall(ctx, client, http.MethodPost, apiURL+"/chat.postMessage", request.Credential, body, &answer); err != nil {
				return ActionResult{}, err
			}
			return ActionResult{
				Resource: integrationsv1.Resource{Type: "message", ID: answer.TS, Name: answer.Channel},
				Result:   Answered(answer),
			}, nil
		},
	}
}

// --- reply_in_thread ---

type slackReplyInThreadInput struct {
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
}

// slackThreadReplyResult is what reply_in_thread answers: the reply Slack
// created and the thread it joined. ThreadTS is not part of chat.postMessage's
// own top-level answer, so it is carried over from the input rather than
// decoded: Slack does not change the thread a reply names.
type slackThreadReplyResult struct {
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

const slackReplyInThreadSchema = `{
	"type": "object",
	"properties": {
		"channel": {"type": "string", "description": "The channel the parent message is in, by ID such as C0123ABC, not by #name."},
		"thread_ts": {"type": "string", "description": "The ts of the parent message to reply to."},
		"text": {"type": "string", "description": "The reply text to post."}
	},
	"required": ["channel", "thread_ts", "text"],
	"additionalProperties": false
}`

func slackReplyInThread(apiURL string) Action {
	return Action{
		Name:    "reply_in_thread",
		Summary: "Reply to a message in its thread, rather than starting a new message.",
		Input:   json.RawMessage(slackReplyInThreadSchema),
		Writes:  true,
		Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
			var input slackReplyInThreadInput
			if err := DecodeInput(request.Input, &input); err != nil {
				return ActionResult{}, err
			}
			if err := Required("channel", input.Channel); err != nil {
				return ActionResult{}, err
			}
			if err := Required("thread_ts", input.ThreadTS); err != nil {
				return ActionResult{}, err
			}
			if err := Required("text", input.Text); err != nil {
				return ActionResult{}, err
			}
			if err := slackChannelID(input.Channel); err != nil {
				return ActionResult{}, err
			}
			// chat.postMessage threads a reply by carrying the parent's ts as
			// thread_ts; it is otherwise the same call as post_message.
			var answer slackMessageResult
			body := map[string]string{"channel": input.Channel, "thread_ts": input.ThreadTS, "text": input.Text}
			if err := slackCall(ctx, client, http.MethodPost, apiURL+"/chat.postMessage", request.Credential, body, &answer); err != nil {
				return ActionResult{}, err
			}
			result := slackThreadReplyResult{Channel: answer.Channel, TS: answer.TS, ThreadTS: input.ThreadTS}
			return ActionResult{
				Resource: integrationsv1.Resource{Type: "message", ID: answer.TS, Name: answer.Channel},
				Result:   Answered(result),
			}, nil
		},
	}
}

// --- add_reaction ---

type slackAddReactionInput struct {
	Channel string `json:"channel"`
	TS      string `json:"ts"`
	Emoji   string `json:"emoji"`
}

// slackReactionResult is add_reaction's minimal answer: Slack's own
// reactions.add gives back nothing more than ok:true.
type slackReactionResult struct {
	OK bool `json:"ok"`
}

const slackAddReactionSchema = `{
	"type": "object",
	"properties": {
		"channel": {"type": "string", "description": "The channel the message is in, by ID such as C0123ABC, not by #name."},
		"ts": {"type": "string", "description": "The ts of the message to react to."},
		"emoji": {"type": "string", "description": "The reaction's emoji name, without colons, such as tada."}
	},
	"required": ["channel", "ts", "emoji"],
	"additionalProperties": false
}`

func slackAddReaction(apiURL string) Action {
	return Action{
		Name:    "add_reaction",
		Summary: "Add an emoji reaction to a Slack message.",
		Input:   json.RawMessage(slackAddReactionSchema),
		Writes:  true,
		Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
			var input slackAddReactionInput
			if err := DecodeInput(request.Input, &input); err != nil {
				return ActionResult{}, err
			}
			if err := Required("channel", input.Channel); err != nil {
				return ActionResult{}, err
			}
			if err := Required("ts", input.TS); err != nil {
				return ActionResult{}, err
			}
			if err := Required("emoji", input.Emoji); err != nil {
				return ActionResult{}, err
			}
			if err := slackChannelID(input.Channel); err != nil {
				return ActionResult{}, err
			}
			// Slack's reactions.add names these fields timestamp and name,
			// not the ts/emoji this action takes.
			body := map[string]string{"channel": input.Channel, "timestamp": input.TS, "name": input.Emoji}
			if err := slackCall(ctx, client, http.MethodPost, apiURL+"/reactions.add", request.Credential, body, nil); err != nil {
				return ActionResult{}, err
			}
			return ActionResult{
				Resource: integrationsv1.Resource{Type: "reaction", ID: input.TS, Name: input.Emoji},
				Result:   Answered(slackReactionResult{OK: true}),
			}, nil
		},
	}
}
