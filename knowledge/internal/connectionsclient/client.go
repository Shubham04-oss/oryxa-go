// Package connectionsclient asks connections for what knowledge may not hold
// itself: a connection's details, and a credential to read a provider with.
// Every internal call carries knowledge's signed service identity; a call
// made while a member acts also forwards the member's access token, so
// connections checks the member's access live.
//
// Connections refreshes an expiring credential itself, behind a lease, so a
// caller simply asks again rather than renewing anything. What a caller must
// do is respect how long an answer lasts, which is what the cache here is
// for: a backfill making thousands of provider calls should not ask for a
// token before each one.
package connectionsclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

const Audience = "connections"

const requestTimeout = 15 * time.Second

// maxResponseBytes bounds what connections may hand back.
const maxResponseBytes = 1 << 20

type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

type Client struct {
	baseURL string
	http    *http.Client
	signer  Signer
	clock   func() time.Time

	mu     sync.Mutex
	tokens map[string]held
}

// held is a credential kept until shortly before connections said it lapses.
type held struct {
	credential domain.Credential
	until      time.Time
}

func New(baseURL string, signer Signer) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: requestTimeout},
		signer:  signer,
		clock:   time.Now,
		tokens:  map[string]held{},
	}
}

type TokenRequest struct {
	OrganizationID string
	ConnectionID   string
	Purpose        domain.Purpose
	// AccessToken is the member's own token, when one is acting. Without it
	// the call is a bare service call, and ActingMemberID says who it is for.
	AccessToken    string
	ActingMemberID string
}

type issuedToken struct {
	AccessToken string     `json:"access_token"`
	ExpiresAt   *time.Time `json:"expires_at"`
	Placement   struct {
		In     string `json:"in"`
		Name   string `json:"name"`
		Prefix string `json:"prefix"`
	} `json:"placement"`
	Domain   string            `json:"domain"`
	Metadata map[string]string `json:"metadata"`
}

// Token answers a credential for a connection, from the cache when one is
// still good for long enough to use.
func (client *Client) Token(ctx context.Context, request TokenRequest) (domain.Credential, error) {
	key := request.ConnectionID + "\x00" + string(request.Purpose) + "\x00" + request.ActingMemberID
	if credential, ok := client.cached(key); ok {
		return credential, nil
	}

	body := map[string]string{"organization_id": request.OrganizationID, "purpose": string(request.Purpose)}
	if request.AccessToken == "" {
		body["acting_member_id"] = request.ActingMemberID
	}
	var token issuedToken
	if err := client.call(ctx, http.MethodPost,
		"/v1/internal/connections/"+url.PathEscape(request.ConnectionID)+"/token",
		request.Purpose, request.AccessToken, body, &token); err != nil {
		return domain.Credential{}, err
	}
	if token.AccessToken == "" {
		return domain.Credential{}, fmt.Errorf("connections issued no credential")
	}
	credential := domain.Credential{
		AccessToken: token.AccessToken, In: token.Placement.In, Name: token.Placement.Name,
		Prefix: token.Placement.Prefix, Domain: token.Domain, Metadata: token.Metadata,
	}
	client.keep(key, credential, token.ExpiresAt)
	return credential, nil
}

func (client *Client) cached(key string) (domain.Credential, bool) {
	client.mu.Lock()
	defer client.mu.Unlock()
	token, ok := client.tokens[key]
	if !ok {
		return domain.Credential{}, false
	}
	if !client.clock().Before(token.until) {
		delete(client.tokens, key)
		return domain.Credential{}, false
	}
	return token.credential, true
}

// keep holds a credential until shortly before it lapses. A credential with
// no expiry is a key-based secret, which is never kept.
func (client *Client) keep(key string, credential domain.Credential, expiresAt *time.Time) {
	if expiresAt == nil {
		return
	}
	until := expiresAt.Add(-domain.RenewBefore)
	if !client.clock().Before(until) {
		return
	}
	client.mu.Lock()
	client.tokens[key] = held{credential: credential, until: until}
	client.mu.Unlock()
}

// Forget drops what is held for a connection, for a caller told the
// connection changed underneath it.
func (client *Client) Forget(connectionID string) {
	client.mu.Lock()
	defer client.mu.Unlock()
	for key := range client.tokens {
		if strings.HasPrefix(key, connectionID+"\x00") {
			delete(client.tokens, key)
		}
	}
}

// Connection reads one connection by id, which a service may do for any
// connection in an organization it names.
func (client *Client) Connection(ctx context.Context, organizationID, connectionID, accessToken string) (domain.Connection, error) {
	var connection domain.Connection
	path := "/v1/internal/connections/" + url.PathEscape(connectionID) +
		"?organization_id=" + url.QueryEscape(organizationID)
	if err := client.call(ctx, http.MethodGet, path, domain.PurposeAction, accessToken, nil, &connection); err != nil {
		return domain.Connection{}, err
	}
	if connection.ID != connectionID || connection.OrganizationID != organizationID {
		return domain.Connection{}, fmt.Errorf("connections answered for a different connection")
	}
	return connection, nil
}

// Connections lists what a member can see. There is no service-authenticated
// listing, so registering a source is the only moment knowledge can learn
// that a connection exists: this call needs the member's own token.
func (client *Client) Connections(ctx context.Context, organizationID, accessToken string) ([]domain.Connection, error) {
	var list struct {
		Connections []domain.Connection `json:"connections"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/connections"
	if err := client.call(ctx, http.MethodGet, path, "", accessToken, nil, &list); err != nil {
		return nil, err
	}
	return list.Connections, nil
}

type errorBody struct {
	Error struct {
		Code string `json:"code"`
	} `json:"error"`
}

func (client *Client) call(ctx context.Context, method, path string, purpose domain.Purpose, accessToken string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode connections request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build connections request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if purpose != "" {
		serviceToken, err := client.signer.Token(ctx, Audience, string(purpose))
		if err != nil {
			return fmt.Errorf("sign connections request: %w", err)
		}
		request.Header.Set(serviceauth.Header, serviceToken)
	}
	if accessToken != "" {
		request.Header.Set("Authorization", "Bearer "+accessToken)
	}

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: connections request failed", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("%w: connections response could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		if target != nil && json.Unmarshal(payload, target) != nil {
			return fmt.Errorf("decode connections response")
		}
		return nil
	}
	var failure errorBody
	_ = json.Unmarshal(payload, &failure)
	return connectionsError(response.StatusCode, failure.Error.Code)
}

func connectionsError(status int, code string) error {
	switch {
	case code == "needs_reconnect":
		return domain.ErrNeedsReconnect
	case code == "app_disabled":
		return fmt.Errorf("the connection's app is disabled: %w", domain.ErrForbidden)
	case code == "credential_rejected":
		return domain.ErrCredentialRejected
	case code == "scope_required":
		return fmt.Errorf("the connection is missing a scope: %w", domain.ErrScopeRequired)
	case status == http.StatusBadRequest:
		return fmt.Errorf("connections refused the request: %w", domain.ErrInvalid)
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden:
		return domain.ErrForbidden
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status == http.StatusConflict:
		return domain.ErrConflict
	case status == http.StatusServiceUnavailable || status == http.StatusTooManyRequests || status >= 500:
		return fmt.Errorf("%w: connections returned %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("connections returned %d", status)
	}
}
