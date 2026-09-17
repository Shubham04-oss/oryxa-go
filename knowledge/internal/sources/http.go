package sources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// Bulk reading is not one request. A toolkit that rate-limits, wobbles or
// asks for a pause will do so partway through a source, and Composio's own
// proxy makes a single attempt with no retry of its own: these are the
// defaults for reading for minutes rather than milliseconds.
const (
	// requestTimeout bounds one call. It is longer than the ten seconds a
	// single provider action gets, because a page of a hundred documents is a
	// bigger answer than one issue.
	requestTimeout = 60 * time.Second
	// maxAttempts counts the first try. Past it a failure is the caller's.
	maxAttempts = 4
	// maxResponseBytes caps what one call may hand back.
	maxResponseBytes = 8 << 20
	// maxBackoff caps the wait between attempts, including one a toolkit
	// asked for: a Retry-After of an hour means stop, not sleep for an hour.
	maxBackoff = 60 * time.Second
	// pace is the least time between two calls to the same host, so a
	// backfill walks a toolkit rather than sprinting at it.
	pace = 100 * time.Millisecond
)

// Response is what a toolkit answered, already read and bounded.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Doer is the HTTP surface this package needs, so a test answers without a
// network. *composioDoer, built by NewClient, satisfies it; so does any fake
// a test supplies to newClient directly.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client is the HTTP behaviour bulk extraction needs: it paces calls to a
// host, waits when a toolkit says to, retries what is worth retrying, and
// refuses to read an unbounded body. It is safe for concurrent use, and one
// is shared by every extractor so pacing is per host across all of them.
//
// Every request runs through Composio, which holds the credential: nothing
// here ever places a token itself. base is what actually sends a request
// once this layer has decided to; NewClient builds one that proxies through
// Composio, and a test builds one directly on a fake.
type Client struct {
	base  Doer
	clock func() time.Time
	sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	nextAt map[string]time.Time
}

// NewClient builds the client extractors share, reading every request
// through Composio's proxy rather than sending it directly.
func NewClient(proxy Proxier) *Client {
	return newClient(&composioDoer{proxy: proxy})
}

func newClient(base Doer) *Client {
	return &Client{base: base, clock: time.Now, sleep: sleepContext, nextAt: map[string]time.Time{}}
}

// For answers an *http.Client that carries this client's pacing, retries and
// credential, for a caller that wants the *http.Client shape rather than Do.
func (client *Client) For(credential domain.Credential) *http.Client {
	return &http.Client{Transport: &transport{client: client, credential: credential}}
}

// Do sends one request and reads the answer, mapping the toolkit's status to
// the reason a caller acts on.
func (client *Client) Do(ctx context.Context, request *http.Request, credential domain.Credential) (*Response, error) {
	response, err := client.For(credential).Do(request.WithContext(ctx))
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if recognizedFailure(err) {
			// The Doer already worked out what this means — Composio unable
			// to reach the connected account, say — so it is passed on as
			// is rather than hidden behind a generic "did not answer".
			return nil, err
		}
		return nil, fmt.Errorf("%w: the provider did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w: the provider's answer could not be read", domain.ErrProviderUnavailable)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("the provider answered with more than %d bytes: %w", maxResponseBytes, domain.ErrInvalid)
	}
	answer := &Response{Status: response.StatusCode, Header: response.Header, Body: body}
	if answer.Status >= 200 && answer.Status < 300 {
		return answer, nil
	}
	return nil, StatusError(answer.Status, answer.Header)
}

// recognizedFailure says whether err already names the reason a caller
// should act on, so Do's blanket "the provider did not answer" would only
// hide it. A network failure never does; a Doer that mapped the failure
// itself — Composio answering with a file rather than text, say — does.
func recognizedFailure(err error) bool {
	for _, known := range []error{
		domain.ErrNeedsReconnect, domain.ErrForbidden, domain.ErrNotFound, domain.ErrScopeRequired,
		domain.ErrRateLimited, domain.ErrInvalid, domain.ErrUnsupportedType,
	} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

// transport is the retrying, pacing round tripper both paths share.
type transport struct {
	client     *Client
	credential domain.Credential
}

func (t *transport) RoundTrip(request *http.Request) (*http.Response, error) {
	ctx := request.Context()
	var response *http.Response
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			delay, ok := retryDelay(response, t.client.clock(), attempt)
			if !ok {
				// The toolkit asked for longer than this run should hold.
				// Hand back what it said so the caller can stop.
				return response, nil
			}
			drain(response)
			response = nil
			if err := t.client.sleep(ctx, delay); err != nil {
				return nil, err
			}
		}
		if err := t.client.wait(ctx, request.URL.Host); err != nil {
			return nil, err
		}

		attemptRequest, err := rewind(request)
		if err != nil {
			return nil, err
		}
		// The credential travels to the base Doer through the request's
		// context rather than a header: Composio places it, on the account
		// this source reads as, never this process.
		attemptRequest = attemptRequest.WithContext(withCredential(attemptRequest.Context(), t.credential))

		response, lastErr = t.client.base.Do(attemptRequest)
		if lastErr != nil {
			// A connection that failed is a wobble like any other, unless the
			// run itself is over or the request cannot be sent again.
			if ctx.Err() != nil || !replayable(request) {
				return nil, lastErr
			}
			response = nil
			continue
		}
		if !worthRetrying(response) || !replayable(request) {
			return response, nil
		}
	}
	if response == nil && lastErr != nil {
		return nil, lastErr
	}
	return response, nil
}

