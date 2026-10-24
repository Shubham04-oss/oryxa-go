package jev

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
)

const (
	// DefaultBaseURL is TypeSafe's API.
	DefaultBaseURL = "https://api.typesafe.ai"
	// DefaultModel follows TypeSafe's stable release.
	DefaultModel = "jev-latest"
	// DefaultMaxResponseBytes bounds how much of a response is read, so a
	// huge or runaway body cannot exhaust memory over one answer.
	DefaultMaxResponseBytes = 1 << 20
	// DefaultMaxAttempts is how many times a call is made in all when
	// TypeSafe says it is throttled (429) or overloaded (529) — its own
	// documented transient statuses. Nothing else is retried.
	DefaultMaxAttempts = 4

	systemOnePath = "/v1/systemone"
	// workersAIRun marks a Cloudflare Workers AI model endpoint
	// (…/ai/run/@cf/cloudflare/clef-flash). Cloudflare's Clef serves the same
	// System One API there, so it stands in for Jev by address alone.
	workersAIRun = "/ai/run/"
)

// Endpoint is where a System One request for base goes: TypeSafe's
// /v1/systemone under base, or base itself when it is a Workers AI model
// endpoint.
func Endpoint(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.Contains(base, workersAIRun) {
		return base
	}
	return base + systemOnePath
}

// ModelFor is the model a request to base names when none is configured: a
// Workers AI model's own name ("clef-flash" for …/@cf/cloudflare/clef-flash),
// which is all Clef accepts, or [DefaultModel] for TypeSafe.
func ModelFor(base string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if strings.Contains(base, workersAIRun) {
		return base[strings.LastIndex(base, "/")+1:]
	}
	return DefaultModel
}

// Unwrap returns a System One response body, taken out of the envelope
// Workers AI answers in ({"result": …, "success": …, "errors": […]}).
// TypeSafe's bare response passes through unchanged.
func Unwrap(data []byte) ([]byte, error) {
	var envelope struct {
		Result  json.RawMessage `json:"result"`
		Success *bool           `json:"success"`
		Errors  []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if json.Unmarshal(data, &envelope) != nil || envelope.Success == nil {
		return data, nil
	}
	if !*envelope.Success {
		messages := make([]string, 0, len(envelope.Errors))
		for _, e := range envelope.Errors {
			messages = append(messages, e.Message)
		}
		return nil, fmt.Errorf("jev: Workers AI refused the request: %s", strings.Join(messages, "; "))
	}
	return envelope.Result, nil
}

// Config configures a [Client].
type Config struct {
	// APIKey is TypeSafe's API key, sent as a bearer token on every
	// request. Required.
	APIKey string
	// BaseURL is TypeSafe's API, [DefaultBaseURL] when empty. Must be
	// HTTPS, except plain HTTP to this machine, which a test can use:
	// every request carries APIKey, so anything else would send it in the
	// clear.
	BaseURL string
	// Model is [DefaultModel] when empty.
	Model string
	// HTTPClient is http.DefaultClient when nil.
	HTTPClient *http.Client
	// MaxResponseBytes bounds a response body, [DefaultMaxResponseBytes]
	// when zero.
	MaxResponseBytes int64
	// MaxAttempts is how many times a call throttled or overloaded by
	// TypeSafe is retried in all, [DefaultMaxAttempts] when zero.
	MaxAttempts int
}

// Client calls TypeSafe's System One API. Build one with [New].
type Client struct {
	http             *http.Client
	endpoint         string
	apiKey           string
	model            string
	maxResponseBytes int64
	maxAttempts      int
	// wait is how long to sleep before retry attempt number attempt (1 for
	// the first retry after the original try); a test shortens it so
	// retries do not slow the test down.
	wait func(attempt int) time.Duration
}

// New builds a Client. It contacts nothing until [Client.Ask] is called.
func New(config Config) (*Client, error) {
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, errors.New("jev: an API key is required")
	}
	base := strings.TrimRight(strings.TrimSpace(config.BaseURL), "/")
	if base == "" {
		base = DefaultBaseURL
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("jev: base URL must be absolute, with no credentials, query or fragment")
	}
	switch host := parsed.Hostname(); {
	case parsed.Scheme == "https":
	case parsed.Scheme == "http" && (host == "localhost" || host == "127.0.0.1" || host == "::1"):
		// Plain HTTP only to this machine, which a test uses.
	default:
		return nil, errors.New("jev: base URL must use HTTPS, except on this machine: every request carries the API key")
	}
	model := strings.TrimSpace(config.Model)
	if model == "" {
		model = ModelFor(base)
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	maxResponseBytes := config.MaxResponseBytes
	if maxResponseBytes <= 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	maxAttempts := config.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = DefaultMaxAttempts
	}
	return &Client{
		http: httpClient, endpoint: Endpoint(parsed.String()), apiKey: strings.TrimSpace(config.APIKey),
		model: model, maxResponseBytes: maxResponseBytes, maxAttempts: maxAttempts,
		wait: func(attempt int) time.Duration { return time.Duration(attempt*attempt) * 250 * time.Millisecond },
	}, nil
}

