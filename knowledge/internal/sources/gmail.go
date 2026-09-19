package sources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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

// gmailAPI is where the Gmail API lives.
const gmailAPI = "https://gmail.googleapis.com/gmail/v1"

// gmailMaxPage is what this extractor asks messages.list and history.list for
// at once.
const gmailMaxPage = 100

// gmailFullPassLookback bounds a source's very first full pass to recent
// mail rather than a member's entire history. Answering from the newest
// ninety days is useful the moment a connection is made; reading years of
// mail before anything is searchable would make a heavy mailbox's first
// connection look hung for no benefit an incremental pass does not give back
// anyway, since that keeps up with everything from here on regardless of
// age. It is not a ceiling on what is ever indexed: a periodic full sweep,
// once the source already has a first pass behind it, reads the whole label
// instead — see full, and the note on why a bounded re-sweep would be wrong.
const gmailFullPassLookback = 90 * 24 * time.Hour

// errGmailHistoryExpired means startHistoryId named in a history.list call is
// older than Gmail's own retention for history (documented as about a week),
// so this pass cannot resume incrementally from it.
var errGmailHistoryExpired = errors.New("gmail's history no longer goes back that far")

// Gmail reads one label's messages. There is no generated client here any
// more than for the other Composio-proxied sources — this speaks the REST
// API directly, hand-rolled on the package's shared client.
//
// Attachments are left out entirely: a message's text is read from its
// text/plain part, or its text/html part stripped when there is no plain
// one, and nothing here follows an attachmentId to fetch what it names.
type Gmail struct {
	client *Client
	api    string
}

func NewGmail(client *Client) *Gmail { return &Gmail{client: client, api: gmailAPI} }

func (source *Gmail) Provider() string { return "gmail" }

// Target accepts {"label": "<id>"} to index one label's messages, or no
// target at all to index INBOX.
func (source *Gmail) Target(target map[string]string) error {
	if label, named := target["label"]; named && strings.TrimSpace(label) == "" {
		return MissingTarget("a label, such as INBOX or SENT")
	}
	return nil
}

func gmailTargetLabel(target map[string]string) string {
	if label := strings.TrimSpace(target["label"]); label != "" {
		return label
	}
	return "INBOX"
}

// Fetch reads one page of a label: messages.list for a full pass, the
// history feed for an incremental one.
func (source *Gmail) Fetch(ctx context.Context, request Request) (Page, error) {
	label := gmailTargetLabel(request.Source.Target)
	if !request.Full {
		return source.incremental(ctx, request, label)
	}
	return source.full(ctx, request, label)
}

// full walks messages.list. A source's very first pass — FullSweepAt unset —
// is bounded to gmailFullPassLookback, so a heavy mailbox is searchable
// quickly; a periodic re-sweep of a source already read once is not bounded
// at all, because a message that has simply aged past ninety days since the
// last sweep must not look like it was deleted — RetractUnseen would retract
// anything this pass does not see, and a message still sitting in the
// mailbox is not gone.
func (source *Gmail) full(ctx context.Context, request Request, label string) (Page, error) {
	query := url.Values{"labelIds": {label}, "maxResults": {strconv.Itoa(Limit(request.Limit, gmailMaxPage))}}
	if request.Source.FullSweepAt == nil {
		query.Set("q", fmt.Sprintf("newer_than:%dd", int(gmailFullPassLookback/(24*time.Hour))))
	}
	if cursor := request.Cursor.Value; cursor != "" {
		query.Set("pageToken", cursor)
	}
	list, err := source.listMessages(ctx, request.Credential, query)
	if err != nil {
		return Page{}, err
	}
	page, err := source.readAll(ctx, request.Credential, list.Messages)
	if err != nil {
		return Page{}, err
	}
	if list.NextPageToken != "" {
		page.Cursor = list.NextPageToken
		return page, nil
	}
	historyID, err := source.currentHistoryID(ctx, request.Credential)
	if err != nil {
		return Page{}, err
	}
	page.Token = historyID
	return page, nil
}

