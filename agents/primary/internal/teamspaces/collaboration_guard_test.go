package teamspaces

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	primaryagents "github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
)

// bedrockFinding is a Bedrock that finds a password, hunter2, which the
// guardrail masks, and the email address priya@example.com, which it only
// detects, and that cannot read a text with UNREADABLE in it. It keeps every
// text it was sent.
type bedrockFinding struct {
	mu    sync.Mutex
	asked []string
}

func (bedrock *bedrockFinding) ApplyGuardrail(_ context.Context, input *bedrockruntime.ApplyGuardrailInput, _ ...func(*bedrockruntime.Options)) (*bedrockruntime.ApplyGuardrailOutput, error) {
	text := aws.ToString(input.Content[0].(*types.GuardrailContentBlockMemberText).Value.Text)
	bedrock.mu.Lock()
	bedrock.asked = append(bedrock.asked, text)
	bedrock.mu.Unlock()
	if strings.Contains(text, "UNREADABLE") {
		return nil, errors.New("bedrock is down")
	}
	answer := &bedrockruntime.ApplyGuardrailOutput{Action: types.GuardrailActionNone}
	var entities []types.GuardrailPiiEntityFilter
	if strings.Contains(text, "priya@example.com") {
		entities = append(entities, types.GuardrailPiiEntityFilter{
			Type: "EMAIL", Match: aws.String("priya@example.com"), Detected: aws.Bool(true), Action: types.GuardrailSensitiveInformationPolicyActionNone,
		})
	}
	if strings.Contains(text, "hunter2") {
		entities = append(entities, types.GuardrailPiiEntityFilter{
			Type: "PASSWORD", Match: aws.String("hunter2"), Detected: aws.Bool(true), Action: types.GuardrailSensitiveInformationPolicyActionAnonymized,
		})
		answer.Action = types.GuardrailActionGuardrailIntervened
		answer.Outputs = []types.GuardrailOutputContent{{Text: aws.String(strings.ReplaceAll(text, "hunter2", "{PASSWORD}"))}}
	}
	if len(entities) > 0 {
		answer.Assessments = []types.GuardrailAssessment{{SensitiveInformationPolicy: &types.GuardrailSensitiveInformationPolicyAssessment{PiiEntities: entities}}}
	}
	return answer, nil
}

// sentToBedrock is what Bedrock was asked to read, in order.
func (bedrock *bedrockFinding) sentToBedrock() []string {
	bedrock.mu.Lock()
	defer bedrock.mu.Unlock()
	sent := slices.Clone(bedrock.asked)
	slices.Sort(sent)
	return sent
}