// wireQuestion, wireRequest, wireAnswer and wireResponse are the API's own
// JSON shapes, kept private so [Question], [Request], [Answer] and [Result]
// stay free to evolve independently of the wire format.
type wireQuestion struct {
	Type         Type   `json:"type"`
	Instructions string `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

type wireRequest struct {
	Model     string                  `json:"model"`
	State     any                     `json:"state"`
	Questions map[string]wireQuestion `json:"questions"`
}

type wireAnswer struct {
	Type          Type               `json:"type"`
	Choice        string             `json:"choice,omitempty"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Confidence    float64            `json:"confidence,omitempty"`
	Noul          float64            `json:"noul,omitempty"`
}

type wireResponse struct {
	Model   string                `json:"model"`
	Answers map[string]wireAnswer `json:"answers"`
	Usage   struct {
		InputTokens int `json:"input_tokens"`
	} `json:"usage"`
}

// Ask sends every Question in request in a single call and returns Jev's
// answer to each — several questions evaluated in parallel over the same
// state, for the cost of one round trip. Questions are independent: Jev
// cannot see one answer while forming another, and Ask makes no attempt to
// reconcile them; that is the caller's policy, applied to the [Result] it
// gets back.
func (c *Client) Ask(ctx context.Context, request Request) (Result, error) {
	if len(request.Questions) == 0 {
		return Result{}, errors.New("jev: a request needs at least one question")
	}
	questions := make(map[string]wireQuestion, len(request.Questions))
	for id, q := range request.Questions {
		if q.Type != TypeChoice && q.Type != TypeNoul {
			return Result{}, fmt.Errorf("jev: question %q has an unsupported type %q", id, q.Type)
		}
		if strings.TrimSpace(q.Instructions) == "" {
			return Result{}, fmt.Errorf("jev: question %q has no instructions", id)
		}
		questions[id] = wireQuestion{Type: q.Type, Instructions: q.Instructions, Criteria: q.Criteria}
	}
	body, err := json.Marshal(wireRequest{Model: c.model, State: request.State, Questions: questions})
	if err != nil {
		return Result{}, fmt.Errorf("jev: encode request: %w", err)
	}
	wire, err := c.post(ctx, body)
	if err != nil {
		return Result{}, err
	}
	return resultFrom(wire, request.Questions)
}

