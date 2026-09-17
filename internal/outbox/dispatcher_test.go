package outbox

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type failure struct {
	id, token  string
	retryAfter time.Duration
	message    string
}

// fakeStore records what a dispatcher asks of a table.
type fakeStore struct {
	mu        sync.Mutex
	pending   []Event
	tokens    []string
	published map[string]string
	failed    []failure
	released  []string
	// stale names events whose marks report that another claim owns them.
	stale map[string]bool
	// deletions is what successive DeletePublished calls report.
	deletions []int
	befores   []time.Time
}

func newFakeStore(events ...Event) *fakeStore {
	return &fakeStore{pending: events, published: map[string]string{}, stale: map[string]bool{}}
}

func (store *fakeStore) Name() string { return "test_outbox" }

func (store *fakeStore) Claim(_ context.Context, limit int, _ time.Duration, token string) ([]Event, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.tokens = append(store.tokens, token)
	claimed := store.pending[:min(limit, len(store.pending))]
	store.pending = store.pending[len(claimed):]
	return slices.Clone(claimed), nil
}

func (store *fakeStore) MarkPublished(_ context.Context, id, token string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.stale[id] {
		return false, nil
	}
	store.published[id] = token
	return true, nil
}

func (store *fakeStore) MarkFailed(_ context.Context, id, token string, retryAfter time.Duration, message string) (bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.failed = append(store.failed, failure{id, token, retryAfter, message})
	return true, nil
}

func (store *fakeStore) Release(_ context.Context, ids []string, _ string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.released = append(store.released, ids...)
	return nil
}

func (store *fakeStore) DeletePublished(_ context.Context, before time.Time, _ int) (int, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.befores = append(store.befores, before)
	if len(store.deletions) == 0 {
		return 0, nil
	}
	deleted := store.deletions[0]
	store.deletions = store.deletions[1:]
	return deleted, nil
}

func events(ids ...string) []Event {
	base := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	result := make([]Event, 0, len(ids))
	for index, id := range ids {
		result = append(result, Event{ID: id, DetailType: "test.happened.v1", OccurredAt: base.Add(time.Duration(index) * time.Second)})
	}
	return result
}

func TestFlushPublishesEachEventAndMarksItUnderTheClaimToken(t *testing.T) {
	store := newFakeStore(events("evt_1", "evt_2", "evt_3")...)
	var mu sync.Mutex
	var sent []string
	dispatcher := NewDispatcher(store, PublisherFunc(func(_ context.Context, event Event) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, event.ID)
		return nil
	}), nil)

	dispatcher.flush(context.Background())

	slices.Sort(sent)
	if !slices.Equal(sent, []string{"evt_1", "evt_2", "evt_3"}) {
		t.Fatalf("published = %v", sent)
	}
	if len(store.tokens) != 1 || store.tokens[0] == "" {
		t.Fatalf("claim tokens = %v", store.tokens)
	}
	for id, token := range store.published {
		if token != store.tokens[0] {
			t.Errorf("%s marked with %q, want the claim's token %q", id, token, store.tokens[0])
		}
	}
	if len(store.published) != 3 {
		t.Fatalf("marked published = %v", store.published)
	}
}

func TestFlushPublishesOldestFirstWithOneWorker(t *testing.T) {
	claimed := events("evt_1", "evt_2", "evt_3")
	slices.Reverse(claimed)
	store := newFakeStore(claimed...)
	var sent []string
	dispatcher := NewDispatcher(store, PublisherFunc(func(_ context.Context, event Event) error {
		sent = append(sent, event.ID)
		return nil
	}), nil)
	dispatcher.workers = 1

	dispatcher.flush(context.Background())

	if !slices.Equal(sent, []string{"evt_1", "evt_2", "evt_3"}) {
		t.Fatalf("published in order %v, want oldest first", sent)
	}
}

func TestFlushSendsAFailedEventBackWithBackoff(t *testing.T) {
	failing := events("evt_1")[0]
	failing.Attempts = 2
	store := newFakeStore(failing)
	dispatcher := NewDispatcher(store, PublisherFunc(func(context.Context, Event) error {
		return errors.New("bus unavailable")
	}), nil)

	dispatcher.flush(context.Background())

	if len(store.failed) != 1 {
		t.Fatalf("failed = %+v", store.failed)
	}
	got := store.failed[0]
	if got.id != "evt_1" || got.token != store.tokens[0] || got.retryAfter != 4*time.Second || got.message != "bus unavailable" {
		t.Fatalf("failure = %+v, want evt_1 under the claim token, retried after 4s", got)
	}
	if len(store.published) != 0 {
		t.Fatalf("published = %v", store.published)
	}
}