// guardedSpace is a teamspace whose whisperers are Ada's primary agent and
// Grace's, with a service that has the answer guard read what agents publish.
func guardedSpace(t *testing.T, options ...Option) (*Service, *fakeRepository) {
	t.Helper()
	repo := &fakeRepository{spaces: []Space{{
		ID: "tsp_1", OrganizationID: "org_1", TeamID: "team_1", Name: "Engineering", Status: StatusActive,
		OrganizationAgent: OrganizationAgent{ID: "oag_1", OrganizationID: "org_1", Name: "Oryxa Agent", Instructions: "Coordinate.", Status: StatusActive},
	}}}
	service, err := New(repo, fakeDirectory{directory: teamDirectory(false, true)}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

func realGuard(t *testing.T, bedrock *bedrockFinding, options ...answerguard.Option) *answerguard.Guard {
	t.Helper()
	guard, err := answerguard.New(bedrock, answerguard.Config{Identifier: "gr_answers", Version: "1"}, options...)
	if err != nil {
		t.Fatal(err)
	}
	return guard
}

// TestAnAgentsPublicationIsReadByTheGuardBeforeTheTeamReadsIt: with a guard,
// what an agent publishes is checked as the ledger would store it, every text
// on its own (the title, the summary, and each list of the artifacts' names,
// URIs and media types and the tools used as one text, a line for each), and
// what is stored is what the guard allows to be shared: nothing found, as it
// was; a password, masked; an email address, replaced by its kind, wherever
// it is, an artifact's URI included. The result says what was found.
func TestAnAgentsPublicationIsReadByTheGuardBeforeTheTeamReadsIt(t *testing.T) {
	for _, testCase := range []struct {
		name          string
		input         primaryagents.SharedEntryInput
		wantGuard     primaryagents.SharedEntryGuard
		wantTitle     string
		wantSummary   string
		wantArtifacts []primaryagents.SharedArtifact
		wantTools     []string
	}{
		{
			name: "nothing found",
			input: primaryagents.SharedEntryInput{
				Kind: "result", Title: " Architecture   review ", Summary: "The design is ready.",
				ToolsUsed: []string{"github.search", "jira.search"},
				Artifacts: []primaryagents.SharedArtifact{
					{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"},
					{Name: "Notes", URI: "/v1/files/notes"},
				},
			},
			wantGuard: primaryagents.SharedEntryGuard{State: "clear"}, wantTitle: "Architecture review", wantSummary: "The design is ready.",
			wantArtifacts: []primaryagents.SharedArtifact{
				{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"},
				{Name: "Notes", URI: "/v1/files/notes"},
			},
			wantTools: []string{"github.search", "jira.search"},
		},
		{
			name:      "a password in the summary",
			input:     primaryagents.SharedEntryInput{Kind: "update", Title: "Staging", Summary: "The staging password is hunter2."},
			wantGuard: primaryagents.SharedEntryGuard{State: "masked", Kinds: []string{"PASSWORD"}}, wantTitle: "Staging",
			wantSummary: "The staging password is {PASSWORD}.",
		},
		{
			name: "an email address in the title, the summary and an artifact's URI",
			input: primaryagents.SharedEntryInput{
				Kind: "handoff", Title: "Ask priya@example.com", Summary: "Priya (priya@example.com) has the numbers.",
				Artifacts: []primaryagents.SharedArtifact{
					{Name: "Contact sheet", URI: "https://example.test/sheet?owner=priya@example.com"},
					{Name: "Plan", URI: "https://example.test/plan"},
				},
			},
			wantGuard: primaryagents.SharedEntryGuard{State: "held", Kinds: []string{"EMAIL"}}, wantTitle: "Ask {EMAIL}",
			wantSummary: "Priya ({EMAIL}) has the numbers.",
			wantArtifacts: []primaryagents.SharedArtifact{
				{Name: "Contact sheet", URI: "https://example.test/sheet?owner={EMAIL}"},
				{Name: "Plan", URI: "https://example.test/plan"},
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			bedrock := &bedrockFinding{}
			service, repo := guardedSpace(t, WithGuard(realGuard(t, bedrock)))
			shared, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", testCase.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(repo.entries) != 1 {
				t.Fatalf("stored %d entries, want one", len(repo.entries))
			}
			stored := repo.entries[0]
			if stored.Title != testCase.wantTitle || stored.Summary != testCase.wantSummary {
				t.Errorf("stored title %q and summary %q, want %q and %q", stored.Title, stored.Summary, testCase.wantTitle, testCase.wantSummary)
			}
			var artifacts []primaryagents.SharedArtifact
			for _, artifact := range stored.Artifacts {
				artifacts = append(artifacts, primaryagents.SharedArtifact{Name: artifact.Name, URI: artifact.URI, MediaType: artifact.MediaType})
			}
			if !slices.Equal(artifacts, testCase.wantArtifacts) || !slices.Equal(stored.ToolsUsed, append([]string{}, testCase.wantTools...)) {
				t.Errorf("stored artifacts %+v and tools %v, want %+v and %v", artifacts, stored.ToolsUsed, testCase.wantArtifacts, testCase.wantTools)
			}
			if shared.Guard == nil || shared.Guard.State != testCase.wantGuard.State || !slices.Equal(shared.Guard.Kinds, testCase.wantGuard.Kinds) {
				t.Errorf("the result says the guard found %+v, want %+v", shared.Guard, testCase.wantGuard)
			}
			if shared.Title != testCase.wantTitle || shared.Summary != testCase.wantSummary || shared.ID != stored.ID {
				t.Errorf("the result is %+v, want the entry as stored", shared)
			}
		})
	}
}

// TestTheGuardIsHandedEachTextAPublicationHolds: title, summary, and the three
// lists of artifacts and one of tools, each as one text with a line for every
// element, in the order they are read; a list with nothing in it is a text
// with nothing in it, which the guard does not send on.
func TestTheGuardIsHandedEachTextAPublicationHolds(t *testing.T) {
	bedrock := &bedrockFinding{}
	service, _ := guardedSpace(t, WithGuard(realGuard(t, bedrock)))
	if _, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{
		Kind: "result", Title: "Review", Summary: "Done.", ToolsUsed: []string{"github.search", "jira.search"},
		Artifacts: []primaryagents.SharedArtifact{
			{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"},
			{Name: "Notes", URI: "/v1/files/notes", MediaType: "text/plain"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	want := []string{"Done.", "Review", "Review\nNotes", "github.search\njira.search", "https://example.test/review\n/v1/files/notes", "text/markdown\ntext/plain"}
	slices.Sort(want)
	if got := bedrock.sentToBedrock(); !slices.Equal(got, want) {
		t.Fatalf("the guard read %q, want %q", got, want)
	}

	bare := &bedrockFinding{}
	service, _ = guardedSpace(t, WithGuard(realGuard(t, bare)))
	if _, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{Kind: "update", Title: "Update", Summary: "Nothing else."}); err != nil {
		t.Fatal(err)
	}
	if got := bare.sentToBedrock(); !slices.Equal(got, []string{"Nothing else.", "Update"}) {
		t.Fatalf("for a publication with no lists the guard read %q, want the title and the summary alone", got)
	}
}

// TestAPublicationThatCannotBeCheckedIsNotStored: whichever text the guard
// could not read, nothing is stored and the agent is told, in words, that it
// was not shared. The teamspace's ledger holds none of it.
func TestAPublicationThatCannotBeCheckedIsNotStored(t *testing.T) {
	for name, input := range map[string]primaryagents.SharedEntryInput{
		"the title":   {Kind: "update", Title: "UNREADABLE", Summary: "Fine."},
		"the summary": {Kind: "update", Title: "Fine", Summary: "UNREADABLE"},
		"an artifact's name": {Kind: "artifact", Title: "Fine", Summary: "Fine.",
			Artifacts: []primaryagents.SharedArtifact{{Name: "UNREADABLE", URI: "https://example.test/a"}}},
		"a tool's name": {Kind: "update", Title: "Fine", Summary: "Fine.", ToolsUsed: []string{"UNREADABLE"}},
	} {
		service, repo := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{})))
		shared, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", input)
		if !errors.Is(err, ErrUnchecked) || !strings.Contains(err.Error(), "couldn't be checked, so it wasn't shared") {
			t.Errorf("%s unreadable: error = %v, want ErrUnchecked saying it was not shared", name, err)
		}
		if len(repo.entries) != 0 || shared.ID != "" {
			t.Errorf("%s unreadable: stored %d entries and returned %+v", name, len(repo.entries), shared)
		}
	}
}

// scriptedGuard is a guard that answers with the verdict it is given, and
// keeps what it was asked.
type scriptedGuard struct {
	verdict   func(texts []string) answerguard.Verdict
	asked     [][]string
	relevance []answerguard.RelevanceInput
}

func (guard *scriptedGuard) Check(_ context.Context, texts []string) answerguard.Verdict {
	guard.asked = append(guard.asked, slices.Clone(texts))
	return guard.verdict(texts)
}

func (guard *scriptedGuard) CheckWith(ctx context.Context, texts []string, input *answerguard.RelevanceInput) answerguard.Verdict {
	guard.relevance = append(guard.relevance, *input)
	return guard.Check(ctx, texts)
}

func TestAgentPublicationInvokesTheRelevanceAwareGuard(t *testing.T) {
	guard := &scriptedGuard{verdict: func(texts []string) answerguard.Verdict {
		return answerguard.Verdict{State: answerguard.StateClear, Texts: texts}
	}}
	service, _ := guardedSpace(t, WithGuard(guard))
	_, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{
		Kind: "result", Title: "Launch", Summary: "Ready.", ToolsUsed: []string{"directory_load"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(guard.relevance) != 1 {
		t.Fatal("agent publication bypassed CheckWith")
	}
}

// TestAVerdictThatDoesNotFitThePublicationSharesNothing: a guard that hands
// back the wrong number of texts, or a list with a line fewer or more than it
// was given, or a state nobody knows, cannot be used to put the publication
// back together, so nothing is stored.
func TestAVerdictThatDoesNotFitThePublicationSharesNothing(t *testing.T) {
	input := primaryagents.SharedEntryInput{
		Kind: "result", Title: "Review", Summary: "Done.", ToolsUsed: []string{"github.search", "jira.search"},
		Artifacts: []primaryagents.SharedArtifact{
			{Name: "Review", URI: "https://example.test/review", MediaType: "text/markdown"},
			{Name: "Notes", URI: "/v1/files/notes", MediaType: "text/plain"},
		},
	}
	clearWith := func(change func(texts []string)) func([]string) answerguard.Verdict {
		return func(texts []string) answerguard.Verdict {
			changed := slices.Clone(texts)
			change(changed)
			return answerguard.Verdict{State: answerguard.StateClear, Texts: changed}
		}
	}
	for name, verdict := range map[string]func([]string) answerguard.Verdict{
		"a text too few": func(texts []string) answerguard.Verdict {
			return answerguard.Verdict{State: answerguard.StateClear, Texts: texts[:len(texts)-1]}
		},
		"a text too many": func(texts []string) answerguard.Verdict {
			return answerguard.Verdict{State: answerguard.StateClear, Texts: append(slices.Clone(texts), "extra")}
		},
		"an artifact name line lost":  clearWith(func(texts []string) { texts[artifactNamesText] = "Review" }),
		"an artifact name line added": clearWith(func(texts []string) { texts[artifactNamesText] = "Review\nNotes\nMore" }),
		"the URIs collapsed to one":   clearWith(func(texts []string) { texts[artifactURIsText] = "{URL}" }),
		"the media types lost a line": clearWith(func(texts []string) { texts[artifactMediaTypesText] = "text/markdown" }),
		"the tools lost a line":       clearWith(func(texts []string) { texts[toolsUsedText] = "github.search" }),
		"a state nobody knows": func(texts []string) answerguard.Verdict {
			return answerguard.Verdict{State: "quarantined", Texts: texts}
		},
		"a held answer with no masked version": func(texts []string) answerguard.Verdict {
			return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{"EMAIL"}, Texts: texts}
		},
		"an answer that could not be checked": func(texts []string) answerguard.Verdict {
			return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: texts}
		},
	} {
		guard := &scriptedGuard{verdict: verdict}
		service, repo := guardedSpace(t, WithGuard(guard))
		_, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", input)
		if !errors.Is(err, ErrUnchecked) || len(repo.entries) != 0 {
			t.Errorf("%s: error = %v, stored %d entries; want ErrUnchecked and nothing stored", name, err, len(repo.entries))
		}
	}

	// A list of a text or two that lost nothing is put back as it was.
	guard := &scriptedGuard{verdict: clearWith(func([]string) {})}
	service, repo := guardedSpace(t, WithGuard(guard))
	if _, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", input); err != nil || len(repo.entries) != 1 {
		t.Fatalf("a verdict that fits: error = %v, stored %d entries", err, len(repo.entries))
	}
	if len(guard.asked) != 1 || len(guard.asked[0]) != publicationTexts {
		t.Fatalf("the guard was asked %d times, %v", len(guard.asked), guard.asked)
	}
}

// TestWhatTheLedgerWouldRefuseIsRefusedBeforeTheGuardIsAsked: a publication
// the ledger would refuse for what it is (no title, an unsafe artifact
// location) is refused as invalid without a check spent on it, and an agent
// with no seat in the teamspace is refused before the guard is asked at all.
func TestWhatTheLedgerWouldRefuseIsRefusedBeforeTheGuardIsAsked(t *testing.T) {
	guard := &scriptedGuard{verdict: func(texts []string) answerguard.Verdict {
		return answerguard.Verdict{State: answerguard.StateClear, Texts: texts}
	}}
	service, repo := guardedSpace(t, WithGuard(guard))
	for name, input := range map[string]primaryagents.SharedEntryInput{
		"no title":           {Kind: "update", Summary: "Something."},
		"no summary":         {Kind: "update", Title: "Something"},
		"an unknown kind":    {Kind: "gossip", Title: "Something", Summary: "Something."},
		"an unsafe artifact": {Kind: "artifact", Title: "Something", Summary: "Something.", Artifacts: []primaryagents.SharedArtifact{{Name: "Page", URI: "javascript:alert(1)"}}},
		"a summary too long": {Kind: "update", Title: "Something", Summary: strings.Repeat("s", maxEntrySummaryRunes+1)},
	} {
		if _, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", input); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: error = %v, want ErrInvalid", name, err)
		}
	}
	stranger := ada
	stranger.AgentID = "agt_stranger"
	if _, err := service.CollaborationPublish(context.Background(), stranger, "token", "tsp_1", primaryagents.SharedEntryInput{Kind: "update", Title: "Something", Summary: "Something."}); !errors.Is(err, ErrForbidden) {
		t.Errorf("an agent with no seat: error = %v, want ErrForbidden", err)
	}
	if len(guard.asked) != 0 || len(repo.entries) != 0 {
		t.Fatalf("the guard was asked %d times and %d entries were stored", len(guard.asked), len(repo.entries))
	}
}