// replayable says a request can be sent again. One with a body needs a way to
// rewind it, or the second attempt would carry nothing.
func replayable(request *http.Request) bool {
	return request.Body == nil || request.GetBody != nil
}

// wait holds a caller until this host may be called again, so concurrent
// extractions against one toolkit do not arrive together.
func (client *Client) wait(ctx context.Context, host string) error {
	client.mu.Lock()
	now := client.clock()
	earliest := client.nextAt[host]
	if earliest.Before(now) {
		earliest = now
	}
	client.nextAt[host] = earliest.Add(pace)
	client.mu.Unlock()
	return client.sleep(ctx, earliest.Sub(now))
}

// rewind copies a request so an attempt never consumes the caller's body.
func rewind(request *http.Request) (*http.Request, error) {
	attempt := request.Clone(request.Context())
	if request.GetBody == nil {
		return attempt, nil
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, fmt.Errorf("rewind request body: %w", err)
	}
	attempt.Body = body
	return attempt, nil
}

// drain empties an answer being thrown away, so its connection is reused.
func drain(response *http.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	_ = response.Body.Close()
}

// worthRetrying says whether sending the same request again could answer
// differently. A refused credential or a missing document will not.
func worthRetrying(response *http.Response) bool {
	switch {
	case response.StatusCode == http.StatusTooManyRequests:
		return true
	case response.StatusCode >= 500:
		return true
	case response.StatusCode == http.StatusForbidden:
		// A toolkit out of budget says so with a 403 and an exhausted
		// remaining count, rather than a 429.
		return response.Header.Get("X-RateLimit-Remaining") == "0"
	default:
		return false
	}
}

// StatusError turns a toolkit's status into the reason a caller acts on. A
// toolkit's own error body never travels with it.
func StatusError(status int, header http.Header) error {
	switch {
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case status == http.StatusUnauthorized:
		return domain.ErrNeedsReconnect
	case status == http.StatusForbidden:
		if header.Get("X-RateLimit-Remaining") == "0" {
			return domain.ErrRateLimited
		}
		return domain.ErrForbidden
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status >= 500:
		return domain.ErrProviderUnavailable
	default:
		return fmt.Errorf("the provider answered %d: %w", status, domain.ErrInvalid)
	}
}

// retryDelay is how long to wait before the next attempt: what the toolkit
// asked for when it said, and a doubling backoff with jitter when it did not.
// It answers false when the wait is longer than this run should hold, which is
// the signal to give the source back and come to it later.
func retryDelay(response *http.Response, now time.Time, attempt int) (time.Duration, bool) {
	delay := time.Duration(1<<uint(attempt)) * time.Second
	if asked := pauseFor(response, now); asked > 0 {
		if asked > maxBackoff {
			return 0, false
		}
		delay = asked
	}
	if delay > maxBackoff {
		delay = maxBackoff
	}
	// Jitter keeps replicas that failed together from returning together.
	return delay + time.Duration(rand.Int64N(int64(delay/4)+1)), true
}

// pauseFor reads how long a toolkit asked to be left alone, from whichever
// header it used. Zero means it did not say, and the caller backs off on its
// own schedule.
func pauseFor(response *http.Response, now time.Time) time.Duration {
	if response == nil {
		return 0
	}
	if after := strings.TrimSpace(response.Header.Get("Retry-After")); after != "" {
		if seconds, err := strconv.Atoi(after); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if when, err := http.ParseTime(after); err == nil {
			return when.Sub(now)
		}
	}
	// GitHub says when the budget refills rather than how long to wait.
	if reset := strings.TrimSpace(response.Header.Get("X-RateLimit-Reset")); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			return time.Unix(epoch, 0).Sub(now)
		}
	}
	return 0
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// JSONRequest builds a request with a body that can be sent again, so a
// retried POST is not an empty one.
func JSONRequest(ctx context.Context, method, url string, body []byte) (*http.Request, error) {
	request, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build provider request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	return request, nil
}
