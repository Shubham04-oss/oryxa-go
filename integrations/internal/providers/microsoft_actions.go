package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	integrationsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/integrations/v1"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/domain"
)

var (
	microsoftListMessagesSchema = json.RawMessage(`{"type":"object","properties":{"folder":{"type":"string","description":"The well-known mail folder to list, such as Inbox or Drafts. Defaults to Inbox."},"limit":{"type":"integer","description":"The most messages to return, 1 to 50. Defaults to 20."}},"required":[],"additionalProperties":false}`)

	microsoftGetMessageSchema = json.RawMessage(`{"type":"object","properties":{"message":{"type":"string","description":"The ID of the message to fetch."}},"required":["message"],"additionalProperties":false}`)

	microsoftSendMailSchema = json.RawMessage(`{"type":"object","properties":{"to":{"type":"array","items":{"type":"string"},"description":"The email addresses to send to. At least one is required."},"subject":{"type":"string","description":"The email's subject line."},"body":{"type":"string","description":"The email's body text."},"cc":{"type":"array","items":{"type":"string"},"description":"Email addresses to copy on the message."}},"required":["to","subject","body"],"additionalProperties":false}`)

	microsoftListEventsSchema = json.RawMessage(`{"type":"object","properties":{"from":{"type":"string","description":"The start of the window to list, as an RFC 3339 UTC timestamp. Defaults to now."},"to":{"type":"string","description":"The end of the window to list, as an RFC 3339 UTC timestamp. Defaults to seven days after from."},"limit":{"type":"integer","description":"The most events to return, 1 to 50. Defaults to 20."}},"required":[],"additionalProperties":false}`)

	microsoftCreateEventSchema = json.RawMessage(`{"type":"object","properties":{"subject":{"type":"string","description":"The event's title."},"start":{"type":"string","description":"When the event starts, as an RFC 3339 UTC timestamp."},"end":{"type":"string","description":"When the event ends, as an RFC 3339 UTC timestamp."},"attendees":{"type":"array","items":{"type":"string"},"description":"Email addresses to invite to the event."},"body":{"type":"string","description":"The event's description."}},"required":["subject","start","end"],"additionalProperties":false}`)
)

