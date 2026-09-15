package modelcache

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"
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
	created  int
	deleted  []string
	configs  []*genai.CreateCachedContentConfig
	failWith error
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
