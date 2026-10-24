package adkmemory

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore/types"
	"golang.org/x/time/rate"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
)

const testMemoryID = "krude_memory-abc123"

// fakeAgentCore is AgentCore Memory's data plane, in memory. Its listings
// answer two items a page, so every caller has to follow the pages.
type fakeAgentCore struct {
	mutex sync.Mutex

	created   []*bedrockagentcore.CreateEventInput
	retrieved []*bedrockagentcore.RetrieveMemoryRecordsInput
	found     []types.MemoryRecordSummary
	// records is the long-term records held, by id, with their namespace.
	records map[string]string
	// listPaths is every namespacePath a record listing asked for.
	listPaths []string
	// batches is the size of every BatchDeleteMemoryRecords.
	batches []int
	// failOnce fails a record's first delete; failAlways every one;
	// goneRecords answers a delete of the record with a 404.
	failOnce, failAlways, goneRecords map[string]bool
	// events is the short-term events held: actor → session → event ids.
	events map[string]map[string][]string
	// deletedEvents is every event deleted, as actor|session|event.
	deletedEvents []string
	// goneEvents answers a delete of these event ids with ResourceNotFound.
	goneEvents map[string]bool
	// deletingEvent, when set, runs before each event delete.
	deletingEvent func(ctx context.Context) error
	// listErr fails every ListMemoryRecords.
	listErr error
}

func newFakeAgentCore() *fakeAgentCore {
	return &fakeAgentCore{
		records:     map[string]string{},
		failOnce:    map[string]bool{},
		failAlways:  map[string]bool{},
		goneRecords: map[string]bool{},
		events:      map[string]map[string][]string{},
		goneEvents:  map[string]bool{},
	}
}

// page returns items[from token] two at a time, and the next token.
func page[T any](items []T, token *string) ([]T, *string) {
	start, _ := strconv.Atoi(aws.ToString(token))
	end := min(start+2, len(items))
	if end >= len(items) {
		return items[start:end], nil
	}
	return items[start:end], aws.String(strconv.Itoa(end))
}

func (f *fakeAgentCore) CreateEvent(_ context.Context, input *bedrockagentcore.CreateEventInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.CreateEventOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.created = append(f.created, input)
	return &bedrockagentcore.CreateEventOutput{}, nil
}

func (f *fakeAgentCore) RetrieveMemoryRecords(_ context.Context, input *bedrockagentcore.RetrieveMemoryRecordsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.RetrieveMemoryRecordsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.retrieved = append(f.retrieved, input)
	return &bedrockagentcore.RetrieveMemoryRecordsOutput{MemoryRecordSummaries: f.found}, nil
}

// ListMemoryRecords matches namespacePath as a plain prefix, which is the
// behaviour the layout relies on (and the integration test checks).
func (f *fakeAgentCore) ListMemoryRecords(_ context.Context, input *bedrockagentcore.ListMemoryRecordsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListMemoryRecordsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	path := aws.ToString(input.NamespacePath)
	if input.NextToken == nil {
		f.listPaths = append(f.listPaths, path)
	}
	var matching []types.MemoryRecordSummary
	ids := make([]string, 0, len(f.records))
	for id := range f.records {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if strings.HasPrefix(f.records[id], path) {
			matching = append(matching, types.MemoryRecordSummary{MemoryRecordId: aws.String(id), Namespaces: []string{f.records[id]}})
		}
	}
	got, next := page(matching, input.NextToken)
	return &bedrockagentcore.ListMemoryRecordsOutput{MemoryRecordSummaries: got, NextToken: next}, nil
}

