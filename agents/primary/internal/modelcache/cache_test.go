package modelcache

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
)

type fakeStore struct {
	name   string
	err    error
	models []string
}

func (s *fakeStore) Ensure(_ context.Context, modelName string, _ Prefix) (string, error) {
	s.models = append(s.models, modelName)
	return s.name, s.err
}

// recordingModel captures the request the wrapper forwarded.
type recordingModel struct {
	name     string
	requests []*model.LLMRequest
}

func (m *recordingModel) Name() string { return m.name }

func (m *recordingModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.requests = append(m.requests, req)
	return func(func(*model.LLMResponse, error) bool) {}
}

func largeInstruction() *genai.Content {
	return genai.NewContentFromText(strings.Repeat("x", 8000), genai.RoleUser)
}

func TestLargePrefixIsServedFromCache(t *testing.T) {
	inner := &recordingModel{name: "gemini-test"}
	store := &fakeStore{name: "cachedContents/abc"}
	cachedModel := Cached(inner, store, Config{TTL: time.Hour, MinTokens: 1024})
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{
		SystemInstruction: largeInstruction(),
		Tools:             []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "notes_add"}}}},
	}}

	for range cachedModel.GenerateContent(context.Background(), req, false) {
	}

	if len(store.models) != 1 || store.models[0] != "gemini-test" {
		t.Fatalf("Ensure() calls = %v, want the model name once", store.models)
	}
	if req.Config.CachedContent != "cachedContents/abc" {
		t.Fatalf("CachedContent = %q", req.Config.CachedContent)
	}
	if req.Config.SystemInstruction != nil || req.Config.Tools != nil {
		t.Fatal("the cached prefix was left on the request")
	}
	if len(inner.requests) != 1 {
		t.Fatalf("inner model got %d requests, want 1", len(inner.requests))
	}
}

func TestSmallPrefixIsLeftToImplicitCaching(t *testing.T) {
	inner := &recordingModel{name: "gemini-test"}
	store := &fakeStore{name: "cachedContents/abc"}
	cachedModel := Cached(inner, store, Config{TTL: time.Hour, MinTokens: 1024})
	instruction := genai.NewContentFromText("You are Krude.", genai.RoleUser)
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: instruction}}

	for range cachedModel.GenerateContent(context.Background(), req, false) {
	}

	if len(store.models) != 0 {
		t.Fatalf("Ensure() was called %d times for a small prefix", len(store.models))
	}
	if req.Config.CachedContent != "" || req.Config.SystemInstruction != instruction {
		t.Fatal("a small prefix was modified")
	}
}

func TestCacheFailureFallsBackToThePlainRequest(t *testing.T) {
	inner := &recordingModel{name: "gemini-test"}
	store := &fakeStore{err: errors.New("no cache for you")}
	cachedModel := Cached(inner, store, Config{TTL: time.Hour, MinTokens: 1024})
	instruction := largeInstruction()
	req := &model.LLMRequest{Config: &genai.GenerateContentConfig{SystemInstruction: instruction}}

	for range cachedModel.GenerateContent(context.Background(), req, false) {
	}

	if req.Config.CachedContent != "" {
		t.Fatalf("CachedContent = %q after a cache failure", req.Config.CachedContent)
	}
	if req.Config.SystemInstruction != instruction {
		t.Fatal("a failed cache dropped the system instruction")
	}
	if len(inner.requests) != 1 {
		t.Fatal("the request was not forwarded")
	}
}