// TestWhatAMemberPublishesIsTheirOwnAndIsNeverRead: PublishEntry, which a
// member's own publication (POST .../entries) and a room message lifted to the
// ledger both go through, never asks the guard, whatever it holds; nor does
// reading the ledger. An agent's publication in the same service is read.
func TestWhatAMemberPublishesIsTheirOwnAndIsNeverRead(t *testing.T) {
	guard := &scriptedGuard{verdict: func(texts []string) answerguard.Verdict {
		return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: texts}
	}}
	service, repo := guardedSpace(t, WithGuard(guard))
	entry, err := service.PublishEntry(context.Background(), actor(), "tsp_1", PublishEntryInput{
		AuthorAgentID: "agt_ada", Kind: EntryDecision, Title: "Ask priya@example.com",
		Summary: "The staging password is hunter2; call +1 415 555 0100.",
	})
	if err != nil {
		t.Fatal(err)
	}
	if entry.Title != "Ask priya@example.com" || entry.Summary != "The staging password is hunter2; call +1 415 555 0100." {
		t.Fatalf("a member's publication was changed: %+v", entry)
	}
	if _, err := service.ListEntries(context.Background(), actor(), "tsp_1", 10); err != nil {
		t.Fatal(err)
	}
	if _, err := service.CollaborationEntries(context.Background(), ada, "token", "tsp_1", 10); err != nil {
		t.Fatal(err)
	}
	if len(guard.asked) != 0 || len(repo.entries) != 1 {
		t.Fatalf("the guard was asked %d times over a member's publication and reads", len(guard.asked))
	}

	// The same service reads an agent's, and, its guard unable to check, stores none.
	if _, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{Kind: "update", Title: "Update", Summary: "Done."}); !errors.Is(err, ErrUnchecked) {
		t.Fatalf("an agent's publication: error = %v, want ErrUnchecked", err)
	}
	if len(guard.asked) != 1 || len(repo.entries) != 1 {
		t.Fatalf("the guard was asked %d times, %d entries stored", len(guard.asked), len(repo.entries))
	}
}

