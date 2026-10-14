package edgedispatch

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
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
)

// What a result is posted with: the edge Worker is the audience, and
// voice-result the purpose its verifier allows agents.
const (
	EdgeAudience  = "edge"
	ResultPurpose = "voice-result"
)

const (
	// attemptTimeout bounds one attempt to post a result.
	attemptTimeout = 10 * time.Second
	// attempts is how many times a result is posted before it is given up:
	// the voice agent then tells the member it lost the answer, which is in
	// their thread all the same.
	attempts = 4
	// firstBackoff doubles after each failed attempt.
	firstBackoff = 500 * time.Millisecond
)

// Signer proves which service is calling. serviceauth.Signer satisfies it,
// and so will the edge-token issuer (F3) once it lands.
type Signer interface {
	Token(ctx context.Context, audience, purpose string) (string, error)
}

// EdgeConfig configures an EdgeClient.
type EdgeConfig struct {
	// BaseURL is the edge Worker's origin, such as https://edge.oryxa.in. It
	// must be HTTPS or loopback HTTP, since a service token travels on it.
	BaseURL string
	Signer  Signer
	// HTTP is copied and made to refuse redirects.
	HTTP *http.Client
	// Sleep waits for d, or for ctx to end. Tests replace it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// EdgeClient posts dispatched runs' results to the room's voice agent.
type EdgeClient struct {
	baseURL string
	signer  Signer
	http    *http.Client
	sleep   func(ctx context.Context, d time.Duration) error
}

// NewEdgeClient returns a client for the edge at config.BaseURL.
func NewEdgeClient(config EdgeConfig) (*EdgeClient, error) {
	baseURL, err := runtime.ValidateAPIURL(strings.TrimSpace(config.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("edgedispatch: the edge's URL: %w", err)
	}
	if config.Signer == nil {
		return nil, errors.New("edgedispatch: a service token signer is required")
	}
	httpClient := config.HTTP
	if httpClient == nil {
		httpClient = &http.Client{Timeout: attemptTimeout, Transport: observability.Transport(nil)}
	}
	copied := *httpClient
	copied.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	sleep := config.Sleep
	if sleep == nil {
		sleep = func(ctx context.Context, d time.Duration) error {
			timer := time.NewTimer(d)
			defer timer.Stop()
			select {
			case <-timer.C:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return &EdgeClient{baseURL: baseURL, signer: config.Signer, http: &copied, sleep: sleep}, nil
}

// ResultPath is where the room's voice agent takes a result:
// POST /v1/orgs/{org}/rooms/{kind}/{id}/voice/result.
func ResultPath(result Result) string {
	return "/v1/orgs/" + url.PathEscape(result.Organization) + "/rooms/" + url.PathEscape(result.Room.Kind) + "/" +
		url.PathEscape(result.Room.ID) + "/voice/result"
}

// Deliver posts the result, again after a network error, a 429 or a 5xx,
// and not after any other refusal.
func (client *EdgeClient) Deliver(ctx context.Context, result Result) error {
	encoded, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("edgedispatch: encode the result: %w", err)
	}
	target := client.baseURL + ResultPath(result)
	backoff := firstBackoff
	for attempt := 1; ; attempt++ {
		retry, err := client.once(ctx, target, encoded)
		if err == nil {
			return nil
		}
		if !retry || attempt == attempts {
			return err
		}
		if err := client.sleep(ctx, backoff); err != nil {
			return err
		}
		backoff *= 2
	}
}

// once makes one attempt, and says whether a failed one is worth another.
func (client *EdgeClient) once(ctx context.Context, target string, encoded []byte) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, attemptTimeout)
	defer cancel()
	token, err := client.signer.Token(ctx, EdgeAudience, ResultPurpose)
	if err != nil {
		return true, fmt.Errorf("edgedispatch: sign this service's token: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(encoded))
	if err != nil {
		return false, fmt.Errorf("edgedispatch: build the request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(serviceauth.Header, token)
	response, err := client.http.Do(request)
	if err != nil {
		// A transport error names the URL, which holds the room's ids:
		// they are the edge's own, not secrets, so it is kept.
		return true, fmt.Errorf("edgedispatch: the edge did not answer: %w", err)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 64<<10))
	switch {
	case response.StatusCode >= 200 && response.StatusCode < 300:
		return false, nil
	case response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= 500:
		return true, fmt.Errorf("edgedispatch: the edge answered %d", response.StatusCode)
	default:
		return false, fmt.Errorf("edgedispatch: the edge refused the result: %d", response.StatusCode)
	}
}