// gmailIncrementalCursor is Fetch's own cursor for an incremental pass:
// startHistoryID is fixed for the whole sweep — it says how far back this
// pass reads from — while page is Gmail's own pagination token within it,
// which moves on every call. JSON encoded so both survive in the one string
// column a cursor is stored in.
type gmailIncrementalCursor struct {
	StartHistoryID string `json:"start"`
	Page           string `json:"page,omitempty"`
}

// incremental reads the history feed from the watermark a previous pass left.
//
// messageAdded, messageDeleted and labelRemoved are the three history types
// asked for: a message added to the label is read, one deleted outright is
// retracted, and one that merely left this label — archived out of INBOX,
// say — is retracted from this source the same way, since this source is
// exactly that label and a message no longer in it is a message this source
// no longer holds.
func (source *Gmail) incremental(ctx context.Context, request Request, label string) (Page, error) {
	cursor := gmailIncrementalCursor{StartHistoryID: request.Cursor.Token}
	if request.Cursor.Value != "" {
		if err := json.Unmarshal([]byte(request.Cursor.Value), &cursor); err != nil || cursor.StartHistoryID == "" {
			cursor = gmailIncrementalCursor{StartHistoryID: request.Cursor.Token}
		}
	}
	if cursor.StartHistoryID == "" {
		historyID, err := source.currentHistoryID(ctx, request.Credential)
		if err != nil {
			return Page{}, err
		}
		return Page{Token: historyID}, nil
	}

	list, err := source.history(ctx, request.Credential, cursor.StartHistoryID, cursor.Page, label, Limit(request.Limit, gmailMaxPage))
	if err != nil {
		if errors.Is(err, errGmailHistoryExpired) {
			// Gmail keeps history for about a week; a sweep that fell behind
			// longer than that cannot resume incrementally from it. The
			// periodic full sweep is what actually recovers a source in this
			// state, so this reads as drained with a fresh watermark rather
			// than a failure a member would see as their source being broken.
			historyID, herr := source.currentHistoryID(ctx, request.Credential)
			if herr != nil {
				return Page{}, herr
			}
			return Page{Token: historyID}, nil
		}
		return Page{}, err
	}

	page := Page{}
	var toRead []string
	for _, record := range list.History {
		for _, added := range record.MessagesAdded {
			if added.Message.ID != "" {
				toRead = append(toRead, added.Message.ID)
			}
		}
		for _, deleted := range record.MessagesDeleted {
			if deleted.Message.ID != "" {
				page.Removed = append(page.Removed, deleted.Message.ID)
			}
		}
		for _, unlabeled := range record.LabelsRemoved {
			if unlabeled.Message.ID != "" && containsString(unlabeled.LabelIDs, label) {
				page.Removed = append(page.Removed, unlabeled.Message.ID)
			}
		}
	}
	read, err := source.readAll(ctx, request.Credential, toReferences(toRead))
	if err != nil {
		return Page{}, err
	}
	page.Items = read.Items

	if list.NextPageToken != "" {
		page.Cursor = mustEncodeJSON(gmailIncrementalCursor{StartHistoryID: cursor.StartHistoryID, Page: list.NextPageToken})
		return page, nil
	}
	nextToken := list.HistoryID
	if nextToken == "" {
		nextToken = cursor.StartHistoryID
	}
	page.Token = nextToken
	return page, nil
}

// gmailMessageRef is the one field this package needs out of a list answer's
// entries: the id to read the message with.
type gmailMessageRef struct {
	ID string `json:"id"`
}

func toReferences(ids []string) []gmailMessageRef {
	refs := make([]gmailMessageRef, len(ids))
	for i, id := range ids {
		refs[i] = gmailMessageRef{ID: id}
	}
	return refs
}

// readAll reads each referenced message in full, skipping one that no longer
// answers: gone between being listed and being read, which the history feed
// or the next full sweep accounts for either way.
func (source *Gmail) readAll(ctx context.Context, credential domain.Credential, refs []gmailMessageRef) (Page, error) {
	page := Page{Items: make([]Item, 0, len(refs))}
	for _, ref := range refs {
		if ref.ID == "" {
			continue
		}
		item, ok, err := source.readMessage(ctx, credential, ref.ID)
		if err != nil {
			return Page{}, err
		}
		if ok {
			page.Items = append(page.Items, item)
		}
	}
	return page, nil
}

