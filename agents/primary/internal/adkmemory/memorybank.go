package adkmemory

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"iter"
	"strings"

	aiplatform "cloud.google.com/go/aiplatform/apiv1beta1"
	"cloud.google.com/go/aiplatform/apiv1beta1/aiplatformpb"
	"cloud.google.com/go/auth"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/genai"

	"google.golang.org/adk/v2/memory"
	"google.golang.org/adk/v2/session"
	adkaiplatform "google.golang.org/adk/v2/util/aiplatform"
)

// defaultRecalled is how many memories a search asks Memory Bank for. Its own
// default is three, which is fewer than a conversation usually needs; ten
// matches what ADK's in-memory service returns.
const defaultRecalled = 10

// maxRecalled is Memory Bank's own ceiling on one search.
const maxRecalled = 100

// wildcard cannot appear in a scope value: Memory Bank reserves it, and a
// scope it rejects would be a run with no memory rather than a clear failure.
const wildcard = "*"

// MemoryBankOptions is what a Memory Bank backend runs against.
type MemoryBankOptions struct {
	// ProjectID and Location are the Vertex AI project and region the agent
	// engine lives in. Agent engines are regional, so the location has to be
	// a region such as us-central1: the global endpoint the models are
	// served from does not host them.
	ProjectID string
	Location  string
	// AgentEngine is the agent engine (reasoning engine) ID whose memory
	// bank holds the memories. Its bank must have similarity search
	// configured, or retrieval fails.
	AgentEngine string
	// Recalled is how many memories a search returns at most. Memory Bank
	// caps it at 100. Zero means defaultRecalled.
	Recalled int
	// WaitForWrites makes Write wait for Memory Bank to finish extracting
	// memories from a conversation rather than returning once the work is
	// accepted. Extraction runs a model and takes seconds, so a run should
	// not wait for it; tests that read back what they wrote must.
	WaitForWrites bool
	// Credentials reach Vertex AI, such as those internal/gcpauth federates
	// from AWS. Nil means Application Default Credentials.
	Credentials *auth.Credentials
}

// memoryBankAPI is the part of Vertex AI's memory bank this backend uses,
// narrowed so the backend can be tested without one. bankClient implements
// it over [aiplatform.MemoryBankClient].
type memoryBankAPI interface {
	generate(ctx context.Context, request *aiplatformpb.GenerateMemoriesRequest, wait bool) error
	retrieve(ctx context.Context, request *aiplatformpb.RetrieveMemoriesRequest) ([]*aiplatformpb.Memory, error)
	list(ctx context.Context, parent string) iter.Seq2[*aiplatformpb.Memory, error]
	remove(ctx context.Context, name string) error
	Close() error
}

// MemoryBank keeps what Krude remembers in Vertex AI Memory Bank, which
// extracts the facts worth keeping from a conversation and consolidates them
// with what it already holds, rather than storing the conversation itself.
//
// Memory Bank partitions by a scope map and returns a memory only to a
// request whose scope has the same keys and values, so [Scope.Fields] makes
// the organization a dimension the service matches on: a memory written in
// one organization cannot be retrieved from another, and that is enforced
// where the memories are rather than here.
type MemoryBank struct {
	api      memoryBankAPI
	parent   string
	recalled int32
	wait     bool
}

var _ Backend = (*MemoryBank)(nil)

// NewMemoryBank connects to the memory bank of the agent engine named in
// options. It contacts nothing: the connection is made on first use, with
// options' credentials or, without any, those Google's own discovery finds.
func NewMemoryBank(ctx context.Context, options MemoryBankOptions) (*MemoryBank, error) {
	location := strings.TrimSpace(options.Location)
	switch {
	case strings.TrimSpace(options.ProjectID) == "":
		return nil, errors.New("adkmemory: a memory bank needs a Google Cloud project")
	case strings.TrimSpace(options.AgentEngine) == "":
		return nil, errors.New("adkmemory: a memory bank needs an agent engine")
	case location == "" || location == "global":
		// Saying so here turns what would be a confusing "not found" on the
		// first run into one message at startup: the models are served from
		// the global endpoint, but the engine holding their memories is not
		// there.
		return nil, errors.New("adkmemory: a memory bank needs the region its agent engine is in, such as us-central1, not the global endpoint")
	case options.Recalled < 0:
		return nil, errors.New("adkmemory: a memory bank cannot recall fewer than no memories")
	}
	clientOptions := []option.ClientOption{option.WithEndpoint(adkaiplatform.HostPortURL(location))}
	if options.Credentials != nil {
		clientOptions = append(clientOptions, option.WithAuthCredentials(options.Credentials))
	}
	client, err := aiplatform.NewMemoryBankClient(ctx, clientOptions...)
	if err != nil {
		return nil, fmt.Errorf("open memory bank client: %w", err)
	}
	recalled := cmp.Or(options.Recalled, defaultRecalled)
	if recalled > maxRecalled {
		// Memory Bank coerces anything above its own ceiling down to it, so
		// this only keeps the conversion below honest.
		recalled = maxRecalled
	}
	return &MemoryBank{
		api: &bankClient{client: client},
		parent: fmt.Sprintf("projects/%s/locations/%s/reasoningEngines/%s",
			options.ProjectID, location, options.AgentEngine),
		recalled: int32(recalled),
		wait:     options.WaitForWrites,
	}, nil
}