func TestNilStoreTurnsCachingOff(t *testing.T) {
	inner := &recordingModel{name: "gemini-test"}
	if got := Cached(inner, nil, Config{}); got != model.LLM(inner) {
		t.Fatalf("Cached(nil store) = %T, want the inner model", got)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(Prefix{Instruction: genai.NewContentFromText(strings.Repeat("x", 400), genai.RoleUser)}); got != 100 {
		t.Fatalf("EstimateTokens() = %d, want 100", got)
	}
	if got := EstimateTokens(Prefix{}); got != 0 {
		t.Fatalf("EstimateTokens(empty) = %d, want 0", got)
	}
}

type fakeCaches struct {
	created    int
	deleted    []string
	configs    []*genai.CreateCachedContentConfig
	failWith   error
	deleteWith error
}

func (f *fakeCaches) Create(_ context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
	if f.failWith != nil {
		return nil, f.failWith
	}
	f.created++
	f.configs = append(f.configs, config)
	return &genai.CachedContent{Name: fmt.Sprintf("cachedContents/%d", f.created)}, nil
}

func (f *fakeCaches) Delete(_ context.Context, name string, _ *genai.DeleteCachedContentConfig) (*genai.DeleteCachedContentResponse, error) {
	f.deleted = append(f.deleted, name)
	if f.deleteWith != nil {
		return nil, f.deleteWith
	}
	return &genai.DeleteCachedContentResponse{}, nil
}

func TestGenaiStoreReusesALiveCache(t *testing.T) {
	caches := &fakeCaches{}
	now := time.Now()
	store := newStore(caches, time.Hour, func() time.Time { return now })
	prefix := Prefix{Instruction: largeInstruction()}

	first, err := store.Ensure(context.Background(), "gemini-test", prefix)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Ensure(context.Background(), "gemini-test", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if first != second || caches.created != 1 {
		t.Fatalf("names = %q, %q with %d creates, want one cache", first, second, caches.created)
	}
}

func TestGenaiStoreRecreatesNearExpiry(t *testing.T) {
	caches := &fakeCaches{}
	now := time.Now()
	store := newStore(caches, time.Hour, func() time.Time { return now })
	prefix := Prefix{Instruction: largeInstruction()}

	if _, err := store.Ensure(context.Background(), "gemini-test", prefix); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour - time.Minute)
	second, err := store.Ensure(context.Background(), "gemini-test", prefix)
	if err != nil {
		t.Fatal(err)
	}
	if caches.created != 2 {
		t.Fatalf("%d creates, want a refresh", caches.created)
	}
	if len(caches.deleted) != 1 || caches.deleted[0] != "cachedContents/1" {
		t.Fatalf("deleted = %v, want the previous cache", caches.deleted)
	}
	if second != "cachedContents/2" {
		t.Fatalf("name = %q, want the refreshed cache", second)
	}
}

func TestGenaiStoreCachesEachPrefixSeparately(t *testing.T) {
	caches := &fakeCaches{}
	store := newStore(caches, time.Hour, time.Now)

	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: largeInstruction()}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: genai.NewContentFromText(strings.Repeat("y", 8000), genai.RoleUser)}); err != nil {
		t.Fatal(err)
	}
	if caches.created != 2 {
		t.Fatalf("%d creates, want one per prefix", caches.created)
	}
}

func TestGenaiStoreCreateFailure(t *testing.T) {
	caches := &fakeCaches{failWith: errors.New("quota")}
	store := newStore(caches, time.Hour, time.Now)
	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: largeInstruction()}); err == nil {
		t.Fatal("Ensure() error = nil when the cache API fails")
	}
}

func TestGenaiStoreCloseDeletesItsCaches(t *testing.T) {
	caches := &fakeCaches{}
	store := newStore(caches, time.Hour, time.Now)
	for _, text := range []string{"x", "y"} {
		if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: genai.NewContentFromText(strings.Repeat(text, 8000), genai.RoleUser)}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caches.deleted) != 2 {
		t.Fatalf("deleted %v, want both caches", caches.deleted)
	}
}

func TestGenaiStoreCloseLeavesExpiredCachesToVertex(t *testing.T) {
	caches := &fakeCaches{}
	now := time.Now()
	store := newStore(caches, time.Hour, func() time.Time { return now })
	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: largeInstruction()}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(time.Hour)
	if err := store.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(caches.deleted) != 0 {
		t.Fatalf("deleted %v, want none: Vertex deleted the expired cache itself", caches.deleted)
	}
}

func TestGenaiStoreCloseTakesAGoneCacheAsDeleted(t *testing.T) {
	caches := &fakeCaches{deleteWith: genai.APIError{Code: http.StatusNotFound, Status: "404 Not Found"}}
	store := newStore(caches, time.Hour, time.Now)
	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: largeInstruction()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(context.Background()); err != nil {
		t.Fatalf("Close: %v, want a cache Vertex no longer has taken as deleted", err)
	}
}

