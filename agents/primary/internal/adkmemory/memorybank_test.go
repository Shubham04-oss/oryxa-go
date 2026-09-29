package adkmemory

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cloud.google.com/go/aiplatform/apiv1beta1/aiplatformpb"
	"cloud.google.com/go/auth"
	"google.golang.org/genai"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"google.golang.org/adk/v2/session"
)

const testParent = "projects/oryxa/locations/us-central1/reasoningEngines/1234"

// fakeBank records what the backend asked Memory Bank for and answers with
// what it was given.
type fakeBank struct {
	// mutex guards removed and held, which Forget's deletes reach from several
	// goroutines at once.
	mutex     sync.Mutex
	generated []*aiplatformpb.GenerateMemoriesRequest
	retrieved []*aiplatformpb.RetrieveMemoriesRequest
	// held is what the bank already holds, as a listing returns it. A memory
	// that is removed is no longer held.
	held    []*aiplatformpb.Memory
	found   []*aiplatformpb.Memory
	removed []string
	// listErr ends a listing, after the memories held.
	listErr error
	// removing, when set, is what deleting a memory does before it is deleted:
	// it may wait, or fail the delete.
	removing func(ctx context.Context, name string) error
}

func (f *fakeBank) generate(_ context.Context, request *aiplatformpb.GenerateMemoriesRequest, _ bool) error {
	f.generated = append(f.generated, request)
	return nil
}

func (f *fakeBank) retrieve(_ context.Context, request *aiplatformpb.RetrieveMemoriesRequest) ([]*aiplatformpb.Memory, error) {
	f.retrieved = append(f.retrieved, request)
	return f.found, nil
}

func (f *fakeBank) list(_ context.Context, _ string) iter.Seq2[*aiplatformpb.Memory, error] {
	return func(yield func(*aiplatformpb.Memory, error) bool) {
		f.mutex.Lock()
		held := slices.Clone(f.held)
		f.mutex.Unlock()
		for _, remembered := range held {
			if !yield(remembered, nil) {
				return
			}
		}
		if f.listErr != nil {
			yield(nil, f.listErr)
		}
	}
}

