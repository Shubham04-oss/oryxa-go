package sources

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"maps"
	"math/rand/v2"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"

	"github.com/Shubham04-oss/oryxa-go/internal/composio"

	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/judge"
	"github.com/Shubham04-oss/oryxa-go/knowledge/internal/transform"
)

// judgeEvalResult is one document as production judged it and as Jev does. It
// carries no content: a title, a few details, and the verdicts.
type judgeEvalResult struct {
	Provider string `json:"provider"`
	Title    string `json:"title"`
	From     string `json:"from,omitempty"`
	Labels   string `json:"labels,omitempty"`
	Bulk     bool   `json:"bulk,omitempty"`
	MIME     string `json:"mime"`
	Size     int    `json:"size"`
	// Original marks a PDF or Office file, which Jev judges by name and
	// details alone.
	Original bool `json:"original"`
	// Refetched marks a document production excluded, whose text is read
	// again from its app because exclusion keeps none.
	Refetched bool `json:"refetched"`

	ProductionKeep   bool   `json:"production_keep"`
	ProductionReason string `json:"production_reason,omitempty"`

	JevKind    string  `json:"jev_kind,omitempty"`
	JevKeep    float64 `json:"jev_keep"`
	JevExclude float64 `json:"jev_exclude"`
	// JevDecision is what judge.Decide makes of Jev's answer, as production
	// would, before any escalation.
	JevDecision  bool    `json:"jev_decision"`
	JevCertainty float64 `json:"jev_certainty"`
	JevReason    string  `json:"jev_reason,omitempty"`
	JevTokens    int     `json:"jev_tokens"`
	JevLatencyMS int64   `json:"jev_latency_ms"`
	JevError     string  `json:"jev_error,omitempty"`

	GeminiSampled   bool   `json:"gemini_sampled,omitempty"`
	GeminiKeep      bool   `json:"gemini_keep,omitempty"`
	GeminiReason    string `json:"gemini_reason,omitempty"`
	GeminiLatencyMS int64  `json:"gemini_latency_ms,omitempty"`
}