// resultFrom turns the wire response into a [Result], checking it against
// what was actually asked: every question answered, answered as the type it
// was asked as, and every probability inside 0 to 1. Whether a Choice answer
// names one of the options its own Criteria offered is left to the caller:
// Criteria is caller-defined and opaque here, and a caller may have its own
// reason to see a stale or unrecognised choice rather than have it hidden
// behind an error — see [Answer].
func resultFrom(wire wireResponse, asked map[string]Question) (Result, error) {
	result := Result{Model: strings.TrimSpace(wire.Model), InputTokens: wire.Usage.InputTokens, Answers: make(map[string]Answer, len(asked))}
	for id, question := range asked {
		raw, ok := wire.Answers[id]
		if !ok {
			return Result{}, fmt.Errorf("jev: no answer for question %q", id)
		}
		if raw.Type != question.Type {
			return Result{}, fmt.Errorf("jev: question %q answered as %q, asked as %q", id, raw.Type, question.Type)
		}
		answer := Answer{Type: raw.Type}
		switch raw.Type {
		case TypeChoice:
			if raw.Choice == "" || raw.Probabilities == nil {
				return Result{}, fmt.Errorf("jev: question %q did not choose an option", id)
			}
			if !probability(raw.Confidence) {
				return Result{}, fmt.Errorf("jev: question %q gave a confidence outside 0 to 1", id)
			}
			for option, p := range raw.Probabilities {
				if !probability(p) {
					return Result{}, fmt.Errorf("jev: question %q gave %q a probability outside 0 to 1", id, option)
				}
			}
			// Whether Choice names one of the options a caller's own
			// Criteria actually offered is that caller's own policy to
			// check, not this package's: Criteria is caller-defined and
			// opaque here (any), and a caller such as a router that
			// discards an answer naming a seat that has since left the
			// room needs to see that mismatch rather than have it hidden
			// behind an error.
			answer.Choice, answer.Probabilities, answer.Confidence = raw.Choice, raw.Probabilities, raw.Confidence
		case TypeNoul:
			if !probability(raw.Noul) {
				return Result{}, fmt.Errorf("jev: question %q gave a noul outside 0 to 1", id)
			}
			answer.Noul = raw.Noul
		}
		result.Answers[id] = answer
	}
	return result, nil
}

func probability(p float64) bool { return p >= 0 && p <= 1 }

// post sends body to TypeSafe and returns its parsed response, retrying
// when it says it is throttled (429) or overloaded (529): the two statuses
// its API documents as transient. Any other failure — a different status, a
// response too large, a body that will not parse — is not retried, since
// trying the same request again would not help.
func (c *Client) post(ctx context.Context, body []byte) (wireResponse, error) {
	if err := ctx.Err(); err != nil {
		return wireResponse{}, fmt.Errorf("jev: %w", err)
	}
	var last error
	for attempt := 1; attempt <= c.maxAttempts; attempt++ {
		if attempt > 1 {
			select {
			case <-ctx.Done():
				return wireResponse{}, fmt.Errorf("jev: %w (last: %w)", ctx.Err(), last)
			case <-time.After(c.wait(attempt - 1)):
			}
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(body))
		if err != nil {
			return wireResponse{}, fmt.Errorf("jev: build request: %w", err)
		}
		request.Header.Set("Authorization", "Bearer "+c.apiKey)
		request.Header.Set("Content-Type", "application/json")
		response, err := c.http.Do(request)
		if err != nil {
			last = fmt.Errorf("jev: call TypeSafe: %w", err)
			continue
		}
		wire, retry, readErr := c.read(response)
		if readErr == nil {
			return wire, nil
		}
		last = readErr
		if !retry {
			break
		}
	}
	return wireResponse{}, last
}

// read reads one response and says whether a failed one is worth retrying.
func (c *Client) read(response *http.Response) (wireResponse, bool, error) {
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		retry := response.StatusCode == http.StatusTooManyRequests || response.StatusCode == 529
		return wireResponse{}, retry, fmt.Errorf("jev: TypeSafe answered HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, c.maxResponseBytes+1))
	if err != nil {
		return wireResponse{}, true, fmt.Errorf("jev: read response: %w", err)
	}
	if int64(len(data)) > c.maxResponseBytes {
		return wireResponse{}, false, fmt.Errorf("jev: response is larger than %d bytes", c.maxResponseBytes)
	}
	if data, err = Unwrap(data); err != nil {
		return wireResponse{}, false, err
	}
	var wire wireResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return wireResponse{}, false, fmt.Errorf("jev: decode response: %w", err)
	}
	return wire, false, nil
}
