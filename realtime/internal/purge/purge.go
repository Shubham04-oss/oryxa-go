// Package purge asks the web app's Worker to purge realtime conversations:
// the direct messages and teamspace side conversations it keeps only in its
// Durable Objects. One call purges what a member wrote in an organization, or
// everything the organization kept, up to when the membership or the
// organization ended.
//
// Every call is signed with a secret the Worker holds too, so nothing but
// this service can make the Worker delete anything:
//
//	POST {worker}/internal/realtime/purge
//	Content-Type: application/json
//	X-Oryxa-Timestamp: <Unix seconds>
//	X-Oryxa-Signature: v1=<lowercase hex HMAC-SHA256(secret, timestamp + "." + body)>
//
//	{"event_id":"…","organization_id":"…","scope":"member"|"organization","user_id":"…","removed_at":"<RFC 3339, UTC>"}
//
// The Worker answers 204 once the purge is done. The purge is idempotent, so
// sending one again is always safe.
package purge

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
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const (
	// Path is where the Worker takes a purge, under its origin.
	Path = "/internal/realtime/purge"
	// TimestampHeader carries when the request was signed, in Unix seconds.
	// The Worker refuses a request signed too long ago, so every attempt is
	// signed afresh rather than when its event happened.
	TimestampHeader = "X-Oryxa-Timestamp"
	// SignatureHeader carries Sign's answer.
	SignatureHeader = "X-Oryxa-Signature"
	// signatureVersion prefixes a signature, so the scheme can change without
	// a flag day.
	signatureVersion = "v1="
	// Timeout bounds one call. An organization's purge walks every
	// conversation the organization had, so it gets longer than a lookup
	// would; a call that runs out is sent again, which is safe.
	Timeout = 30 * time.Second
	// userAgent names the caller to the Worker and to Cloudflare in front of
	// it, rather than Go's default.
	userAgent = "oryxa-realtime/1"
)

// Scope says what a purge takes: one member's conversations in the
// organization, or all of the organization's. It is always sent, so an empty
// user_id can never be read as the whole organization by accident.
type Scope string

const (
	// ScopeMember purges what UserID kept in the organization.
	ScopeMember Scope = "member"
	// ScopeOrganization purges everything the organization kept. UserID is
	// empty.
	ScopeOrganization Scope = "organization"
)

// Request is one purge, sent as JSON with its keys in this order and every
// one present, UserID included when it is empty. EventID is the auth event
// that asked for the purge, by which the Worker can tell a repeat. RemovedAt
// is when the membership or the organization ended: the Worker deletes only
// what was written then or before, so a purge that runs late, after the
// member was invited back, leaves what they wrote since. It is sent in UTC,
// in RFC 3339 with as much of a fraction of a second as it has, and none when
// it has none.
type Request struct {
	EventID        string    `json:"event_id"`
	OrganizationID string    `json:"organization_id"`
	Scope          Scope     `json:"scope"`
	UserID         string    `json:"user_id"`
	RemovedAt      time.Time `json:"removed_at"`
}

// Sign is the X-Oryxa-Signature value for body sent at timestamp: v1= and the
// lowercase hex HMAC-SHA256, keyed with the secret's bytes as written — not
// decoded from hex or base64 — of the timestamp in decimal, a dot, and the
// body exactly as sent.
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(strconv.AppendInt(nil, timestamp, 10))
	mac.Write([]byte{'.'})
	mac.Write(body)
	return signatureVersion + hex.EncodeToString(mac.Sum(nil))
}

// ErrPermanent marks a purge the Worker refused in a way that sending it again
// cannot change: any 4xx but 401, 408 and 429. Its event is dropped, with the
// Worker's answer logged, rather than retried into the dead-letter queue.
var ErrPermanent = errors.New("the Worker refused the purge for good")

