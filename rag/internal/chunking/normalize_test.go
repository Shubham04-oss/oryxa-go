package chunking

import (
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/rag/internal/domain"
)

func TestNormalizeTidiesLineEndings(t *testing.T) {
	t.Parallel()
	got := Normalize("one\r\ntwo\rthree", "text/plain")
	if strings.ContainsRune(got, '\r') {
		t.Fatalf("carriage returns survived: %q", got)
	}
	if got != "one\ntwo\nthree" {
		t.Fatalf("Normalize = %q", got)
	}
}

func TestNormalizeHTMLKeepsHeadingsAndDropsScripts(t *testing.T) {
	t.Parallel()
	source := `<html><head><style>.a{color:red}</style></head><body>
		<h1>Rotation policy</h1>
		<p>Credentials rotate every <b>ninety</b> days.</p>
		<script>alert("indexed nonsense")</script>
		<ul><li>First item</li><li>Second item</li></ul>
	</body></html>`
	got := Normalize(source, "text/html")

	if !strings.Contains(got, "# Rotation policy") {
		t.Fatalf("the heading did not survive: %q", got)
	}
	if !strings.Contains(got, "Credentials rotate every ninety days.") {
		t.Fatalf("inline markup broke the sentence: %q", got)
	}
	for _, unwanted := range []string{"alert(", "color:red", "<p>", "</li>"} {
		if strings.Contains(got, unwanted) {
			t.Fatalf("%q survived normalization: %q", unwanted, got)
		}
	}
	if !strings.Contains(got, "- First item") || !strings.Contains(got, "- Second item") {
		t.Fatalf("list items were lost: %q", got)
	}
}

func TestNormalizeHTMLUnescapesEntities(t *testing.T) {
	t.Parallel()
	got := Normalize("<p>Tom &amp; Jerry &lt;tags&gt; &quot;quoted&quot;</p>", "text/html")
	if !strings.Contains(got, `Tom & Jerry <tags> "quoted"`) {
		t.Fatalf("entities were not unescaped: %q", got)
	}
}

func TestNormalizeHTMLFeedsTheChunker(t *testing.T) {
	t.Parallel()
	source := "<h1>Top</h1><h2>Nested</h2><p>" + strings.Repeat("A sentence about storage. ", 40) + "</p>"
	normalized := Normalize(source, "text/html")
	chunkPolicy := policy(64, 8)
	pieces := Cut(normalized, "text/html", chunkPolicy)
	if len(pieces) < 2 {
		t.Fatalf("expected several pieces, got %d", len(pieces))
	}
	// The whole point of normalizing first: offsets address what is stored.
	assertInvariants(t, normalized, pieces, chunkPolicy)
	found := false
	for _, piece := range pieces {
		if len(piece.HeadingPath) == 2 && piece.HeadingPath[0] == "Top" && piece.HeadingPath[1] == "Nested" {
			found = true
		}
	}
	if !found {
		t.Fatal("HTML headings did not become a heading trail")
	}
}

