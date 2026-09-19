// Package ragclient reads an organization's corpora and searches them
// through rag's API, as the member a run acts for: every call carries that
// member's bearer token, so rag decides what they may read exactly as it does
// for the member themselves.
package ragclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
)

const requestTimeout = 30 * time.Second

const maxResponseBytes = 8 << 20

// The ways asking rag can fail, each in words a model can repeat.
var (
	// ErrDenied reports that rag does not let the member read what was asked
	// for, or does not know them.
	ErrDenied = errors.New("the member cannot read that knowledge")
	// ErrNotFound reports a corpus rag does not have, or conceals.
	ErrNotFound = errors.New("no such knowledge collection")
	// ErrUnavailable reports that rag did not answer, or answered with a
	// server error; the caller may retry.
	ErrUnavailable = errors.New("the knowledge service did not answer; try again shortly")
)

// Corpus is one collection of documents the member can read.
type Corpus struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Slug          string `json:"slug"`
	Scope         string `json:"scope"`
	TeamID        string `json:"team_id,omitempty"`
	DocumentCount int    `json:"document_count"`
}

// Match is one passage a search found.
type Match struct {
	Chunk struct {
		Text        string   `json:"text"`
		HeadingPath []string `json:"heading_path"`
	} `json:"chunk"`
	Document struct {
		ID     string `json:"id"`
		Title  string `json:"title"`
		Source struct {
			Kind string `json:"kind"`
			Ref  string `json:"ref"`
		} `json:"source"`
	} `json:"document"`
	Score float64 `json:"score"`
}

// Client calls rag.
type Client struct {
	baseURL string
	http    *http.Client
}

// New returns a client for rag at baseURL. httpClient may be nil.
func New(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: requestTimeout, Transport: observability.Transport(nil)}
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), http: httpClient}
}

// Corpora lists the corpora the member can read by default: their own and
// their organization's.
func (client *Client) Corpora(ctx context.Context, organizationID, token string) ([]Corpus, error) {
	var answer struct {
		Corpora []Corpus `json:"corpora"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/corpora"
	if err := client.call(ctx, http.MethodGet, path, token, nil, &answer); err != nil {
		return nil, err
	}
	return answer.Corpora, nil
}

// Search finds the passages of one corpus that best match query.
func (client *Client) Search(ctx context.Context, organizationID, corpusID, token, query string, topK int, minScore float64) ([]Match, error) {
	var answer struct {
		Matches []Match `json:"matches"`
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/corpora/" + url.PathEscape(corpusID) + "/search"
	// Reranking reads the question and each passage together, which put the
	// answer first far more often when measured, and its scores come from one
	// model reading one question, so passages from different corpora can be
	// merged by them. A rag with no reranker answers in fused order instead.
	// minScore, when set, drops what the reranker judged irrelevant; a fused
	// score in the top results sits far above any such floor.
	body := map[string]any{"query": query, "top_k": topK, "rerank": true}
	if minScore > 0 {
		body["min_score"] = minScore
	}
	if err := client.call(ctx, http.MethodPost, path, token, body, &answer); err != nil {
		return nil, err
	}
	return answer.Matches, nil
}

func (client *Client) call(ctx context.Context, method, path, token string, body, target any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode rag request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequestWithContext(ctx, method, client.baseURL+path, reader)
	if err != nil {
		return fmt.Errorf("build rag request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := client.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return ErrUnavailable
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return ErrUnavailable
	}
	switch {
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		return ErrDenied
	case response.StatusCode == http.StatusNotFound:
		return ErrNotFound
	case response.StatusCode >= 500, response.StatusCode == http.StatusTooManyRequests:
		return ErrUnavailable
	case response.StatusCode < 200 || response.StatusCode >= 300:
		return fmt.Errorf("rag refused the request: %s", refusal(payload, response.StatusCode))
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("decode rag response: %w", err)
	}
	return nil
}

// refusal is rag's own reason for refusing a request, {"error": "..."},
// which says what to change, or the status when it gives none.
func refusal(payload []byte, status int) string {
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Error != "" {
		return body.Error
	}
	return http.StatusText(status)
}