// Config is how to reach the Worker.
type Config struct {
	// WorkerURL is the origin the Worker serves, such as
	// https://app.oryxa.in; Path is appended to it.
	WorkerURL string
	// Secret signs every request. The Worker holds the same one.
	Secret string
	// HTTP makes the calls. Nil is a client bounded by Timeout, traced like
	// every outbound call, that does not follow redirects.
	HTTP *http.Client
	// Now is the clock requests are signed by. Nil is time.Now.
	Now func() time.Time
}

// Client purges through the Worker. It is safe for concurrent use.
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
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return nil, errors.New("purge: the Worker's URL must be an absolute http or https URL")
	}
	if config.Secret == "" {
		return nil, errors.New("purge: a secret to sign with is required")
	}
	client := &Client{endpoint: base + Path, secret: config.Secret, http: config.HTTP, now: config.Now}
	if client.http == nil {
		client.http = &http.Client{
			Timeout:   Timeout,
			Transport: observability.Transport(nil),
			// A redirect is not the Worker answering. Following one would
			// send the purge somewhere the contract never named, or send it
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

// Purge asks the Worker for the purge request names, and returns once the
// Worker says it is done. An error wrapping ErrPermanent fails the same way
// however often it is sent. Any other error may not: the Worker was
// unreachable, slow or failing, or it refused the signature, which is the
// secret or the clock being wrong, not the purge.
func (client *Client) Purge(ctx context.Context, request Request) error {
	// Nothing here builds a purge the Worker could read more widely than it
	// was meant, and nothing ever should: each is refused before it is sent.
	switch {
	case request.EventID == "" || request.OrganizationID == "":
		return errors.New("a purge names the event that asked for it and its organization")
	case request.Scope == ScopeMember && request.UserID == "":
		return errors.New("a member's purge names the member")
	case request.Scope == ScopeOrganization && request.UserID != "":
		return errors.New("an organization's purge names no member")
	case request.Scope != ScopeMember && request.Scope != ScopeOrganization:
		return fmt.Errorf("a purge's scope is member or organization, not %q", request.Scope)
	case request.RemovedAt.IsZero():
		return errors.New("a purge says when the membership ended")
	}
	request.RemovedAt = request.RemovedAt.UTC()
	body, err := json.Marshal(request)
	if err != nil {
		return fmt.Errorf("encode the purge: %w", err)
	}
	timestamp := client.now().Unix()
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build the purge request: %w", err)
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
	said := excerpt(response.Body)
	switch status := response.StatusCode; {
	case status == http.StatusUnauthorized:
		// Not about this purge but about the secret or the clock, which an
		// operator can put right. Retried, it reaches the dead-letter queue,
		// whose alarm says so, and can be redriven once they have; dropped, it
		// would be lost, and so would every purge until then.
		return fmt.Errorf("the Worker refused the signature (%s): REALTIME_PURGE_SECRET is not the Worker's, "+
			"or this host's clock is further from the Worker's than it allows%s", response.Status, said)
	case status == http.StatusRequestTimeout, status == http.StatusTooManyRequests:
		return fmt.Errorf("the Worker answered %s; it may take the purge later%s", response.Status, said)
	case status >= 400 && status < 500:
		return fmt.Errorf("%w: it answered %s%s", ErrPermanent, response.Status, said)
	default:
		// A 5xx, or anything else the contract does not name, which is no
		// sign the purge happened.
		return fmt.Errorf("the Worker answered %s, not 204%s", response.Status, said)
	}
}

// excerpt is the start of what the Worker said, on one line, for an error: ""
// when it said nothing, else ": " and the text.
func excerpt(body io.Reader) string {
	const most = 200
	raw, _ := io.ReadAll(io.LimitReader(body, 1024))
	text := strings.Join(strings.Fields(strings.ToValidUTF8(string(raw), "�")), " ")
	if text == "" {
		return ""
	}
	if runes := []rune(text); len(runes) > most {
		text = string(runes[:most]) + "…"
	}
	return ": " + text
}
