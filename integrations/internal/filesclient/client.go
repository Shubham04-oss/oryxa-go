// Package filesclient asks the files service for object bytes. Integrations
// never talks to S3: the files process is the only AWS principal on the bucket.
package filesclient

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	PurposePiece       = "integrations.piece"
	maxContentBytes    = 8 << 20
	defaultHTTPTimeout = 60 * time.Second
)

var (
	ErrUnauthorized = errors.New("files authentication failed")
	ErrForbidden    = errors.New("files operation is not allowed")
	ErrNotFound     = errors.New("file was not found")
	ErrInvalid      = errors.New("files request is invalid")
)

type Credentials struct {
	Authorization string
}

func (credentials Credentials) Present() bool {
	return strings.TrimSpace(credentials.Authorization) != ""
}

type Object struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	SHA256      string `json:"sha256"`
}

type CreateInput struct {
	Filename    string
	ContentType string
	Purpose     string
	Content     []byte
}

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		http:    &http.Client{Timeout: defaultHTTPTimeout},
	}
}

func (client *Client) Create(ctx context.Context, credentials Credentials, organizationID string, input CreateInput) (Object, error) {
	if client == nil || client.baseURL == "" {
		return Object{}, fmt.Errorf("files service is not configured: %w", ErrInvalid)
	}
	purpose := strings.TrimSpace(input.Purpose)
	if purpose == "" {
		purpose = PurposePiece
	}
	contentType := strings.TrimSpace(input.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	body, err := json.Marshal(map[string]any{
		"filename":     input.Filename,
		"content_type": contentType,
		"purpose":      purpose,
		"scope":        "member",
		"content":      base64.StdEncoding.EncodeToString(input.Content),
	})
	if err != nil {
		return Object{}, fmt.Errorf("encode files create: %w", err)
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/files"
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return Object{}, fmt.Errorf("build files create: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if err := applyCredentials(request, credentials); err != nil {
		return Object{}, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return Object{}, fmt.Errorf("create file: %w", err)
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return Object{}, err
	}
	var object Object
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&object); err != nil {
		return Object{}, fmt.Errorf("decode files create: %w", err)
	}
	if object.ID == "" || object.Filename == "" {
		return Object{}, fmt.Errorf("files create returned an incomplete object")
	}
	return object, nil
}

func (client *Client) Content(ctx context.Context, credentials Credentials, organizationID, fileID string) (Object, []byte, error) {
	if client == nil || client.baseURL == "" {
		return Object{}, nil, fmt.Errorf("files service is not configured: %w", ErrInvalid)
	}
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/files/" + url.PathEscape(fileID) + "/content"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return Object{}, nil, fmt.Errorf("build files content: %w", err)
	}
	if err := applyCredentials(request, credentials); err != nil {
		return Object{}, nil, err
	}
	response, err := client.http.Do(request)
	if err != nil {
		return Object{}, nil, fmt.Errorf("read file content: %w", err)
	}
	defer response.Body.Close()
	if err := statusError(response.StatusCode); err != nil {
		return Object{}, nil, err
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxContentBytes+1))
	if err != nil {
		return Object{}, nil, fmt.Errorf("read file content: %w", err)
	}
	if len(body) > maxContentBytes {
		return Object{}, nil, fmt.Errorf("file exceeds %d bytes: %w", maxContentBytes, ErrInvalid)
	}
	filename := filenameFromDisposition(response.Header.Get("Content-Disposition"))
	return Object{
		ID:          fileID,
		Filename:    filename,
		ContentType: response.Header.Get("Content-Type"),
		SizeBytes:   int64(len(body)),
		SHA256:      response.Header.Get("X-Oryxa-SHA256"),
	}, body, nil
}

func applyCredentials(request *http.Request, credentials Credentials) error {
	if !credentials.Present() {
		return ErrUnauthorized
	}
	request.Header.Set("Authorization", credentials.Authorization)
	return nil
}

func statusError(status int) error {
	switch status {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusUnauthorized:
		return ErrUnauthorized
	case http.StatusForbidden:
		return ErrForbidden
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusBadRequest:
		return ErrInvalid
	default:
		return fmt.Errorf("files status %d", status)
	}
}

func filenameFromDisposition(value string) string {
	_, params, err := mime.ParseMediaType(value)
	if err == nil {
		if name := strings.TrimSpace(params["filename"]); name != "" {
			return name
		}
	}
	return "file"
}
