package domain

import (
	"errors"
	"strings"
	"testing"
)

func titanSpec() EmbeddingSpec {
	return EmbeddingSpec{Provider: ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 1024, Metric: MetricCosine}
}

func TestResolveSpec(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		provider   Provider
		model      string
		dimensions int
		metric     Metric
		want       EmbeddingSpec
		wantErr    bool
	}{{
		name:     "a known model takes its default width",
		provider: ProviderBedrock,
		model:    "amazon.titan-embed-text-v2:0",
		want:     titanSpec(),
	}, {
		name:       "a known model accepts a width it can emit",
		provider:   ProviderBedrock,
		model:      "amazon.titan-embed-text-v2:0",
		dimensions: 256,
		want:       EmbeddingSpec{ProviderBedrock, "amazon.titan-embed-text-v2:0", 256, MetricCosine},
	}, {
		name:       "a known model refuses a width it cannot emit",
		provider:   ProviderBedrock,
		model:      "amazon.titan-embed-text-v2:0",
		dimensions: 999,
		wantErr:    true,
	}, {
		name:     "vertex defaults",
		provider: ProviderVertex,
		model:    "gemini-embedding-001",
		want:     EmbeddingSpec{ProviderVertex, "gemini-embedding-001", 3072, MetricCosine},
	}, {
		name:       "an unknown model is allowed when its width is stated",
		provider:   ProviderBedrock,
		model:      "some.model-released-tomorrow",
		dimensions: 1536,
		want:       EmbeddingSpec{ProviderBedrock, "some.model-released-tomorrow", 1536, MetricCosine},
	}, {
		name:     "an unknown model with no width is refused",
		provider: ProviderBedrock,
		model:    "some.model-released-tomorrow",
		wantErr:  true,
	}, {
		name:     "an unknown provider is refused",
		provider: Provider("azure"),
		model:    "whatever",
		wantErr:  true,
	}, {
		name:     "an empty model is refused",
		provider: ProviderBedrock,
		wantErr:  true,
	}, {
		name:     "an unknown metric is refused",
		provider: ProviderBedrock,
		model:    "amazon.titan-embed-text-v2:0",
		metric:   Metric("manhattan"),
		wantErr:  true,
	}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := ResolveSpec(testCase.provider, testCase.model, testCase.dimensions, testCase.metric)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("ResolveSpec = %v, want an error", got)
				}
				if !errors.Is(err, ErrInvalid) {
					t.Fatalf("error = %v, want ErrInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSpec: %v", err)
			}
			if !got.Equal(testCase.want) {
				t.Fatalf("ResolveSpec = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestChunkPolicyNormalize(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		policy  ChunkPolicy
		spec    EmbeddingSpec
		wantErr string
	}{{
		name:   "empty takes the defaults",
		policy: ChunkPolicy{},
		spec:   titanSpec(),
	}, {
		name:   "overlap at half the budget is refused",
		policy: ChunkPolicy{MaxTokens: 256, OverlapTokens: 128},
		spec:   titanSpec(), wantErr: "half",
	}, {
		name:   "negative overlap is refused",
		policy: ChunkPolicy{MaxTokens: 256, OverlapTokens: -1},
		spec:   titanSpec(), wantErr: "negative",
	}, {
		name:   "a budget over the model's input limit is refused",
		policy: ChunkPolicy{MaxTokens: 1024},
		// Cohere on Bedrock reads 512 tokens, so a 1024-token chunk would be
		// silently truncated by the provider.
		spec:    EmbeddingSpec{ProviderBedrock, "cohere.embed-english-v3", 1024, MetricCosine},
		wantErr: "reads",
	}, {
		name:    "a budget under the floor is refused",
		policy:  ChunkPolicy{MaxTokens: 8},
		spec:    titanSpec(),
		wantErr: "between",
	}, {
		name:    "an unknown strategy is refused",
		policy:  ChunkPolicy{Strategy: Strategy("semantic"), MaxTokens: 256},
		spec:    titanSpec(),
		wantErr: "strategy",
	}}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			got, err := testCase.policy.Normalize(testCase.spec)
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("Normalize = %+v, want an error about %q", got, testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize: %v", err)
			}
			if got.MaxTokens < MinChunkTokens || got.OverlapTokens*2 >= got.MaxTokens {
				t.Fatalf("Normalize returned an unusable policy: %+v", got)
			}
		})
	}
}