func (f *fakeAgentCore) BatchDeleteMemoryRecords(_ context.Context, input *bedrockagentcore.BatchDeleteMemoryRecordsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.BatchDeleteMemoryRecordsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	f.batches = append(f.batches, len(input.Records))
	output := &bedrockagentcore.BatchDeleteMemoryRecordsOutput{}
	for _, record := range input.Records {
		id := aws.ToString(record.MemoryRecordId)
		outcome := types.MemoryRecordOutput{MemoryRecordId: aws.String(id)}
		switch {
		case f.goneRecords[id]:
			outcome.Status, outcome.ErrorCode = types.MemoryRecordStatusFailed, aws.Int32(404)
			output.FailedRecords = append(output.FailedRecords, outcome)
			delete(f.records, id)
		case f.failAlways[id], f.failOnce[id]:
			delete(f.failOnce, id)
			outcome.Status, outcome.ErrorCode = types.MemoryRecordStatusFailed, aws.Int32(500)
			output.FailedRecords = append(output.FailedRecords, outcome)
		default:
			outcome.Status = types.MemoryRecordStatusSucceeded
			output.SuccessfulRecords = append(output.SuccessfulRecords, outcome)
			delete(f.records, id)
		}
	}
	return output, nil
}

func (f *fakeAgentCore) ListActors(_ context.Context, input *bedrockagentcore.ListActorsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListActorsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	var actors []types.ActorSummary
	for _, actor := range sortedKeys(f.events) {
		actors = append(actors, types.ActorSummary{ActorId: aws.String(actor)})
	}
	got, next := page(actors, input.NextToken)
	return &bedrockagentcore.ListActorsOutput{ActorSummaries: got, NextToken: next}, nil
}

func (f *fakeAgentCore) ListSessions(_ context.Context, input *bedrockagentcore.ListSessionsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListSessionsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	var sessions []types.SessionSummary
	for _, id := range sortedKeys(f.events[aws.ToString(input.ActorId)]) {
		sessions = append(sessions, types.SessionSummary{ActorId: input.ActorId, SessionId: aws.String(id)})
	}
	got, next := page(sessions, input.NextToken)
	return &bedrockagentcore.ListSessionsOutput{SessionSummaries: got, NextToken: next}, nil
}

func (f *fakeAgentCore) ListEvents(_ context.Context, input *bedrockagentcore.ListEventsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListEventsOutput, error) {
	f.mutex.Lock()
	defer f.mutex.Unlock()
	var events []types.Event
	for _, id := range f.events[aws.ToString(input.ActorId)][aws.ToString(input.SessionId)] {
		events = append(events, types.Event{EventId: aws.String(id)})
	}
	got, next := page(events, input.NextToken)
	return &bedrockagentcore.ListEventsOutput{Events: got, NextToken: next}, nil
}

func (f *fakeAgentCore) DeleteEvent(ctx context.Context, input *bedrockagentcore.DeleteEventInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.DeleteEventOutput, error) {
	if f.deletingEvent != nil {
		if err := f.deletingEvent(ctx); err != nil {
			return nil, err
		}
	}
	f.mutex.Lock()
	defer f.mutex.Unlock()
	id := aws.ToString(input.EventId)
	if f.goneEvents[id] {
		return nil, &types.ResourceNotFoundException{Message: aws.String("no such event")}
	}
	f.deletedEvents = append(f.deletedEvents, aws.ToString(input.ActorId)+"|"+aws.ToString(input.SessionId)+"|"+id)
	return &bedrockagentcore.DeleteEventOutput{EventId: input.EventId}, nil
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func agentCoreWith(t *testing.T, api agentCoreAPI) *AgentCoreMemory {
	t.Helper()
	store, err := newAgentCoreMemory(api, AgentCoreMemoryOptions{MemoryID: testMemoryID})
	if err != nil {
		t.Fatal(err)
	}
	store.deletingEvents = rate.NewLimiter(rate.Inf, 1)
	return store
}

// said is one turn of a test conversation.
type said struct {
	role, text string
	thought    bool
}

// thread returns a conversation in session threadID holding turns, the
// first at start and each a second after the one before.
func thread(t *testing.T, threadID string, start time.Time, turns ...said) session.Session {
	t.Helper()
	ctx := context.Background()
	sessions := session.InMemoryService()
	created, err := sessions.Create(ctx, &session.CreateRequest{AppName: "krude", UserID: "user-1", SessionID: threadID})
	if err != nil {
		t.Fatal(err)
	}
	for i, spoke := range turns {
		event := session.NewEvent(ctx, "invocation")
		event.Author = spoke.role
		event.Timestamp = start.Add(time.Duration(i) * time.Second)
		event.Content = &genai.Content{Role: spoke.role, Parts: []*genai.Part{{Text: spoke.text, Thought: spoke.thought}}}
		if err := sessions.AppendEvent(ctx, created.Session, event); err != nil {
			t.Fatal(err)
		}
	}
	return created.Session
}

var testStart = time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC)

