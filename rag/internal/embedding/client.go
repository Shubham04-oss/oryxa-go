package embedding

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/chunking"
	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// Defaults for how hard this service pushes a provider. Concurrency is
// deliberately low: Bedrock and Vertex both meter embeddings per second per
// model, and a backfill that opens thirty-two parallel calls spends most of
// its time being throttled and retried, which is slower than four calls that
// are not.
const (
	DefaultConcurrency = 4
	DefaultMaxAttempts = 5
	DefaultBaseDelay   = 200 * time.Millisecond
	DefaultMaxDelay    = 20 * time.Second
)

// batchTokenHeadroom is the share of a provider's per-call token limit a batch
// is packed to. Token counts here are estimates, biased high on ordinary prose
// but not on every script, and a call over the limit is refused whole.
const batchTokenHeadroom = 0.9

// Options tune batching and retries. Zero values take the defaults above.
type Options struct {
	// MaxBatch and MaxBatchTokens override what the catalog says the model
	// accepts. They are only useful for a model the catalog does not know.
	MaxBatch       int
	MaxBatchTokens int
	Concurrency    int
	MaxAttempts    int
	BaseDelay      time.Duration
	MaxDelay       time.Duration
	Logger         *slog.Logger
}

func (options Options) withDefaults(profile domain.ModelProfile) Options {
	if options.MaxBatch <= 0 {
		options.MaxBatch = max(profile.MaxBatch, 1)
	}
	if options.MaxBatchTokens <= 0 {
		options.MaxBatchTokens = profile.MaxBatchTokens
	}
	if options.Concurrency <= 0 {
		options.Concurrency = DefaultConcurrency
	}
	if options.MaxAttempts <= 0 {
		options.MaxAttempts = DefaultMaxAttempts
	}
	if options.BaseDelay <= 0 {
		options.BaseDelay = DefaultBaseDelay
	}
	if options.MaxDelay <= 0 {
		options.MaxDelay = DefaultMaxDelay
	}
	if options.Logger == nil {
		options.Logger = slog.New(slog.DiscardHandler)
	}
	return options
}

// Client is the Embedder every caller holds. It owns the parts that are the
// same whichever provider is behind it: splitting a request into batches the
// model accepts, running a bounded number at once, retrying what is worth
// retrying, and checking that what came back is the shape the corpus expects.
type Client struct {
	backend backend
	spec    domain.EmbeddingSpec
	profile domain.ModelProfile
	known   bool
	options Options
	// sleep is a seam for tests, which must not actually wait out a backoff.
	sleep func(context.Context, time.Duration) error
}

func newClient(transport backend, spec domain.EmbeddingSpec, options Options) *Client {
	profile, known := domain.Profile(spec.Provider, spec.Model)
	return &Client{
		backend: transport,
		spec:    spec,
		profile: profile,
		known:   known,
		options: options.withDefaults(profile),
		sleep:   sleepContext,
	}
}

func (client *Client) Spec() domain.EmbeddingSpec { return client.spec }

// Embed returns one vector per text, in the order the texts were given.
func (client *Client) Embed(ctx context.Context, kind domain.InputKind, texts []string) (Result, error) {
	if !kind.Valid() {
		return Result{}, fmt.Errorf("embedding input kind %q is not document or query: %w", kind, domain.ErrInvalid)
	}
	if len(texts) == 0 {
		return Result{}, nil
	}
	prepared := make([]string, len(texts))
	for index, text := range texts {
		if text == "" {
			return Result{}, fmt.Errorf("text %d is empty, which embeds to nothing: %w", index, domain.ErrInvalid)
		}
		cut, wasCut := truncate(text, client.profile.MaxInputTokens)
		if wasCut {
			// Worth a line in the log: on an ingest it means the chunk policy
			// and the model have drifted apart.
			client.options.Logger.WarnContext(ctx, "truncated text to the model's input limit",
				"model", client.spec.Model, "kind", kind, "limit_tokens", client.profile.MaxInputTokens)
		}
		prepared[index] = cut
	}

	batches := client.split(prepared)
	results := make([]Result, len(batches))
	if err := client.run(ctx, len(batches), func(ctx context.Context, index int) error {
		result, err := client.embedBatch(ctx, kind, batches[index].texts)
		if err != nil {
			return fmt.Errorf("embed batch %d of %d: %w", index+1, len(batches), err)
		}
		if len(result.Vectors) != len(batches[index].texts) {
			return fmt.Errorf("%s returned %d vectors for %d texts", client.spec.Model, len(result.Vectors), len(batches[index].texts))
		}
		results[index] = result
		return nil
	}); err != nil {
		return Result{}, err
	}

	combined := Result{Vectors: make([][]float32, 0, len(texts))}
	for _, result := range results {
		for _, vector := range result.Vectors {
			if len(vector) != client.spec.Dimensions {
				return Result{}, fmt.Errorf("%s returned %d dimensions but the corpus is %d: %w",
					client.spec.Model, len(vector), client.spec.Dimensions, domain.ErrConflict)
			}
			// A catalogued model is trusted to return unit vectors. Anything
			// else is normalized here, so cosine and inner product agree.
			if !client.known || !client.profile.Normalized {
				normalize(vector)
			}
			combined.Vectors = append(combined.Vectors, vector)
		}
		combined.Usage.Add(result.Usage)
		combined.Truncated += result.Truncated
	}
	combined.Usage.EmbeddedTexts = len(texts)
	if combined.Truncated > 0 {
		// The provider read less than it was sent, so part of each of these
		// texts is not in its vector. On an ingest it means chunks run longer
		// than the model reads.
		client.options.Logger.WarnContext(ctx, "the provider truncated texts to its input limit",
			"model", client.spec.Model, "kind", kind, "truncated", combined.Truncated, "texts", len(texts))
	}
	return combined, nil
}