func TestDefaultChunkPolicyFitsTheModel(t *testing.T) {
	t.Parallel()
	// Cohere reads far less than the default budget, so the default must come
	// down to meet it rather than produce chunks the model truncates.
	spec := EmbeddingSpec{ProviderBedrock, "cohere.embed-english-v3", 1024, MetricCosine}
	policy := DefaultChunkPolicy(spec)
	profile, _ := Profile(spec.Provider, spec.Model)
	if policy.MaxTokens > profile.MaxInputTokens {
		t.Fatalf("default budget %d is over %s's %d", policy.MaxTokens, spec.Model, profile.MaxInputTokens)
	}
	if _, err := policy.Normalize(spec); err != nil {
		t.Fatalf("the default policy does not validate: %v", err)
	}
}

func TestEveryCataloguedModelHasAWorkableDefault(t *testing.T) {
	t.Parallel()
	for _, profile := range Catalog() {
		spec, err := ResolveSpec(profile.Provider, profile.Model, 0, MetricCosine)
		if err != nil {
			t.Fatalf("ResolveSpec(%s): %v", profile.Model, err)
		}
		if _, err := DefaultChunkPolicy(spec).Normalize(spec); err != nil {
			t.Fatalf("%s: default policy does not validate: %v", profile.Model, err)
		}
		if profile.MaxBatch < 1 {
			t.Fatalf("%s: MaxBatch is %d", profile.Model, profile.MaxBatch)
		}
		if profile.MaxInputTokens < MinChunkTokens {
			t.Fatalf("%s: MaxInputTokens is %d", profile.Model, profile.MaxInputTokens)
		}
	}
}

func TestMetadataValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		metadata Metadata
		wantErr  string
	}{
		{name: "empty"},
		{name: "ordinary", metadata: Metadata{"team": "platform", "year_2024": "yes"}},
		{name: "uppercase key", metadata: Metadata{"Team": "platform"}, wantErr: "lowercase"},
		{name: "key with a dash", metadata: Metadata{"my-key": "x"}, wantErr: "lowercase"},
		{name: "reserved key", metadata: Metadata{"document_id": "doc_1"}, wantErr: "reserved"},
		{name: "oversized value", metadata: Metadata{"note": strings.Repeat("x", MaxMetadataValue+1)}, wantErr: "at most"},
		{name: "invalid utf8", metadata: Metadata{"note": string([]byte{0xff, 0xfe})}, wantErr: "UTF-8"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.metadata.Validate()
			if testCase.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}
}

// TestCallerMetadataCannotClaimKnowledgeProvenance: the keys rag's Knowledge
// bridge writes say where a document came from, and origin says a corpus is
// the bridge's own. A caller may set none of them, or a note of their own
// could cite as a Gmail financial record. The bridge checks what it writes
// with Validate, so they must still pass there.
func TestCallerMetadataCannotClaimKnowledgeProvenance(t *testing.T) {
	t.Parallel()
	for _, key := range []string{"provider", "knowledge_source_id", "knowledge_document_id", "document_kind", "category"} {
		metadata := Metadata{key: "forged"}
		if err := metadata.Validate(); err != nil {
			t.Fatalf("Validate refused %q, which rag's Knowledge bridge writes: %v", key, err)
		}
		if err := metadata.ValidateCallerDocument(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("a caller's document metadata carried %q: %v", key, err)
		}
	}
	origin := Metadata{"origin": "knowledge"}
	if err := origin.Validate(); err != nil {
		t.Fatalf("Validate refused origin, which rag's Knowledge bridge writes: %v", err)
	}
	if err := origin.ValidateCallerCorpus(); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a caller's corpus metadata carried origin: %v", err)
	}

	// What a caller could always set still passes, and every other rule
	// still holds for them.
	if err := (Metadata{"team": "platform", "origin": "import"}).ValidateCallerDocument(); err != nil {
		t.Fatalf("ordinary document metadata refused: %v", err)
	}
	if err := (Metadata{"team": "platform"}).ValidateCallerCorpus(); err != nil {
		t.Fatalf("ordinary corpus metadata refused: %v", err)
	}
	for _, metadata := range []Metadata{{"document_id": "doc_1"}, {"Team": "platform"}, {"note": strings.Repeat("x", MaxMetadataValue+1)}} {
		if err := metadata.ValidateCallerDocument(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ValidateCallerDocument(%v) = %v, want ErrInvalid", metadata, err)
		}
		if err := metadata.ValidateCallerCorpus(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ValidateCallerCorpus(%v) = %v, want ErrInvalid", metadata, err)
		}
	}
}