func TestAgentCoreActorIsOrganizationMemberAndAgent(t *testing.T) {
	for _, test := range []struct {
		scope            Scope
		actor, namespace string
	}{
		{Scope{OrganizationID: "org_acme", UserID: "usr_1"}, "org_acme/usr_1/primary", "/m/org_acme/usr_1/primary/"},
		{Scope{OrganizationID: "org_acme", UserID: "usr_1", AgentID: "agt_Ab-9"}, "org_acme/usr_1/agt_Ab-9", "/m/org_acme/usr_1/agt_Ab-9/"},
	} {
		actor, err := agentCoreActor(test.scope)
		if err != nil {
			t.Fatalf("%+v: %v", test.scope, err)
		}
		if actor != test.actor || agentCoreNamespace(actor) != test.namespace {
			t.Errorf("%+v: actor %q in %q, want %q in %q", test.scope, actor, agentCoreNamespace(actor), test.actor, test.namespace)
		}
	}
}

// A "/" in any id would add a segment to the actor, so that one scope's
// address names another's: org "a" member "b/c" would be org "a/b" member
// "c". Every such scope is refused, as is anything else the layout cannot
// hold.
func TestAgentCoreRefusesAScopeThatWouldBreakItsLayout(t *testing.T) {
	for name, scope := range map[string]Scope{
		"slash in organization": {OrganizationID: "org/acme", UserID: "usr_1"},
		"slash in member":       {OrganizationID: "org_acme", UserID: "usr/1"},
		"slash in agent":        {OrganizationID: "org_acme", UserID: "usr_1", AgentID: "agt/1"},
		"agent named primary":   {OrganizationID: "org_acme", UserID: "usr_1", AgentID: "primary"},
		"colon":                 {OrganizationID: "org:acme", UserID: "usr_1"},
		"space":                 {OrganizationID: "org_acme", UserID: "usr 1"},
		"leading underscore":    {OrganizationID: "_org", UserID: "usr_1"},
		"too long":              {OrganizationID: "org_acme", UserID: strings.Repeat("u", 250)},
		"no member":             {OrganizationID: "org_acme"},
	} {
		if actor, err := agentCoreActor(scope); err == nil {
			t.Errorf("%s: accepted as %q", name, actor)
		}
	}
	_, err := agentCoreActor(Scope{OrganizationID: "org_acme", UserID: "usr/1"})
	if err == nil || !strings.Contains(err.Error(), `"/"`) {
		t.Fatalf("error = %v, want it to name the slash", err)
	}

	store := agentCoreWith(t, newFakeAgentCore())
	bad := Scope{OrganizationID: "org_acme", UserID: "usr/1"}
	if err := store.Write(context.Background(), bad, thread(t, "thread-1", testStart, said{role: genai.RoleUser, text: "hi"})); err == nil {
		t.Error("Write accepted a scope with a slash")
	}
	if _, err := store.Search(context.Background(), bad, "anything"); err == nil {
		t.Error("Search accepted a scope with a slash")
	}
	if err := store.Forget(context.Background(), "org/acme", ""); err == nil {
		t.Error("Forget accepted an organization with a slash")
	}
	if err := store.Forget(context.Background(), "org_acme", "usr/1"); err == nil {
		t.Error("Forget accepted a member with a slash")
	}
}

