package adkmemory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMemoryBankRoundTrip runs the backend against a real Vertex AI memory
// bank: it writes a conversation, waits for the facts to be extracted, reads
// them back, and forgets them again. It is skipped without a project and an
// agent engine whose bank has similarity search configured:
//
//	GOOGLE_CLOUD_PROJECT=oryxa \
//	GOOGLE_CLOUD_LOCATION=us-central1 \
//	AGENTS_PRIMARY_TEST_MEMORY_AGENT_ENGINE=1234567890 \
//	go test ./agents/primary/internal/adkmemory/ -run TestMemoryBankRoundTrip -v
//
// It writes under an organization of its own, named after the run, and
// forgets it at the end, so it never touches a real member's memories.
func TestMemoryBankRoundTrip(t *testing.T) {
	project := os.Getenv("GOOGLE_CLOUD_PROJECT")
	engine := os.Getenv("AGENTS_PRIMARY_TEST_MEMORY_AGENT_ENGINE")
	location := os.Getenv("GOOGLE_CLOUD_LOCATION")
	if project == "" || engine == "" {
		t.Skip("GOOGLE_CLOUD_PROJECT and AGENTS_PRIMARY_TEST_MEMORY_AGENT_ENGINE are not both set")
	}
	if location == "" || location == "global" {
		t.Skip("GOOGLE_CLOUD_LOCATION must name the agent engine's own region, such as us-central1")
	}
	ctx := context.Background()

	bank, err := NewMemoryBank(ctx, MemoryBankOptions{
		ProjectID:   project,
		Location:    location,
		AgentEngine: engine,
		// Reading back what was just written means waiting for the model
		// that extracts it.
		WaitForWrites: true,
	})
	if err != nil {
		t.Fatalf("NewMemoryBank: %v", err)
	}
	defer func() {
		if err := bank.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	}()

	organization := "test-org-" + time.Now().UTC().Format("20060102150405")
	scope := Scope{OrganizationID: organization, UserID: "test-user"}
	elsewhere := Scope{OrganizationID: organization + "-other", UserID: "test-user"}
	t.Cleanup(func() {
		for _, org := range []string{organization, organization + "-other"} {
			if err := bank.Forget(context.Background(), org, ""); err != nil {
				t.Errorf("forget %s: %v", org, err)
			}
		}
	})

	if err := bank.Write(ctx, scope, conversation(t, scope.UserID,
		"Remember that the Orion migration is scheduled for March.")); err != nil {
		t.Fatalf("Write: %v", err)
	}

	found, err := bank.Search(ctx, scope, "When is the Orion migration?")
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(found) == 0 {
		t.Fatal("nothing was remembered from the conversation")
	}
	var facts []string
	for _, entry := range found {
		for _, part := range entry.Content.Parts {
			facts = append(facts, part.Text)
		}
	}
	if !strings.Contains(strings.ToLower(strings.Join(facts, " ")), "orion") {
		t.Fatalf("remembered %q, want something about the migration", facts)
	}

	// The same member in another organization must not see it, which is the
	// isolation Memory Bank itself enforces on the scope.
	other, err := bank.Search(ctx, elsewhere, "When is the Orion migration?")
	if err != nil {
		t.Fatalf("Search elsewhere: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("another organization read %d of these memories", len(other))
	}

	if err := bank.Forget(ctx, organization, scope.UserID); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	gone, err := bank.Search(ctx, scope, "When is the Orion migration?")
	if err != nil {
		t.Fatalf("Search after forgetting: %v", err)
	}
	if len(gone) != 0 {
		t.Fatalf("%d memories survived being forgotten", len(gone))
	}
}