// readMessage reads one message in full and turns it into an item.
func (source *Gmail) readMessage(ctx context.Context, credential domain.Credential, id string) (Item, bool, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet,
		source.api+"/users/me/messages/"+url.PathEscape(id)+"?format=full", nil)
	if err != nil {
		return Item{}, false, fmt.Errorf("build the Gmail message request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		if skippable(err) {
			return Item{}, false, nil
		}
		return Item{}, false, err
	}
	var message gmailMessage
	if err := json.Unmarshal(response.Body, &message); err != nil {
		return Item{}, false, fmt.Errorf("%w: Gmail's message could not be read", domain.ErrProviderUnavailable)
	}
	return gmailItem(message), true, nil
}

// gmailPart is one MIME part of a message, which nests: a multipart message's
// text lives a level or two below payload itself.
type gmailPart struct {
	MimeType string `json:"mimeType"`
	Headers  []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"headers"`
	Body struct {
		Data string `json:"data"`
	} `json:"body"`
	Parts []gmailPart `json:"parts"`
}

// gmailMessage is the slice of Gmail's message resource this reads, asked for
// with format=full.
type gmailMessage struct {
	ID           string    `json:"id"`
	ThreadID     string    `json:"threadId"`
	InternalDate string    `json:"internalDate"`
	Payload      gmailPart `json:"payload"`
}

// gmailHeader reads one header off a message's top-level part, where Gmail
// always places Subject, From, To and Date regardless of how the body itself
// is nested.
func gmailHeader(part gmailPart, name string) string {
	for _, header := range part.Headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

// gmailFindPart walks a message's MIME tree for the first part of the given
// type that actually carries inline data — never an attachment, which holds
// an attachmentId instead of data and is left alone entirely.
func gmailFindPart(part gmailPart, mimeType string) (string, bool) {
	if part.MimeType == mimeType && part.Body.Data != "" {
		return part.Body.Data, true
	}
	for _, child := range part.Parts {
		if data, ok := gmailFindPart(child, mimeType); ok {
			return data, true
		}
	}
	return "", false
}

// gmailBody reads a message's text: its text/plain part if it has one, else
// its text/html part with the markup stripped out through the same transform
// an HTML page from any other source reads through.
//
// A part that cannot be read is passed over like a missing one. It is one
// message's problem, and failing the pass over it would stop the whole
// mailbox being read, on every retry, for as long as the message is there.
func gmailBody(payload gmailPart) string {
	if data, ok := gmailFindPart(payload, transform.MIMEPlain); ok {
		if text, ok := gmailDecode(data); ok {
			return text
		}
	}
	if data, ok := gmailFindPart(payload, transform.MIMEHTML); ok {
		if html, ok := gmailDecode(data); ok {
			if stripped, err := transform.Normalize([]byte(html), transform.MIMEHTML); err == nil {
				return stripped.Body
			}
		}
	}
	// No readable part at all — an attachment-only message, say. Still worth
	// indexing by its subject and metadata alone rather than skipped outright.
	return ""
}

// gmailDecode reads one part's body: base64url, which Gmail sends padded or
// not from one message to the next, so padding is dropped rather than
// expected either way.
func gmailDecode(data string) (string, bool) {
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(data, "="))
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

func gmailItem(message gmailMessage) Item {
	text := gmailBody(message.Payload)
	metadata := map[string]string{
		"from":      gmailHeader(message.Payload, "From"),
		"to":        gmailHeader(message.Payload, "To"),
		"date":      gmailHeader(message.Payload, "Date"),
		"thread_id": message.ThreadID,
	}
	var updated *time.Time
	if millis, err := strconv.ParseInt(message.InternalDate, 10, 64); err == nil {
		parsed := time.UnixMilli(millis).UTC()
		updated = &parsed
	}
	return Item{
		ExternalID: message.ID,
		Kind:       knowledgev1.KindMessage,
		Title:      gmailHeader(message.Payload, "Subject"),
		// "all" resolves regardless of which label the message currently
		// sits under, unlike a label-specific link that stops working once a
		// message is archived or moved.
		URL:       "https://mail.google.com/mail/u/0/#all/" + message.ID,
		MIME:      transform.MIMEPlain,
		Content:   []byte(text),
		Metadata:  metadata,
		UpdatedAt: updated,
	}
}

// gmailMessageList is what messages.list answers.
type gmailMessageList struct {
	Messages      []gmailMessageRef `json:"messages"`
	NextPageToken string            `json:"nextPageToken"`
}

func (source *Gmail) listMessages(ctx context.Context, credential domain.Credential, query url.Values) (gmailMessageList, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/users/me/messages?"+query.Encode(), nil)
	if err != nil {
		return gmailMessageList{}, fmt.Errorf("build the Gmail messages.list request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return gmailMessageList{}, err
	}
	var list gmailMessageList
	if err := json.Unmarshal(response.Body, &list); err != nil {
		return gmailMessageList{}, fmt.Errorf("%w: Gmail's message list could not be read", domain.ErrProviderUnavailable)
	}
	return list, nil
}

// gmailHistoryList is what history.list answers: a page of history records
// behind Gmail's own pageToken, and the mailbox's current historyId, which is
// present whether or not there was anything to report.
type gmailHistoryList struct {
	History []struct {
		MessagesAdded []struct {
			Message gmailMessageRef `json:"message"`
		} `json:"messagesAdded"`
		MessagesDeleted []struct {
			Message gmailMessageRef `json:"message"`
		} `json:"messagesDeleted"`
		LabelsRemoved []struct {
			Message  gmailMessageRef `json:"message"`
			LabelIDs []string        `json:"labelIds"`
		} `json:"labelsRemoved"`
	} `json:"history"`
	NextPageToken string `json:"nextPageToken"`
	HistoryID     string `json:"historyId"`
}

func (source *Gmail) history(ctx context.Context, credential domain.Credential, startHistoryID, pageToken, label string, size int) (gmailHistoryList, error) {
	query := url.Values{}
	query.Set("startHistoryId", startHistoryID)
	query.Set("maxResults", strconv.Itoa(size))
	if label != "" {
		query.Set("labelId", label)
	}
	if pageToken != "" {
		query.Set("pageToken", pageToken)
	}
	for _, historyType := range []string{"messageAdded", "messageDeleted", "labelRemoved"} {
		query.Add("historyTypes", historyType)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/users/me/history?"+query.Encode(), nil)
	if err != nil {
		return gmailHistoryList{}, fmt.Errorf("build the Gmail history.list request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			// Gmail answers 404 for a startHistoryId it no longer has, which
			// is the one case this package reads that status as something
			// other than a missing resource.
			return gmailHistoryList{}, errGmailHistoryExpired
		}
		return gmailHistoryList{}, err
	}
	var list gmailHistoryList
	if err := json.Unmarshal(response.Body, &list); err != nil {
		return gmailHistoryList{}, fmt.Errorf("%w: Gmail's history could not be read", domain.ErrProviderUnavailable)
	}
	return list, nil
}

// currentHistoryID asks Gmail where "now" is, the way Drive's
// changes.startPageToken does, so the pass that just drained knows where the
// next incremental one should resume from.
func (source *Gmail) currentHistoryID(ctx context.Context, credential domain.Credential) (string, error) {
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, source.api+"/users/me/profile", nil)
	if err != nil {
		return "", fmt.Errorf("build the Gmail profile request: %w", err)
	}
	response, err := source.client.Do(ctx, httpRequest, credential)
	if err != nil {
		return "", err
	}
	var profile struct {
		HistoryID string `json:"historyId"`
	}
	if err := json.Unmarshal(response.Body, &profile); err != nil {
		return "", fmt.Errorf("%w: Gmail's profile could not be read", domain.ErrProviderUnavailable)
	}
	return profile.HistoryID, nil
}