func TestAgentCoreForgetsByPrefixesThatEndInASlash(t *testing.T) {
	actors, namespaces, err := agentCoreForgetting("org_acme", "")
	if err != nil || actors != "org_acme/" || namespaces != "/m/org_acme/" {
		t.Fatalf("organization: %q, %q, %v", actors, namespaces, err)
	}
	actors, namespaces, err = agentCoreForgetting("org_acme", "usr_1")
	if err != nil || actors != "org_acme/usr_1/" || namespaces != "/m/org_acme/usr_1/" {
		t.Fatalf("member: %q, %q, %v", actors, namespaces, err)
	}
	if _, _, err := agentCoreForgetting(" ", ""); err == nil {
		t.Fatal("forgetting with no organization was accepted")
	}
}

func TestAgentCoreSessionIsTheThreadWhenItCanBe(t *testing.T) {
	if got := agentCoreSession("4b1f0c3e-9a2d-4c1b-8f00-0123456789ab"); got != "4b1f0c3e-9a2d-4c1b-8f00-0123456789ab" {
		t.Errorf("a uuid thread became %q", got)
	}
	for _, threadID := range []string{"requests-org.acme", "_leading", strings.Repeat("x", 101), ""} {
		got := agentCoreSession(threadID)
		if !sessionIDPattern.MatchString(got) {
			t.Errorf("thread %q became %q, which AgentCore refuses", threadID, got)
		}
		if again := agentCoreSession(threadID); again != got {
			t.Errorf("thread %q became %q and then %q", threadID, got, again)
		}
	}
	if agentCoreSession("a.b") == agentCoreSession("a:b") {
		t.Error("two threads became one session")
	}
}