// TestWithoutAGuardAnAgentsPublicationIsStoredAsGiven: no guard, no check, as
// before: the entry is stored as given and the result has no guard finding.
// Nor does a guard passed as nil read anything.
func TestWithoutAGuardAnAgentsPublicationIsStoredAsGiven(t *testing.T) {
	for name, options := range map[string][]Option{"no option": nil, "a nil guard": {WithGuard(nil)}, "a nil logger": {WithLogger(nil)}} {
		service, repo := guardedSpace(t, options...)
		shared, err := service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", primaryagents.SharedEntryInput{
			Kind: "update", Title: "Ask priya@example.com", Summary: "The staging password is hunter2.",
		})
		if err != nil || shared.Guard != nil || len(repo.entries) != 1 || repo.entries[0].Summary != "The staging password is hunter2." {
			t.Errorf("%s: shared=%+v error=%v stored=%+v", name, shared, err, repo.entries)
		}
	}
}

// TestWhatTheGuardFindsInAPublicationIsLoggedByKindAlone: the log names the
// state and the kinds and never a value, nor any text, however the guard's
// findings came out.
func TestWhatTheGuardFindsInAPublicationIsLoggedByKindAlone(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service, _ := guardedSpace(t, WithGuard(realGuard(t, &bedrockFinding{})), WithLogger(logger))
	for _, input := range []primaryagents.SharedEntryInput{
		{Kind: "update", Title: "Ask priya@example.com", Summary: "The staging password is hunter2."},
		{Kind: "update", Title: "Fine", Summary: "UNREADABLE secret-plan"},
	} {
		_, _ = service.CollaborationPublish(context.Background(), ada, "token", "tsp_1", input)
	}
	for _, secret := range []string{"priya@example.com", "hunter2", "secret-plan", "Ask priya", "UNREADABLE"} {
		if strings.Contains(logs.String(), secret) {
			t.Errorf("the log holds %q:\n%s", secret, logs.String())
		}
	}
	for _, said := range []string{`"state":"held"`, `"EMAIL"`, `"teamspace_id":"tsp_1"`, `"published":true`, `"published":false`, `"UNCHECKED"`} {
		if !strings.Contains(logs.String(), said) {
			t.Errorf("the log does not say %s:\n%s", said, logs.String())
		}
	}
}
