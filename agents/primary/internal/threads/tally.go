package threads

import (
	"maps"
	"path"
	"slices"
	"strings"

	"google.golang.org/adk/v2/session"
)

// The state keys that say what files a thread's runs kept, so the thread list
// can show them without reading any thread's history. A run writes them as
// it ends, with its closing bracket (Run.record). A thread whose last run ended before they existed has
// none, and lists no files until its next run counts them all.
const (
	stateArtifactCount  = "thread_artifact_count"
	stateLatestArtifact = "thread_latest_artifact"
)

// ArtifactRef names a file one of a thread's runs kept, readable through the
// thread's artifact route.
type ArtifactRef struct {
	Name string `json:"name"`
	// Kind is the name's extension, lowercased and without its dot, such as
	// "pdf" or "xlsx"; empty for a name with none.
	Kind string `json:"kind"`
}

func artifactRef(name string) *ArtifactRef {
	if name == "" {
		return nil
	}
	return &ArtifactRef{Name: name, Kind: strings.ToLower(strings.TrimPrefix(path.Ext(name), "."))}
}

// artifactTally is the files a thread's history names: every name an event
// kept a version of, counted once however many versions it has, and the last
// one kept, in the order the history shows them as artifact items.
type artifactTally struct {
	names  map[string]bool
	latest string
}

func tallyArtifacts(events []*session.Event) artifactTally {
	tally := artifactTally{names: map[string]bool{}}
	for _, event := range events {
		tally.add(event)
	}
	return tally
}

// add counts what one stored event kept. A partial event keeps nothing: its
// finished event says it again.
func (tally *artifactTally) add(event *session.Event) {
	if event == nil || event.Partial {
		return
	}
	for _, name := range slices.Sorted(maps.Keys(event.Actions.ArtifactDelta)) {
		tally.names[name] = true
		tally.latest = name
	}
}

// changes is the state that brings what the thread says of its files up to
// the tally, or nil when it already says it.
func (tally artifactTally) changes(state session.ReadonlyState) map[string]any {
	count, counted := stateInt(state, stateArtifactCount)
	if counted && count == len(tally.names) && stateString(state, stateLatestArtifact) == tally.latest {
		return nil
	}
	return map[string]any{stateArtifactCount: len(tally.names), stateLatestArtifact: tally.latest}
}

// stateInt reads a whole number from state, which holds it as an int until
// stored and as a JSON number, a float64, once read back.
func stateInt(state session.ReadonlyState, key string) (int, bool) {
	if state == nil {
		return 0, false
	}
	value, err := state.Get(key)
	if err != nil {
		return 0, false
	}
	switch number := value.(type) {
	case int:
		return number, true
	case int64:
		return int(number), true
	case float64:
		return int(number), true
	default:
		return 0, false
	}
}