type batch struct {
	texts []string
}

// split cuts texts into the batches one call carries: at most MaxBatch texts,
// and, where the model limits a call's tokens too, no more text than that
// limit less some headroom. A text is never split across batches, and none is
// over the limit alone, since each was already truncated to MaxInputTokens.
func (client *Client) split(texts []string) []batch {
	size := max(client.options.MaxBatch, 1)
	budget := int(float64(client.options.MaxBatchTokens) * batchTokenHeadroom)
	var (
		batches []batch
		start   int
		tokens  int
	)
	for index, text := range texts {
		cost := chunking.EstimateTokens(text)
		if index > start && (index-start >= size || (budget > 0 && tokens+cost > budget)) {
			batches = append(batches, batch{texts: texts[start:index]})
			start, tokens = index, 0
		}
		tokens += cost
	}
	if start < len(texts) {
		batches = append(batches, batch{texts: texts[start:]})
	}
	return batches
}

// embedBatch calls the provider, retrying what the provider said was worth
// retrying, with exponential backoff and full jitter. Jitter matters here
// because a backfill's batches all start together and would otherwise all
// retry together, rebuilding the burst that caused the throttle.
func (client *Client) embedBatch(ctx context.Context, kind domain.InputKind, texts []string) (Result, error) {
	var lastErr error
	for attempt := 1; attempt <= client.options.MaxAttempts; attempt++ {
		result, err := client.backend.embed(ctx, kind, texts)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		if errors.Is(err, ErrBatchTooLarge) && len(texts) > 1 {
			return client.embedHalves(ctx, kind, texts)
		}
		if !retryable(err) || attempt == client.options.MaxAttempts {
			return Result{}, err
		}
		delay := client.backoff(attempt)
		client.options.Logger.WarnContext(ctx, "retrying an embedding call",
			"model", client.spec.Model, "attempt", attempt, "delay", delay, "error", err)
		if err := client.sleep(ctx, delay); err != nil {
			return Result{}, err
		}
	}
	return Result{}, lastErr
}

// embedHalves embeds a batch the provider refused as too large in two halves,
// each of which may be halved again. The batch was sized by an estimate, and
// no estimate holds for every kind of text: code, and languages the model's
// vocabulary serves less well, cost more tokens than they look.
func (client *Client) embedHalves(ctx context.Context, kind domain.InputKind, texts []string) (Result, error) {
	client.options.Logger.WarnContext(ctx, "an embedding call was over the model's token limit; embedding it in halves",
		"model", client.spec.Model, "texts", len(texts))
	middle := len(texts) / 2
	first, err := client.embedBatch(ctx, kind, texts[:middle])
	if err != nil {
		return Result{}, err
	}
	second, err := client.embedBatch(ctx, kind, texts[middle:])
	if err != nil {
		return Result{}, err
	}
	first.Vectors = append(first.Vectors, second.Vectors...)
	first.Usage.Add(second.Usage)
	first.Truncated += second.Truncated
	return first, nil
}

// backoff is exponential with jitter, and never exceeds MaxDelay: the result
// lands in [delay/2, delay), so the ceiling is a ceiling rather than an
// average that half the waits overshoot.
func (client *Client) backoff(attempt int) time.Duration {
	delay := client.options.BaseDelay << min(attempt-1, 16)
	if delay > client.options.MaxDelay || delay <= 0 {
		delay = client.options.MaxDelay
	}
	half := int64(delay) / 2
	if half <= 0 {
		return delay
	}
	return time.Duration(half + rand.Int64N(half))
}

func retryable(err error) bool {
	return errors.Is(err, ErrThrottled) || errors.Is(err, ErrTransient)
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// run works through count items with at most Concurrency in flight, stopping
// the rest as soon as one fails so a broken run does not keep spending money.
func (client *Client) run(ctx context.Context, count int, work func(context.Context, int) error) error {
	if count == 1 {
		return work(ctx, 0)
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		group     sync.WaitGroup
		mutex     sync.Mutex
		firstErr  error
		semaphore = make(chan struct{}, min(client.options.Concurrency, count))
	)
	for index := range count {
		select {
		case semaphore <- struct{}{}:
		case <-ctx.Done():
			group.Wait()
			mutex.Lock()
			defer mutex.Unlock()
			if firstErr != nil {
				return firstErr
			}
			return ctx.Err()
		}
		group.Add(1)
		go func() {
			defer group.Done()
			defer func() { <-semaphore }()
			if err := work(ctx, index); err != nil {
				mutex.Lock()
				if firstErr == nil {
					firstErr = err
					cancel()
				}
				mutex.Unlock()
			}
		}()
	}
	group.Wait()
	mutex.Lock()
	defer mutex.Unlock()
	return firstErr
}
