package agents

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/tool"
)

// What a run delivering an interaction read, for the consent guard.
//
// A run that answers a question asked in Slack answers from what its tools
// returned, and the guard has to read the answer against exactly that: whether
// the answer stands on it, and whether it copies it. So each tool result of a
// delivery run is kept, as text, for as long as the run can still answer, and
// the respond tool hands the ledger the run's latest ones, in its context and
// never in the response, as it hands the tools the run reaches (WithRunSources).
//
// Where they are kept is a decision of what ADK Go allows, checked in v2.4.0:
//
//   - A tool cannot read its session's events: the tool context's Session()
//     logs that it is not supported and returns nil, so the respond tool cannot
//     look back over the run's earlier calls.
//   - A callback can write session state (State().Set lands in the event's state
//     delta), but a delta is applied only when the event that carries it is
//     appended, after every call of its step has finished. Two calls of one
//     step, which ADK runs at once, would each read the list and write it back
//     without the other's result. And state is the thread's, kept in its
//     storage, so a private result would be written there once more.
//   - The agent's AfterToolCallbacks see every call's result, of every tool the
//     agent has (a toolset's included), whether the tool returned it or a
//     callback did, with the tool, its arguments and the call's error; returning
//     nothing leaves the result as it was. The context they are given has the
//     invocation's id, which the respond tool's has too.
//
// So an after-tool callback records the result in memory under the invocation's
// id, and the respond tool reads the run's own record back out by the same id.
// The record is the process's and lives only as long as the run can answer:
// never persisted, never logged, and dropped once nothing has touched it for
// runResultsTTL. An answer whose run recorded nothing (a restart lost it, the
// run was not a delivery, or a call was made in the same step as the answer)
// reaches the guard with no results, and is held as one with nothing to stand
// on: the record can only be missing, never wrong.

const (
	// maxRunResults is how many of a run's results are kept: its latest.
	maxRunResults = 16
	// maxRunResultChars bounds one result kept, in characters. A search of the
	// knowledge returns at most 20 passages of 1,500, so 40,000 keeps a whole
	// one with its details; a longer result is kept from its start.
	maxRunResultChars = 40_000
	// maxRunResultsChars bounds all of a run's results together, in characters:
	// what Bedrock's contextual grounding check takes as a source. The oldest
	// are dropped first.
	maxRunResultsChars = 100_000
	// runResultsTTL is how long after its last result a run's record is kept.
	runResultsTTL = 30 * time.Minute
	// maxRecordedRuns bounds how many runs' records are held at once; the one
	// touched longest ago is dropped for the next.
	maxRecordedRuns = 128
	// maxResultDepth and maxResultParts bound the reading of one result, so a
	// result of any shape is read in bounded work.
	maxResultDepth = 12
	maxResultParts = 4_000
)

// ToolResult is what one tool returned to a run delivering an interaction.
type ToolResult struct {
	// Tool is the tool's name.
	Tool string
	// Parts are what it returned, one text for each value in the result, as
	// "path: value" with the names of the fields it was found under, in order.
	// Text values are as the tool returned them, so that a run of words the
	// answer copies from one can be found in it.
	Parts []string
}

type runResultsKey struct{}

// WithRunResults returns ctx carrying what the run behind it read, which the
// ledger's Respond reads for the answer it is given (RunResults). Only the
// respond tool sets it, from the run's own record; it is no field of the
// response, so neither the model nor an API caller can say what the run read.
func WithRunResults(ctx context.Context, results []ToolResult) context.Context {
	return context.WithValue(ctx, runResultsKey{}, cloneResults(results))
}

// RunResults are the latest results of the run behind ctx, oldest first, from
// WithRunResults: none for an answer that does not come from a run that
// recorded any, such as the one a member gives in their own session.
func RunResults(ctx context.Context) []ToolResult {
	results, _ := ctx.Value(runResultsKey{}).([]ToolResult)
	return cloneResults(results)
}

func cloneResults(results []ToolResult) []ToolResult {
	cloned := make([]ToolResult, len(results))
	for index, result := range results {
		cloned[index] = ToolResult{Tool: result.Tool, Parts: slices.Clone(result.Parts)}
	}
	return cloned
}

// deliveryResults is the record of every run delivering an interaction in this
// process, by invocation. It is one for the process because the callback is
// registered when the agent is built and the respond tool in a toolbox built
// elsewhere, and what joins them is the invocation's id.
var deliveryResults = newResultLog(time.Now)

// afterTools are what a run delivering an interaction does after each tool call:
// record its result. A run that does not deliver one records nothing.
func (delivery *delivery) afterTools() []llmagent.AfterToolCallback {
	if delivery == nil {
		return nil
	}
	return []llmagent.AfterToolCallback{recordToolResult}
}

// recordToolResult keeps the result of a tool call that succeeded, and leaves
// the result as it was. Which tool it was makes no difference, so a tool added
// tomorrow is recorded like the rest, but for the ones that read nothing (notRead).
func recordToolResult(ctx agent.Context, called tool.Tool, _, result map[string]any, err error) (map[string]any, error) {
	invocation := ctx.InvocationID()
	if err != nil || called == nil || invocation == "" || notRead(called.Name()) {
		return nil, nil
	}
	if parts := resultParts(result, maxRunResultChars); len(parts) > 0 {
		deliveryResults.record(invocation, ToolResult{Tool: called.Name(), Parts: parts})
	}
	return nil, nil
}

