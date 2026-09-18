// Package knowledgeclient reads back a document body that was too large to
// travel on the bus.
//
// Knowledge's events carry a body up to a limit and flag the rest as
// omitted, which used to mean the largest documents an organization has —
// the handbook, the specification, the long thread — were the ones it could
// not search. This is the way in for those: one document's body, named by an
// id nothing guesses, with this service's own identity on the request.
package knowledgeclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

const (
	requestTimeout = 30 * time.Second
	// maxBodyBytes bounds what one document's body may be. Knowledge keeps
	// at most 4 MiB of text per document; this leaves room and still refuses
	// an answer that is not one.
	maxBodyBytes = 8 << 20
)

var (
	// ErrNotFound means knowledge holds no such document, or holds it in
	// another organization.
	ErrNotFound = errors.New("knowledge holds no such document")
	// ErrUnavailable means knowledge did not answer usefully, which retrying
	// may fix.
	ErrUnavailable = errors.New("knowledge is unavailable")
	// ErrNotConfigured means this deployment cannot reach knowledge's
	// internal API — no URL, no signer, or knowledge does not admit this
	// service. None of those is fixed by asking again.
	ErrNotConfigured = errors.New("reading knowledge's documents is not configured")
)

// Signer proves which service is asking. serviceauth.Signer satisfies it.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

type Config struct {
	// BaseURL is knowledge's own URL.
	BaseURL string
	Signer  Signer
	HTTP    *http.Client
}

type Client struct {
	baseURL string
	signer  Signer
	http    *http.Client
}

func New(config Config) *Client {
	client := config.HTTP
	if client == nil {
		client = &http.Client{Timeout: requestTimeout}
	}
	return &Client{
		baseURL: strings.TrimRight(strings.TrimSpace(config.BaseURL), "/"),
		signer:  config.Signer,
		http:    client,
	}
}

// Document is what knowledge answered: enough to index the document, and
// nothing about who may read it, since that was decided before this was
// published.
type Document struct {
	ID          string `json:"id"`
	SourceID    string `json:"source_id"`
	ExternalID  string `json:"external_id"`
	MIME        string `json:"mime"`
	ContentHash string `json:"content_hash"`
	Body        string `json:"body"`
	ByteSize    int    `json:"byte_size"`
}

// Body reads one document's body from knowledge.
func (client *Client) Body(ctx context.Context, organizationID, documentID string) (Document, error) {
	if client == nil || client.baseURL == "" || client.signer == nil {
		return Document{}, ErrNotConfigured
	}
	token, err := client.signer.Token(ctx, "knowledge", "documents")
	if err != nil {
		return Document{}, fmt.Errorf("sign this service's token: %w", err)
	}
	path := "/v1/internal/organizations/" + url.PathEscape(organizationID) +
		"/documents/" + url.PathEscape(documentID) + "/body"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return Document{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set(serviceauth.Header, token)

	response, err := client.http.Do(request)
	if err != nil {
		return Document{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	defer response.Body.Close()

	switch {
	case response.StatusCode == http.StatusOK:
		var document Document
		if err := json.NewDecoder(io.LimitReader(response.Body, maxBodyBytes)).Decode(&document); err != nil {
			return Document{}, fmt.Errorf("%w: knowledge's answer could not be read", ErrUnavailable)
		}
		return document, nil
	case response.StatusCode == http.StatusNotFound:
		// Also what a deployment with no internal API answers, which is
		// indistinguishable from here and means the same thing to a caller:
		// there is no body to be had.
		return Document{}, ErrNotFound
	case response.StatusCode == http.StatusUnauthorized, response.StatusCode == http.StatusForbidden:
		// Knowledge does not admit this service. That is configuration.
		return Document{}, fmt.Errorf("%w: knowledge refused this service (status %d)", ErrNotConfigured, response.StatusCode)
	default:
		return Document{}, fmt.Errorf("%w: status %d", ErrUnavailable, response.StatusCode)
	}
}
