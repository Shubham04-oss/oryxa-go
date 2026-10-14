package application

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

// TestAnAgentsSearchIsLoggedWithTheAgent: what an agent reads is recorded as
// that agent's, by ID and role, not merely as "the member's"; a member's own
// search is not logged as an agent's.
func TestAnAgentsSearchIsLoggedWithTheAgent(t *testing.T) {
	h := newHarness()
	var logs bytes.Buffer
	h.service.logger = slog.New(slog.NewJSONHandler(&logs, nil))
	ctx := context.Background()
	corpus, err := h.service.CreateCorpus(ctx, creds("user-creator"), testOrganization, CreateCorpusInput{Name: "Notes"})
	if err != nil {
		t.Fatalf("CreateCorpus: %v", err)
	}

	if _, err := h.service.Search(ctx, creds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "plans"}); err != nil {
		t.Fatal(err)
	}
	if logs.Len() != 0 {
		t.Fatalf("a member's own search was logged as an agent's: %s", logs.String())
	}
	if _, err := h.service.Search(ctx, agentCreds("user-creator"), testOrganization, corpus.ID, domain.Query{Text: "plans"}); err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("log = %q: %v", logs.String(), err)
	}
	if entry["operation"] != "search" || entry["member_id"] != "user-creator" || entry["agent_id"] != "agt_user-creator" ||
		entry["agent_role"] != "specialist" || entry["corpus_id"] != corpus.ID {
		t.Fatalf("log entry = %v, want the search naming the specialist", entry)
	}
}