func TestMetadataTooManyKeys(t *testing.T) {
	t.Parallel()
	metadata := Metadata{}
	for index := range MaxMetadataKeys + 1 {
		metadata[string(rune('a'+index%26))+"_"+string(rune('a'+index/26))] = "x"
	}
	if err := metadata.Validate(); err == nil {
		t.Fatal("Validate accepted more keys than the backends will hold")
	}
}

func TestMetadataMergeDoesNotMutate(t *testing.T) {
	t.Parallel()
	base := Metadata{"a": "1", "b": "2"}
	overlay := Metadata{"b": "overridden", "c": "3"}
	merged := base.Merge(overlay)
	if base["b"] != "2" || len(base) != 2 {
		t.Fatalf("Merge changed the receiver: %v", base)
	}
	if len(overlay) != 2 {
		t.Fatalf("Merge changed the argument: %v", overlay)
	}
	if merged["b"] != "overridden" || merged["a"] != "1" || merged["c"] != "3" {
		t.Fatalf("Merge = %v", merged)
	}
}

func TestVectorValidateCatchesAMismatchedWidth(t *testing.T) {
	t.Parallel()
	spec := titanSpec()
	if err := (Vector{ChunkID: "chk_1", DocumentID: "doc_1", Values: make([]float32, 1024)}).Validate(spec); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	err := (Vector{ChunkID: "chk_1", DocumentID: "doc_1", Values: make([]float32, 512)}).Validate(spec)
	if err == nil {
		t.Fatal("a 512-wide vector was accepted into a 1024-wide corpus")
	}
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("error = %v, want ErrConflict", err)
	}
	// A store filters and deletes by both, so neither may be left out.
	if err := (Vector{DocumentID: "doc_1", Values: make([]float32, 1024)}).Validate(spec); err == nil {
		t.Fatal("a vector with no chunk was accepted")
	}
	if err := (Vector{ChunkID: "chk_1", Values: make([]float32, 1024)}).Validate(spec); err == nil {
		t.Fatal("a vector with no document was accepted")
	}
}

func TestQueryNormalize(t *testing.T) {
	t.Parallel()
	got, err := Query{Text: "  how do credentials rotate?  "}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if got.Text != "how do credentials rotate?" {
		t.Fatalf("text = %q", got.Text)
	}
	if got.Mode != ModeHybrid || got.TopK != DefaultTopK || got.OverFetch != DefaultOverFetch {
		t.Fatalf("defaults not applied: %+v", got)
	}
	if got.Diversity == nil || *got.Diversity != 1 {
		t.Fatalf("diversity = %v, want 1 (pure relevance)", got.Diversity)
	}
	if got.Candidates() != DefaultTopK*DefaultOverFetch {
		t.Fatalf("Candidates = %d", got.Candidates())
	}

	for _, bad := range []Query{
		{Text: ""},
		{Text: "   "},
		{Text: "x", TopK: MaxTopK + 1},
		{Text: "x", TopK: -1},
		{Text: "x", Mode: SearchMode("magic")},
		{Text: "x", Diversity: pointerTo(1.5)},
		{Text: "x", MinScore: 2},
		{Text: "x", OverFetch: 99},
		{Text: strings.Repeat("x", MaxQueryCharacters+1)},
	} {
		if _, err := bad.Normalize(); err == nil {
			t.Fatalf("Normalize(%+v) accepted an unusable query", bad)
		}
	}
}

func TestQueryCandidatesAreCapped(t *testing.T) {
	t.Parallel()
	query, err := Query{Text: "x", TopK: MaxTopK, OverFetch: 20}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if query.Candidates() != MaxCandidates {
		t.Fatalf("Candidates = %d, want the cap of %d", query.Candidates(), MaxCandidates)
	}
}

func TestConditionValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		condition Condition
		wantErr   bool
	}{
		{name: "equal", condition: Condition{Key: "team", Operator: OperatorEqual, Values: []string{"platform"}}},
		{name: "in", condition: Condition{Key: "team", Operator: OperatorIn, Values: []string{"a", "b"}}},
		{name: "equal with two values", condition: Condition{Key: "team", Operator: OperatorEqual, Values: []string{"a", "b"}}, wantErr: true},
		{name: "no values", condition: Condition{Key: "team", Operator: OperatorIn}, wantErr: true},
		{name: "bad key", condition: Condition{Key: "Team", Operator: OperatorEqual, Values: []string{"a"}}, wantErr: true},
		{name: "bad operator", condition: Condition{Key: "team", Operator: Operator("like"), Values: []string{"a"}}, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := testCase.condition.Validate()
			if testCase.wantErr != (err != nil) {
				t.Fatalf("Validate = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestFilterRefusesSomethingThatIsNotADocumentID(t *testing.T) {
	t.Parallel()
	err := Filter{DocumentIDs: []string{"cor_abc"}}.Validate()
	if err == nil {
		t.Fatal("a corpus id was accepted as a document id")
	}
}

func TestPlace(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		scope      Scope
		teamID     string
		createdBy  string
		wantTeam   string
		wantMember string
		wantErr    bool
	}{
		{name: "member", scope: ScopeMember, createdBy: "usr_1", wantMember: "usr_1"},
		{name: "member naming a team", scope: ScopeMember, teamID: "tm_1", createdBy: "usr_1", wantErr: true},
		{name: "member with no owner", scope: ScopeMember, wantErr: true},
		{name: "team", scope: ScopeTeam, teamID: "tm_1", createdBy: "usr_1", wantTeam: "tm_1"},
		{name: "team with no team", scope: ScopeTeam, createdBy: "usr_1", wantErr: true},
		{name: "organization", scope: ScopeOrganization, createdBy: "usr_1"},
		{name: "organization naming a team", scope: ScopeOrganization, teamID: "tm_1", wantErr: true},
		{name: "unknown scope", scope: Scope("global"), wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			team, member, err := Place(testCase.scope, testCase.teamID, testCase.createdBy)
			if testCase.wantErr != (err != nil) {
				t.Fatalf("Place = (%q, %q, %v), wantErr %v", team, member, err, testCase.wantErr)
			}
			if err == nil && (team != testCase.wantTeam || member != testCase.wantMember) {
				t.Fatalf("Place = (%q, %q), want (%q, %q)", team, member, testCase.wantTeam, testCase.wantMember)
			}
		})
	}
}

func TestSourceValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		source  Source
		wantErr bool
	}{
		{name: "inline", source: Source{Kind: SourceInline}},
		{name: "inline with a ref", source: Source{Kind: SourceInline, Ref: "x"}, wantErr: true},
		{name: "file", source: Source{Kind: SourceFile, Ref: "file_abc"}},
		{name: "file with a non-file ref", source: Source{Kind: SourceFile, Ref: "doc_abc"}, wantErr: true},
		{name: "external", source: Source{Kind: SourceExternal, Ref: "https://example.test/a"}},
		{name: "external with no ref", source: Source{Kind: SourceExternal}, wantErr: true},
		{name: "unknown kind", source: Source{Kind: SourceKind("ftp")}, wantErr: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if err := testCase.source.Validate(); testCase.wantErr != (err != nil) {
				t.Fatalf("Validate = %v, wantErr %v", err, testCase.wantErr)
			}
		})
	}
}

func TestContentTypeRefusesWhatCannotBeChunked(t *testing.T) {
	t.Parallel()
	if got, err := ContentType(""); err != nil || got != "text/plain" {
		t.Fatalf("ContentType(\"\") = (%q, %v)", got, err)
	}
	if got, err := ContentType("TEXT/Markdown; charset=utf-8"); err != nil || got != "text/markdown" {
		t.Fatalf("ContentType = (%q, %v)", got, err)
	}
	for _, unsupported := range []string{"application/pdf", "image/png", "application/zip"} {
		if _, err := ContentType(unsupported); err == nil {
			t.Fatalf("ContentType(%q) was accepted; rag indexes extracted text only", unsupported)
		}
	}
}

func TestTextRefusesInvalidUTF8(t *testing.T) {
	t.Parallel()
	if _, err := Text(string([]byte{0xff, 0xfe, 0xfd})); err == nil {
		t.Fatal("invalid UTF-8 was accepted")
	}
	if _, err := Text("   "); err == nil {
		t.Fatal("blank text was accepted")
	}
	if got, err := Text("hello"); err != nil || got != "hello" {
		t.Fatalf("Text = (%q, %v)", got, err)
	}
}

