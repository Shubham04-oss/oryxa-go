// Package github contains the OAuth adapter and the native webhook surface
// that cannot be delegated to piece-github. Inbound issue and pull-request
// triggers create repository or organization hooks with the member's token.
package github

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/internal/piece"
)

const (
	maxResponseSize  = 1 << 20
	defaultTimeout   = 15 * time.Second
	githubAPIVersion = "2022-11-28"
)

var defaultHTTPClient = &http.Client{Timeout: defaultTimeout}

var (
	ErrInvalidHook    = errors.New("invalid GitHub webhook")
	ErrGitHubRejected = errors.New("GitHub rejected webhook request")
)

const (
	HookScopeRepository   = "repository"
	HookScopeOrganization = "organization"
	EventIssues           = "issues"
	EventPullRequest      = "pull_request"
)

// RejectionError exposes only GitHub's HTTP status. It intentionally omits
// the response body, which can contain repository or organization details.
type RejectionError struct {
	StatusCode int
}

func (e RejectionError) Error() string {
	return fmt.Sprintf("%s (%d)", ErrGitHubRejected, e.StatusCode)
}

func (e RejectionError) Unwrap() error { return ErrGitHubRejected }

func IsAuthorizationRejection(err error) bool {
	var rejection RejectionError
	return errors.As(err, &rejection) && (rejection.StatusCode == http.StatusUnauthorized || rejection.StatusCode == http.StatusForbidden)
}

type HookRequest struct {
	Scope  string
	Owner  string
	Repo   string
	Events []string
	URL    string
	Secret string
}

type Hook struct {
	ID  string
	URL string
}

// HookState is the non-secret state a GitHub trigger stores in the existing
// trigger runtime store. The HMAC secret is held only in the encrypted
// webhook-route record.
type HookState struct {
	ID    string
	URL   string
	Scope string
	Owner string
	Repo  string
}

const (
	storeHookID    = "github_hook_id"
	storeHookURL   = "github_hook_url"
	storeHookScope = "github_hook_scope"
	storeHookOwner = "github_hook_owner"
	storeHookRepo  = "github_hook_repo"
)

func (s HookState) StoreMutations() []piece.StoreMutation {
	if !validHookID(s.ID) || !validHookTarget(s.Scope, s.Owner, s.Repo) || (s.URL != "" && !validHTTPSURL(s.URL) && !validGitHubAPIURL(s.URL)) {
		return nil
	}
	mutations := []piece.StoreMutation{
		{Operation: "put", Key: storeHookID, Value: s.ID},
		{Operation: "put", Key: storeHookScope, Value: s.Scope},
		{Operation: "put", Key: storeHookOwner, Value: s.Owner},
	}
	if s.URL != "" {
		mutations = append(mutations, piece.StoreMutation{Operation: "put", Key: storeHookURL, Value: s.URL})
	}
	if s.Scope == HookScopeRepository {
		mutations = append(mutations, piece.StoreMutation{Operation: "put", Key: storeHookRepo, Value: s.Repo})
	}
	return mutations
}

func HookStateFromStore(store map[string]any) (HookState, error) {
	if store == nil {
		return HookState{}, ErrInvalidHook
	}
	id, _ := store[storeHookID].(string)
	url, _ := store[storeHookURL].(string)
	scope, _ := store[storeHookScope].(string)
	owner, _ := store[storeHookOwner].(string)
	repo, _ := store[storeHookRepo].(string)
	if !validHookID(id) || !validHookTarget(scope, owner, repo) {
		return HookState{}, ErrInvalidHook
	}
	if url != "" && !validHTTPSURL(url) && !validGitHubAPIURL(url) {
		return HookState{}, ErrInvalidHook
	}
	return HookState{ID: id, URL: url, Scope: scope, Owner: owner, Repo: repo}, nil
}

type Client struct {
	HTTPClient *http.Client
	BaseURL    string
}

func (c Client) Create(ctx context.Context, accessToken string, request HookRequest) (Hook, error) {
	if err := validateCreate(request); err != nil {
		return Hook{}, err
	}
	body, err := json.Marshal(struct {
		Name   string            `json:"name"`
		Active bool              `json:"active"`
		Events []string          `json:"events"`
		Config map[string]string `json:"config"`
	}{
		Name: "web", Active: true, Events: append([]string(nil), request.Events...),
		Config: map[string]string{
			"url": request.URL, "content_type": "json", "secret": request.Secret, "insecure_ssl": "0",
		},
	})
	if err != nil {
		return Hook{}, err
	}
	path, err := hookCollectionPath(request.Scope, request.Owner, request.Repo)
	if err != nil {
		return Hook{}, err
	}
	return c.request(ctx, http.MethodPost, path, accessToken, body, http.StatusCreated)
}

