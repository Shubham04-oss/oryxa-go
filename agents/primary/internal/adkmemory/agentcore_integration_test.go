package adkmemory

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/bedrockagentcore"
	"google.golang.org/genai"
)

// TestAgentCoreMemoryRoundTrip runs the backend against a real AgentCore
// memory: it writes a conversation for two members of one organization and
// one of another, waits for the semantic strategy to extract records, reads
// them back, checks that a namespacePath of one organization lists only that
// organization's records (the layout's unverified assumption: that the "/"
// in an actor id makes namespace segments), and forgets them again. It is
// skipped without a memory created as deploy/production/memory.tf creates it:
//
//	AWS_REGION=ap-south-1 \
//	AGENTS_PRIMARY_TEST_AGENTCORE_MEMORY_ID=krude_memory-abc123 \
//	go test ./agents/primary/internal/adkmemory/ -run TestAgentCoreMemoryRoundTrip -v -timeout 10m
//
// It writes under organizations of its own, named after the run, so it never
// touches a real member's memories.
func TestAgentCoreMemoryRoundTrip(t *testing.T) {
	memoryID := os.Getenv("AGENTS_PRIMARY_TEST_AGENTCORE_MEMORY_ID")
	if memoryID == "" {
		t.Skip("AGENTS_PRIMARY_TEST_AGENTCORE_MEMORY_ID is not set")
	}
	ctx := context.Background()
	awsConfig, err := awsconfig.LoadDefaultConfig(ctx)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewAgentCoreMemory(AgentCoreMemoryOptions{AWS: awsConfig, MemoryID: memoryID})
	if err != nil {
		t.Fatal(err)
	}
	run := time.Now().UTC().Format("20060102150405")
	organization, other := "test-org-"+run, "test-org-"+run+"-other"
	scope := Scope{OrganizationID: organization, UserID: "test-user"}
	colleague := Scope{OrganizationID: organization, UserID: "test-colleague"}
	elsewhere := Scope{OrganizationID: other, UserID: "test-user"}
	t.Cleanup(func() {
		for _, org := range []string{organization, other} {
			if err := store.Forget(context.Background(), org, ""); err != nil {
				t.Errorf("forget %s: %v", org, err)
			}
		}
	})

	for _, write := range []struct {
		scope Scope
		fact  string
	}{
		{scope, "My favourite programming language is Haskell, and I write it every day."},
		{colleague, "I always drink green tea in the afternoon."},
		{elsewhere, "My favourite programming language is COBOL."},
	} {
		conversation := thread(t, "thread-"+run, time.Now(),
			said{role: genai.RoleUser, text: write.fact},
			said{role: genai.RoleModel, text: "Noted, I will remember that."})
		if err := store.Write(ctx, write.scope, conversation); err != nil {
			t.Fatalf("Write %+v: %v", write.scope, err)
		}
	}

	// Extraction is asynchronous and takes a minute or more.
	var found []string
	for deadline := time.Now().Add(5 * time.Minute); time.Now().Before(deadline); time.Sleep(15 * time.Second) {
		entries, err := store.Search(ctx, scope, "favourite programming language")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		found = found[:0]
		for _, entry := range entries {
			found = append(found, entry.Content.Parts[0].Text)
		}
		if len(found) > 0 {
			break
		}
	}
	if len(found) == 0 {
		t.Fatal("nothing was extracted within five minutes")
	}
	for _, fact := range found {
		if strings.Contains(fact, "COBOL") || strings.Contains(fact, "tea") {
			t.Fatalf("a search in %+v found another scope's memory: %q", scope, fact)
		}
	}

	// The layout's assumption: a namespacePath of the organization lists
	// both its members' records and nothing of the other organization.
	_, namespacePath, err := agentCoreForgetting(organization, "")
	if err != nil {
		t.Fatal(err)
	}
	listed, err := bedrockagentcore.NewFromConfig(awsConfig).ListMemoryRecords(ctx, &bedrockagentcore.ListMemoryRecordsInput{
		MemoryId: aws.String(memoryID), NamespacePath: aws.String(namespacePath),
	})
	if err != nil {
		t.Fatalf("ListMemoryRecords %s: %v", namespacePath, err)
	}
	if len(listed.MemoryRecordSummaries) == 0 {
		t.Fatalf("namespacePath %s listed nothing: the actor id's \"/\" does not make namespace segments, and the layout needs its fallback", namespacePath)
	}
	for _, record := range listed.MemoryRecordSummaries {
		if _, ok := underNamespace(record.Namespaces, namespacePath); !ok {
			t.Fatalf("namespacePath %s listed a record in %v", namespacePath, record.Namespaces)
		}
	}

	if err := store.Forget(ctx, organization, ""); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if entries, err := store.Search(ctx, scope, "favourite programming language"); err != nil || len(entries) != 0 {
		t.Fatalf("after forgetting, Search = %d entries, %v", len(entries), err)
	}
	if entries, err := store.Search(ctx, elsewhere, "favourite programming language"); err != nil {
		t.Fatalf("Search elsewhere: %v", err)
	} else if len(entries) == 0 {
		t.Log("the other organization's record was not extracted yet; its isolation from Forget is unchecked")
	}
}