func TestHashIsStableAndDistinct(t *testing.T) {
	t.Parallel()
	if Hash("a") != Hash("a") {
		t.Fatal("Hash is not stable")
	}
	if Hash("a") == Hash("b") {
		t.Fatal("Hash collided on different text")
	}
}

func TestChunkEmbedTextLeadsWithHeadings(t *testing.T) {
	t.Parallel()
	chunk := Chunk{Text: "It rotates every ninety days.", HeadingPath: []string{"Security", "Credentials"}}
	if got := chunk.EmbedText(); got != "Security > Credentials\n\nIt rotates every ninety days." {
		t.Fatalf("EmbedText = %q", got)
	}
	bare := Chunk{Text: "No headings here."}
	if got := bare.EmbedText(); got != "No headings here." {
		t.Fatalf("EmbedText = %q", got)
	}
}

func TestChunkProvenanceCannotBeForged(t *testing.T) {
	t.Parallel()
	// A caller cannot set these keys, so what Provenance writes is the only
	// source of them and a citation cannot be made to point somewhere else.
	for _, key := range reservedMetadataKeys {
		if err := (Metadata{key: "forged"}).Validate(); err == nil {
			t.Fatalf("%q was accepted as caller metadata", key)
		}
	}
	document := Document{Source: Source{Kind: SourceFile, Ref: "file_abc"}, Title: "Runbook"}
	chunk := Chunk{ID: "chk_1", CorpusID: "cor_1", DocumentID: "doc_1", OrganizationID: "org_1", HeadingPath: []string{"A", "B"}}
	provenance := chunk.Provenance(document)
	for key, want := range map[string]string{
		"chunk_id": "chk_1", "corpus_id": "cor_1", "document_id": "doc_1",
		"organization_id": "org_1", "source_kind": "file", "source_ref": "file_abc",
		"title": "Runbook", "heading_path": "A > B",
	} {
		if provenance[key] != want {
			t.Fatalf("provenance[%q] = %q, want %q", key, provenance[key], want)
		}
	}
}

// TestOnlyAPersonsOwnSessionActsAsAdmin: an admin's own session gets an org
// admin's reach, admin-only documents included; an agent acting for that
// admin does not, since what it reads can reach members who may not.
// Documents every member may read stay readable to everyone, and a label
// this service does not know is read by nobody.
func TestOnlyAPersonsOwnSessionActsAsAdmin(t *testing.T) {
	t.Parallel()
	callers := map[string]struct {
		membership  OrgMembership
		actsAsAdmin bool
	}{
		"owner":                 {OrgMembership{Role: OrgRoleOwner}, true},
		"admin":                 {OrgMembership{Role: OrgRoleAdmin}, true},
		"member":                {OrgMembership{Role: OrgRoleMember}, false},
		"agent for an owner":    {OrgMembership{Role: OrgRoleOwner, Agent: true}, false},
		"agent for an admin":    {OrgMembership{Role: OrgRoleAdmin, Agent: true}, false},
		"agent for a member":    {OrgMembership{Role: OrgRoleMember, Agent: true}, false},
		"unrecognised role":     {OrgMembership{Role: OrgRole("pending")}, false},
		"agent with no role":    {OrgMembership{Agent: true}, false},
		"nothing known of them": {OrgMembership{}, false},
	}
	for name, caller := range callers {
		if got := caller.membership.ActsAsAdmin(); got != caller.actsAsAdmin {
			t.Errorf("%s: ActsAsAdmin() = %v, want %v", name, got, caller.actsAsAdmin)
		}
		for access, want := range map[DocumentAccess]bool{
			"": true, AccessMembers: true, AccessAdmins: caller.actsAsAdmin, "owners": false,
		} {
			if got := (Document{Access: access}).VisibleTo(caller.membership); got != want {
				t.Errorf("%s: VisibleTo(access %q) = %v, want %v", name, access, got, want)
			}
		}
	}
}

func TestSimilarityIsMonotonic(t *testing.T) {
	t.Parallel()
	for _, metric := range []Metric{MetricCosine, MetricL2, MetricInnerProduct} {
		near := metric.Similarity(0.1)
		far := metric.Similarity(0.9)
		if near <= far {
			t.Fatalf("%s: a nearer distance scored %v, not above %v", metric, near, far)
		}
	}
	if got := MetricCosine.Similarity(2); got != 0 {
		t.Fatalf("an opposite cosine vector scored %v, want 0", got)
	}
}

