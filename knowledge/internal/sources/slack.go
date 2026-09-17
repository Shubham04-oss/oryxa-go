package sources

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	knowledgev1 "github.com/Shubham04-oss/oryxa-go/contracts/events/knowledge/v1"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// slackAPI is where Slack's Web API lives.
const slackAPI = "https://slack.com/api"

// slackPageSize is what this extractor asks conversations.history for.
// Slack allows up to a thousand, but recommends far less: a page is the unit
// of work a lease has to cover, and a smaller one resumes more precisely.
const slackPageSize = 200

// Slack reads one channel's messages through conversations.history. There is
// no generated client for Slack the way there is for GitHub, so this
// extractor speaks the Web API directly, hand-rolled on the package's shared
// client.
type Slack struct {
	client *Client
	api    string
}

func NewSlack(client *Client) *Slack {
	return &Slack{client: client, api: slackAPI}
}

func (source *Slack) Provider() string { return "slack" }

func (source *Slack) Target(target map[string]string) error {
	_, err := slackChannel(target)
	return err
}

// Fetch walks one channel's history, resuming from the next_cursor Slack
// answers. An incremental pass asks only for what came after the watermark,
// passed as oldest a second early: Slack's default treats oldest as
// exclusive, so asking for the watermark itself would rely on that default
// never changing, and the failure mode if it ever did is the message the
// watermark names being skipped silently. Asking a second early instead
// means the idempotent upsert absorbs the one-message overlap, whichever way
// Slack treats the boundary. A full pass asks for everything, because only a
// full pass may retract what it did not see.
//
// Slack answers newest message first, unlike GitHub's oldest-change-first
// walk. So a watermark taken from the first page is already the newest of the
// whole window, and a partial walk must not persist it: doing so would make
// the next run skip the older messages this one had not reached. The caller
// advances a watermark only once a source is drained, which is what makes
// that safe.
func (source *Slack) Fetch(ctx context.Context, request Request) (Page, error) {
	channel, err := slackChannel(request.Source.Target)
	if err != nil {
		return Page{}, err
	}

	query := url.Values{
		"channel": {channel},
		"limit":   {strconv.Itoa(Limit(request.Limit, slackPageSize))},
	}
	if cursor := request.Cursor.Value; cursor != "" {
		query.Set("cursor", cursor)
	}
	if !request.Full && request.Cursor.Watermark != nil {
		query.Set("oldest", slackOldest(request.Cursor.Watermark.Add(-time.Second)))
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/conversations.history?"+query.Encode(), nil)
	if err != nil {
		return Page{}, fmt.Errorf("build the Slack request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, request.Credential)
	if err != nil {
		return Page{}, err
	}

	// Slack answers 200 whether or not the call worked: a failure rides in the
	// body as ok:false, so the status this far says nothing about success.
	var answer slackHistoryResponse
	if err := json.Unmarshal(response.Body, &answer); err != nil {
		return Page{}, fmt.Errorf("%w: Slack response is not valid", domain.ErrProviderUnavailable)
	}
	if !answer.OK {
		return Page{}, slackError(answer.Error)
	}

	page := Page{Items: make([]Item, 0, len(answer.Messages))}
	for _, message := range answer.Messages {
		updated, err := slackTime(message.TS)
		if err != nil {
			return Page{}, err
		}
		page.Items = append(page.Items, slackItem(message, updated, channel, request.Credential.Domain))
		page.Watermark = watermarkAfter(page.Watermark, updated)
	}
	page.Cursor = answer.ResponseMetadata.NextCursor
	return page, nil
}

// slackHistoryResponse is what conversations.history answers: ok true
// alongside a page of messages, or ok false naming what went wrong. Slack
// reports most failures this way on an HTTP 200, so a status check alone
// would miss them.
type slackHistoryResponse struct {
	OK               bool           `json:"ok"`
	Error            string         `json:"error"`
	Messages         []slackMessage `json:"messages"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

// slackMessage is one message as conversations.history answers it.
type slackMessage struct {
	User     string `json:"user"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
}

// slackItem turns one message into what a source yields. updated is passed in
// already parsed, since Fetch needs it again for the page's watermark.
func slackItem(message slackMessage, updated time.Time, channel, workspace string) Item {
	metadata := map[string]string{"channel": channel}
	if message.User != "" {
		metadata["user"] = message.User
	}
	if message.ThreadTS != "" {
		metadata["thread_ts"] = message.ThreadTS
	}
	return Item{
		ExternalID: message.TS,
		Kind:       knowledgev1.KindMessage,
		URL:        slackPermalink(workspace, channel, message.TS),
		MIME:       transform.MIMEPlain,
		Content:    []byte(message.Text),
		Metadata:   metadata,
		UpdatedAt:  &updated,
	}
}

// slackPermalink is where a person reads this message, for a citation to
// point at. The workspace is what connections issued the credential for; with
// none, slack.com still resolves the archive link for whoever follows it.
func slackPermalink(workspace, channel, ts string) string {
	host := strings.TrimSpace(workspace)
	if host == "" {
		host = "slack.com"
	}
	return "https://" + host + "/archives/" + url.PathEscape(channel) +
		"/p" + strings.ReplaceAll(ts, ".", "")
}

// slackChannel reads the one field a Slack source names.
func slackChannel(target map[string]string) (string, error) {
	channel := strings.TrimSpace(target["channel"])
	if channel == "" {
		return "", MissingTarget("a channel, by ID such as C0123ABC")
	}
	return channel, nil
}

// slackTime turns a ts into the moment it names: a Unix timestamp with up to
// six digits of fractional seconds, carried as a string because it doubles as
// the message's id.
//
// The halves are parsed as integers rather than as one float. A float64 holds
// about sixteen significant digits and a ts already spends sixteen, so
// parsing it as a number would round — and a rounded watermark handed back as
// oldest is how a message quietly stops being read.
func slackTime(ts string) (time.Time, error) {
	whole, fraction, _ := strings.Cut(strings.TrimSpace(ts), ".")
	seconds, err := strconv.ParseInt(whole, 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse Slack timestamp %q: %w", ts, domain.ErrInvalid)
	}
	micros := int64(0)
	if fraction != "" {
		// Slack sends six digits; pad a shorter one so "5" means 500000.
		padded := (fraction + "000000")[:6]
		if micros, err = strconv.ParseInt(padded, 10, 64); err != nil {
			return time.Time{}, fmt.Errorf("parse Slack timestamp %q: %w", ts, domain.ErrInvalid)
		}
	}
	return time.UnixMicro(seconds*1e6 + micros).UTC(), nil
}

// slackOldest is a moment in the form conversations.history takes it for its
// oldest parameter: the same encoding ts itself uses.
func slackOldest(moment time.Time) string {
	return fmt.Sprintf("%d.%06d", moment.Unix(), moment.Nanosecond()/1000)
}

// slackError maps one of Slack's ok:false codes to the reason a caller acts
// on. Slack documents far more codes than these; one this does not recognize
// is treated as the provider's own trouble rather than guessed at, and only
// the short code, never a fuller message, ever reaches the caller.
func slackError(code string) error {
	switch code {
	case "ratelimited":
		return fmt.Errorf("%w: Slack %s", domain.ErrRateLimited, code)
	case "invalid_auth", "token_revoked", "account_inactive":
		return fmt.Errorf("%w: Slack %s", domain.ErrNeedsReconnect, code)
	case "missing_scope", "not_allowed_token_type":
		return fmt.Errorf("%w: Slack %s", domain.ErrScopeRequired, code)
	case "channel_not_found":
		return fmt.Errorf("%w: Slack %s", domain.ErrNotFound, code)
	default:
		return fmt.Errorf("%w: Slack %s", domain.ErrProviderUnavailable, code)
	}
}
