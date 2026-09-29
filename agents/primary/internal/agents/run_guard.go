package agents

import (
	"context"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/tool"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/relevance"
)

// RunGuardInput is trusted run metadata for an agent's shared publication.
// It is carried on context, never in tool arguments. Sources live only for
// the run. Context contains earlier member messages, or a side run's shared
// conversation, never tool results or private model replies. The side run's
// original question/context is separately kept in its private session for
// confirmation restoration. Sources are actual attempted calls plus synthetic
// sources for material that entered the run without a tool.
type RunGuardInput struct {
	Request string
	Context []string
	Sources []string
}

const (
	maxGuardSources      = 64
	maxGuardSourceRunes  = 128
	maxGuardRequestRunes = 32_000
)

type runGuardKey struct{}
type runGuard struct {
	mu    sync.Mutex
	input RunGuardInput
}

// WithRunGuard starts a new run's record. A context-scoped record avoids the
// cross-invocation lifetime of a global log; parallel calls share its lock.
func WithRunGuard(ctx context.Context, input RunGuardInput) context.Context {
	record := &runGuard{input: RunGuardInput{Request: guardText(input.Request, maxGuardRequestRunes)}}
	lines := input.Context
	if len(lines) > relevance.MaxContextLines {
		lines = lines[len(lines)-relevance.MaxContextLines:]
	}
	for _, line := range lines {
		record.input.Context = append(record.input.Context, guardText(line, relevance.MaxContextLineRunes))
	}
	for _, source := range input.Sources {
		record.add(source)
	}
	return context.WithValue(ctx, runGuardKey{}, record)
}

// GuardRun returns a copy of the run's metadata. Missing provenance must not
// mean a public-only run: the existing unknown-source marker forces judging.
func GuardRun(ctx context.Context) RunGuardInput {
	record, _ := ctx.Value(runGuardKey{}).(*runGuard)
	if record == nil {
		return RunGuardInput{Sources: []string{SourcesUnrecorded}}
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	return RunGuardInput{Request: record.input.Request, Context: slices.Clone(record.input.Context), Sources: slices.Clone(record.input.Sources)}
}

// recordRunSource records before execution, so a parallel publication cannot
// mistake an in-flight private read for no read. Failed calls are conservative
// sources too. Nothing from the arguments or result is read or retained.
func recordRunSource(ctx agent.Context, called tool.Tool, _ map[string]any) (map[string]any, error) {
	record, _ := ctx.Value(runGuardKey{}).(*runGuard)
	if record == nil {
		return nil, nil
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if called == nil {
		record.add(SourcesUnrecorded)
	} else {
		record.add(called.Name())
		// A transferred sub-agent may use provider tools that do not invoke
		// Go's callback. Its read cannot be inferred from transfer bookkeeping.
		// interactions_get is harmless only in a pinned delivery. General
		// owner runs can read other requests, so sharing surfaces must not
		// infer the delivery restriction from the name alone.
		if called.Name() == "transfer_to_agent" || called.Name() == "interactions_get" {
			record.add(SourcesUnrecorded)
		}
	}
	return nil, nil
}

func (record *runGuard) add(source string) {
	source = strings.TrimSpace(source)
	if source == "" || utf8.RuneCountInString(source) > maxGuardSourceRunes {
		source = SourcesUnrecorded
	}
	if slices.Contains(record.input.Sources, source) {
		return
	}
	// Reserve the final slot for an unknown source, so omitted names can
	// never turn overflow into a harmless-only provenance set.
	if len(record.input.Sources) >= maxGuardSources-1 {
		source = SourcesUnrecorded
		if slices.Contains(record.input.Sources, source) {
			return
		}
	}
	record.input.Sources = append(record.input.Sources, source)
}

func guardText(text string, limit int) string {
	if utf8.RuneCountInString(text) <= limit {
		return text
	}
	return string([]rune(text)[:limit])
}
