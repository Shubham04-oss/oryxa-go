// Package workspaceclient reads the workspace's member-visible ownership and
// organization-chart context for personal agents.
package workspaceclient

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/internal/observability"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func New(baseURL string, httpClient *http.Client) (*Client, error) {
	baseURL, err := runtime.ValidateAPIURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("workspace client URL: %w", err)
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	copyClient := *httpClient
	copyClient.Transport = observability.Transport(copyClient.Transport)
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &Client{baseURL: baseURL, http: &copyClient}, nil
}

func (client *Client) Load(ctx context.Context, organizationID, token string) (agents.DirectoryContext, error) {
	path := "/v1/organizations/" + url.PathEscape(organizationID) + "/workspace/context"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, client.baseURL+path, nil)
	if err != nil {
		return agents.DirectoryContext{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := client.http.Do(request)
	if err != nil {
		return agents.DirectoryContext{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return agents.DirectoryContext{}, fmt.Errorf("workspace context returned %d", response.StatusCode)
	}
	var result agents.DirectoryContext
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(&result); err != nil {
		return agents.DirectoryContext{}, fmt.Errorf("workspace context response: %w", err)
	}
	return result, nil
}
