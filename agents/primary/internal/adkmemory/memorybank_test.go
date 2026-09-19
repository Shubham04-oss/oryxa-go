package adkmemory

import (
	"context"
	"errors"
	"iter"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"cloud.google.com/go/aiplatform/apiv1beta1/aiplatformpb"
	"cloud.google.com/go/auth"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/session"
)

const testParent = "projects/oryxa/locations/us-central1/reasoningEngines/1234"

// fakeBank records what the backend asked Memory Bank for and answers with
// what it was given.
type fakeBank struct {
	generated []*aiplatformpb.GenerateMemoriesRequest
	retrieved []*aiplatformpb.RetrieveMemoriesRequest
	// held is what the bank already holds, as a listing returns it.
	held    []*aiplatformpb.Memory
	found   []*aiplatformpb.Memory
	removed []string
	listErr error
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
		if f.listErr != nil {
			yield(nil, f.listErr)
			return
		}
		for _, remembered := range f.held {
			if !yield(remembered, nil) {
				return
			}
		}
	}
}

func (f *fakeBank) remove(_ context.Context, name string) error {
	f.removed = append(f.removed, name)
	return nil
}

func (f *fakeBank) Close() error { return nil }

func bankWith(api memoryBankAPI) *MemoryBank {
	return &MemoryBank{api: api, parent: testParent, recalled: defaultRecalled}
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