func TestFlushWarnsWhenAnotherClaimOwnsTheEvent(t *testing.T) {
	store := newFakeStore(events("evt_1")...)
	store.stale["evt_1"] = true
	var logs bytes.Buffer
	dispatcher := NewDispatcher(store, PublisherFunc(func(context.Context, Event) error { return nil }),
		slog.New(slog.NewJSONHandler(&logs, nil)))

	dispatcher.flush(context.Background())

	if !strings.Contains(logs.String(), "claimed again before it could be marked") || !strings.Contains(logs.String(), `"level":"WARN"`) {
		t.Fatalf("logs = %s", logs.String())
	}
}

func TestFlushReleasesWhatItNeverStartedWhenStopped(t *testing.T) {
	store := newFakeStore(events("evt_1", "evt_2", "evt_3")...)
	started := make(chan struct{})
	unblock := make(chan struct{})
	dispatcher := NewDispatcher(store, PublisherFunc(func(ctx context.Context, event Event) error {
		if event.ID == "evt_1" {
			close(started)
			<-unblock
		}
		return ctx.Err()
	}), nil)
	dispatcher.workers = 1

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		dispatcher.flush(ctx)
		close(done)
	}()
	<-started
	cancel()
	// Stopping must not cut short the event already being published.
	time.Sleep(50 * time.Millisecond)
	close(unblock)
	<-done

	if store.published["evt_1"] == "" {
		t.Fatalf("the event in flight when the dispatcher stopped was not marked published: %+v %+v", store.published, store.failed)
	}
	if !slices.Equal(store.released, []string{"evt_2", "evt_3"}) {
		t.Fatalf("released = %v, want the events never started", store.released)
	}
	if len(store.failed) != 0 {
		t.Fatalf("failed = %+v: stopping is not a failed attempt", store.failed)
	}
}

func TestFlushGivesUpOnWorkThatOutlastsTheGrace(t *testing.T) {
	store := newFakeStore(events("evt_1")...)
	started := make(chan struct{})
	dispatcher := NewDispatcher(store, PublisherFunc(func(ctx context.Context, _ Event) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}), nil)
	dispatcher.grace = 20 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		dispatcher.flush(ctx)
		close(done)
	}()
	<-started
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("flush waited on a publish long after its grace ended")
	}
}

func TestSweepDeletesInBatchesUntilOneComesBackShort(t *testing.T) {
	store := newFakeStore()
	store.deletions = []int{500, 500, 12, 500}
	now := time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)
	dispatcher := NewDispatcher(store, PublisherFunc(func(context.Context, Event) error { return nil }), nil)
	dispatcher.now = func() time.Time { return now }

	dispatcher.sweep(context.Background())

	if len(store.befores) != 3 {
		t.Fatalf("delete calls = %d, want three: two full batches and a short one", len(store.befores))
	}
	if want := now.Add(-7 * 24 * time.Hour); !store.befores[0].Equal(want) {
		t.Fatalf("deleted before %s, want a week ago, %s", store.befores[0], want)
	}
}

func TestRunClaimsAndSweepsAtOnceAndStopsWhenCancelled(t *testing.T) {
	store := newFakeStore()
	dispatcher := NewDispatcher(store, PublisherFunc(func(context.Context, Event) error { return nil }), nil)
	dispatcher.interval = 5 * time.Millisecond

	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() {
		dispatcher.Run(ctx)
		close(stopped)
	}()
	time.Sleep(40 * time.Millisecond)
	cancel()
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.befores) != 1 {
		t.Fatalf("sweeps = %d, want one at start and none before an hour has passed", len(store.befores))
	}
	if len(store.tokens) < 2 {
		t.Fatalf("claims = %d, want one at start and more on each tick", len(store.tokens))
	}
	if store.tokens[0] == store.tokens[1] {
		t.Fatal("two batches were claimed under one token")
	}
}

func TestNewTableRefusesAnUnsafeName(t *testing.T) {
	if _, err := NewTable(nil, "teams_outbox"); err == nil {
		t.Fatal("NewTable accepted a nil pool")
	}
}

func TestTableNames(t *testing.T) {
	for name, want := range map[string]bool{
		"teams_outbox":                 true,
		"agents_primary.agents_outbox": true,
		"Teams_Outbox":                 false,
		"teams_outbox; DROP TABLE x":   false,
		"a.b.c":                        false,
		"":                             false,
	} {
		if got := tableName.MatchString(name); got != want {
			t.Errorf("tableName(%q) = %v, want %v", name, got, want)
		}
	}
}
