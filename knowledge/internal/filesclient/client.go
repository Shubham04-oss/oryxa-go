// Package filesclient reads the organization's own object storage.
//
// Files has no service identity: it never verifies a token itself, forwarding
// the caller's credentials to auth instead, and nothing in it accepts a
// signed service token the way connections does. So every call here carries a
// member's access token, and knowledge reads exactly what that member could
// read and nothing more. Obtaining that token is the caller's problem, not
// this package's.
package filesclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
)

// maxObjectBytes is what files itself caps an upload at, so nothing larger
// can come back.
const maxObjectBytes = 8 << 20

const requestTimeout = 30 * time.Second

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: &http.Client{Timeout: requestTimeout}}
}

// Object is one stored file, as files describes it. Objects are immutable:
// there is no version and no overwrite, so SHA256 is what says a file is the
// same file.
type Object struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	Scope          string    `json:"scope"`
	TeamID         string    `json:"team_id,omitempty"`
	MemberID       string    `json:"member_id,omitempty"`
	Filename       string    `json:"filename"`
	ContentType    string    `json:"content_type"`
	Purpose        string    `json:"purpose"`
	SizeBytes      int64     `json:"size_bytes"`
	SHA256         string    `json:"sha256"`
	CreatedBy      string    `json:"created_by"`
	CreatedAt      time.Time `json:"created_at"`
	ExpiresAt      time.Time `json:"expires_at"`
}

// Query narrows a listing the way files' own API does.
type Query struct {
	Scope   string
	TeamID  string
	Purpose string
}

// Objects lists what the member can see in an organization.
//
// Files answers the whole set at once: its listing takes no page or cursor,
// and a team's files are only included when a team is named. So a caller that
// wants everything an organization holds must ask once per team it knows of,
// and page the answer itself.
func (client *Client) Objects(ctx context.Context, organizationID, accessToken string, query Query) ([]Object, error) {
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/files"
	values := url.Values{}
	if query.Scope != "" {
		values.Set("scope", query.Scope)
	}
	if query.TeamID != "" {
		values.Set("team_id", query.TeamID)
	}
	if query.Purpose != "" {
		values.Set("purpose", query.Purpose)
	}
	if encoded := values.Encode(); encoded != "" {
		path += "?" + encoded
	}

	var answer struct {
		Files []Object `json:"files"`
	}
	if err := client.call(ctx, path, accessToken, &answer); err != nil {
		return nil, err
	}
	return answer.Files, nil
}

// Content reads a file's bytes through files, which is the only way in: a
// caller never holds credentials for the bucket itself.
//
// A file whose retention lapsed is simply gone — files publishes no event for
// an expiry, it only stops answering — so a caller that gets ErrNotFound here
// should retire what it held rather than treat the run as broken.
func (client *Client) Content(ctx context.Context, organizationID, fileID, accessToken string) ([]byte, string, error) {
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/files/" + url.PathEscape(fileID) + "/content"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return nil, "", fmt.Errorf("build files request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+accessToken)

	response, err := client.http.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("%w: files did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxObjectBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("%w: the file could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", filesError(response.StatusCode)
	}
	if len(body) > maxObjectBytes {
		return nil, "", fmt.Errorf("the file is larger than files allows: %w", domain.ErrInvalid)
	}
	return body, response.Header.Get("Content-Type"), nil
}

func (client *Client) call(ctx context.Context, path, accessToken string, target any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return fmt.Errorf("build files request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)

	response, err := client.http.Do(request)
	if err != nil {
		return fmt.Errorf("%w: files did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxObjectBytes+1))
	if err != nil {
		return fmt.Errorf("%w: files' answer could not be read", domain.ErrProviderUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return filesError(response.StatusCode)
	}
	if target != nil && json.Unmarshal(payload, target) != nil {
		return fmt.Errorf("decode files response")
	}
	return nil
}

func filesError(status int) error {
	switch {
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden:
		return domain.ErrForbidden
	case status == http.StatusNotFound:
		return domain.ErrNotFound
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case status >= 500:
		return fmt.Errorf("%w: files answered %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("files answered %d: %w", status, domain.ErrInvalid)
	}
}
