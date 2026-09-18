// Package membertoken mints short-lived access tokens that act as a member
// while they are away, by exchanging the agent grant they gave auth.
//
// Some work has no member at a browser to carry credentials: a scheduled
// pass over an organization's own files, a source registered because an app
// was connected. The services those reach — files above all — forward a
// caller's own credentials to auth and accept nothing else, so there is no
// bare-service way in. A member's standing grant is what fills that gap, and
// this is how a service redeems one.
//
// A token is only ever as good as the grant behind it: a member who never
// granted, or whose grant auth has since dropped, answers ErrGrantRequired,
// and the work that needed it stops rather than falling back to anything
// wider.
package membertoken

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

var (
	// ErrGrantRequired means the member has not given the standing consent
	// this needs, or auth has dropped the one they gave. Only they can fix
	// it, so retrying changes nothing.
	ErrGrantRequired = errors.New("the member has not granted standing access")
	// ErrUnavailable means auth did not answer usefully, which retrying may.
	ErrUnavailable = errors.New("auth is unavailable")
	// ErrNotConfigured means this deployment has no way to reach auth for a
	// token, so whatever needed one cannot run at all.
	ErrNotConfigured = errors.New("member tokens are not configured")
)

// refreshWindow is how long before its expiry a held token is treated as
// already gone, so a caller is never handed one the service it calls is
// about to refuse mid-request.
const refreshWindow = 2 * time.Minute

// maxResponseBytes bounds what auth's answer may be.
const maxResponseBytes = 1 << 20

// requestTimeout bounds one request to auth. The callers are event handlers
// and background passes whose contexts carry no deadline of their own, and
// every caller asking for the same member waits on the one request out, so
// an auth that stops answering must not hold them for ever.
const requestTimeout = 30 * time.Second

// Signer signs this service's own identity for auth. serviceauth.Signer
// satisfies it.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

type Config struct {
	// AuthURL is auth's own URL; the request goes to
	// {AuthURL}/v1/internal/agent-tokens.
	AuthURL string
	// Signer proves which service is asking. Auth decides from its own
	// configuration whether that service may mint tokens at all.
	Signer Signer
	// HTTP is the client requests go out on; one that gives up after
	// requestTimeout when unset.
	HTTP *http.Client
	// Now is the clock, for tests that need expiry to be decidable.
	Now func() time.Time
}

// Client mints member tokens, holding each one until shortly before it
// expires. It is safe for concurrent use, and callers arriving for the same
// member while a request is out share that one request rather than each
// making their own — a pass over many sources would otherwise ask auth once
// per source.
type Client struct {
	authURL string
	signer  Signer
	http    *http.Client
	now     func() time.Time

	mu       sync.Mutex
	held     map[string]heldToken
	inflight map[string]*request
}

type heldToken struct {
	token     string
	expiresAt time.Time
}

type request struct {
	done      chan struct{}
	token     string
	expiresAt time.Time
	err       error
}

func New(config Config) *Client {
	client := config.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &Client{
		authURL:  strings.TrimRight(strings.TrimSpace(config.AuthURL), "/"),
		signer:   config.Signer,
		http:     client,
		now:      now,
		held:     map[string]heldToken{},
		inflight: map[string]*request{},
	}
}

// MemberToken answers an access token acting as memberID. The organization
// is not sent: a member's grant is theirs across every organization they are
// in, and auth decides what the token may reach from the member alone. It is
// taken so a caller's own scoping reads the same as everywhere else.
func (client *Client) MemberToken(ctx context.Context, _ string, memberID string) (string, error) {
	memberID = strings.TrimSpace(memberID)
	if memberID == "" {
		return "", fmt.Errorf("%w: no member was named", ErrGrantRequired)
	}
	if client.authURL == "" || client.signer == nil {
		return "", ErrNotConfigured
	}

	client.mu.Lock()
	if held, ok := client.held[memberID]; ok && client.now().Before(held.expiresAt.Add(-refreshWindow)) {
		client.mu.Unlock()
		return held.token, nil
	}
	if outstanding, ok := client.inflight[memberID]; ok {
		client.mu.Unlock()
		select {
		case <-outstanding.done:
			return outstanding.token, outstanding.err
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	pending := &request{done: make(chan struct{})}
	client.inflight[memberID] = pending
	client.mu.Unlock()

	pending.token, pending.expiresAt, pending.err = client.mint(ctx, memberID)

	client.mu.Lock()
	delete(client.inflight, memberID)
	if pending.err == nil {
		// Tokens that have run out are dropped as they are noticed, so what
		// is held is what is still usable.
		for member, held := range client.held {
			if !client.now().Before(held.expiresAt) {
				delete(client.held, member)
			}
		}
		client.held[memberID] = heldToken{token: pending.token, expiresAt: pending.expiresAt}
	}
	client.mu.Unlock()
	close(pending.done)

	return pending.token, pending.err
}

// mint asks auth once, without consulting or filling what is held.
func (client *Client) mint(ctx context.Context, memberID string) (string, time.Time, error) {
	serviceToken, err := client.signer.Token(ctx, "auth", "agent")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign this service's token: %w", err)
	}
	body, err := json.Marshal(map[string]string{"user_id": memberID})
	if err != nil {
		return "", time.Time{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		client.authURL+"/v1/internal/agent-tokens", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set(serviceauth.Header, serviceToken)

	response, err := client.http.Do(httpRequest)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
		var answer struct {
			AccessToken string `json:"access_token"`
			ExpiresIn   int64  `json:"expires_in"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&answer); err != nil {
			return "", time.Time{}, fmt.Errorf("read auth's answer: %w", err)
		}
		if answer.AccessToken == "" {
			return "", time.Time{}, errors.New("auth answered no access token")
		}
		return answer.AccessToken, client.now().Add(time.Duration(answer.ExpiresIn) * time.Second), nil
	case response.StatusCode == http.StatusNotFound, response.StatusCode == http.StatusConflict:
		// 404: the member never granted. 409: the grant was refused at the
		// identity provider and auth has dropped it. Either way the member
		// has to grant again, and asking again will not help.
		return "", time.Time{}, ErrGrantRequired
	case response.StatusCode == http.StatusForbidden, response.StatusCode == http.StatusUnauthorized:
		// This service is not one auth mints tokens for. That is
		// configuration, not something a retry fixes.
		return "", time.Time{}, fmt.Errorf("%w: auth refused this service (status %d)", ErrNotConfigured, response.StatusCode)
	case response.StatusCode >= http.StatusInternalServerError:
		return "", time.Time{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return "", time.Time{}, fmt.Errorf("auth refused the token request: status %d", response.StatusCode)
	}
}
