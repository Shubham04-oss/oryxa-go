package appsclient

// The pages route's contract, mirrored here.
//
// Go's internal rule stops knowledge importing apps/internal/integration,
// which is where these shapes are written once and are pinned by
// docs/design/integrations-plan.md. They are mirrored rather than shared so
// that nothing here can drift quietly: the wire format is the contract, and
// the tests below post and read these shapes against a fake apps, so a change
// on either side that the other has not made fails here first. Every change
// is additive only.

import (
	"bytes"
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

// maxPageBytes is what apps promises a page may weigh, at most 6 MiB. It is
// the only answer here that is large — one connection, never a listing — so
// it is read to that bound and no further: a larger one is apps' own doing,
// and is refused rather than read into memory to be discarded.
const maxPageBytes = 6 << 20

// PageRequest asks a connection's own reader for one page of one target.
type PageRequest struct {
	// Target always carries "reader", the reader it is for, and whatever
	// else that reader needs to find what it reads — a repository, a
	// channel, a folder.
	Target    map[string]string `json:"target"`
	Cursor    string            `json:"cursor,omitempty"`
	Watermark *time.Time        `json:"watermark,omitempty"`
	Token     string            `json:"token,omitempty"`
	Full      bool              `json:"full"`
	Limit     int               `json:"limit"`
}

// Page is one page a reader answered. It maps one-to-one onto knowledge's
// sources.Page.
type Page struct {
	Items []Item `json:"items"`
	// Cursor is where to carry on; "" means the target is drained.
	Cursor    string     `json:"cursor,omitempty"`
	Watermark *time.Time `json:"watermark,omitempty"`
	Token     string     `json:"token,omitempty"`
	Removed   []string   `json:"removed,omitempty"`
}

// Item is one document a reader yielded, as the app gave it.
type Item struct {
	ExternalID string            `json:"external_id"`
	Kind       string            `json:"kind"`
	Title      string            `json:"title,omitempty"`
	URL        string            `json:"url,omitempty"`
	MIME       string            `json:"mime,omitempty"`
	Content    []byte            `json:"content,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
	UpdatedAt  *time.Time        `json:"updated_at,omitempty"`
}

// Pages asks one connection's reader for one page of one target.
//
// The call is made as the member: the route is member-authenticated and
// answers only for the connection's own member, and apps asks auth about the
// token rather than trusting this. So the member's own token travels in the
// request and nowhere else, and neither it nor what the page holds is ever
// logged, quoted back, or put in an error.
func (client *Client) Pages(ctx context.Context, organizationID, connectionID, accessToken string, page PageRequest) (Page, error) {
	if strings.TrimSpace(accessToken) == "" {
		// No request is worth making: apps would only say the same thing back.
		return Page{}, domain.ErrUnauthorized
	}
	body, err := json.Marshal(page)
	if err != nil {
		return Page{}, fmt.Errorf("build the page request: %w", err)
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) +
		"/apps/connections/" + url.PathEscape(connectionID) + "/pages"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return Page{}, fmt.Errorf("build apps request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+accessToken)

	response, err := client.http.Do(request)
	if err != nil {
		return Page{}, fmt.Errorf("%w: apps did not answer", domain.ErrProviderUnavailable)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxPageBytes+1))
	if err != nil {
		return Page{}, fmt.Errorf("%w: apps' answer could not be read", domain.ErrProviderUnavailable)
	}
	if len(payload) > maxPageBytes {
		return Page{}, fmt.Errorf("%w: a page weighs more than apps may answer", domain.ErrProviderUnavailable)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Page{}, pagesError(response.StatusCode, payload)
	}
	var answered Page
	if err := json.Unmarshal(payload, &answered); err != nil {
		// Nothing from the answer goes into this: what was not read is not
		// repeated, and a page holds a member's own documents.
		return Page{}, fmt.Errorf("decode apps' page")
	}
	return answered, nil
}

// pagesError says what a refused page means.
//
// The code is read because the status alone cannot tell a connection whose
// credential can no longer be used from one that is merely in conflict, and
// only the first is worth pausing a source for rather than backing it off
// for ever. A refusal that is not the shape apps writes is read by its status
// alone, and nothing from the body is quoted back: an answer is only ever read
// for its code.
func pagesError(status int, payload []byte) error {
	var failure struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &failure)
	switch failure.Error.Code {
	case "needs_reconnect", "not_connected":
		// The credential behind the connection cannot be used, so every
		// source on it is paused until the member connects again — which is
		// what domain.ErrNeedsReconnect asks the caller to do.
		return domain.ErrNeedsReconnect
	}
	switch {
	case status == http.StatusUnauthorized:
		return domain.ErrUnauthorized
	case status == http.StatusForbidden, status == http.StatusNotFound:
		// A connection, a target or a reader the member may not see answers
		// the same as one that is not there, so neither tells them anything
		// they did not know.
		return domain.ErrNotFound
	case status == http.StatusTooManyRequests:
		return domain.ErrRateLimited
	case status >= 500:
		return fmt.Errorf("%w: apps answered %d", domain.ErrProviderUnavailable, status)
	default:
		return fmt.Errorf("apps answered %d: %w", status, domain.ErrInvalid)
	}
}