func (f *fakeBank) remove(ctx context.Context, name string) error {
	if f.removing != nil {
		if err := f.removing(ctx, name); err != nil {
			return err
		}
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.removed = append(f.removed, name)
	f.held = slices.DeleteFunc(f.held, func(remembered *aiplatformpb.Memory) bool { return remembered.GetName() == name })
	return nil
}

func (f *fakeBank) Close() error { return nil }

func bankWith(api memoryBankAPI) *MemoryBank {
	return &MemoryBank{api: api, parent: testParent, recalled: defaultRecalled, deleting: make(chan struct{}, forgetParallelism)}
}

// memoryIn returns a memory the bank holds in the given scope.
func memoryIn(name, organizationID, userID, fact string) *aiplatformpb.Memory {
	return &aiplatformpb.Memory{
		Name: name,
		Fact: fact,
		Scope: map[string]string{
			FieldOrganizationID: organizationID,
			FieldUserID:         userID,
		},
	}
}

// TestMemoryBankScopesEveryCallByOrganization is the point of using Memory
// Bank's own scope map rather than encoding the organization into the user:
// the service matches a memory's scope by its keys and values, so both
// dimensions have to be on every request it sees.
func TestMemoryBankScopesEveryCallByOrganization(t *testing.T) {
	bank := bankWith(&fakeBank{})
	scope := Scope{OrganizationID: "org-acme", UserID: "user-1"}
	want := map[string]string{FieldOrganizationID: "org-acme", FieldUserID: "user-1"}

	if err := bank.Write(context.Background(), scope, conversation(t, "user-1", "the migration ships in March")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, err := bank.Search(context.Background(), scope, "migration"); err != nil {
		t.Fatalf("Search: %v", err)
	}

	sent := bank.api.(*fakeBank)
	if len(sent.generated) != 1 {
		t.Fatalf("generate calls = %d, want 1", len(sent.generated))
	}
	if got := sent.generated[0].GetScope(); !sameScope(got, want) {
		t.Fatalf("write scope = %v, want %v", got, want)
	}
	if len(sent.retrieved) != 1 {
		t.Fatalf("retrieve calls = %d, want 1", len(sent.retrieved))
	}
	if got := sent.retrieved[0].GetScope(); !sameScope(got, want) {
		t.Fatalf("search scope = %v, want %v", got, want)
	}
	if got := sent.generated[0].GetParent(); got != testParent {
		t.Fatalf("parent = %q", got)
	}
}

// TestMemoryBankSendsTheConversationItself covers the reason this backend
// does not use ADK's: threads live in PostgreSQL, so there is no agent
// engine session for Memory Bank to read and the turns have to be sent.
func TestMemoryBankSendsTheConversationItself(t *testing.T) {
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "krude", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	add := func(role, text string, thought bool) {
		t.Helper()
		event := session.NewEvent(ctx, "invocation")
		event.Author = role
		event.Content = &genai.Content{Role: role, Parts: []*genai.Part{{Text: text, Thought: thought}}}
		if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	add(genai.RoleUser, "the migration ships in March", false)
	add(genai.RoleModel, "let me work that out", true)
	add(genai.RoleModel, "noted", false)
	add("krude", "a sub-agent said this", false)

	bank := bankWith(&fakeBank{})
	if err := bank.Write(ctx, Scope{OrganizationID: "org-acme", UserID: "user-1"}, created.Session); err != nil {
		t.Fatalf("Write: %v", err)
	}

	said := bank.api.(*fakeBank).generated[0].GetDirectContentsSource().GetEvents()
	if len(said) != 3 {
		t.Fatalf("sent %d turns, want the thought left out", len(said))
	}
	type turn struct{ role, text string }
	var got []turn
	for _, event := range said {
		got = append(got, turn{event.GetContent().GetRole(), event.GetContent().GetParts()[0].GetText()})
	}
	want := []turn{
		{genai.RoleUser, "the migration ships in March"},
		{genai.RoleModel, "noted"},
		{genai.RoleModel, "a sub-agent said this"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("turns = %v, want %v", got, want)
	}
}

func TestMemoryBankWithNothingSaidAsksForNothing(t *testing.T) {
	ctx := context.Background()
	bank := bankWith(&fakeBank{})
	if err := bank.Write(ctx, Scope{OrganizationID: "org-acme", UserID: "user-1"}, conversation(t, "user-1")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if calls := len(bank.api.(*fakeBank).generated); calls != 0 {
		t.Fatalf("an empty conversation made %d calls, want none", calls)
	}
}

// Memory Bank refuses a conversation that is only the model's side, and one
// in which the member said nothing has nothing of theirs to remember anyway.
func TestMemoryBankWithOnlyTheModelSpeakingAsksForNothing(t *testing.T) {
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "krude", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	event := session.NewEvent(ctx, "invocation")
	event.Author = "krude"
	event.Content = &genai.Content{Role: genai.RoleModel, Parts: []*genai.Part{{Text: "the draft is saved"}}}
	if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
		t.Fatal(err)
	}

	bank := bankWith(&fakeBank{})
	if err := bank.Write(ctx, Scope{OrganizationID: "org-acme", UserID: "user-1"}, created.Session); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if calls := len(bank.api.(*fakeBank).generated); calls != 0 {
		t.Fatalf("a run with only the model speaking made %d calls, want none", calls)
	}
}

func TestMemoryBankSearchReturnsTheFactsItRemembers(t *testing.T) {
	bank := bankWith(&fakeBank{found: []*aiplatformpb.Memory{
		memoryIn("memories/1", "org-acme", "user-1", "the migration ships in March"),
		memoryIn("memories/2", "org-acme", "user-1", ""),
	}})

	entries, err := bank.Search(context.Background(), Scope{OrganizationID: "org-acme", UserID: "user-1"}, "migration")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want the empty fact left out", len(entries))
	}
	if entries[0].ID != "memories/1" || entries[0].Content.Parts[0].Text != "the migration ships in March" {
		t.Fatalf("entry = %+v", entries[0])
	}
	if got := bank.api.(*fakeBank).retrieved[0].GetSimilaritySearchParams().GetTopK(); got != defaultRecalled {
		t.Fatalf("TopK = %d, want %d", got, defaultRecalled)
	}
}

func TestMemoryBankSearchWithNoQueryAsksForNothing(t *testing.T) {
	bank := bankWith(&fakeBank{})
	entries, err := bank.Search(context.Background(), Scope{OrganizationID: "org-acme", UserID: "user-1"}, "   ")
	if err != nil || len(entries) != 0 {
		t.Fatalf("Search(blank) = %v, %v", entries, err)
	}
	if calls := len(bank.api.(*fakeBank).retrieved); calls != 0 {
		t.Fatalf("a blank query made %d calls, want none", calls)
	}
}

// TestMemoryBankForgetsOnlyWhatItWasAsked checks the scope matching that
// replaces a listing filter: a member leaving one organization takes their
// memories there and nothing else.
func TestMemoryBankForgetsOnlyWhatItWasAsked(t *testing.T) {
	held := []*aiplatformpb.Memory{
		memoryIn("memories/1", "org-acme", "user-1", "the acme migration"),
		memoryIn("memories/2", "org-acme", "user-2", "another member here"),
		memoryIn("memories/3", "org-initech", "user-1", "the same member elsewhere"),
		memoryIn("memories/4", "org-acme", "user-1", "a second acme memory"),
	}
	bank := bankWith(&fakeBank{held: held})

	if err := bank.Forget(context.Background(), "org-acme", "user-1"); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	removed := bank.api.(*fakeBank).removed
	slices.Sort(removed)
	if want := []string{"memories/1", "memories/4"}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
}

func TestMemoryBankForgetsEveryMemberOfAnOrganization(t *testing.T) {
	held := []*aiplatformpb.Memory{
		memoryIn("memories/1", "org-acme", "user-1", "one member"),
		memoryIn("memories/2", "org-acme", "user-2", "another member"),
		memoryIn("memories/3", "org-initech", "user-1", "a different organization"),
	}
	bank := bankWith(&fakeBank{held: held})

	if err := bank.Forget(context.Background(), "org-acme", ""); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	removed := bank.api.(*fakeBank).removed
	slices.Sort(removed)
	if want := []string{"memories/1", "memories/2"}; !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
}

func TestMemoryBankForgetReportsAFailedListing(t *testing.T) {
	bank := bankWith(&fakeBank{listErr: errors.New("the bank was unreachable")})
	err := bank.Forget(context.Background(), "org-acme", "user-1")
	if err == nil {
		t.Fatal("a listing that failed was reported as everything forgotten")
	}
	if !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("error = %v, want the listing's own failure", err)
	}
}

// heldIn returns count memories the bank holds in one scope.
func heldIn(count int, organizationID, userID string) []*aiplatformpb.Memory {
	held := make([]*aiplatformpb.Memory, 0, count)
	for index := range count {
		held = append(held, memoryIn(fmt.Sprintf("memories/%02d", index), organizationID, userID, fmt.Sprintf("fact %d", index)))
	}
	return held
}

// TestMemoryBankForgetDeletesSeveralMemoriesAtOnceButOnlyAFew is the speed of
// forgetting: deleting is a long-running operation waited on, so one at a time
// made a member with many memories a cleanup of minutes. A few at once, and
// never more, is what keeps it to seconds without a burst Memory Bank may
// refuse.
func TestMemoryBankForgetDeletesSeveralMemoriesAtOnceButOnlyAFew(t *testing.T) {
	var running, most atomic.Int32
	atBound := make(chan struct{})
	var reached sync.Once
	release := make(chan struct{})
	api := &fakeBank{held: heldIn(3*forgetParallelism, "org-acme", "user-1")}
	api.removing = func(ctx context.Context, _ string) error {
		now := running.Add(1)
		defer running.Add(-1)
		for previous := most.Load(); now > previous && !most.CompareAndSwap(previous, now); previous = most.Load() {
		}
		if now == forgetParallelism {
			reached.Do(func() { close(atBound) })
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}

	forgot := make(chan error, 1)
	go func() { forgot <- bankWith(api).Forget(context.Background(), "org-acme", "user-1") }()
	select {
	case <-atBound:
	case <-time.After(5 * time.Second):
		t.Fatalf("only %d deletes ran at once, want %d", most.Load(), forgetParallelism)
	}
	// Room for any that would run beyond the bound to have started.
	time.Sleep(50 * time.Millisecond)
	if got := running.Load(); got != forgetParallelism {
		t.Fatalf("%d deletes running at once, want the bound of %d", got, forgetParallelism)
	}
	close(release)
	if err := <-forgot; err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if got := most.Load(); got != forgetParallelism {
		t.Fatalf("at most %d deletes ran at once, want %d", got, forgetParallelism)
	}
	if got := len(api.removed); got != 3*forgetParallelism {
		t.Fatalf("removed %d memories, want %d", got, 3*forgetParallelism)
	}
}

// An organization going takes each of its members with it, so several Forgets
// run at once. Together they send Memory Bank no more deletes at once than
// one does.
func TestMemoryBankForgetsOverlappingDeleteNoMoreAtOnceThanOne(t *testing.T) {
	var running, most atomic.Int32
	atBound := make(chan struct{})
	var reached sync.Once
	release := make(chan struct{})
	api := &fakeBank{held: append(heldIn(3*forgetParallelism, "org-acme", "user-1"), heldIn(3*forgetParallelism, "org-initech", "user-2")...)}
	api.removing = func(ctx context.Context, _ string) error {
		now := running.Add(1)
		defer running.Add(-1)
		for previous := most.Load(); now > previous && !most.CompareAndSwap(previous, now); previous = most.Load() {
		}
		if now == forgetParallelism {
			reached.Do(func() { close(atBound) })
		}
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}
	bank := bankWith(api)

	forgot := make(chan error, 2)
	for _, organizationID := range []string{"org-acme", "org-initech"} {
		go func() { forgot <- bank.Forget(context.Background(), organizationID, "") }()
	}
	select {
	case <-atBound:
	case <-time.After(5 * time.Second):
		t.Fatalf("only %d deletes ran at once, want %d", most.Load(), forgetParallelism)
	}
	// Room for the second Forget's deletes to have started, were they let.
	time.Sleep(50 * time.Millisecond)
	if got := running.Load(); got != forgetParallelism {
		t.Fatalf("%d deletes running at once across two Forgets, want the bound of %d", got, forgetParallelism)
	}
	close(release)
	for range 2 {
		if err := <-forgot; err != nil {
			t.Fatalf("Forget: %v", err)
		}
	}
	if got := most.Load(); got != forgetParallelism {
		t.Fatalf("at most %d deletes ran at once, want %d", got, forgetParallelism)
	}
	if got := len(api.removed); got != 6*forgetParallelism {
		t.Fatalf("removed %d memories, want %d", got, 6*forgetParallelism)
	}
}

// One memory that will not delete must not keep the rest, which are worth
// forgetting on their own, from being forgotten; and what is left is what the
// next Forget finds, so it finishes the job without repeating the rest.
func TestMemoryBankForgetKeepsGoingPastAMemoryItCannotDelete(t *testing.T) {
	api := &fakeBank{held: heldIn(6, "org-acme", "user-1")}
	stuck := map[string]bool{"memories/01": true, "memories/04": true}
	api.removing = func(_ context.Context, name string) error {
		if stuck[name] {
			return errors.New("the bank refused the delete")
		}
		return nil
	}
	bank := bankWith(api)

	err := bank.Forget(context.Background(), "org-acme", "user-1")
	if err == nil {
		t.Fatal("a Forget that left two memories behind reported everything forgotten")
	}
	if !strings.Contains(err.Error(), "forgot 4 of 6") || !strings.Contains(err.Error(), "refused the delete") {
		t.Fatalf("error = %v, want how many were left and why", err)
	}
	slices.Sort(api.removed)
	if want := []string{"memories/00", "memories/02", "memories/03", "memories/05"}; !slices.Equal(api.removed, want) {
		t.Fatalf("removed %v, want everything the bank did not refuse: %v", api.removed, want)
	}

	clear(stuck)
	if err := bank.Forget(context.Background(), "org-acme", "user-1"); err != nil {
		t.Fatalf("Forget again: %v", err)
	}
	if got := len(api.removed); got != 6 {
		t.Fatalf("%d memories removed in all, want each of the six once", got)
	}
}

// A bank too slow to empty in the time allowed is left for the next Forget,
// and Forget says so and comes back, instead of holding what called it.
func TestMemoryBankForgetStopsAtItsDeadlineAndSaysWhatItLeft(t *testing.T) {
	api := &fakeBank{held: heldIn(10, "org-acme", "user-1")}
	api.removing = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	bank := bankWith(api)
	bank.forgetTimeout = 50 * time.Millisecond

	started := time.Now()
	err := bank.Forget(context.Background(), "org-acme", "user-1")
	if took := time.Since(started); took > 5*time.Second {
		t.Fatalf("Forget took %s against a deadline of %s", took, bank.forgetTimeout)
	}
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "forgot 0 of 10") {
		t.Fatalf("error = %v, want the deadline, and how many were left", err)
	}
}

// Its caller giving up, a deploy stopping the queue's consumer, ends the
// deleting as its own deadline does.
func TestMemoryBankForgetStopsWhenItsCallerDoes(t *testing.T) {
	api := &fakeBank{held: heldIn(10, "org-acme", "user-1")}
	ctx, cancel := context.WithCancel(context.Background())
	api.removing = func(ctx context.Context, _ string) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	err := bankWith(api).Forget(ctx, "org-acme", "user-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want the caller's own cancellation", err)
	}
}

// Two Forgets can overlap, an organization going while one of its members
// does: the memory one deleted first is gone when the other comes to it, which
// is what the other meant.
func TestMemoryBankForgetTakesAMemoryThatIsAlreadyGoneAsForgotten(t *testing.T) {
	api := &fakeBank{held: heldIn(3, "org-acme", "user-1")}
	api.removing = func(_ context.Context, name string) error {
		if name == "memories/01" {
			return fmt.Errorf("wait for the memory to be deleted: %w", status.Error(codes.NotFound, "the memory is not there"))
		}
		return nil
	}
	if err := bankWith(api).Forget(context.Background(), "org-acme", "user-1"); err != nil {
		t.Fatalf("Forget = %v, want a memory that was already gone to count as forgotten", err)
	}
}

// A listing that breaks off partway has still found memories worth deleting.
func TestMemoryBankForgetDeletesWhatItFoundBeforeAListingFailed(t *testing.T) {
	api := &fakeBank{held: heldIn(2, "org-acme", "user-1"), listErr: errors.New("the listing broke off")}
	err := bankWith(api).Forget(context.Background(), "org-acme", "user-1")
	if err == nil || !strings.Contains(err.Error(), "the listing broke off") {
		t.Fatalf("error = %v, want the listing's own failure", err)
	}
	if got := len(api.removed); got != 2 {
		t.Fatalf("removed %d memories, want the two found before the listing failed", got)
	}
}

// TestMemoryBankRefusesAWildcardScope covers Memory Bank's own rule: a scope
// value cannot hold '*'. Sending one would be rejected by the service, and a
// write rejected after the fact is a memory quietly not kept.
func TestMemoryBankRefusesAWildcardScope(t *testing.T) {
	ctx := context.Background()
	bank := bankWith(&fakeBank{})
	scope := Scope{OrganizationID: "org-*", UserID: "user-1"}

	if err := bank.Write(ctx, scope, conversation(t, "user-1", "something")); err == nil {
		t.Fatal("Write allowed a wildcard in the scope")
	}
	if _, err := bank.Search(ctx, scope, "something"); err == nil {
		t.Fatal("Search allowed a wildcard in the scope")
	}
	if err := bank.Forget(ctx, "org-*", ""); err == nil {
		t.Fatal("Forget allowed a wildcard in the organization")
	}
}

func TestMemoryBankNeedsAProjectAndAnEngine(t *testing.T) {
	ctx := context.Background()
	for name, options := range map[string]MemoryBankOptions{
		"no project":      {AgentEngine: "1234", Location: "us-central1"},
		"no engine":       {ProjectID: "oryxa", Location: "us-central1"},
		"no region":       {ProjectID: "oryxa", AgentEngine: "1234"},
		"global endpoint": {ProjectID: "oryxa", AgentEngine: "1234", Location: "global"},
		"negative recall": {
			ProjectID: "oryxa", AgentEngine: "1234", Location: "us-central1", Recalled: -1,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewMemoryBank(ctx, options); err == nil {
				t.Fatalf("NewMemoryBank(%+v) allowed it", options)
			}
		})
	}
}

