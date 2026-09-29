package threads

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"
)

func askedNote(request string) Note {
	return Note{RequestID: request, State: NoteAsked, Text: "Asked Raya's agent in #launch"}
}

func requestItems(t *testing.T, h *harness, member Member) []Item {
	t.Helper()
	detail, err := h.service.Get(context.Background(), member, RequestsThreadID(member.OrganizationID))
	if err != nil {
		t.Fatalf("Get the requests thread: %v", err)
	}
	return detail.Items
}

func TestANoteIsAModelFreeLineInTheMembersRequestsThread(t *testing.T) {
	h := newHarness(t, func(*model.LLMRequest) (*genai.Content, error) {
		t.Error("a note called a model")
		return genai.NewContentFromText("no", genai.RoleModel), nil
	})
	if err := h.service.Note(context.Background(), asha, askedNote("irq_1")); err != nil {
		t.Fatal(err)
	}

	detail, err := h.service.Get(context.Background(), asha, RequestsThreadID("org_1"))
	if err != nil {
		t.Fatal(err)
	}
	if detail.Title != RequestsTitle || detail.Origin != OriginChat || detail.CreatedAt.IsZero() {
		t.Fatalf("thread = %+v, want a chat thread titled %q", detail.Thread, RequestsTitle)
	}
	if len(detail.Items) != 1 {
		t.Fatalf("items = %+v", detail.Items)
	}
	item := detail.Items[0]
	if item.Type != ItemRequest || item.RequestID != "irq_1" || item.Status != NoteAsked ||
		item.Text != "Asked Raya's agent in #launch" || item.Author != AuthorOryxa || item.ID == "" || item.At.IsZero() {
		t.Fatalf("item = %+v", item)
	}
	if len(detail.Pending) != 0 {
		t.Fatalf("pending = %v, want none", detail.Pending)
	}
	// It is a chat thread, so the member finds it in their list.
	listed, err := h.service.List(context.Background(), asha)
	if err != nil || len(listed) != 1 || listed[0].ID != detail.ID || listed[0].Title != RequestsTitle {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	// No run was started for it.
	if len(h.runs.started) != 0 || len(h.model.tokens) != 0 {
		t.Fatalf("a note started runs %v or called the model %v", h.runs.started, h.model.tokens)
	}

	encoded, err := json.Marshal(item)
	if err != nil || !strings.Contains(string(encoded), `"type":"request"`) || !strings.Contains(string(encoded), `"request_id":"irq_1"`) {
		t.Fatalf("item JSON = %s, %v", encoded, err)
	}
}

func TestTheSameNoteAgainIsNothingAndAnotherStateIsAnotherLine(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	for range 3 {
		if err := h.service.Note(ctx, asha, askedNote("irq_1")); err != nil {
			t.Fatal(err)
		}
	}
	// Words are the caller's: a retry with other words is still the same note.
	reworded := askedNote("irq_1")
	reworded.Text = "Asked again"
	if err := h.service.Note(ctx, asha, reworded); err != nil {
		t.Fatal(err)
	}
	if items := requestItems(t, h, asha); len(items) != 1 || items[0].Text != "Asked Raya's agent in #launch" {
		t.Fatalf("items = %+v, want the one line", items)
	}

	if err := h.service.Note(ctx, asha, Note{RequestID: "irq_1", State: NoteAnswered, Text: "Raya's agent answered"}); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Note(ctx, asha, askedNote("irq_2")); err != nil {
		t.Fatal(err)
	}
	items := requestItems(t, h, asha)
	var got []string
	for _, item := range items {
		got = append(got, item.RequestID+"/"+item.Status)
	}
	if strings.Join(got, " ") != "irq_1/asked irq_1/answered irq_2/asked" {
		t.Fatalf("items = %v, want them in the order they were written", got)
	}
}

func TestANoteIsNeverShownToAModel(t *testing.T) {
	var seen []string
	h := newHarness(t, func(request *model.LLMRequest) (*genai.Content, error) {
		for _, content := range request.Contents {
			seen = append(seen, contentText(content))
		}
		return genai.NewContentFromText("Hello.", genai.RoleModel), nil
	})
	ctx := context.Background()
	if err := h.service.Note(ctx, asha, Note{RequestID: "irq_1", State: NoteAsked, Text: "Asked Raya's agent in #secret-plans"}); err != nil {
		t.Fatal(err)
	}
	// The member chats in the thread the note is in.
	items, _ := h.run(asha, RequestsThreadID("org_1"), RunInput{Message: "hello"})
	if got := find(items, ItemRunCompleted); len(got) != 1 {
		t.Fatalf("the run did not complete: %v", types(items))
	}
	for _, text := range seen {
		if strings.Contains(text, "secret-plans") || strings.Contains(text, "irq_1") || strings.Contains(text, "Raya") {
			t.Fatalf("the model was shown a note: %q", text)
		}
	}
	if len(seen) == 0 {
		t.Fatal("the model saw nothing at all")
	}
	// The history holds the note beside the conversation, in order.
	detail, err := h.service.Get(ctx, asha, RequestsThreadID("org_1"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(types(detail.Items), ","); got != "request,message,message" {
		t.Fatalf("history = %s", got)
	}
}

func TestNotesAreTheirMembersOwnInTheirOrganization(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	other := Member{UserID: "user_ben", OrganizationID: "org_1"}
	elsewhere := Member{UserID: "user_asha", OrganizationID: "org_2"}
	for _, member := range []Member{asha, other, elsewhere} {
		if err := h.service.Note(ctx, member, askedNote("irq_"+member.UserID+member.OrganizationID)); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range []Member{asha, other, elsewhere} {
		items := requestItems(t, h, member)
		if len(items) != 1 || items[0].RequestID != "irq_"+member.UserID+member.OrganizationID {
			t.Fatalf("%s in %s reads %+v", member.UserID, member.OrganizationID, items)
		}
	}
	// A member's list holds only the threads of the organization they ask in.
	listed, err := h.service.List(ctx, asha)
	if err != nil || len(listed) != 1 || listed[0].ID != RequestsThreadID("org_1") {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
	// Nobody reads another's requests thread by its id.
	if _, err := h.service.Get(ctx, Member{UserID: "user_cy", OrganizationID: "org_1"}, RequestsThreadID("org_1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() by another member error = %v, want ErrNotFound", err)
	}
}

func TestADeletedRequestsThreadIsMadeAgainWithTheNextNote(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.service.Note(ctx, asha, askedNote("irq_1")); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Delete(ctx, asha, RequestsThreadID("org_1")); err != nil {
		t.Fatal(err)
	}
	if err := h.service.Note(ctx, asha, askedNote("irq_2")); err != nil {
		t.Fatal(err)
	}
	items := requestItems(t, h, asha)
	if len(items) != 1 || items[0].RequestID != "irq_2" {
		t.Fatalf("items = %+v, want only what was noted since", items)
	}
}

func TestForgettingAMemberRemovesTheirRequestsThread(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	if err := h.service.Note(ctx, asha, askedNote("irq_1")); err != nil {
		t.Fatal(err)
	}
	removed, err := h.service.Forget(ctx, "org_1", "user_asha")
	if err != nil || removed != 1 {
		t.Fatalf("Forget() = %d, %v", removed, err)
	}
	if _, err := h.service.Get(ctx, asha, RequestsThreadID("org_1")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get() after forgetting error = %v, want ErrNotFound", err)
	}
}

func TestNotesWrittenAtOnceAllLand(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	const writers = 12
	var group sync.WaitGroup
	errs := make([]error, writers)
	for i := range writers {
		group.Add(1)
		go func() {
			defer group.Done()
			errs[i] = h.service.Note(ctx, asha, askedNote(fmt.Sprintf("irq_%d", i)))
		}()
	}
	group.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("writer %d: %v", i, err)
		}
	}
	items := requestItems(t, h, asha)
	if len(items) != writers {
		t.Fatalf("got %d items, want %d: %+v", len(items), writers, items)
	}
	seen := map[string]bool{}
	for _, item := range items {
		seen[item.RequestID] = true
	}
	if len(seen) != writers {
		t.Fatalf("requests noted = %v, want every one once", seen)
	}
	// Only one thread was made, whichever writer made it.
	if listed, err := h.service.List(ctx, asha); err != nil || len(listed) != 1 {
		t.Fatalf("List() = %+v, %v", listed, err)
	}
}

// flakySessions refuses the first appends, as a session service does that
// finds the thread was written to since it was read.
type flakySessions struct {
	session.Service
	mu       sync.Mutex
	refuse   int
	appended int
	// afterStoring makes a refused append store the event first, as a call that
	// failed on the way back does.
	afterStoring bool
}

func (s *flakySessions) AppendEvent(ctx context.Context, current session.Session, event *session.Event) error {
	s.mu.Lock()
	s.appended++
	refuse := s.refuse > 0
	if refuse {
		s.refuse--
	}
	s.mu.Unlock()
	if refuse {
		if s.afterStoring {
			if err := s.Service.AppendEvent(ctx, current, event); err != nil {
				return err
			}
		}
		return errors.New("stale session error: last update time from request is older than in database")
	}
	return s.Service.AppendEvent(ctx, current, event)
}

func TestARefusedAppendIsTriedAgainOnAFreshReadOfTheThread(t *testing.T) {
	h := newHarness(t, nil)
	flaky := &flakySessions{Service: h.sessions, refuse: 2}
	h.service.sessions = flaky
	ctx := context.Background()
	if err := h.service.Note(ctx, asha, askedNote("irq_1")); err != nil {
		t.Fatalf("Note() = %v, want it to try again", err)
	}
	if flaky.appended != 3 {
		t.Fatalf("appended %d times, want the two refusals and the one that worked", flaky.appended)
	}
	if items := requestItems(t, h, asha); len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}

	// One that is refused every time is the caller's to try again.
	stuck := &flakySessions{Service: h.sessions, refuse: 100}
	h.service.sessions = stuck
	if err := h.service.Note(ctx, asha, askedNote("irq_2")); err == nil {
		t.Fatal("Note() = nil, want the failure")
	}
	if stuck.appended != maxNoteAttempts {
		t.Fatalf("appended %d times, want at most %d", stuck.appended, maxNoteAttempts)
	}
}

func TestAnAppendThatFailedAfterStoringTheNoteIsNotRepeated(t *testing.T) {
	h := newHarness(t, nil)
	flaky := &flakySessions{Service: h.sessions, refuse: 1, afterStoring: true}
	h.service.sessions = flaky
	if err := h.service.Note(context.Background(), asha, askedNote("irq_1")); err != nil {
		t.Fatalf("Note() = %v, want the stored note to count", err)
	}
	if flaky.appended != 1 {
		t.Fatalf("appended %d times, want it left alone once it was there", flaky.appended)
	}
	if items := requestItems(t, h, asha); len(items) != 1 {
		t.Fatalf("items = %+v, want the note once", items)
	}
}

func TestANoteIsRefusedUnlessItIsWhole(t *testing.T) {
	h := newHarness(t, nil)
	ctx := context.Background()
	long := strings.Repeat("x", maxNoteRunes+1)
	for name, note := range map[string]Note{
		"no request": {State: NoteAsked, Text: "Asked"},
		"no text":    {RequestID: "irq_1", State: NoteAsked, Text: "  \n "},
		"long text":  {RequestID: "irq_1", State: NoteAsked, Text: long},
		"odd state":  {RequestID: "irq_1", State: "approved", Text: "Asked"},
		"no state":   {RequestID: "irq_1", Text: "Asked"},
		"nul":        {RequestID: "irq_1", State: NoteAsked, Text: "As\x00ked"},
		"long id":    {RequestID: strings.Repeat("r", 129), State: NoteAsked, Text: "Asked"},
	} {
		if err := h.service.Note(ctx, asha, note); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	for name, member := range map[string]Member{
		"no user": {OrganizationID: "org_1"},
		"no org":  {UserID: "user_asha"},
	} {
		if err := h.service.Note(ctx, member, askedNote("irq_1")); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	// Nothing invalid made a thread.
	if listed, err := h.service.List(ctx, asha); err != nil || len(listed) != 0 {
		t.Fatalf("List() = %+v, %v, want none", listed, err)
	}
}

func TestAnEventWithAMembersWordsIsNeverTakenForANote(t *testing.T) {
	// The member's own message carries content; even one that somehow held the
	// note's metadata would read as the message it is.
	event := session.NewEvent(context.Background(), "inv")
	event.Author = adkUserAuthor
	event.Content = genai.NewContentFromText("hello", genai.RoleUser)
	event.CustomMetadata = map[string]any{noteMetadataKey: map[string]any{"request_id": "irq_1", "state": NoteAsked, "text": "Asked"}}
	items := eventItems(event, nil)
	if len(items) != 1 || items[0].Type != ItemMessage || items[0].Text != "hello" {
		t.Fatalf("items = %+v, want the member's message", items)
	}

	// A malformed note is no item at all.
	bare := session.NewEvent(context.Background(), "inv")
	bare.CustomMetadata = map[string]any{noteMetadataKey: "not an object"}
	if items := eventItems(bare, nil); len(items) != 0 {
		t.Fatalf("items = %+v, want none", items)
	}
}
