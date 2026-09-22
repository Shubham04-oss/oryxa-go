// Package knowledgeclient reads the knowledge service's indexing status as
// the member. It never registers or changes a source.
package knowledgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

var ErrUnavailable = errors.New("the knowledge index did not answer; try again shortly")
var ErrDenied = errors.New("the member cannot inspect those knowledge sources")
var ErrNeedsReconnect = errors.New("this source needs its app reconnected before it can be indexed")
var ErrNotFound = errors.New("no such indexing source is visible to the member")
var ErrConflict = errors.New("this indexing source is not active")

// Source is the status of one member-visible indexing source. No credentials
// or document bodies are returned by the knowledge API's source listing.
type Source struct {
	ID           string            `json:"id"`
	Provider     string            `json:"provider"`
	ConnectionID string            `json:"connection_id,omitempty"`
	Target       map[string]string `json:"target"`
	Status       string            `json:"status"`
	Reason       string            `json:"reason,omitempty"`
	SyncAt       *time.Time        `json:"sync_at,omitempty"`
	LastSyncedAt *time.Time        `json:"last_synced_at,omitempty"`
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second, Transport: observability.Transport(nil)}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// Sources lists only the sources the knowledge service permits this member
// to see. Filtering is done on the server; organization-wide listings are
// intentionally not requested by an agent.
func (client *Client) Sources(ctx context.Context, organizationID, token, provider, connectionID string) ([]Source, error) {
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/sources"
	query := url.Values{}
	if provider != "" {
		query.Set("provider", provider)
	}
	if connectionID != "" {
		query.Set("connection_id", connectionID)
	}
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	var result struct {
		Sources []Source `json:"sources"`
	}
	if err := client.call(ctx, http.MethodGet, path, token, &result); err != nil {
		return nil, err
	}
	return result.Sources, nil
}

// SyncSource schedules a new extraction of a source the member already owns.
// It does not wait for extraction or RAG ingestion to complete.
func (client *Client) SyncSource(ctx context.Context, organizationID, sourceID, token string) (Source, error) {
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/sources/" + url.PathEscape(sourceID) + "/sync"
	var source Source
	if err := client.call(ctx, http.MethodPost, path, token, &source); err != nil {
		return Source{}, err
	}
	return source, nil
}

func (client *Client) call(ctx context.Context, method, path, token string, target any) error {
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	switch {
	case response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden:
		return ErrDenied
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode == http.StatusConflict:
		var problem struct {
			Error struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&problem) == nil && problem.Error.Code == "needs_reconnect" {
			return ErrNeedsReconnect
		}
		return ErrConflict
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return ErrUnavailable
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return ErrUnavailable
	}
	return nil
}
