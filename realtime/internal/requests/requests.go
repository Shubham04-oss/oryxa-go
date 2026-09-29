// Package requests tells the web app's Worker that an agent request between
// two members moved: it was made, decided, answered, or held for its owner's
// review. The Worker notes it in the two people's direct message and in both
// their inboxes, so that a page hears of it over its socket instead of at the
// next of its polls. What a note says is only who asked whose agent, and the
// state the request reached: identities and a lifecycle word, never what was
// asked or answered. The page reads the request itself from Go, as the member.
//
// Every call is signed with the secret the Worker's purges are, over a string
// that starts with a prefix of its own, so a purge's signature is never taken
// for a note's, nor a note's for a purge's:
//
//	POST {worker}/internal/realtime/requests
//	Content-Type: application/json
//	X-Oryxa-Timestamp: <Unix seconds>
//	X-Oryxa-Signature: v1=<lowercase hex HMAC-SHA256(secret, "requests." + timestamp + "." + body)>
//
//	{"organization_id":"…","request_id":"…","revision":2,"asker_user_id":"…","owner_user_id":"…","origin":"agent"|"slack","state":"…","changed_at":"<RFC 3339, UTC>"}
//
// The Worker answers 204 once it has noted the request, or has one at least as
// new. Sending a note again is always safe: the Worker keeps a request's
// highest revision.
package requests

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const (
	// Path is where the Worker takes a request note, under its origin.
	Path = "/internal/realtime/requests"
	// TimestampHeader carries when the request was signed, in Unix seconds.
	// The Worker refuses one signed more than five minutes from its own
	// clock, so every attempt is signed afresh.
	TimestampHeader = "X-Oryxa-Timestamp"
	// SignatureHeader carries Sign's answer.
	SignatureHeader = "X-Oryxa-Signature"
	// SignaturePrefix begins what a note is signed over, ahead of its
	// timestamp. A purge is signed over its timestamp first, which is digits,
	// and the Worker's events to Go over "events.": three directions that no
	// signature of one can pass for another.
	SignaturePrefix = "requests."
	// signatureVersion prefixes a signature, so the scheme can change without
	// a flag day.
	signatureVersion = "v1="
	// Timeout bounds one call. The Worker answers once the two inboxes and
	// the conversation have noted the request, which takes a moment; a call
	// that runs out is sent again, which is safe.
	Timeout = 15 * time.Second
	// userAgent names the caller to the Worker and to Cloudflare in front of
	// it, rather than Go's default.
	userAgent = "oryxa-realtime/1"
)

// State is where a request stands, in the words a note says it: the ledger's
// status, less the ones nobody says.
type State string

const (
	// StateWaiting is a request its owner, or the agent asked, has yet to decide.
	StateWaiting State = "waiting"
	// StateApproved is one that was approved and is being worked on.
	StateApproved State = "approved"
	// StateDeclined is one that was rejected, by its owner, its agent or Oryxa.
	StateDeclined State = "declined"
	// StateAnswered is one whose answer was delivered.
	StateAnswered State = "answered"
	// StateHeld is one whose answer waits for its owner's review.
	StateHeld State = "held"
	// StateFailed is one that ended without an answer.
	StateFailed State = "failed"
	// StateWithheld is one whose answer its owner, or its expiry, kept back.
	StateWithheld State = "withheld"
)

// States lists every state, for a check and a test to walk.
var States = []State{StateWaiting, StateApproved, StateDeclined, StateAnswered, StateHeld, StateFailed, StateWithheld}

func (state State) valid() bool {
	for _, known := range States {
		if state == known {
			return true
		}
	}
	return false
}

// Where a request was made, as a note says it. Any other word a build
// publishes is passed on as it is (ValidOrigin), and the Worker says nothing
// more of it than that a request was made.
const (
	OriginAgent = "agent"
	OriginSlack = "slack"
)

// nameID is what the Worker lets stand in the name of a conversation: 1 to
// 128 of A-Z a-z 0-9 _ -, so a name always splits back into the ids it was
// made of. A note's organization, request and people are all of that shape.
var nameID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// IsID reports whether value may stand in a note as an organization's,
// a request's or a person's id: the shape a conversation's name can hold.
func IsID(value string) bool {
	return nameID.MatchString(value)
}

