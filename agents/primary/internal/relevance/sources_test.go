package relevance

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestNeedsJudging(t *testing.T) {
	for _, test := range []struct {
		name    string
		sources []string
		want    bool
	}{
		{"no sources: the run read nothing", nil, false},
		{"an empty list", []string{}, false},
		{"blank names are nothing", []string{"", "  ", "\t"}, false},
		{"memory that was preloaded into the context", []string{MemoryPreload}, true},
		{"the literal synthetic name", []string{"memory_preload"}, true},
		{"a thread's earlier turns in the context", []string{ThreadHistory}, true},
		{"the member's apps", []string{"apps_run_read_tool"}, true},
		{"the apps' other tools", []string{"apps_connections", "apps_search_tools", "apps_run_write_tool"}, true},
		{"a tool of the apps family nobody has heard of", []string{"apps_something_new"}, true},
		{"knowledge", []string{"knowledge_search"}, true},
		{"the inbox", []string{"interactions_inbox"}, true},
		{"requests sent", []string{"interactions_sent"}, true},
		{"memory read as a tool", []string{"load_memory"}, true},
		{"artifacts read back", []string{"load_artifacts"}, true},
		{"the browser", []string{"browser_navigate"}, true},
		{"a tool nobody has heard of", []string{"quantum_oracle"}, true},
		{"a sub-agent the model named", []string{"fact_checker"}, true},
		{"a directory lookup", []string{"directory_find_people"}, false},
		{"workspace ownership hints", []string{"directory_find_owners"}, false},
		{"workspace organization chart", []string{"directory_is_above"}, false},
		{"every directory tool", []string{"directory_load", "directory_find_people", "directory_find_teams", "directory_find_agents", "directory_find_owners", "directory_is_above"}, false},
		{"workspace directory with private reads", []string{"directory_find_owners", "directory_is_above", "apps_run_read_tool"}, true},
		{"computation", []string{"sandbox_execute_code", "sandbox_execute_command"}, false},
		{"the delivered request, read and answered", []string{"interactions_get", "interactions_respond"}, false},
		{"bookkeeping", []string{"list_tools", "load_tools", "list_helpers", "spawn_helper", "create_subagent", "list_subagents", "remove_subagent", "planner"}, false},
		{"one private among harmless ones", []string{"directory_find_people", "knowledge_search", "list_tools"}, true},
		{"one unknown among harmless ones", []string{"directory_find_people", "quantum_oracle"}, true},
		{"one private among blanks", []string{"", "apps_connections", " "}, true},
		{"names in any case", []string{"  Directory_Find_People  "}, false},
		{"private names in any case", []string{"APPS_Run_Read_Tool"}, true},
		{"a toolbox name is not a tool", []string{"directory"}, true},
		{"a family prefix is not a tool", []string{"directory_"}, true},
		{"an allowed name with a suffix is another tool", []string{"directory_find_people_2"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := NeedsJudging(test.sources); got != test.want {
				t.Fatalf("NeedsJudging(%q) = %v, want %v", test.sources, got, test.want)
			}
		})
	}
}

func TestTheSyntheticSourcesAreNamedAsDocumented(t *testing.T) {
	if MemoryPreload != "memory_preload" || ThreadHistory != "thread_history" {
		t.Fatalf("the synthetic sources are %q and %q", MemoryPreload, ThreadHistory)
	}
	if !NeedsJudging([]string{MemoryPreload}) || !NeedsJudging([]string{ThreadHistory}) || NeedsJudging(nil) {
		t.Fatal("a synthetic source must need judging and no source must not")
	}
}