// Remove treats GitHub's 404 as success so compensation after a failed local
// transaction and trigger teardown stay idempotent.
func (c Client) Remove(ctx context.Context, accessToken string, state HookState) error {
	if !validHookID(state.ID) || !validHookTarget(state.Scope, state.Owner, state.Repo) {
		return ErrInvalidHook
	}
	if strings.TrimSpace(accessToken) == "" {
		return ErrInvalidHook
	}
	path, err := hookItemPath(state.Scope, state.Owner, state.Repo, state.ID)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL()+path, nil)
	if err != nil {
		return err
	}
	c.authorize(request, accessToken)
	response, err := c.client().Do(request)
	if err != nil {
		return fmt.Errorf("delete GitHub webhook: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNoContent || response.StatusCode == http.StatusNotFound {
		return nil
	}
	return githubError(response)
}

func (c Client) request(ctx context.Context, method, path, accessToken string, body []byte, success int) (Hook, error) {
	if strings.TrimSpace(accessToken) == "" {
		return Hook{}, ErrInvalidHook
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL()+path, strings.NewReader(string(body)))
	if err != nil {
		return Hook{}, err
	}
	c.authorize(request, accessToken)
	request.Header.Set("content-type", "application/json")
	response, err := c.client().Do(request)
	if err != nil {
		return Hook{}, fmt.Errorf("request GitHub webhook: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != success {
		return Hook{}, githubError(response)
	}
	var payload struct {
		ID  json.Number `json:"id"`
		URL string      `json:"url"`
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxResponseSize+1))
	if err := decoder.Decode(&payload); err != nil {
		return Hook{}, ErrInvalidHook
	}
	id := payload.ID.String()
	if !validHookID(id) {
		return Hook{}, ErrInvalidHook
	}
	if payload.URL != "" && !validHTTPSURL(payload.URL) && !validGitHubAPIURL(payload.URL) {
		return Hook{}, ErrInvalidHook
	}
	return Hook{ID: id, URL: payload.URL}, nil
}

func (c Client) authorize(request *http.Request, accessToken string) {
	request.Header.Set("authorization", "Bearer "+accessToken)
	request.Header.Set("accept", "application/vnd.github+json")
	request.Header.Set("x-github-api-version", githubAPIVersion)
}

func (c Client) baseURL() string {
	return strings.TrimRight(firstNonEmpty(c.BaseURL, defaultAPIURL), "/")
}

func (c Client) client() *http.Client {
	if c.HTTPClient != nil {
		return c.HTTPClient
	}
	return defaultHTTPClient
}

func githubError(response *http.Response) error {
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8*1024))
	return RejectionError{StatusCode: response.StatusCode}
}

func validateCreate(request HookRequest) error {
	if !validHookTarget(request.Scope, request.Owner, request.Repo) || !validHTTPSURL(request.URL) ||
		len(request.Secret) < 32 || len(request.Secret) > 256 || !validHookEvents(request.Events) {
		return ErrInvalidHook
	}
	return nil
}

func NewHookSecret(source io.Reader) (string, error) {
	if source == nil {
		source = rand.Reader
	}
	bytes := make([]byte, 32)
	if _, err := io.ReadFull(source, bytes); err != nil {
		return "", fmt.Errorf("generate GitHub webhook secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func validHookEvents(events []string) bool {
	if len(events) != 1 {
		return false
	}
	return events[0] == EventIssues || events[0] == EventPullRequest
}

func validHookTarget(scope, owner, repo string) bool {
	if !validGitHubName(owner) {
		return false
	}
	switch scope {
	case HookScopeRepository:
		return validGitHubName(repo)
	case HookScopeOrganization:
		return repo == ""
	default:
		return false
	}
}

func validHookID(value string) bool {
	if value == "" || len(value) > 32 {
		return false
	}
	id, err := strconv.ParseInt(value, 10, 64)
	return err == nil && id > 0
}

func hookCollectionPath(scope, owner, repo string) (string, error) {
	if !validHookTarget(scope, owner, repo) {
		return "", ErrInvalidHook
	}
	if scope == HookScopeOrganization {
		return "/orgs/" + owner + "/hooks", nil
	}
	return "/repos/" + owner + "/" + repo + "/hooks", nil
}

func hookItemPath(scope, owner, repo, id string) (string, error) {
	collection, err := hookCollectionPath(scope, owner, repo)
	if err != nil {
		return "", err
	}
	if !validHookID(id) {
		return "", ErrInvalidHook
	}
	return collection + "/" + id, nil
}

func validGitHubName(value string) bool {
	if value == "" || len(value) > 100 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("-_.", character)) {
			return false
		}
	}
	return true
}

func validHTTPSURL(value string) bool {
	request, err := http.NewRequest(http.MethodGet, value, nil)
	return err == nil && request.URL.Scheme == "https" && request.URL.Host != "" && request.URL.User == nil && request.URL.Fragment == ""
}

func validGitHubAPIURL(value string) bool {
	request, err := http.NewRequest(http.MethodGet, value, nil)
	if err != nil || request.URL.User != nil || request.URL.Fragment != "" {
		return false
	}
	host := strings.ToLower(request.URL.Host)
	return (request.URL.Scheme == "https" || request.URL.Scheme == "http") && host != "" &&
		(host == "api.github.com" || strings.HasSuffix(host, ".github.com"))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