// origin is the shape of an origin: a short lowercase word.
var origin = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)

// ValidOrigin reports whether value is an origin a note may carry.
func ValidOrigin(value string) bool {
	return origin.MatchString(value)
}

// Note is one movement of a request, sent as JSON with its keys in this order
// and every one present. Revision is the request's own, which goes up by one
// each time it changes and never down: the Worker keeps the highest it has
// seen for a request, so a note sent twice, or late, does nothing. ChangedAt
// is when the request reached this state, in UTC, with as much of a fraction
// of a second as it has.
type Note struct {
	OrganizationID string `json:"organization_id"`
	RequestID      string `json:"request_id"`
	Revision       int64  `json:"revision"`
	// AskerUserID is the member who asked, and OwnerUserID the member whose
	// agent was asked. They are two people.
	AskerUserID string    `json:"asker_user_id"`
	OwnerUserID string    `json:"owner_user_id"`
	Origin      string    `json:"origin"`
	State       State     `json:"state"`
	ChangedAt   time.Time `json:"changed_at"`
}

// check refuses a note the Worker would refuse for good, before it is sent: a
// conversation needs two different people and ids that can name it.
func (note Note) check() error {
	for _, id := range []struct{ what, value string }{
		{"organization_id", note.OrganizationID},
		{"request_id", note.RequestID},
		{"asker_user_id", note.AskerUserID},
		{"owner_user_id", note.OwnerUserID},
	} {
		if !IsID(id.value) {
			return fmt.Errorf("a request note's %s is 1 to 128 of A-Z a-z 0-9 _ -", id.what)
		}
	}
	switch {
	case note.AskerUserID == note.OwnerUserID:
		return errors.New("a request note is between two different people")
	case note.Revision < 1:
		return errors.New("a request note names its request's revision, from 1")
	case !ValidOrigin(note.Origin):
		return fmt.Errorf("a request note's origin is a short lowercase word, not %q", note.Origin)
	case !note.State.valid():
		return fmt.Errorf("a request note's state is one of %v, not %q", States, note.State)
	case note.ChangedAt.IsZero():
		return errors.New("a request note says when the request reached its state")
	}
	return nil
}

// Sign is the X-Oryxa-Signature value for body sent at timestamp: v1= and the
// lowercase hex HMAC-SHA256, keyed with the secret's bytes as written — not
// decoded from hex or base64 — of SignaturePrefix, the timestamp in decimal,
// a dot, and the body exactly as sent.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(SignaturePrefix))
	mac.Write(strconv.AppendInt(nil, timestamp, 10))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signatureVersion + hex.EncodeToString(mac.Sum(nil))
}

// ErrPermanent marks a note the Worker refused in a way that sending it again
// cannot change: a 400, the body malformed, or a 413, the body too large, each
// carrying the Worker's own refusal, {"error":{"code":…}}. Its event is
// dropped, with the Worker's answer logged, rather than retried into the
// dead-letter queue. No other answer is taken for one: a 403, 404 or 405 comes
// from a URL that reaches something other than the Worker's route, a Worker
// without it, or Cloudflare in front of it, which an operator can put right.
var ErrPermanent = errors.New("the Worker refused the request note for good")

// Config is how to reach the Worker.
type Config struct {
	// WorkerURL is the origin the Worker serves, such as
	// https://app.oryxa.in, with no path: Path is appended to it, and the
	// Worker matches Path exactly.
	WorkerURL string
	// Secret signs every request. The Worker holds the same one, the purge's.
	Secret string
	// HTTP makes the calls. Nil is a client traced like every outbound call
	// that does not follow redirects. Whichever makes it, each call is
	// bounded by Timeout.
	HTTP *http.Client
	// Now is the clock requests are signed by. Nil is time.Now.
	Now func() time.Time
}

// Client notes requests through the Worker. It is safe for concurrent use.
type Client struct {
	endpoint string
	secret   string
	http     *http.Client
	now      func() time.Time
}