// notRead reports whether a tool's result is nothing the run read, and nothing
// an answer could stand on: the interactions tools, whose results are the request
// the run delivers and the ledger's own record of the answer, and the response
// ADK has a model with an output schema give as its last word, which is the
// answer itself.
func notRead(tool string) bool {
	return strings.HasPrefix(tool, InteractionsToolboxName+"_") || tool == "set_model_response"
}

// runResultsOf is what the run behind ctx has read so far.
func runResultsOf(ctx agent.ReadonlyContext) []ToolResult {
	return deliveryResults.read(ctx.InvocationID())
}

// resultParts is a tool's result as text: one part for each value in it that
// says something, as "path: value", where path is the names of the fields it was
// found under, joined by points (a list adds nothing to it), in the order of the
// names. A result that is only an error says nothing, and a result of any size
// is read for at most limit characters and maxResultParts values.
func resultParts(result map[string]any, limit int) []string {
	if len(result) == 1 && result["error"] != nil {
		return nil
	}
	reader := resultReader{left: limit}
	reader.read("", result, 0)
	return reader.parts
}

type resultReader struct {
	parts []string
	// left is how many characters the parts may still take.
	left int
}

func (reader *resultReader) read(path string, value any, depth int) {
	if reader.left <= 0 || len(reader.parts) >= maxResultParts || depth > maxResultDepth {
		return
	}
	switch value := value.(type) {
	case nil:
	case string:
		reader.add(path, value)
	case bool:
		reader.add(path, strconv.FormatBool(value))
	case float64:
		reader.add(path, strconv.FormatFloat(value, 'f', -1, 64))
	case map[string]any:
		for _, name := range slices.Sorted(maps.Keys(value)) {
			reader.read(joinPath(path, name), value[name], depth+1)
		}
	case []any:
		for _, item := range value {
			reader.read(path, item, depth+1)
		}
	case []string:
		for _, item := range value {
			reader.add(path, item)
		}
	case []map[string]any:
		for _, item := range value {
			reader.read(path, item, depth+1)
		}
	default:
		reader.add(path, fmt.Sprint(value))
	}
}

func joinPath(path, name string) string {
	if path == "" {
		return name
	}
	return path + "." + name
}

// add keeps one value as a part, clipped to what is left.
func (reader *resultReader) add(path, value string) {
	value = strings.TrimSpace(value)
	if value == "" || reader.left <= 0 || len(reader.parts) >= maxResultParts {
		return
	}
	part := value
	if path != "" {
		part = path + ": " + value
	}
	if utf8.RuneCountInString(part) > reader.left {
		part = string([]rune(part)[:reader.left])
	}
	reader.left -= utf8.RuneCountInString(part)
	reader.parts = append(reader.parts, part)
}

// resultLog is the results of the runs in flight, by invocation id.
type resultLog struct {
	mu   sync.Mutex
	runs map[string]*recordedRun
	now  func() time.Time
}

type recordedRun struct {
	results []ToolResult
	// chars is the characters of results altogether.
	chars   int
	touched time.Time
}

func newResultLog(now func() time.Time) *resultLog {
	return &resultLog{runs: map[string]*recordedRun{}, now: now}
}

// record adds a result to its run's, dropping its oldest while there are more
// than maxRunResults, or more than maxRunResultsChars characters, and drops the
// records of runs nothing has touched for runResultsTTL.
func (log *resultLog) record(invocation string, result ToolResult) {
	log.mu.Lock()
	defer log.mu.Unlock()
	now := log.now()
	log.expire(now)
	run, known := log.runs[invocation]
	if !known {
		if len(log.runs) >= maxRecordedRuns {
			log.dropStalest()
		}
		run = &recordedRun{}
		log.runs[invocation] = run
	}
	run.touched = now
	run.results = append(run.results, result)
	run.chars += charsOf(result)
	for len(run.results) > maxRunResults || run.chars > maxRunResultsChars {
		run.chars -= charsOf(run.results[0])
		run.results = run.results[1:]
	}
}

// read is a copy of what a run has recorded so far, oldest first, or nothing for
// a run with no record.
func (log *resultLog) read(invocation string) []ToolResult {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.expire(log.now())
	run, known := log.runs[invocation]
	if !known {
		return nil
	}
	return cloneResults(run.results)
}

func (log *resultLog) expire(now time.Time) {
	for invocation, run := range log.runs {
		if now.Sub(run.touched) > runResultsTTL {
			delete(log.runs, invocation)
		}
	}
}

func (log *resultLog) dropStalest() {
	stalest, oldest := "", time.Time{}
	for invocation, run := range log.runs {
		if stalest == "" || run.touched.Before(oldest) {
			stalest, oldest = invocation, run.touched
		}
	}
	delete(log.runs, stalest)
}

func charsOf(result ToolResult) int {
	total := 0
	for _, part := range result.Parts {
		total += utf8.RuneCountInString(part)
	}
	return total
}
