// Package filesclient fetches a document's bytes from the files service.
//
// Rag never talks to the bucket. Files owns object storage and decides who may
// read an object, so this forwards the caller's credentials and lets that
// decision stay in one place: a member who cannot read a file cannot index it
// either, without rag having to know why.
//
// The fetch happens while the caller is still on the request, not later in the
// ingest worker. That is what keeps the worker from ever needing to act as
// anyone, and it is why the document row holds the text it was cut from.
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

	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: 30 * time.Second, Transport: observability.Transport(nil)},
	}
}

type metadataResponse struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// Fetch reads a file's metadata and its bytes as the caller.
func (client *Client) Fetch(ctx context.Context, organizationID, fileID string, credentials authclient.Credentials) (domain.SourceContent, error) {
	if !credentials.Present() {
		return domain.SourceContent{}, domain.ErrUnauthorized
	}
	base := "/v1/organizations/" + url.PathEscape(organizationID) + "/files/" + url.PathEscape(fileID)

	// Metadata first: it is what says the content type and the filename, and
	// it is the cheaper of the two calls to be refused on.
	var metadata metadataResponse
	if _, err := client.get(ctx, base, credentials, &metadata); err != nil {
		return domain.SourceContent{}, err
	}
	content, err := client.get(ctx, base+"/content", credentials, nil)
	if err != nil {
		return domain.SourceContent{}, err
	}
	return domain.SourceContent{
		ID:          metadata.ID,
		Filename:    metadata.Filename,
		ContentType: metadata.ContentType,
		Body:        string(content),
	}, nil
}

// get reads a files response, decoding it as JSON when target is set and
// returning the raw bytes otherwise.
func (client *Client) get(ctx context.Context, path string, credentials authclient.Credentials, target any) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("build files request: %w", err)
	}
	if target != nil {
		request.Header.Set("Accept", "application/json")
	}
	if credentials.Authorization != "" {
		request.Header.Set("Authorization", credentials.Authorization)
	} else {
		request.AddCookie(&http.Cookie{Name: authclient.AccessCookieName, Value: credentials.AccessCookie})
	}

	response, err := client.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("ask files: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	switch response.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, domain.ErrUnauthorized
	case http.StatusForbidden:
		return nil, domain.ErrForbidden
	case http.StatusNotFound:
		// Files conceals what the caller may not see, and so does rag.
		return nil, domain.ErrNotFound
	default:
		return nil, fmt.Errorf("ask files: status %d", response.StatusCode)
	}
	// Bounded by what a document may hold, so a file larger than rag will
	// index is refused by the size check rather than read into memory whole.
	limited := io.LimitReader(response.Body, domain.MaxDocumentBytes+1)
	if target != nil {
		if err := json.NewDecoder(io.LimitReader(limited, 1<<20)).Decode(target); err != nil {
			return nil, fmt.Errorf("decode files response: %w", err)
		}
		return nil, nil
	}
	content, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read the file: %w", err)
	}
	if len(content) > domain.MaxDocumentBytes {
		return nil, fmt.Errorf("the file is larger than %d bytes: %w", domain.MaxDocumentBytes, domain.ErrInvalid)
	}
	return content, nil
}
