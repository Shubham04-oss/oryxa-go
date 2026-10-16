package rooms

import (
	"context"
	"errors"
	"testing"
	"time"
)

// catchupFakeStore is a minimal, in-memory [Store] for Catchup's tests.
// Only Brief and Messages matter here; the rest are stubs that satisfy the
// interface without Catchup ever noticing them.
type catchupFakeStore struct {
	brief       Brief
	briefErr    error
	messages    []Message
	messagesErr error

	// calls records every Messages(afterSeq, limit) call, so tests can
	// check what Catchup actually asked the store for.
	calls []catchupFakeMessagesCall
}

type catchupFakeMessagesCall struct {
	afterSeq int64
	limit    int
}

var _ Store = (*catchupFakeStore)(nil)

func (store *catchupFakeStore) Append(_ context.Context, message Message) (Message, error) {
	return message, nil
}

func (store *catchupFakeStore) Messages(_ context.Context, _ string, afterSeq int64, limit int) ([]Message, error) {
	store.calls = append(store.calls, catchupFakeMessagesCall{afterSeq: afterSeq, limit: limit})
	if store.messagesErr != nil {
		return nil, store.messagesErr
	}
	var result []Message
	for _, message := range store.messages {
		if message.Seq > afterSeq {
			result = append(result, message)
		}
	}
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (store *catchupFakeStore) Recent(_ context.Context, _ string, _ int) ([]Message, error) {
	return nil, nil
}

func (store *catchupFakeStore) Brief(_ context.Context, _ string) (Brief, error) {
	if store.briefErr != nil {
		return Brief{}, store.briefErr
	}
	return store.brief, nil
}

func (store *catchupFakeStore) SaveBrief(_ context.Context, brief Brief, _ int64) (Brief, error) {
	return brief, nil
}

func (store *catchupFakeStore) Watch(_ context.Context, _, _ string, _ time.Time) error {
	return nil
}

func (store *catchupFakeStore) Watching(_ context.Context, _ string, _ time.Time) ([]string, error) {
	return nil, nil
}

func (store *catchupFakeStore) AppendNotes(context.Context, string, string, ParleyNotes, string) (Message, bool, error) {
	return Message{}, false, nil
}

func (store *catchupFakeStore) MarkRead(context.Context, string, string, int64) (int64, error) {
	return 0, nil
}

func (store *catchupFakeStore) Activity(context.Context, string, string, []string) (map[string]Activity, error) {
	return map[string]Activity{}, nil
}

func (store *catchupFakeStore) Forget(_ context.Context, _, _ string) error {
	return nil
}

func TestCatchupSmallGapReturnsBriefAndTheUnseenMessages(t *testing.T) {
	store := &catchupFakeStore{
		brief: Brief{RoomID: "room_1", Revision: 2, ThroughSeq: 10, Summary: "Caught up brief"},
		messages: []Message{
			{RoomID: "room_1", Seq: 11, Body: "eleven"},
			{RoomID: "room_1", Seq: 12, Body: "twelve"},
		},
	}
	brief, messages, err := Catchup(context.Background(), store, "room_1", 5)
	if err != nil {
		t.Fatal(err)
	}
	if brief.Summary != "Caught up brief" || brief.ThroughSeq != 10 {
		t.Fatalf("brief=%+v, want the store's brief returned untouched", brief)
	}
	if len(messages) != 2 || messages[0].Seq != 11 || messages[1].Seq != 12 {
		t.Fatalf("messages=%+v, want seq 11 and 12", messages)
	}
	if len(store.calls) != 1 || store.calls[0].afterSeq != 10 {
		t.Fatalf("calls=%+v, want one Messages call after seq 10 (the brief's ThroughSeq, not sinceSeq=5)", store.calls)
	}
}

func TestCatchupDoesNotReplayMessagesTheBriefAlreadyCovers(t *testing.T) {
	// The person has been gone since message 1, but the brief already
	// consolidates everything up through message 100. Catchup must not
	// also hand back messages 2..100 verbatim: that would mix a summary of
	// that range with the raw range itself.
	store := &catchupFakeStore{
		brief: Brief{RoomID: "room_1", ThroughSeq: 100, Summary: "Covers up to 100"},
	}
	for seq := int64(2); seq <= 105; seq++ {
		store.messages = append(store.messages, Message{RoomID: "room_1", Seq: seq, Body: "msg"})
	}
	_, messages, err := Catchup(context.Background(), store, "room_1", 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 {
		t.Fatalf("messages=%d, want exactly 5 (seq 101..105)", len(messages))
	}
	for _, message := range messages {
		if message.Seq <= 100 {
			t.Fatalf("message seq=%d was already covered by the brief and should not be replayed", message.Seq)
		}
	}
}

func TestCatchupHugeGapCapsMessagesAndLeavesTheRestToTheBrief(t *testing.T) {
	store := &catchupFakeStore{brief: Brief{RoomID: "room_1", ThroughSeq: 0}}
	for seq := int64(1); seq <= int64(catchupMessageCap)+50; seq++ {
		store.messages = append(store.messages, Message{RoomID: "room_1", Seq: seq, Body: "msg"})
	}
	_, messages, err := Catchup(context.Background(), store, "room_1", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != catchupMessageCap {
		t.Fatalf("messages=%d, want the cap of %d", len(messages), catchupMessageCap)
	}
	if messages[0].Seq != 1 {
		t.Fatalf("messages[0].Seq=%d, want 1 (oldest first)", messages[0].Seq)
	}
}

func TestCatchupWithNoBriefAtAll(t *testing.T) {
	store := &catchupFakeStore{
		brief: Brief{}, // store.Brief's documented zero value for "no brief"
		messages: []Message{
			{RoomID: "room_1", Seq: 4, Body: "four"},
			{RoomID: "room_1", Seq: 5, Body: "five"},
		},
	}
	brief, messages, err := Catchup(context.Background(), store, "room_1", 3)
	if err != nil {
		t.Fatal(err)
	}
	if brief.RoomID != "" || brief.ThroughSeq != 0 || brief.Summary != "" {
		t.Fatalf("brief=%+v, want the zero brief passed through as-is", brief)
	}
	if len(messages) != 2 || messages[0].Seq != 4 {
		t.Fatalf("messages=%+v, want seq 4 and 5", messages)
	}
	if len(store.calls) != 1 || store.calls[0].afterSeq != 3 {
		t.Fatalf("calls=%+v, want one Messages call after seq 3 (max(sinceSeq=3, ThroughSeq=0))", store.calls)
	}
}

func TestCatchupWithAStaleBriefReturnsItHonestlyRatherThanMixingIt(t *testing.T) {
	// Compaction has not run since the person last read: the brief is
	// older than what they already saw. Catchup must not advance
	// brief.ThroughSeq to hide that, and must not skip fetching the
	// messages they have not seen either.
	store := &catchupFakeStore{
		brief: Brief{RoomID: "room_1", ThroughSeq: 5, Summary: "Stale: only covers up to message 5"},
		messages: []Message{
			{RoomID: "room_1", Seq: 51, Body: "fifty-one"},
		},
	}
	brief, messages, err := Catchup(context.Background(), store, "room_1", 50)
	if err != nil {
		t.Fatal(err)
	}
	if brief.ThroughSeq != 5 {
		t.Fatalf("through_seq=%d, want the honest, stale value of 5 rather than being advanced to hide the gap", brief.ThroughSeq)
	}
	if brief.ThroughSeq >= 50 {
		t.Fatal("test setup is wrong: the brief needs to be behind sinceSeq to exercise the stale case")
	}
	if len(messages) != 1 || messages[0].Seq != 51 {
		t.Fatalf("messages=%+v, want just seq 51 (after sinceSeq, since the brief has nothing newer to offer)", messages)
	}
	if len(store.calls) != 1 || store.calls[0].afterSeq != 50 {
		t.Fatalf("calls=%+v, want one Messages call after seq 50 (max(sinceSeq=50, ThroughSeq=5))", store.calls)
	}
}

func TestCatchupPropagatesABriefLoadError(t *testing.T) {
	wantErr := errors.New("brief store is down")
	store := &catchupFakeStore{briefErr: wantErr}
	_, _, err := Catchup(context.Background(), store, "room_1", 0)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v, want it to wrap %v", err, wantErr)
	}
}

func TestCatchupPropagatesAMessagesLoadError(t *testing.T) {
	wantErr := errors.New("messages store is down")
	store := &catchupFakeStore{messagesErr: wantErr}
	_, _, err := Catchup(context.Background(), store, "room_1", 0)
	if !errors.Is(err, wantErr) {
		t.Fatalf("err=%v, want it to wrap %v", err, wantErr)
	}
}

func TestCatchupRejectsANilStore(t *testing.T) {
	if _, _, err := Catchup(context.Background(), nil, "room_1", 0); err == nil {
		t.Fatal("want an error for a nil store")
	}
}