func TestAgentCoreWritesWhatWasSaidAsOneEvent(t *testing.T) {
	api := newFakeAgentCore()
	store := agentCoreWith(t, api)
	conversation := thread(t, "thread-1", testStart,
		said{role: genai.RoleUser, text: "the migration ships in March"},
		said{role: genai.RoleModel, text: "let me work that out", thought: true},
		said{role: genai.RoleModel, text: "noted"},
		said{role: "krude", text: "a sub-agent said this"},
	)
	if err := store.Write(context.Background(), Scope{OrganizationID: "org_acme", UserID: "user-1"}, conversation); err != nil {
		t.Fatal(err)
	}
	if len(api.created) != 1 {
		t.Fatalf("%d events, want one", len(api.created))
	}
	event := api.created[0]
	if aws.ToString(event.MemoryId) != testMemoryID || aws.ToString(event.ActorId) != "org_acme/user-1/primary" || aws.ToString(event.SessionId) != "thread-1" {
		t.Fatalf("event in %q for %q in %q", aws.ToString(event.MemoryId), aws.ToString(event.ActorId), aws.ToString(event.SessionId))
	}
	if !aws.ToTime(event.EventTimestamp).Equal(testStart) {
		t.Errorf("event at %v, want the first turn's %v", aws.ToTime(event.EventTimestamp), testStart)
	}
	type message struct {
		role types.Role
		text string
	}
	var got []message
	for _, payload := range event.Payload {
		conversational := payload.(*types.PayloadTypeMemberConversational).Value
		got = append(got, message{conversational.Role, conversational.Content.(*types.ContentMemberText).Value})
	}
	want := []message{
		{types.RoleUser, "the migration ships in March"},
		{types.RoleAssistant, "noted"},
		{types.RoleAssistant, "a sub-agent said this"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("payload = %v, want %v", got, want)
	}
	if token := aws.ToString(event.ClientToken); len(token) < 33 || len(token) > 256 {
		t.Errorf("client token %q is outside AgentCore's 33 to 256 characters", token)
	}
}

func TestAgentCoreWritesNothingWhenTheMemberSaidNothing(t *testing.T) {
	api := newFakeAgentCore()
	store := agentCoreWith(t, api)
	scope := Scope{OrganizationID: "org_acme", UserID: "user-1"}
	for name, conversation := range map[string]session.Session{
		"empty":      thread(t, "thread-1", testStart),
		"model only": thread(t, "thread-2", testStart, said{role: genai.RoleModel, text: "a reminder fired"}),
		"thought":    thread(t, "thread-3", testStart, said{role: genai.RoleUser, text: "hm", thought: true}),
	} {
		if err := store.Write(context.Background(), scope, conversation); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(api.created) != 0 {
		t.Fatalf("%d events written for runs the member said nothing in", len(api.created))
	}
}

func TestAgentCoreSplitsALongRunAndCutsALongMessage(t *testing.T) {
	api := newFakeAgentCore()
	store := agentCoreWith(t, api)
	turns := make([]said, 0, 230)
	turns = append(turns, said{role: genai.RoleUser, text: strings.Repeat("é", maxMessageBytes)})
	for i := 1; i < 230; i++ {
		turns = append(turns, said{role: genai.RoleModel, text: fmt.Sprintf("turn %d", i)})
	}
	if err := store.Write(context.Background(), Scope{OrganizationID: "org_acme", UserID: "user-1"}, thread(t, "thread-1", testStart, turns...)); err != nil {
		t.Fatal(err)
	}
	if len(api.created) != 3 {
		t.Fatalf("%d events, want 230 turns in three", len(api.created))
	}
	tokens := map[string]bool{}
	for i, event := range api.created {
		if want := []int{100, 100, 30}[i]; len(event.Payload) != want {
			t.Errorf("event %d holds %d messages, want %d", i, len(event.Payload), want)
		}
		if want := testStart.Add(time.Duration(i*100) * time.Second); !aws.ToTime(event.EventTimestamp).Equal(want) {
			t.Errorf("event %d at %v, want its first turn's %v", i, aws.ToTime(event.EventTimestamp), want)
		}
		tokens[aws.ToString(event.ClientToken)] = true
	}
	if len(tokens) != 3 {
		t.Errorf("%d distinct client tokens for three events", len(tokens))
	}
	first := api.created[0].Payload[0].(*types.PayloadTypeMemberConversational).Value.Content.(*types.ContentMemberText).Value
	if len(first) > maxMessageBytes || len(first) < maxMessageBytes-1 || !strings.HasPrefix(strings.Repeat("é", maxMessageBytes), first) {
		t.Fatalf("a long message was kept as %d bytes, want it cut at a character to at most %d", len(first), maxMessageBytes)
	}
}

// Writing the same run again — a retry — must be the same request, so
// AgentCore ignores it rather than recording the run twice; a different run
// in the same thread must not be.
func TestAgentCoreClientTokenIsTheSameForTheSameRun(t *testing.T) {
	api := newFakeAgentCore()
	store := agentCoreWith(t, api)
	scope := Scope{OrganizationID: "org_acme", UserID: "user-1"}
	run := thread(t, "thread-1", testStart, said{role: genai.RoleUser, text: "hello"})
	later := thread(t, "thread-1", testStart.Add(time.Minute), said{role: genai.RoleUser, text: "hello"})
	elsewhere := thread(t, "thread-2", testStart, said{role: genai.RoleUser, text: "hello"})
	for _, conversation := range []session.Session{run, run, later, elsewhere} {
		if err := store.Write(context.Background(), scope, conversation); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Write(context.Background(), Scope{OrganizationID: "org_acme", UserID: "user-1", AgentID: "agt_1"}, run); err != nil {
		t.Fatal(err)
	}
	tokens := make([]string, 0, len(api.created))
	for _, event := range api.created {
		tokens = append(tokens, aws.ToString(event.ClientToken))
	}
	if tokens[0] != tokens[1] {
		t.Error("the same run written twice had two client tokens")
	}
	if tokens[0] == tokens[2] || tokens[0] == tokens[3] || tokens[0] == tokens[4] {
		t.Errorf("different runs shared a client token: %v", tokens)
	}
}

func TestAgentCoreSearchReadsTheActorsOwnNamespace(t *testing.T) {
	written := testStart.Add(-time.Hour)
	api := newFakeAgentCore()
	api.found = []types.MemoryRecordSummary{
		{MemoryRecordId: aws.String("mem-1"), Content: &types.MemoryContentMemberText{Value: "ships in March"}, Timestamp: aws.Time(written)},
		{MemoryRecordId: aws.String("mem-2"), Content: &types.MemoryContentMemberText{Value: ""}},
		{MemoryRecordId: aws.String("mem-3"), Content: &types.MemoryContentMemberText{Value: "prefers tea"}, CreatedAt: aws.Time(testStart)},
	}
	store := agentCoreWith(t, api)
	entries, err := store.Search(context.Background(), Scope{OrganizationID: "org_acme", UserID: "user-1", AgentID: "agt_1"}, "when does it ship?")
	if err != nil {
		t.Fatal(err)
	}
	request := api.retrieved[0]
	if aws.ToString(request.Namespace) != "/m/org_acme/user-1/agt_1/" || request.NamespacePath != nil {
		t.Fatalf("searched namespace %q, path %q; want exactly the agent's own", aws.ToString(request.Namespace), aws.ToString(request.NamespacePath))
	}
	if aws.ToString(request.SearchCriteria.SearchQuery) != "when does it ship?" || aws.ToInt32(request.SearchCriteria.TopK) != defaultRecalled {
		t.Fatalf("criteria = %q, top %d", aws.ToString(request.SearchCriteria.SearchQuery), aws.ToInt32(request.SearchCriteria.TopK))
	}
	if len(entries) != 2 || entries[0].ID != "mem-1" || entries[0].Content.Parts[0].Text != "ships in March" || !entries[0].Timestamp.Equal(written) ||
		entries[1].ID != "mem-3" || !entries[1].Timestamp.Equal(testStart) || entries[0].Content.Role != genai.RoleUser {
		t.Fatalf("entries = %+v", entries)
	}

	if _, err := store.Search(context.Background(), Scope{OrganizationID: "org_acme", UserID: "user-1"}, "  "); err != nil || len(api.retrieved) != 1 {
		t.Fatalf("an empty query asked AgentCore anyway (%v)", err)
	}
}

// heldRecords puts n records in namespace, named prefix-0 onwards.
func heldRecords(api *fakeAgentCore, prefix, namespace string, n int) {
	for i := range n {
		api.records[fmt.Sprintf("%s-%03d", prefix, i)] = namespace
	}
}

func TestAgentCoreForgetsAMembersRecordsAndEventsOnly(t *testing.T) {
	api := newFakeAgentCore()
	heldRecords(api, "mine", "/m/org_acme/usr_1/primary/", 3)
	heldRecords(api, "agent", "/m/org_acme/usr_1/agt_1/", 2)
	heldRecords(api, "colleague", "/m/org_acme/usr_10/primary/", 2)
	heldRecords(api, "elsewhere", "/m/org_acme2/usr_1/primary/", 2)
	api.events["org_acme/usr_1/primary"] = map[string][]string{"t1": {"e1", "e2", "e3"}, "t2": {"e4"}, "t3": {"e5"}}
	api.events["org_acme/usr_1/agt_1"] = map[string][]string{"t4": {"e6"}}
	api.events["org_acme/usr_10/primary"] = map[string][]string{"t5": {"e7"}}
	api.events["org_acme2/usr_1/primary"] = map[string][]string{"t6": {"e8"}}

	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.listPaths, []string{"/m/org_acme/usr_1/"}) {
		t.Fatalf("listed %v", api.listPaths)
	}
	if left := sortedKeys(api.records); !slices.Equal(left, []string{"colleague-000", "colleague-001", "elsewhere-000", "elsewhere-001"}) {
		t.Fatalf("records left = %v", left)
	}
	sort.Strings(api.deletedEvents)
	want := []string{
		"org_acme/usr_1/agt_1|t4|e6",
		"org_acme/usr_1/primary|t1|e1", "org_acme/usr_1/primary|t1|e2", "org_acme/usr_1/primary|t1|e3",
		"org_acme/usr_1/primary|t2|e4", "org_acme/usr_1/primary|t3|e5",
	}
	if !slices.Equal(api.deletedEvents, want) {
		t.Fatalf("deleted events %v, want %v", api.deletedEvents, want)
	}
}

func TestAgentCoreForgetsAWholeOrganizationAndNoOther(t *testing.T) {
	api := newFakeAgentCore()
	heldRecords(api, "a", "/m/org_acme/usr_1/primary/", 2)
	heldRecords(api, "b", "/m/org_acme/usr_2/agt_1/", 2)
	heldRecords(api, "other", "/m/org_acme2/usr_1/primary/", 1)
	api.events["org_acme/usr_1/primary"] = map[string][]string{"t1": {"e1"}}
	api.events["org_acme/usr_2/agt_1"] = map[string][]string{"t2": {"e2"}}
	api.events["org_acme2/usr_1/primary"] = map[string][]string{"t3": {"e3"}}

	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", ""); err != nil {
		t.Fatal(err)
	}
	if left := sortedKeys(api.records); !slices.Equal(left, []string{"other-000"}) {
		t.Fatalf("records left = %v", left)
	}
	sort.Strings(api.deletedEvents)
	if !slices.Equal(api.deletedEvents, []string{"org_acme/usr_1/primary|t1|e1", "org_acme/usr_2/agt_1|t2|e2"}) {
		t.Fatalf("deleted events %v", api.deletedEvents)
	}
}

// A listing that answers more widely than asked must still delete nothing
// outside the prefix.
func TestAgentCoreForgetMatchesTheNamespaceItself(t *testing.T) {
	api := &widerListing{fakeAgentCore: newFakeAgentCore()}
	heldRecords(api.fakeAgentCore, "mine", "/m/org_acme/usr_1/primary/", 1)
	heldRecords(api.fakeAgentCore, "theirs", "/m/org_other/usr_1/primary/", 1)
	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", ""); err != nil {
		t.Fatal(err)
	}
	if left := sortedKeys(api.records); !slices.Equal(left, []string{"theirs-000"}) {
		t.Fatalf("records left = %v", left)
	}
}

