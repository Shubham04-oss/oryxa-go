package sources

import (
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

// Bulk reading is not one request. A provider that rate-limits, wobbles or
// asks for a pause will do so partway through a source, and nothing in this
// repo handles that: the provider client integrations uses makes a single
// attempt with a flat timeout. These are the defaults for reading for
// minutes rather than milliseconds.
const (
	// requestTimeout bounds one call. It is longer than the 10 seconds a
	// single provider action gets, because a page of a hundred documents is a
	// bigger answer than one issue.
	requestTimeout = 60 * time.Second
	// maxAttempts counts the first try. Past it a failure is the caller's.
	maxAttempts = 4
	// maxResponseBytes caps what one call may hand back.
	maxResponseBytes = 8 << 20
	// maxBackoff caps the wait between attempts, including one a provider
	// asked for: a Retry-After of an hour means stop, not sleep for an hour.
	maxBackoff = 60 * time.Second
	// pace is the least time between two calls to the same host, so a
	// backfill walks a provider rather than sprinting at it.
	pace = 100 * time.Millisecond
)

// Response is what a provider answered, already read and bounded.
type Response struct {
	Status int
	Header http.Header
	Body   []byte
}

// Doer is the HTTP surface Client needs, so a test answers without a network.
type Doer interface {
	Do(*http.Request) (*http.Response, error)
}

// Client is the HTTP behaviour bulk extraction needs: it paces calls to a
// host, waits when a provider says to, retries what is worth retrying, and
// refuses to read an unbounded body. It is safe for concurrent use.
type Client struct {
	http  Doer
	clock func() time.Time
	sleep func(context.Context, time.Duration) error

	mu     sync.Mutex
	nextAt map[string]time.Time
}

// NewClient builds the client extractors share. Redirects are not followed:
// a redirect would carry the provider's credential somewhere it was not
// issued for.
func NewClient() *Client {
	return newClient(&http.Client{
		Timeout:       requestTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	})
}

func newClient(doer Doer) *Client {
	return &Client{
		http:   doer,
		clock:  time.Now,
		sleep:  sleepContext,
		nextAt: map[string]time.Time{},
	}
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

// Do sends a request with the connection's credential on it, retrying what a
// provider says to retry. The body it answers is already read, so the caller
// never holds an open connection between pages.
func (client *Client) Do(ctx context.Context, request *http.Request, credential domain.Credential) (*Response, error) {
	var lastErr error
	for attempt := range maxAttempts {
		if attempt > 0 {
			delay, ok := retryDelay(lastErr, attempt)
			if !ok {
				break
			}
			if err := client.sleep(ctx, delay); err != nil {
				return nil, err
			}
		}
		if err := client.wait(ctx, request.URL.Host); err != nil {
			return nil, err
		}
		response, err := client.send(ctx, request, credential)
		if err == nil {
			return response, nil
		}
		lastErr = err
		if !retryable(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

// wait holds a caller until this host may be called again, so concurrent
// extractions against one provider do not arrive together.
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

func (client *Client) send(ctx context.Context, request *http.Request, credential domain.Credential) (*Response, error) {
	attempt := request.Clone(ctx)
	if attempt.Body == nil && request.GetBody != nil {
		body, err := request.GetBody()
		if err != nil {
			return nil, fmt.Errorf("rewind request body: %w", err)
		}
		attempt.Body = body
	}
	authorize(attempt, credential)

	response, err := client.http.Do(attempt)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
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
	return nil, client.statusError(answer)
}

// authorize puts the credential where connections said it goes. It mirrors
// what integrations does, which is not reachable from here.
func authorize(request *http.Request, credential domain.Credential) {
	if credential.AccessToken == "" {
		return
	}
	if credential.In == "query" {
		query := request.URL.Query()
		query.Set(credential.Name, credential.Prefix+credential.AccessToken)
		request.URL.RawQuery = query.Encode()
		return
	}
	name := credential.Name
	if name == "" {
		name = "Authorization"
	}
	request.Header.Set(name, credential.Prefix+credential.AccessToken)
}

// paused is a provider asking for a wait, with how long it asked for.
type paused struct {
	after time.Duration
	err   error
}

func (p *paused) Error() string { return p.err.Error() }
func (p *paused) Unwrap() error { return p.err }

// statusError turns a provider's status into the reason a caller acts on. A
// provider's own error body never travels with it.
func (client *Client) statusError(response *Response) error {
	switch {
	case response.Status == http.StatusTooManyRequests:
		return &paused{after: client.pauseFor(response), err: domain.ErrRateLimited}
	case response.Status == http.StatusUnauthorized:
		return domain.ErrNeedsReconnect
	case response.Status == http.StatusForbidden:
		// A provider that has run out of budget says so with a 403 and an
		// exhausted remaining count, rather than a 429.
		if response.Header.Get("X-RateLimit-Remaining") == "0" {
			return &paused{after: client.pauseFor(response), err: domain.ErrRateLimited}
		}
		return domain.ErrForbidden
	case response.Status == http.StatusNotFound:
		return domain.ErrNotFound
	case response.Status >= 500:
		return &paused{after: client.pauseFor(response), err: domain.ErrProviderUnavailable}
	default:
		return fmt.Errorf("the provider answered %d: %w", response.Status, domain.ErrInvalid)
	}
}

// pauseFor reads how long a provider asked to be left alone, from whichever
// header it used. Zero means it did not say, and the caller backs off on its
// own schedule.
func (client *Client) pauseFor(response *Response) time.Duration {
	if after := strings.TrimSpace(response.Header.Get("Retry-After")); after != "" {
		if seconds, err := strconv.Atoi(after); err == nil && seconds >= 0 {
			return time.Duration(seconds) * time.Second
		}
		if when, err := http.ParseTime(after); err == nil {
			return when.Sub(client.clock())
		}
	}
	// GitHub says when the budget refills rather than how long to wait.
	if reset := strings.TrimSpace(response.Header.Get("X-RateLimit-Reset")); reset != "" {
		if epoch, err := strconv.ParseInt(reset, 10, 64); err == nil {
			return time.Unix(epoch, 0).Sub(client.clock())
		}
	}
	return 0
}

// retryable says whether trying again could answer differently. A credential
// that was refused, or a document that is not there, will not.
func retryable(err error) bool {
	return errors.Is(err, domain.ErrRateLimited) || errors.Is(err, domain.ErrProviderUnavailable)
}

// retryDelay is how long to wait before the next attempt: what the provider
// asked for when it said, and a doubling backoff with jitter when it did not.
// It answers false when the wait is longer than this run should hold for,
// which is the signal to give the source back and try it later.
func retryDelay(err error, attempt int) (time.Duration, bool) {
	delay := time.Duration(1<<uint(attempt)) * time.Second
	var pause *paused
	if errors.As(err, &pause) && pause.after > 0 {
		if pause.after > maxBackoff {
			return 0, false
		}
		delay = pause.after
	}
	if delay > maxBackoff {
		delay = maxBackoff
	}
	// Jitter keeps replicas that failed together from returning together.
	jitter := time.Duration(rand.Int64N(int64(delay/4) + 1))
	return delay + jitter, true
}