func TestGenaiStoreCloseReportsAFailedDelete(t *testing.T) {
	caches := &fakeCaches{deleteWith: genai.APIError{Code: http.StatusForbidden, Status: "403 Forbidden"}}
	store := newStore(caches, time.Hour, time.Now)
	if _, err := store.Ensure(context.Background(), "gemini-test", Prefix{Instruction: largeInstruction()}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(context.Background()); err == nil {
		t.Fatal("Close succeeded, want the refused delete reported")
	}
}

// gatedCaches is a cache API whose creates wait to be let go, so a test can
// hold them in flight and see which of them run at the same time. It is safe
// for concurrent use.
type gatedCaches struct {
	started  chan string
	release  chan struct{}
	failWith error

	mu       sync.Mutex
	attempts int
	created  int
	deleted  []string
}

func newGatedCaches() *gatedCaches {
	return &gatedCaches{started: make(chan string, 100), release: make(chan struct{})}
}

func (g *gatedCaches) Create(ctx context.Context, _ string, config *genai.CreateCachedContentConfig) (*genai.CachedContent, error) {
	g.mu.Lock()
	g.attempts++
	g.mu.Unlock()
	g.started <- config.DisplayName
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if g.failWith != nil {
		return nil, g.failWith
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.created++
	return &genai.CachedContent{Name: fmt.Sprintf("cachedContents/%d", g.created)}, nil
}

func (g *gatedCaches) Delete(_ context.Context, name string, _ *genai.DeleteCachedContentConfig) (*genai.DeleteCachedContentResponse, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.deleted = append(g.deleted, name)
	return &genai.DeleteCachedContentResponse{}, nil
}

func (g *gatedCaches) counts() (attempts, created int, deleted []string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.attempts, g.created, append([]string(nil), g.deleted...)
}

// awaitStarts waits for n creates to have started, failing the test when
// they do not within a few seconds, as they would not behind a lock.
func (g *gatedCaches) awaitStarts(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-g.started:
		case <-time.After(5 * time.Second):
			t.Fatalf("fewer than %d creates started: a create waited for another", n)
		}
	}
}

func prefixOf(text string) Prefix {
	return Prefix{Instruction: genai.NewContentFromText(strings.Repeat(text, 8000), genai.RoleUser)}
}

// ensured is the outcome of one Ensure call made in the background.
type ensured struct {
	name string
	err  error
}

// ensureInBackground calls Ensure in a goroutine that is running by the time it
// returns.
func ensureInBackground(ctx context.Context, store *GenaiStore, prefix Prefix) <-chan ensured {
	outcome := make(chan ensured, 1)
	running := make(chan struct{})
	go func() {
		close(running)
		name, err := store.Ensure(ctx, "gemini-test", prefix)
		outcome <- ensured{name, err}
	}()
	<-running
	return outcome
}

func awaitEnsured(t *testing.T, outcome <-chan ensured) ensured {
	t.Helper()
	select {
	case done := <-outcome:
		return done
	case <-time.After(5 * time.Second):
		t.Fatal("Ensure did not return")
		return ensured{}
	}
}

// settle gives goroutines that were just started time to reach the point a
// test is checking, for the tests that assert something did not happen.
func settle() { time.Sleep(50 * time.Millisecond) }

func TestGenaiStoreCreatesDifferentPrefixesAtTheSameTime(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	first := ensureInBackground(context.Background(), store, prefixOf("x"))
	second := ensureInBackground(context.Background(), store, prefixOf("y"))

	// Both creates are in flight before either is let go.
	caches.awaitStarts(t, 2)
	close(caches.release)

	names := map[string]bool{}
	for _, outcome := range []<-chan ensured{first, second} {
		done := awaitEnsured(t, outcome)
		if done.err != nil {
			t.Fatal(done.err)
		}
		names[done.name] = true
	}
	if len(names) != 2 {
		t.Fatalf("names = %v, want one cache per prefix", names)
	}
}