func TestNormalizeCSVLabelsEveryValue(t *testing.T) {
	t.Parallel()
	source := "org,plan,seats\nacme,enterprise,250\nbeta,starter,5\n"
	got := Normalize(source, "text/csv")
	for _, want := range []string{"org: acme", "plan: enterprise", "seats: 250", "org: beta", "seats: 5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
	// Rows must stay separable, so the packer can fit whole rows in a chunk.
	if !strings.Contains(got, "seats: 250\n\norg: beta") {
		t.Fatalf("rows were not separated by a blank line: %q", got)
	}
}

func TestNormalizeCSVSkipsEmptyValuesAndKeepsGoingWithRaggedRows(t *testing.T) {
	t.Parallel()
	source := "a,b,c\n1,,3\n4,5\n"
	got := Normalize(source, "text/csv")
	first, _, _ := strings.Cut(got, "\n\n")
	if strings.Contains(first, "b:") {
		t.Fatalf("the empty value in row one was labelled: %q", first)
	}
	for _, want := range []string{"a: 1", "c: 3", "a: 4", "b: 5"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
}

func TestNormalizeCSVLeavesUnparseableInputAlone(t *testing.T) {
	t.Parallel()
	if got := Normalize("", "text/csv"); got != "" {
		t.Fatalf("Normalize(%q) = %q", "", got)
	}
}

func TestNormalizeJSONFlattensRecords(t *testing.T) {
	t.Parallel()
	source := `[{"name":"acme","plan":{"tier":"enterprise","seats":250}},{"name":"beta","plan":{"tier":"starter","seats":5}}]`
	got := Normalize(source, "application/json")
	for _, want := range []string{"name: acme", "plan.tier: enterprise", "plan.seats: 250", "name: beta"} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q missing from %q", want, got)
		}
	}
	if strings.Contains(got, "{") || strings.Contains(got, `"`) {
		t.Fatalf("JSON punctuation survived: %q", got)
	}
}

func TestNormalizeJSONIsStable(t *testing.T) {
	t.Parallel()
	// Map iteration is random; the same document must still chunk the same
	// way every time or every re-ingest looks like a change.
	source := `{"zulu":1,"alpha":2,"mike":3,"bravo":4,"yankee":5,"delta":6}`
	first := Normalize(source, "application/json")
	for attempt := 0; attempt < 20; attempt++ {
		if again := Normalize(source, "application/json"); again != first {
			t.Fatalf("flattening changed between runs:\n%q\n%q", first, again)
		}
	}
	if !strings.HasPrefix(first, "alpha: 2") {
		t.Fatalf("keys were not sorted: %q", first)
	}
}

func TestNormalizeJSONLeavesInvalidInputAlone(t *testing.T) {
	t.Parallel()
	source := "{not json at all"
	if got := Normalize(source, "application/json"); got != source {
		t.Fatalf("Normalize = %q, want the input unchanged", got)
	}
}

func TestNormalizeJSONDropsNulls(t *testing.T) {
	t.Parallel()
	got := Normalize(`{"kept":"yes","dropped":null}`, "application/json")
	if strings.Contains(got, "dropped") {
		t.Fatalf("a null was given a token: %q", got)
	}
	if !strings.Contains(got, "kept: yes") {
		t.Fatalf("Normalize = %q", got)
	}
}

// TestNormalizeThenCutHoldsInvariants is the pairing that matters: whatever
// the content type, a piece is still an exact range of the stored text.
func TestNormalizeThenCutHoldsInvariants(t *testing.T) {
	t.Parallel()
	sources := map[string]string{
		"text/html":        "<h1>T</h1>" + strings.Repeat("<p>A paragraph of ordinary prose.</p>", 40),
		"text/csv":         "org,note\n" + strings.Repeat("acme,a note about something\n", 200),
		"application/json": `[` + strings.TrimSuffix(strings.Repeat(`{"a":"one","b":"two"},`, 200), ",") + `]`,
		"text/markdown":    manual,
		"text/plain":       strings.Repeat("Plain sentences, one after another. ", 100),
	}
	chunkPolicy, err := domain.ChunkPolicy{MaxTokens: 96, OverlapTokens: 12}.Normalize(
		domain.EmbeddingSpec{Provider: domain.ProviderBedrock, Model: "amazon.titan-embed-text-v2:0", Dimensions: 1024, Metric: domain.MetricCosine})
	if err != nil {
		t.Fatalf("Normalize policy: %v", err)
	}
	for contentType, source := range sources {
		t.Run(contentType, func(t *testing.T) {
			t.Parallel()
			normalized := Normalize(source, contentType)
			pieces := Cut(normalized, contentType, chunkPolicy)
			if len(pieces) == 0 {
				t.Fatalf("no pieces from %s", contentType)
			}
			assertInvariants(t, normalized, pieces, chunkPolicy)
		})
	}
}