// widerListing lists every record whatever the path.
type widerListing struct{ *fakeAgentCore }

func (w *widerListing) ListMemoryRecords(ctx context.Context, input *bedrockagentcore.ListMemoryRecordsInput, _ ...func(*bedrockagentcore.Options)) (*bedrockagentcore.ListMemoryRecordsOutput, error) {
	wide := *input
	wide.NamespacePath = aws.String("/")
	return w.fakeAgentCore.ListMemoryRecords(ctx, &wide)
}

func TestAgentCoreForgetDeletesAHundredAtATime(t *testing.T) {
	api := newFakeAgentCore()
	heldRecords(api, "mine", "/m/org_acme/usr_1/primary/", 250)
	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.batches, []int{100, 100, 50}) || len(api.records) != 0 {
		t.Fatalf("batches %v, %d left", api.batches, len(api.records))
	}
}

func TestAgentCoreForgetRetriesWhatFailedOnce(t *testing.T) {
	api := newFakeAgentCore()
	heldRecords(api, "mine", "/m/org_acme/usr_1/primary/", 120)
	api.failOnce["mine-005"], api.failOnce["mine-110"] = true, true
	api.goneRecords["mine-007"] = true
	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.batches, []int{100, 20, 2}) || len(api.records) != 0 {
		t.Fatalf("batches %v, %d left; want the two failures sent once more and the gone one taken as forgotten", api.batches, len(api.records))
	}
}