// Close releases the connection to Vertex AI.
func (bank *MemoryBank) Close() error {
	return bank.api.Close()
}

// Write implements [Backend]. It hands Memory Bank the conversation's own
// turns rather than a session resource: threads live in this deployment's
// PostgreSQL, not in the agent engine, so there is no session there for
// Memory Bank to read.
func (bank *MemoryBank) Write(ctx context.Context, scope Scope, conversation session.Session) error {
	fields, err := scopeFields(scope)
	if err != nil {
		return err
	}
	said := spoken(conversation)
	if !memberSpoke(said) {
		// A run in which the member said nothing has nothing of theirs to
		// remember, and Memory Bank refuses one anyway: an empty source, or
		// one that is only the model's side ("Requests ending with a model
		// turn are not supported"), is rejected, which production saw.
		return nil
	}
	return bank.api.generate(ctx, &aiplatformpb.GenerateMemoriesRequest{
		Parent: bank.parent,
		Source: &aiplatformpb.GenerateMemoriesRequest_DirectContentsSource_{
			DirectContentsSource: &aiplatformpb.GenerateMemoriesRequest_DirectContentsSource{Events: said},
		},
		Scope: fields,
	}, bank.wait)
}

// Search implements [Backend].
func (bank *MemoryBank) Search(ctx context.Context, scope Scope, query string) ([]memory.Entry, error) {
	fields, err := scopeFields(scope)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	found, err := bank.api.retrieve(ctx, &aiplatformpb.RetrieveMemoriesRequest{
		Parent: bank.parent,
		Scope:  fields,
		RetrievalParams: &aiplatformpb.RetrieveMemoriesRequest_SimilaritySearchParams_{
			SimilaritySearchParams: &aiplatformpb.RetrieveMemoriesRequest_SimilaritySearchParams{
				SearchQuery: query,
				TopK:        bank.recalled,
			},
		},
	})
	if err != nil {
		return nil, err
	}
	entries := make([]memory.Entry, 0, len(found))
	for _, remembered := range found {
		if remembered == nil || remembered.GetFact() == "" {
			continue
		}
		entry := memory.Entry{
			ID:      remembered.GetName(),
			Content: genai.NewContentFromText(remembered.GetFact(), genai.RoleUser),
		}
		if written := remembered.GetUpdateTime(); written != nil {
			entry.Timestamp = written.AsTime()
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// Forget implements [Backend].
//
// Memory Bank matches a listing's scope filter by equality on the whole
// scope, so it cannot express "every member of this organization". Rather
// than build a filter string whose exact form decides whether memories are
// deleted or silently left behind, this reads the bank and matches the scope
// here: forgetting too little is the failure that matters, and it would be a
// quiet one. Forgetting runs when a member leaves or an organization is
// deleted, not on any hot path.
func (bank *MemoryBank) Forget(ctx context.Context, organizationID, userID string) error {
	if strings.Contains(organizationID, wildcard) || strings.Contains(userID, wildcard) {
		return fmt.Errorf("adkmemory: a scope cannot contain %q", wildcard)
	}
	// Collect before deleting: the listing is paginated, and removing
	// memories while paging through it would move the page under the cursor
	// and leave some of them behind.
	var forgetting []string
	for remembered, err := range bank.api.list(ctx, bank.parent) {
		if err != nil {
			return fmt.Errorf("list memories to forget: %w", err)
		}
		scope := remembered.GetScope()
		if scope[FieldOrganizationID] != organizationID {
			continue
		}
		if userID != "" && scope[FieldUserID] != userID {
			continue
		}
		forgetting = append(forgetting, remembered.GetName())
	}
	for _, name := range forgetting {
		if err := bank.api.remove(ctx, name); err != nil {
			return fmt.Errorf("forget memory %s: %w", name, err)
		}
	}
	return nil
}

// scopeFields validates the scope and renders it as Memory Bank's own.
func scopeFields(scope Scope) (map[string]string, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	fields := scope.Fields()
	for key, value := range fields {
		if strings.Contains(value, wildcard) {
			return nil, fmt.Errorf("adkmemory: scope %s cannot contain %q", key, wildcard)
		}
	}
	return fields, nil
}

// spoken is what was said in a conversation, as the turns Memory Bank reads
// to work out what is worth remembering. Only text is sent: a tool's call and
// its result are how an answer was reached, not something the member told
// Krude, and they carry the contents of whatever the tools read.
func spoken(conversation session.Session) []*aiplatformpb.GenerateMemoriesRequest_DirectContentsSource_Event {
	if conversation == nil {
		return nil
	}
	var said []*aiplatformpb.GenerateMemoriesRequest_DirectContentsSource_Event
	for event := range conversation.Events().All() {
		if event == nil || event.Content == nil {
			continue
		}
		var parts []*aiplatformpb.Part
		for _, part := range event.Content.Parts {
			if part == nil || part.Text == "" || part.Thought {
				continue
			}
			parts = append(parts, &aiplatformpb.Part{
				Data: &aiplatformpb.Part_Text{Text: part.Text},
			})
		}
		if len(parts) == 0 {
			continue
		}
		role := event.Content.Role
		if role != genai.RoleUser {
			// Memory Bank takes the roles Gemini does, and anything Krude,
			// a sub-agent or a tool authored is the model's side of it.
			role = genai.RoleModel
		}
		said = append(said, &aiplatformpb.GenerateMemoriesRequest_DirectContentsSource_Event{
			Content: &aiplatformpb.Content{Role: role, Parts: parts},
		})
	}
	return said
}

// memberSpoke reports whether any of turns is the member's.
func memberSpoke(turns []*aiplatformpb.GenerateMemoriesRequest_DirectContentsSource_Event) bool {
	for _, turn := range turns {
		if turn.GetContent().GetRole() == genai.RoleUser {
			return true
		}
	}
	return false
}

// bankClient is memoryBankAPI over the generated client.
type bankClient struct {
	client *aiplatform.MemoryBankClient
}

func (c *bankClient) generate(ctx context.Context, request *aiplatformpb.GenerateMemoriesRequest, wait bool) error {
	operation, err := c.client.GenerateMemories(ctx, request)
	if err != nil {
		return fmt.Errorf("generate memories: %w", err)
	}
	if !wait {
		return nil
	}
	if _, err := operation.Wait(ctx); err != nil {
		return fmt.Errorf("wait for generated memories: %w", err)
	}
	return nil
}

func (c *bankClient) retrieve(ctx context.Context, request *aiplatformpb.RetrieveMemoriesRequest) ([]*aiplatformpb.Memory, error) {
	response, err := c.client.RetrieveMemories(ctx, request)
	if err != nil {
		return nil, fmt.Errorf("retrieve memories: %w", err)
	}
	found := make([]*aiplatformpb.Memory, 0, len(response.GetRetrievedMemories()))
	for _, retrieved := range response.GetRetrievedMemories() {
		found = append(found, retrieved.GetMemory())
	}
	return found, nil
}

func (c *bankClient) list(ctx context.Context, parent string) iter.Seq2[*aiplatformpb.Memory, error] {
	return func(yield func(*aiplatformpb.Memory, error) bool) {
		memories := c.client.ListMemories(ctx, &aiplatformpb.ListMemoriesRequest{Parent: parent})
		for {
			remembered, err := memories.Next()
			if errors.Is(err, iterator.Done) {
				return
			}
			if err != nil {
				yield(nil, err)
				return
			}
			if !yield(remembered, nil) {
				return
			}
		}
	}
}

func (c *bankClient) remove(ctx context.Context, name string) error {
	operation, err := c.client.DeleteMemory(ctx, &aiplatformpb.DeleteMemoryRequest{Name: name})
	if err != nil {
		return err
	}
	if err := operation.Wait(ctx); err != nil {
		return fmt.Errorf("wait for the memory to be deleted: %w", err)
	}
	return nil
}

func (c *bankClient) Close() error { return c.client.Close() }