// microsoftActions are what a caller may do with a Microsoft connection: read
// and send the member's mail, and read and create events on their calendar.
// Every call goes through sendJSON, which places the credential, caps the
// answer at 1 MiB, follows no redirect, and maps Graph's status to a domain
// error. A GET's query parameters are built with net/url, which escapes $top
// and $select as %24top and %24select; Graph accepts both forms, since many
// HTTP clients escape $ the same way sendJSON's caller here does.
func microsoftActions(apiURL string) []Action {
	return []Action{
		{
			Name:    "list_messages",
			Summary: "Lists messages in one of the member's mail folders, newest first.",
			Input:   microsoftListMessagesSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return microsoftListMessages(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "get_message",
			Summary: "Answers one mail message's subject, sender, recipients and body as text.",
			Input:   microsoftGetMessageSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return microsoftGetMessage(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "send_mail",
			Summary: "Sends an email from the member's mailbox and saves a copy to Sent Items.",
			Input:   microsoftSendMailSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return microsoftSendMail(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "list_events",
			Summary: "Lists calendar events in a time window, defaulting to the next seven days.",
			Input:   microsoftListEventsSchema,
			Writes:  false,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return microsoftListEvents(ctx, client, apiURL, request)
			},
		},
		{
			Name:    "create_event",
			Summary: "Creates an event on the member's calendar.",
			Input:   microsoftCreateEventSchema,
			Writes:  true,
			Run: func(ctx context.Context, client *http.Client, request ActionRequest) (ActionResult, error) {
				return microsoftCreateEvent(ctx, client, apiURL, request)
			},
		},
	}
}

// --- list_messages ---

type microsoftListMessagesInput struct {
	Folder string `json:"folder"`
	Limit  int    `json:"limit"`
}

// microsoftMessageSummary is one entry of list_messages' answer.
type microsoftMessageSummary struct {
	ID         string `json:"id"`
	Subject    string `json:"subject"`
	From       string `json:"from"`
	ReceivedAt string `json:"received_at"`
	IsRead     bool   `json:"is_read"`
	Preview    string `json:"preview"`
}

func microsoftListMessages(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input microsoftListMessagesInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	folder := input.Folder
	if folder == "" {
		folder = "Inbox"
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ActionResult{}, fmt.Errorf("%w: limit must be 1 to 50", domain.ErrInvalid)
	}
	query := url.Values{
		"$top":    {strconv.Itoa(limit)},
		"$select": {"id,subject,from,receivedDateTime,isRead,bodyPreview"},
	}
	endpoint := apiURL + "/me/mailFolders('" + url.PathEscape(folder) + "')/messages?" + query.Encode()
	var answer struct {
		Value []microsoftGraphMessage `json:"value"`
	}
	if err := sendJSON(ctx, client, http.MethodGet, endpoint, request.Credential, nil, &answer); err != nil {
		return ActionResult{}, err
	}
	summaries := make([]microsoftMessageSummary, 0, len(answer.Value))
	for _, message := range answer.Value {
		summaries = append(summaries, microsoftMessageSummary{
			ID:         message.ID,
			Subject:    message.Subject,
			From:       microsoftAddress(message.From),
			ReceivedAt: message.ReceivedDateTime.UTC().Format(time.RFC3339),
			IsRead:     message.IsRead,
			Preview:    message.BodyPreview,
		})
	}
	// Read-only: this names no single resource the action changed.
	return ActionResult{Result: Answered(summaries)}, nil
}

// --- get_message ---

type microsoftGetMessageInput struct {
	Message string `json:"message"`
}

// microsoftMessageDetail is get_message's answer, with the body as text
// rather than the HTML Graph sends by default.
type microsoftMessageDetail struct {
	ID         string   `json:"id"`
	Subject    string   `json:"subject"`
	From       string   `json:"from"`
	To         []string `json:"to"`
	ReceivedAt string   `json:"received_at"`
	Body       string   `json:"body"`
}

func microsoftGetMessage(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input microsoftGetMessageInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("message", input.Message); err != nil {
		return ActionResult{}, err
	}
	query := url.Values{"$select": {"id,subject,from,toRecipients,receivedDateTime,body,webLink"}}
	endpoint := apiURL + "/me/messages/" + url.PathEscape(input.Message) + "?" + query.Encode()
	var message microsoftGraphMessage
	if err := sendJSON(ctx, client, http.MethodGet, endpoint, request.Credential, nil, &message); err != nil {
		return ActionResult{}, err
	}
	detail := microsoftMessageDetail{
		ID:         message.ID,
		Subject:    message.Subject,
		From:       microsoftAddress(message.From),
		To:         microsoftAddresses(message.ToRecipients),
		ReceivedAt: message.ReceivedDateTime.UTC().Format(time.RFC3339),
		Body:       microsoftBodyText(message.Body.ContentType, message.Body.Content),
	}
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "message", ID: message.ID, Name: message.Subject, URL: message.WebLink},
		Result:   Answered(detail),
	}, nil
}

// --- send_mail ---

type microsoftSendMailInput struct {
	To      []string `json:"to"`
	Subject string   `json:"subject"`
	Body    string   `json:"body"`
	Cc      []string `json:"cc"`
}

// microsoftMailSent is send_mail's answer: Graph's sendMail answers 202 with
// no body, so there is nothing of Graph's own to report back beyond success.
type microsoftMailSent struct {
	Sent    bool   `json:"sent"`
	Subject string `json:"subject"`
}