func TestAgentCoreForgetSaysWhatItCouldNotDelete(t *testing.T) {
	api := newFakeAgentCore()
	heldRecords(api, "mine", "/m/org_acme/usr_1/primary/", 4)
	api.failAlways["mine-002"] = true
	api.events["org_acme/usr_1/primary"] = map[string][]string{"t1": {"e1"}}
	store := agentCoreWith(t, api)
	err := store.Forget(context.Background(), "org_acme", "usr_1")
	if err == nil || !strings.Contains(err.Error(), "forgot 3 of 4 memory records") {
		t.Fatalf("Forget = %v, want it to say one record was left", err)
	}
	if !slices.Equal(api.batches, []int{4, 1}) {
		t.Fatalf("batches %v: the failed record should be retried exactly once", api.batches)
	}
	if len(api.deletedEvents) != 1 {
		t.Fatalf("a record that would not go stopped the events going too (%v)", api.deletedEvents)
	}
}

func TestAgentCoreForgetTakesAnEventAlreadyGoneAsForgotten(t *testing.T) {
	api := newFakeAgentCore()
	api.events["org_acme/usr_1/primary"] = map[string][]string{"t1": {"e1", "e2", "e3"}}
	api.goneEvents["e2"] = true
	store := agentCoreWith(t, api)
	if err := store.Forget(context.Background(), "org_acme", "usr_1"); err != nil {
		t.Fatal(err)
	}
	if len(api.deletedEvents) != 2 {
		t.Fatalf("deleted %v", api.deletedEvents)
	}
}