// TestEvalJevAgainstProductionVerdicts asks Jev about every document
// production judged, from an export of knowledge_documents
// (KNOWLEDGE_JUDGE_EVAL_DOCS, a CSV) and the Gmail ids of what production
// excluded (KNOWLEDGE_JUDGE_EVAL_GMAIL_IDS), whose text is read again through
// Composio (COMPOSIO_API_KEY, KNOWLEDGE_JUDGE_EVAL_GMAIL). It writes one line
// per document to KNOWLEDGE_JUDGE_EVAL_OUT. With GOOGLE_CLOUD_PROJECT and
// KNOWLEDGE_JUDGE_LIVE_MODEL it also times the Gemini judge on
// KNOWLEDGE_JUDGE_EVAL_GEMINI_SAMPLE documents. An operator's tool for
// comparing judges on real mail and files; skipped unless all of it is named.
func TestEvalJevAgainstProductionVerdicts(t *testing.T) {
	docsPath, idsPath, out := os.Getenv("KNOWLEDGE_JUDGE_EVAL_DOCS"), os.Getenv("KNOWLEDGE_JUDGE_EVAL_GMAIL_IDS"), os.Getenv("KNOWLEDGE_JUDGE_EVAL_OUT")
	composioKey, gmailAccount, typesafeKey := os.Getenv("COMPOSIO_API_KEY"), os.Getenv("KNOWLEDGE_JUDGE_EVAL_GMAIL"), os.Getenv("TYPESAFE_API_KEY")
	if docsPath == "" || idsPath == "" || out == "" || composioKey == "" || gmailAccount == "" || typesafeKey == "" {
		t.Skip("an evaluation against production's verdicts needs its inputs and accounts named; see the comment")
	}
	ctx := context.Background()

	externalIDs := map[string]string{}
	for _, record := range readCSV(t, idsPath) {
		externalIDs[record[0]] = record[1]
	}
	proxy, err := composio.New(composio.Config{APIKey: composioKey})
	if err != nil {
		t.Fatal(err)
	}
	gmail := NewGmail(NewClient(proxy))
	credential := domain.Credential{Metadata: map[string]string{"composio_account_id": gmailAccount}}

	records := readCSV(t, docsPath)
	header := map[string]int{}
	for index, name := range records[0] {
		header[name] = index
	}
	field := func(record []string, name string) string { return record[header[name]] }

	var results []*judgeEvalResult
	var candidates []domain.Candidate
	// refetch names, for each result production excluded, the Gmail message
	// to read it again from.
	refetch := map[int]string{}
	for _, record := range records[1:] {
		live := field(record, "live") == "t"
		reason := field(record, "excluded_reason")
		if strings.HasPrefix(reason, "empty:") {
			continue // the rules decided these; no judge ever sees them
		}
		metadata := map[string]string{}
		_ = json.Unmarshal([]byte(field(record, "metadata")), &metadata)
		size, _ := strconv.Atoi(field(record, "byte_size"))
		mime := field(record, "mime")
		result := &judgeEvalResult{
			Provider: field(record, "provider"), Title: field(record, "title"), MIME: mime, Size: size,
			From: metadata["from"], Labels: metadata["labels"], Bulk: metadata["bulk"] == "yes",
			ProductionKeep: live, ProductionReason: reason,
		}
		candidate := domain.Candidate{
			Provider: result.Provider, Kind: field(record, "kind"), Title: result.Title, MIME: mime,
			Metadata: metadata, Size: size,
		}
		body := field(record, "body")
		switch {
		case transform.IsPassthroughMIME(mime) || (!live && result.Provider == "googledrive"):
			// Jev reads no files; it is shown the name and details, and a
			// stand-in so it knows the content is not there to read.
			result.Original, candidate.Original = true, []byte{0}
		case live:
			candidate.Text = body
		default:
			id := externalIDs[field(record, "id")]
			if id == "" {
				continue
			}
			result.Refetched = true
			refetch[len(results)] = id
		}
		results = append(results, result)
		candidates = append(candidates, candidate)
	}

	// What production excluded is read again, as the extractor read it.
	positions := slices.Collect(maps.Keys(refetch))
	started := time.Now()
	parallel(len(positions), 8, func(position int) {
		index := positions[position]
		item, ok, err := gmail.readMessage(ctx, credential, refetch[index])
		if err != nil || !ok {
			results[index].JevError = "could not read it again"
			return
		}
		text, err := transform.Normalize(item.Content, item.MIME)
		if err != nil {
			results[index].JevError = "could not normalize it"
			return
		}
		candidates[index].Text, candidates[index].MIME, candidates[index].Size = text.Body, text.MIME, text.Bytes
		candidates[index].Metadata = item.Metadata
		if strings.IndexFunc(text.Body, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) < 0 {
			results[index].JevError = "empty once read again"
		}
	})
	t.Logf("read %d excluded messages again in %s", len(refetch), time.Since(started).Round(time.Second))

	jev, err := judge.NewJev(judge.JevConfig{APIKey: typesafeKey})
	if err != nil {
		t.Fatal(err)
	}
	started = time.Now()
	parallel(len(results), 8, func(index int) {
		if results[index].JevError != "" {
			return
		}
		began := time.Now()
		answer, err := jev.Classify(ctx, candidates[index])
		results[index].JevLatencyMS = time.Since(began).Milliseconds()
		if err != nil {
			results[index].JevError = err.Error()
			return
		}
		result := results[index]
		result.JevKind, result.JevKeep, result.JevExclude = answer.Kind, answer.KeepProbability, answer.ExcludeProbability
		result.JevTokens = answer.InputTokens
		result.JevDecision, result.JevCertainty, result.JevReason = judge.Decide(answer)
	})
	t.Logf("Jev judged %d documents in %s", len(results), time.Since(started).Round(time.Second))

	if sample, _ := strconv.Atoi(os.Getenv("KNOWLEDGE_JUDGE_EVAL_GEMINI_SAMPLE")); sample > 0 && os.Getenv("GOOGLE_CLOUD_PROJECT") != "" && os.Getenv("KNOWLEDGE_JUDGE_LIVE_MODEL") != "" {
		gemini, err := judge.New(ctx, judge.Config{Project: os.Getenv("GOOGLE_CLOUD_PROJECT"), Location: "global", Model: os.Getenv("KNOWLEDGE_JUDGE_LIVE_MODEL")})
		if err != nil {
			t.Fatal(err)
		}
		var textual []int
		for index, result := range results {
			if !result.Original && result.JevError == "" {
				textual = append(textual, index)
			}
		}
		rand.Shuffle(len(textual), func(i, j int) { textual[i], textual[j] = textual[j], textual[i] })
		textual = textual[:min(sample, len(textual))]
		started = time.Now()
		parallel(len(textual), 8, func(position int) {
			index := textual[position]
			began := time.Now()
			verdict, err := gemini.Judge(ctx, candidates[index])
			if err != nil {
				return
			}
			results[index].GeminiSampled, results[index].GeminiKeep, results[index].GeminiReason = true, verdict.Keep, verdict.Reason
			results[index].GeminiLatencyMS = time.Since(began).Milliseconds()
		})
		t.Logf("Gemini judged a sample of %d in %s", len(textual), time.Since(started).Round(time.Second))
	}

	file, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	encoder := json.NewEncoder(file)
	for _, result := range results {
		if err := encoder.Encode(result); err != nil {
			t.Fatal(err)
		}
	}
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	var records [][]string
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, record)
	}
	return records
}

// parallel runs work for 0..n-1, limit at a time.
func parallel(n, limit int, work func(int)) {
	var wait sync.WaitGroup
	slots := make(chan struct{}, limit)
	for index := range n {
		wait.Go(func() {
			slots <- struct{}{}
			defer func() { <-slots }()
			work(index)
		})
	}
	wait.Wait()
}