func microsoftSendMail(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input microsoftSendMailInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if len(input.To) == 0 {
		return ActionResult{}, fmt.Errorf("%w: to is required", domain.ErrInvalid)
	}
	if err := Required("subject", input.Subject); err != nil {
		return ActionResult{}, err
	}
	if err := Required("body", input.Body); err != nil {
		return ActionResult{}, err
	}
	addresses := append(append([]string{}, input.To...), input.Cc...)
	for _, address := range addresses {
		if !microsoftLooksLikeEmail(address) {
			return ActionResult{}, fmt.Errorf("%w: %q is not an email address", domain.ErrInvalid, address)
		}
	}
	message := map[string]any{
		"subject":      input.Subject,
		"body":         map[string]string{"contentType": "Text", "content": input.Body},
		"toRecipients": microsoftRecipients(input.To),
	}
	if len(input.Cc) > 0 {
		message["ccRecipients"] = microsoftRecipients(input.Cc)
	}
	payload := map[string]any{"message": message, "saveToSentItems": true}
	// Graph answers sendMail with 202 Accepted and no body; sendJSON only
	// decodes when its target is non-nil and the payload is non-empty, so a
	// nil target here never trips on the empty answer.
	if err := sendJSON(ctx, client, http.MethodPost, apiURL+"/me/sendMail", request.Credential, payload, nil); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "message", Name: input.Subject},
		Result:   Answered(microsoftMailSent{Sent: true, Subject: input.Subject}),
	}, nil
}

// --- list_events ---

type microsoftListEventsInput struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Limit int    `json:"limit"`
}

// microsoftEventSummary is one entry of list_events' answer.
type microsoftEventSummary struct {
	ID               string `json:"id"`
	Subject          string `json:"subject"`
	Start            string `json:"start"`
	End              string `json:"end"`
	Organizer        string `json:"organizer"`
	OnlineMeetingURL string `json:"online_meeting_url,omitempty"`
}

func microsoftListEvents(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input microsoftListEventsInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	from := request.Now.UTC()
	if input.From != "" {
		parsed, err := time.Parse(time.RFC3339, input.From)
		if err != nil {
			return ActionResult{}, fmt.Errorf("%w: from must be an RFC 3339 timestamp", domain.ErrInvalid)
		}
		from = parsed.UTC()
	}
	to := from.Add(7 * 24 * time.Hour)
	if input.To != "" {
		parsed, err := time.Parse(time.RFC3339, input.To)
		if err != nil {
			return ActionResult{}, fmt.Errorf("%w: to must be an RFC 3339 timestamp", domain.ErrInvalid)
		}
		to = parsed.UTC()
	}
	if !to.After(from) {
		return ActionResult{}, fmt.Errorf("%w: to must be after from", domain.ErrInvalid)
	}
	limit := input.Limit
	if limit == 0 {
		limit = 20
	}
	if limit < 1 || limit > 50 {
		return ActionResult{}, fmt.Errorf("%w: limit must be 1 to 50", domain.ErrInvalid)
	}
	query := url.Values{
		"startDateTime": {from.Format(time.RFC3339)},
		"endDateTime":   {to.Format(time.RFC3339)},
		"$top":          {strconv.Itoa(limit)},
		"$select":       {"id,subject,start,end,organizer,onlineMeeting,webLink"},
	}
	endpoint := apiURL + "/me/calendarView?" + query.Encode()
	var answer struct {
		Value []microsoftGraphEvent `json:"value"`
	}
	if err := sendJSON(ctx, client, http.MethodGet, endpoint, request.Credential, nil, &answer); err != nil {
		return ActionResult{}, err
	}
	summaries := make([]microsoftEventSummary, 0, len(answer.Value))
	for _, event := range answer.Value {
		summaries = append(summaries, microsoftEventSummary{
			ID:               event.ID,
			Subject:          event.Subject,
			Start:            microsoftUTC(event.Start),
			End:              microsoftUTC(event.End),
			Organizer:        microsoftAddress(event.Organizer),
			OnlineMeetingURL: microsoftJoinURL(event.OnlineMeeting),
		})
	}
	// Read-only: this names no single resource the action changed.
	return ActionResult{Result: Answered(summaries)}, nil
}

// --- create_event ---