// fixedToken is credentials that always hold the same token.
type fixedToken string

func (token fixedToken) Token(context.Context) (*auth.Token, error) {
	return &auth.Token{Value: string(token), Type: "Bearer"}, nil
}

// On Fargate there are no Application Default Credentials to find: the
// credentials internal/gcpauth federates from AWS stand in for them.
func TestMemoryBankUsesTheCredentialsItIsGiven(t *testing.T) {
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", filepath.Join(t.TempDir(), "missing.json"))
	options := MemoryBankOptions{ProjectID: "oryxa", AgentEngine: "1234", Location: "us-west1"}
	if bank, err := NewMemoryBank(context.Background(), options); err == nil {
		_ = bank.Close()
		t.Fatal("NewMemoryBank() found Application Default Credentials without a credentials file, so this test proves nothing")
	}

	options.Credentials = auth.NewCredentials(&auth.CredentialsOptions{TokenProvider: fixedToken("service-account-token")})
	bank, err := NewMemoryBank(context.Background(), options)
	if err != nil {
		t.Fatalf("NewMemoryBank() with credentials = %v, want it built on them", err)
	}
	if err := bank.Close(); err != nil {
		t.Fatal(err)
	}
}

// sameScope reports whether two scope maps are the same.
func sameScope(got, want map[string]string) bool {
	if len(got) != len(want) {
		return false
	}
	for key, value := range want {
		if got[key] != value {
			return false
		}
	}
	return true
}