func TestGenaiStoreCreatesOneCachePerPrefixAtATime(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	var outcomes []<-chan ensured
	for range 5 {
		outcomes = append(outcomes, ensureInBackground(context.Background(), store, prefixOf("x")))
	}

	caches.awaitStarts(t, 1)
	settle()
	if attempts, _, _ := caches.counts(); attempts != 1 {
		t.Fatalf("%d creates started for one prefix, want the runs to share one", attempts)
	}
	close(caches.release)

	for _, outcome := range outcomes {
		if done := awaitEnsured(t, outcome); done.err != nil || done.name != "cachedContents/1" {
			t.Fatalf("Ensure() = %q, %v, want the shared cache", done.name, done.err)
		}
	}
	if attempts, created, _ := caches.counts(); attempts != 1 || created != 1 {
		t.Fatalf("%d attempts and %d caches, want one of each", attempts, created)
	}
}

func TestGenaiStoreSharesAFailedCreate(t *testing.T) {
	caches := newGatedCaches()
	caches.failWith = errors.New("quota")
	store := newStore(caches, time.Hour, time.Now)
	var outcomes []<-chan ensured
	for range 3 {
		outcomes = append(outcomes, ensureInBackground(context.Background(), store, prefixOf("x")))
	}

	caches.awaitStarts(t, 1)
	settle()
	close(caches.release)

	for _, outcome := range outcomes {
		if done := awaitEnsured(t, outcome); done.err == nil {
			t.Fatal("Ensure() error = nil when the create failed")
		}
	}
	// Runs that were waiting share the failure instead of queueing to fail
	// one after another against an API that is refusing.
	if attempts, _, _ := caches.counts(); attempts != 1 {
		t.Fatalf("%d creates, want the waiting runs to share the one that failed", attempts)
	}
}

func TestGenaiStoreStopsWaitingWhenItsRunEnds(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	outcome := ensureInBackground(ctx, store, prefixOf("x"))

	caches.awaitStarts(t, 1)
	cancel()
	if done := awaitEnsured(t, outcome); !errors.Is(done.err, context.Canceled) {
		t.Fatalf("Ensure() error = %v, want the run's cancellation", done.err)
	}

	// The create went on without the run, and the next run finds its cache.
	close(caches.release)
	next := awaitEnsured(t, ensureInBackground(context.Background(), store, prefixOf("x")))
	if next.err != nil || next.name != "cachedContents/1" {
		t.Fatalf("Ensure() = %q, %v, want the cache the cancelled run started", next.name, next.err)
	}
	if attempts, created, _ := caches.counts(); attempts != 1 || created != 1 {
		t.Fatalf("%d attempts and %d caches, want the one create kept", attempts, created)
	}
}

func TestGenaiStoreDoesNotFailARunBecauseAnotherWasCancelled(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	starter, cancel := context.WithCancel(context.Background())
	first := ensureInBackground(starter, store, prefixOf("x"))
	caches.awaitStarts(t, 1)
	second := ensureInBackground(context.Background(), store, prefixOf("x"))
	settle()

	cancel()
	awaitEnsured(t, first)
	close(caches.release)

	if done := awaitEnsured(t, second); done.err != nil || done.name != "cachedContents/1" {
		t.Fatalf("Ensure() = %q, %v, want the cache the cancelled run started", done.name, done.err)
	}
}

func TestGenaiStoreCreatesNothingForARunThatIsOver(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := store.Ensure(ctx, "gemini-test", prefixOf("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Ensure() error = %v, want the run's cancellation", err)
	}
	if attempts, _, _ := caches.counts(); attempts != 0 {
		t.Fatalf("%d creates for a run that was over", attempts)
	}
}

func TestGenaiStoreCloseWaitsForACreateInFlight(t *testing.T) {
	caches := newGatedCaches()
	store := newStore(caches, time.Hour, time.Now)
	outcome := ensureInBackground(context.Background(), store, prefixOf("x"))
	caches.awaitStarts(t, 1)

	closed := make(chan error, 1)
	go func() { closed <- store.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned (%v) with a create in flight, which would leave its cache behind", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(caches.release)

	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Close did not return once the create finished")
	}
	awaitEnsured(t, outcome)
	if _, _, deleted := caches.counts(); len(deleted) != 1 || deleted[0] != "cachedContents/1" {
		t.Fatalf("deleted %v, want the cache the create made", deleted)
	}
}