func TestNewIDCarriesItsPrefixAndIsUnique(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for range 256 {
		id, err := NewID(ChunkIDPrefix)
		if err != nil {
			t.Fatalf("NewID: %v", err)
		}
		if !strings.HasPrefix(id, ChunkIDPrefix) {
			t.Fatalf("NewID = %q, want the %q prefix", id, ChunkIDPrefix)
		}
		if seen[id] {
			t.Fatalf("NewID repeated %q", id)
		}
		seen[id] = true
	}
}

func pointerTo[T any](value T) *T { return &value }

// TestDiversityZeroIsExpressible is the difference between a knob and a
// footgun: 0 is documented as maximum spread, and if it were indistinguishable
// from not asking it would silently become 1, the exact opposite.
func TestDiversityZeroIsExpressible(t *testing.T) {
	t.Parallel()
	spread, err := Query{Text: "q", Diversity: pointerTo(0.0)}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spread.Diversity == nil || *spread.Diversity != 0 {
		t.Fatalf("diversity = %v, want the 0 that was asked for", spread.Diversity)
	}
	unset, err := Query{Text: "q"}.Normalize()
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if *unset.Diversity != 1 {
		t.Fatalf("an unset diversity = %v, want 1", *unset.Diversity)
	}
	if _, err := (Query{Text: "q", Diversity: pointerTo(-0.1)}).Normalize(); err == nil {
		t.Fatal("a negative diversity was accepted")
	}
}

func TestBackendValid(t *testing.T) {
	t.Parallel()
	for _, backend := range []Backend{BackendPgvector, BackendS3Vectors, BackendBedrockKB} {
		if !backend.Valid() {
			t.Errorf("%q is not valid", backend)
		}
	}
	if Backend("firestore").Valid() {
		t.Fatal("an unknown backend is valid")
	}
}

func TestBackendSupportsMetric(t *testing.T) {
	t.Parallel()
	if err := BackendS3Vectors.Supports(MetricInnerProduct); err == nil {
		t.Fatal("S3 Vectors accepted inner product")
	}
	if err := BackendS3Vectors.Supports(MetricCosine); err != nil {
		t.Fatalf("S3 Vectors refused cosine: %v", err)
	}
	// A Bedrock knowledge base embeds with exactly one metric — whatever it
	// was created with — so anything else is refused before a corpus can
	// even be pinned to it, the same way S3 Vectors refuses inner product.
	if err := BackendBedrockKB.Supports(MetricInnerProduct); err == nil {
		t.Fatal("bedrock-kb accepted inner product")
	}
	if err := BackendBedrockKB.Supports(MetricL2); err == nil {
		t.Fatal("bedrock-kb accepted l2")
	}
	if err := BackendBedrockKB.Supports(MetricCosine); err != nil {
		t.Fatalf("bedrock-kb refused cosine: %v", err)
	}
	if err := BackendPgvector.Supports(MetricInnerProduct); err != nil {
		t.Fatalf("pgvector refused inner product: %v", err)
	}
}

// TestBackendRequiredEmbeddingSpec: only bedrock-kb fixes a spec of its own;
// a corpus on pgvector or S3 Vectors is free to pin whatever its caller
// resolved.
func TestBackendRequiredEmbeddingSpec(t *testing.T) {
	t.Parallel()
	required, ok := BackendBedrockKB.RequiredEmbeddingSpec()
	if !ok {
		t.Fatal("bedrock-kb has no required embedding spec")
	}
	if required != BedrockKnowledgeBaseEmbeddingSpec {
		t.Fatalf("required spec = %+v, want %+v", required, BedrockKnowledgeBaseEmbeddingSpec)
	}
	if required.Provider != ProviderBedrock || required.Model != "amazon.titan-embed-text-v2:0" ||
		required.Dimensions != 1024 || required.Metric != MetricCosine {
		t.Fatalf("the knowledge base's spec changed unexpectedly: %+v", required)
	}
	for _, backend := range []Backend{BackendPgvector, BackendS3Vectors} {
		if _, ok := backend.RequiredEmbeddingSpec(); ok {
			t.Fatalf("%q reported a required embedding spec, but a corpus there may choose its own", backend)
		}
	}
}