func TestAgentCoreForgetStopsAtItsDeadlineAndSaysWhatItLeft(t *testing.T) {
	api := newFakeAgentCore()
	sessions := map[string][]string{}
	for i := range 10 {
		sessions["t1"] = append(sessions["t1"], fmt.Sprintf("e%d", i))
	}
	api.events["org_acme/usr_1/primary"] = sessions
	deletes := 0
	api.deletingEvent = func(ctx context.Context) error {
		deletes++
		if deletes <= 3 {
			return nil
		}
		<-ctx.Done()
		return ctx.Err()
	}
	store := agentCoreWith(t, api)
	store.forgetTimeout = 50 * time.Millisecond
	err := store.Forget(context.Background(), "org_acme", "usr_1")
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "forgot 3 of 10 memory events") {
		t.Fatalf("Forget = %v, want it to stop at its deadline having forgotten 3 of 10", err)
	}
}

func TestAgentCoreForgetReportsAFailedListing(t *testing.T) {
	api := newFakeAgentCore()
	api.listErr = errors.New("the memory was unreachable")
	api.events["org_acme/usr_1/primary"] = map[string][]string{"t1": {"e1"}}
	store := agentCoreWith(t, api)
	err := store.Forget(context.Background(), "org_acme", "usr_1")
	if err == nil || !strings.Contains(err.Error(), "unreachable") {
		t.Fatalf("Forget = %v, want the listing's failure", err)
	}
	if len(api.deletedEvents) != 1 {
		t.Fatal("a failed record listing stopped the events being forgotten")
	}
}

func TestAgentCoreNeedsAMemoryID(t *testing.T) {
	if _, err := newAgentCoreMemory(newFakeAgentCore(), AgentCoreMemoryOptions{MemoryID: " "}); err == nil {
		t.Fatal("a backend with no memory id was built")
	}
	if _, err := newAgentCoreMemory(newFakeAgentCore(), AgentCoreMemoryOptions{MemoryID: "m", Recalled: -1}); err == nil {
		t.Fatal("a backend recalling fewer than none was built")
	}
	store, err := newAgentCoreMemory(newFakeAgentCore(), AgentCoreMemoryOptions{MemoryID: "m", Recalled: 500})
	if err != nil || store.recalled != maxRecalled {
		t.Fatalf("recalled = %d, %v; want the ceiling", store.recalled, err)
	}
}