// New returns a client for the Worker config names.
func New(config Config) (*Client, error) {
	base := strings.TrimRight(strings.TrimSpace(config.WorkerURL), "/")
	parsed, err := url.Parse(base)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return nil, errors.New("requests: the Worker's URL must be an origin, an absolute http or https URL with no path or query")
	}
	if config.Secret == "" {
		return nil, errors.New("requests: a secret to sign with is required")
	}
	client := &Client{endpoint: base + Path, secret: config.Secret, http: config.HTTP, now: config.Now}
	if client.http == nil {
		client.http = &http.Client{
			Transport: observability.Transport(nil),
			// A redirect is not the Worker answering. Following one would
			// send the note somewhere the contract never named, or send it
			// again as a GET, so it is answered like any other status that is
			// not 204.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}
	if client.now == nil {
		client.now = time.Now
	}
	return client, nil
}

// Note asks the Worker to note what note says, and returns once the Worker
// says it has. An error wrapping ErrPermanent fails the same way however
// often it is sent. Any other error may not: the Worker was unreachable, slow
// or failing, something other than its route answered, or it refused the
// signature, which is the secret or the clock being wrong, not the note.
func (client *Client) Note(ctx context.Context, note Note) error {
	// Nothing the Worker would refuse for good is sent: each is refused here.
	if err := note.check(); err != nil {
		return err
	}
	note.ChangedAt = note.ChangedAt.UTC()
	body, err := json.Marshal(note)
	if err != nil {
		return fmt.Errorf("encode the request note: %w", err)
	}
	timestamp := client.now().Unix()
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build the request note: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("User-Agent", userAgent)
	httpRequest.Header.Set(TimestampHeader, strconv.FormatInt(timestamp, 10))
	httpRequest.Header.Set(SignatureHeader, Sign(client.secret, timestamp, body))

	response, err := client.http.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("reach the Worker: %w", err)
	}
	defer func() {
		// Read what is left, within reason, so the connection can be reused.
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
		response.Body.Close()
	}()
	if response.StatusCode == http.StatusNoContent {
		return nil
	}
	answer, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
	said := excerpt(answer)
	switch status := response.StatusCode; {
	case (status == http.StatusBadRequest || status == http.StatusRequestEntityTooLarge) && refusedByTheWorker(answer):
		return fmt.Errorf("%w: it answered %s%s", ErrPermanent, response.Status, said)
	case status == http.StatusUnauthorized:
		// Not about this note but about the secret or the clock, which an
		// operator can put right. Retried, it reaches the dead-letter queue,
		// whose alarm says so, and can be redriven once they have; dropped, it
		// would be lost, and so would every note until then.
		return fmt.Errorf("the Worker refused the signature (%s): REALTIME_PURGE_SECRET is not the Worker's, "+
			"or this host's clock is further from the Worker's than it allows%s", response.Status, said)
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return fmt.Errorf("the Worker answered %s; it may take the note later%s", response.Status, said)
	default:
		// A 5xx, any other 4xx, or anything else the contract does not name,
		// which is no sign the note was taken. A 403, 404 or 405 is a
		// misrouted URL, a Worker without the route, or Cloudflare refusing
		// the call: retried, it reaches the dead-letter queue, whose alarm
		// says so, and can be redriven once that is put right.
		return fmt.Errorf("the Worker answered %s, not 204%s", response.Status, said)
	}
}

// refusedByTheWorker reports whether answer is the Worker's own refusal,
// {"error":{"code":"…","message":"…"}}, rather than a page something else
// answered with.
func refusedByTheWorker(answer []byte) bool {
	var refusal struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal(answer, &refusal) == nil && refusal.Error.Code != ""
}

// excerpt is the start of what the Worker said, on one line, for an error: ""
// when it said nothing, else ": " and the text.
func excerpt(raw []byte) string {
	const most = 200
	text := strings.Join(strings.Fields(strings.ToValidUTF8(string(raw), "�")), " ")
	if text == "" {
		return ""
	}
	if runes := []rune(text); len(runes) > most {
		text = string(runes[:most]) + "…"
	}
	return ": " + text
}
