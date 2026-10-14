package memberauth

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runprofile"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// tokenRefreshWindow is how long before its expiry a cached agent token is
// treated as already expired, so a caller is never handed a token the service
// it calls is about to refuse mid-call.
const tokenRefreshWindow = 2 * time.Minute

// AuthClientConfig configures an AuthClient.
type AuthClientConfig struct {
	// BaseURL is auth's own URL (AUTH_URL); requests go to
	// {BaseURL}/v1/internal/agent-tokens.
	BaseURL string
	// HTTP is the client requests go out on. Defaults to http.DefaultClient
	// when nil.
	HTTP *http.Client
	// Signer signs each request's service token: agents is the caller,
	// "auth" the audience, "agent" the purpose.
	Signer serviceauth.Signer
	// Now returns the current time; defaults to time.Now. Tests override it
	// to control caching deterministically.
	Now func() time.Time
}

// AuthClient implements TokenSource by calling auth's internal agent-token
// endpoint. It caches each user's token for each agent until
// tokenRefreshWindow before it expires, and is safe for concurrent use:
// concurrent callers for the same user and agent who all find no usable
// cached token share a single request rather than each firing their own.
type AuthClient struct {
	baseURL string
	http    *http.Client
	signer  serviceauth.Signer
	now     func() time.Time

	mu       sync.Mutex
	cache    map[string]cachedToken
	inflight map[string]*tokenRequest
}

type cachedToken struct {
	token     string
	expiresAt time.Time
}

// tokenRequest is one in-flight request for a user's token, shared by every
// caller that arrives while it is outstanding.
type tokenRequest struct {
	done      chan struct{}
	token     string
	expiresAt time.Time
	err       error
}

// NewAuthClient returns an AuthClient calling
// {config.BaseURL}/v1/internal/agent-tokens.
func NewAuthClient(config AuthClientConfig) *AuthClient {
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	return &AuthClient{
		baseURL:  strings.TrimRight(config.BaseURL, "/"),
		http:     httpClient,
		signer:   config.Signer,
		now:      now,
		cache:    make(map[string]cachedToken),
		inflight: make(map[string]*tokenRequest),
	}
}

// AgentToken implements TokenSource.
func (c *AuthClient) AgentToken(ctx context.Context, userID string, agent Agent) (string, time.Time, error) {
	if userID == "" {
		return "", time.Time{}, fmt.Errorf("memberauth: user id is required")
	}
	if agent.Role == "" {
		agent.Role = runprofile.Primary
	}
	// One agent's token must never be handed to another, nor one minted under
	// a grant since changed, so the cache is keyed by all of them. NUL cannot
	// appear in any.
	key := userID + "\x00" + agent.ID + "\x00" + string(agent.Role) + "\x00" + agent.Grant

	c.mu.Lock()
	if cached, ok := c.cache[key]; ok && c.now().Before(cached.expiresAt.Add(-tokenRefreshWindow)) {
		c.mu.Unlock()
		return cached.token, cached.expiresAt, nil
	}
	if req, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		select {
		case <-req.done:
			return req.token, req.expiresAt, req.err
		case <-ctx.Done():
			return "", time.Time{}, ctx.Err()
		}
	}
	req := &tokenRequest{done: make(chan struct{})}
	c.inflight[key] = req
	c.mu.Unlock()

	req.token, req.expiresAt, req.err = c.requestToken(ctx, userID, agent)

	c.mu.Lock()
	delete(c.inflight, key)
	if req.err == nil {
		// Drop tokens that have expired, so the cache holds only members
		// agents are acting for now.
		for cachedKey, cached := range c.cache {
			if !c.now().Before(cached.expiresAt) {
				delete(c.cache, cachedKey)
			}
		}
		c.cache[key] = cachedToken{token: req.token, expiresAt: req.expiresAt}
	}
	c.mu.Unlock()
	close(req.done)

	return req.token, req.expiresAt, req.err
}

// agentTokenRequestBody's agent and role are the edge token's claim names.
type agentTokenRequestBody struct {
	UserID string          `json:"user_id"`
	Agent  string          `json:"agent,omitempty"`
	Role   string          `json:"role"`
	Grant  json.RawMessage `json:"grant,omitempty"`
}

type agentTokenResponseBody struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int64  `json:"expires_in"`
}

// requestToken calls auth once, uncached, for userID's token for agent.
func (c *AuthClient) requestToken(ctx context.Context, userID string, agent Agent) (string, time.Time, error) {
	if c.baseURL == "" {
		return "", time.Time{}, fmt.Errorf("memberauth: AUTH_URL is not configured")
	}
	serviceToken, err := c.signer.Token(ctx, "auth", "agent")
	if err != nil {
		return "", time.Time{}, fmt.Errorf("memberauth: sign service token: %w", err)
	}
	requested := agentTokenRequestBody{UserID: userID, Agent: agent.ID, Role: string(agent.Role)}
	if agent.Grant != "" {
		requested.Grant = json.RawMessage(agent.Grant)
	}
	body, err := json.Marshal(requested)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("memberauth: encode agent token request: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/internal/agent-tokens", bytes.NewReader(body))
	if err != nil {
		return "", time.Time{}, fmt.Errorf("memberauth: build agent token request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(serviceauth.Header, serviceToken)

	response, err := c.http.Do(request)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
		var decoded agentTokenResponseBody
		if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
			return "", time.Time{}, fmt.Errorf("memberauth: decode agent token response: %w", err)
		}
		if decoded.AccessToken == "" {
			return "", time.Time{}, fmt.Errorf("memberauth: agent token response carried no access_token")
		}
		return decoded.AccessToken, c.now().Add(time.Duration(decoded.ExpiresIn) * time.Second), nil
	case response.StatusCode == http.StatusNotFound, response.StatusCode == http.StatusConflict:
		// 404: the member has never granted agents access. 409: Cognito
		// refused the stored refresh token and auth has already deleted the
		// grant. Either way the member has to grant again.
		return "", time.Time{}, ErrGrantRequired
	case response.StatusCode >= http.StatusInternalServerError:
		return "", time.Time{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	default:
		return "", time.Time{}, fmt.Errorf("memberauth: agent token request failed: status %d", response.StatusCode)
	}
}

var _ TokenSource = (*AuthClient)(nil)