type microsoftCreateEventInput struct {
	Subject   string   `json:"subject"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Attendees []string `json:"attendees"`
	Body      string   `json:"body"`
}

// microsoftEventCreated is create_event's answer.
type microsoftEventCreated struct {
	ID      string `json:"id"`
	Subject string `json:"subject"`
	Start   string `json:"start"`
	End     string `json:"end"`
	URL     string `json:"url"`
}

func microsoftCreateEvent(ctx context.Context, client *http.Client, apiURL string, request ActionRequest) (ActionResult, error) {
	var input microsoftCreateEventInput
	if err := DecodeInput(request.Input, &input); err != nil {
		return ActionResult{}, err
	}
	if err := Required("subject", input.Subject); err != nil {
		return ActionResult{}, err
	}
	if err := Required("start", input.Start); err != nil {
		return ActionResult{}, err
	}
	if err := Required("end", input.End); err != nil {
		return ActionResult{}, err
	}
	start, err := time.Parse(time.RFC3339, input.Start)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: start must be an RFC 3339 timestamp", domain.ErrInvalid)
	}
	end, err := time.Parse(time.RFC3339, input.End)
	if err != nil {
		return ActionResult{}, fmt.Errorf("%w: end must be an RFC 3339 timestamp", domain.ErrInvalid)
	}
	if !end.After(start) {
		return ActionResult{}, fmt.Errorf("%w: end must be after start", domain.ErrInvalid)
	}
	for _, address := range input.Attendees {
		if !microsoftLooksLikeEmail(address) {
			return ActionResult{}, fmt.Errorf("%w: %q is not an email address", domain.ErrInvalid, address)
		}
	}
	body := map[string]any{
		"subject": input.Subject,
		"start":   microsoftDateTimeUTC(start),
		"end":     microsoftDateTimeUTC(end),
	}
	if len(input.Attendees) > 0 {
		attendees := make([]map[string]any, len(input.Attendees))
		for index, address := range input.Attendees {
			attendees[index] = map[string]any{
				"emailAddress": map[string]string{"address": address},
				"type":         "required",
			}
		}
		body["attendees"] = attendees
	}
	if input.Body != "" {
		body["body"] = map[string]string{"contentType": "Text", "content": input.Body}
	}
	var event microsoftGraphEvent
	if err := sendJSON(ctx, client, http.MethodPost, apiURL+"/me/events", request.Credential, body, &event); err != nil {
		return ActionResult{}, err
	}
	return ActionResult{
		Resource: integrationsv1.Resource{Type: "event", ID: event.ID, Name: event.Subject, URL: event.WebLink},
		Result: Answered(microsoftEventCreated{
			ID: event.ID, Subject: event.Subject,
			Start: microsoftUTC(event.Start), End: microsoftUTC(event.End), URL: event.WebLink,
		}),
	}, nil
}

// --- Graph decoding and shared helpers ---

// microsoftGraphRecipient is the emailAddress wrapper Graph puts around a
// mail or calendar participant.
type microsoftGraphRecipient struct {
	EmailAddress struct {
		Name    string `json:"name"`
		Address string `json:"address"`
	} `json:"emailAddress"`
}

// microsoftGraphMessage is a Graph message, decoded the same way whether it
// came from mailFolders(...)/messages or messages/{id}.
type microsoftGraphMessage struct {
	ID               string                    `json:"id"`
	Subject          string                    `json:"subject"`
	From             microsoftGraphRecipient   `json:"from"`
	ToRecipients     []microsoftGraphRecipient `json:"toRecipients"`
	ReceivedDateTime time.Time                 `json:"receivedDateTime"`
	IsRead           bool                      `json:"isRead"`
	BodyPreview      string                    `json:"bodyPreview"`
	Body             struct {
		ContentType string `json:"contentType"`
		Content     string `json:"content"`
	} `json:"body"`
	WebLink string `json:"webLink"`
}

// microsoftGraphDateTime is Graph's dateTimeTimeZone: a wall-clock time and
// the zone it is in, rather than one offset timestamp. integrations never
// asks Graph for a zone other than the UTC it defaults to.
type microsoftGraphDateTime struct {
	DateTime string `json:"dateTime"`
	TimeZone string `json:"timeZone"`
}

type microsoftGraphOnlineMeeting struct {
	JoinURL string `json:"joinUrl"`
}

// microsoftGraphEvent is a Graph calendar event, decoded the same way whether
// it came from calendarView or events.
type microsoftGraphEvent struct {
	ID            string                       `json:"id"`
	Subject       string                       `json:"subject"`
	Start         microsoftGraphDateTime       `json:"start"`
	End           microsoftGraphDateTime       `json:"end"`
	Organizer     microsoftGraphRecipient      `json:"organizer"`
	OnlineMeeting *microsoftGraphOnlineMeeting `json:"onlineMeeting"`
	WebLink       string                       `json:"webLink"`
}

// microsoftAddress reads a recipient as its email address, falling back to
// its display name when Graph left the address out.
func microsoftAddress(recipient microsoftGraphRecipient) string {
	if recipient.EmailAddress.Address != "" {
		return recipient.EmailAddress.Address
	}
	return recipient.EmailAddress.Name
}

func microsoftAddresses(recipients []microsoftGraphRecipient) []string {
	if len(recipients) == 0 {
		return nil
	}
	addresses := make([]string, len(recipients))
	for index, recipient := range recipients {
		addresses[index] = microsoftAddress(recipient)
	}
	return addresses
}

func microsoftJoinURL(meeting *microsoftGraphOnlineMeeting) string {
	if meeting == nil {
		return ""
	}
	return meeting.JoinURL
}

// microsoftRecipients turns plain addresses into the emailAddress objects
// Graph's sendMail and events take.
func microsoftRecipients(addresses []string) []map[string]any {
	recipients := make([]map[string]any, len(addresses))
	for index, address := range addresses {
		recipients[index] = map[string]any{"emailAddress": map[string]string{"address": address}}
	}
	return recipients
}

// microsoftUTC reads a Graph dateTimeTimeZone as an RFC 3339 UTC timestamp.
// integrations never sends a Prefer: outlook.timezone header, so Graph's
// default zone applies and this only reformats what is already UTC.
func microsoftUTC(value microsoftGraphDateTime) string {
	parsed, err := time.Parse("2006-01-02T15:04:05", value.DateTime)
	if err != nil {
		return value.DateTime
	}
	return parsed.UTC().Format(time.RFC3339)
}

// microsoftDateTimeUTC is the reverse of microsoftUTC: a time as the
// dateTimeTimeZone object Graph takes for an event's start and end.
func microsoftDateTimeUTC(value time.Time) map[string]string {
	return map[string]string{"dateTime": value.UTC().Format("2006-01-02T15:04:05"), "timeZone": "UTC"}
}

var (
	// microsoftHTMLBreak matches the HTML Graph uses to separate lines and
	// paragraphs, replaced with a newline before tags are stripped so words
	// on different lines do not run together.
	microsoftHTMLBreak = regexp.MustCompile(`(?is)<br\s*/?>|</p>|</div>|</tr>`)
	microsoftHTMLTag   = regexp.MustCompile(`<[^>]+>`)
)

// microsoftBodyText reads a Graph body as plain text. Graph answers a
// message's body as HTML unless asked otherwise with a Prefer header, which
// sendJSON has no way to set, so HTML is converted to text here instead.
func microsoftBodyText(contentType, content string) string {
	if !strings.EqualFold(contentType, "html") {
		return strings.TrimSpace(content)
	}
	text := microsoftHTMLBreak.ReplaceAllString(content, "\n")
	text = microsoftHTMLTag.ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	lines := strings.Split(text, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if line = strings.TrimSpace(line); line != "" {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// microsoftLooksLikeEmail is a plausible check for an email address —
// something, an @, and something else — not a full RFC validator.
func microsoftLooksLikeEmail(address string) bool {
	local, host, found := strings.Cut(address, "@")
	return found && local != "" && host != "" && !strings.ContainsAny(address, " \t\n\r")
}