// TestNoToolIsBothLetThroughAndPrivate: a tool is decided one way. The tools
// let through are exact names, and none of them is also private, by name or
// by family.
func TestNoToolIsBothLetThroughAndPrivate(t *testing.T) {
	for name, why := range readsNothingPrivate {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is let through without a reason", name)
		}
		if name != normalizeSource(name) {
			t.Errorf("%q is not written as names are read: %q", name, normalizeSource(name))
		}
		if _, private := privateSources[name]; private || knownPrivate(name) {
			t.Errorf("%s is both let through and private", name)
		}
		if NeedsJudging([]string{name}) {
			t.Errorf("%s is let through and still needs judging", name)
		}
	}
	for name, why := range privateSources {
		if strings.TrimSpace(why) == "" {
			t.Errorf("%s is private without a reason", name)
		}
		if name != normalizeSource(name) {
			t.Errorf("%q is not written as names are read: %q", name, normalizeSource(name))
		}
		if !NeedsJudging([]string{name}) {
			t.Errorf("%s is private and does not need judging", name)
		}
	}
	for prefix, why := range privateFamilies {
		if strings.TrimSpace(why) == "" || !strings.HasSuffix(prefix, "_") {
			t.Errorf("family %q needs a reason and a trailing underscore", prefix)
		}
		if !NeedsJudging([]string{prefix + "anything"}) {
			t.Errorf("family %q does not need judging", prefix)
		}
	}
}

// TestTheKnownPrivateSourcesAre: the tools the owner named are private.
func TestTheKnownPrivateSourcesAre(t *testing.T) {
	for _, name := range []string{
		// apps, Composio's and Oryxa's toolkits, as the apps MCP endpoint names its tools
		"apps_connections", "apps_search_tools", "apps_run_read_tool", "apps_run_write_tool",
		// knowledge
		"knowledge_collections", "knowledge_search", "knowledge_sources", "knowledge_sync_source",
		// inbox reads
		"interactions_inbox", "interactions_sent",
		// files and artifacts
		"load_artifacts",
		// memory
		"load_memory", "preload_memory",
		// the browser
		"browser_navigate", "browser_snapshot", "browser_click", "browser_fill", "browser_screenshot", "browser_reset",
		// the rest of what reads a member's data
		"get_user_details", "teamspaces_list", "teamspaces_entries", "teamspaces_context",
		"agent_system_list_agents", "agent_system_delegate", "agent_system_continue_assignment",
	} {
		if !knownPrivate(name) {
			t.Errorf("%s is not decided to be private", name)
		}
		if !NeedsJudging([]string{name}) {
			t.Errorf("%s does not need judging", name)
		}
	}
}

// toolDeclaration finds the name of each function tool a file declares.
var toolDeclaration = regexp.MustCompile(`(?s)functiontool\.Config\{\s*Name:\s*"([a-z0-9_]+)"`)

// TestEveryToolTheAgentsDeclareWasDecidedAbout reads the code that declares
// the agents' tools and fails on a tool that neither list knows. An unknown
// tool is judged, which is safe, so this is not a safety check: it is the
// nudge that makes whoever adds a tool decide whether it reads anything
// private, instead of leaving every run that calls it to cost a model call
// for good.
func TestEveryToolTheAgentsDeclareWasDecidedAbout(t *testing.T) {
	var declared []string
	for _, pattern := range []string{"../agents/*.go", "../agentcorebrowser/*.go", "../agentcoresandbox/*.go"} {
		files, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			source, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			for _, match := range toolDeclaration.FindAllStringSubmatch(string(source), -1) {
				declared = append(declared, match[1])
			}
		}
	}
	if len(declared) == 0 {
		t.Skip("the agents' source is not beside this package, so there is nothing to compare with")
	}
	// A pattern that stopped matching would pass for ever: the agents declare
	// far more than this today.
	if len(declared) < 40 {
		t.Fatalf("found only %d declared tools (%v): the pattern no longer matches how tools are declared", len(declared), declared)
	}
	t.Logf("compared %d declared tools with the two lists", len(declared))
	for _, name := range slices.Compact(slices.Sorted(slices.Values(declared))) {
		if !classified(name) {
			t.Errorf("the tool %q is declared but neither relevance.readsNothingPrivate nor relevance.privateSources decides it: add it to one (until then it counts as private and every run that calls it is judged)", name)
		}
	}
}
