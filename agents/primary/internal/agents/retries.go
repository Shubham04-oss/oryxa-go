package agents

import (
	"net/http"
	"time"

	"google.golang.org/genai"
)

// maxAttempts bounds WithAttempts, so a setting cannot make a run wait long
// on an endpoint that is down.
const maxAttempts = 6

// retryPolicy is how a call to Vertex AI that failed is made again. Only the
// request is repeated, and only until Vertex starts its answer: see Gemini.
type retryPolicy struct {
	// attempts is how many times a call is made before it fails, the first
	// time included.
	attempts int
	// initialDelay is the wait after the first attempt fails. Each wait is
	// twice the one before, no longer than maxDelay, with up to jitter added
	// so runs that failed together do not come back together.
	initialDelay, maxDelay, jitter time.Duration
}

// defaultRetries makes four attempts, waiting about 2, 4 and 8 seconds between
// them: a call that keeps failing gives up after 14 to 17 seconds. Vertex
// answers 429 when the project is over its shared quota and 5xx when it is
// having a bad moment, and both mostly pass within seconds, so this rides one
// out without holding a member's request for long.
var defaultRetries = retryPolicy{attempts: 4, initialDelay: 2 * time.Second, maxDelay: 10 * time.Second, jitter: time.Second}

// options is the policy as the Gen AI client takes it. Only 429 and 5xx
// answers are retried, so a request Vertex refuses for what it holds, such as
// a 400, fails at once. The client also retries a call that got no answer at
// all, a failure to get a credential included, until the run's context ends.
// It does not read Retry-After.
func (policy retryPolicy) options() *genai.HTTPRetryOptions {
	return &genai.HTTPRetryOptions{
		Attempts:     genai.Ptr(int32(policy.attempts)),
		InitialDelay: genai.Ptr(policy.initialDelay.Seconds()),
		MaxDelay:     genai.Ptr(policy.maxDelay.Seconds()),
		ExpBase:      genai.Ptr(2.0),
		Jitter:       genai.Ptr(policy.jitter.Seconds()),
		HTTPStatusCodes: []int32{
			http.StatusTooManyRequests,
			http.StatusInternalServerError,
			http.StatusBadGateway,
			http.StatusServiceUnavailable,
			http.StatusGatewayTimeout,
		},
	}
}

// GeminiOption adjusts the model Gemini builds.
type GeminiOption func(*geminiConfig)

type geminiConfig struct {
	retries retryPolicy
}

// WithAttempts sets how many times a call to Vertex AI is made before it
// fails, the first time included. One turns retries off, and more than
// maxAttempts is maxAttempts.
func WithAttempts(attempts int) GeminiOption {
	return func(config *geminiConfig) {
		config.retries.attempts = min(max(attempts, 1), maxAttempts)
	}
}
