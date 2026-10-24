package relevance

import "strings"

// The synthetic sources: private material that reaches a run without a tool
// call, so no tool name says it did. The caller adds one to the sources it
// passes whenever its run had it.
const (
	// MemoryPreload is what the run remembered about its member before it
	// started: memories placed in its context by a preload, which never
	// appears among the tools a run called. Krude does not preload memory
	// today: it reads it with load_memory, a tool, and ADK's preload_memory
	// is left off on purpose (see Memory in the agents README), so today
	// the name is never needed. It is here so that turning a preload on
	// cannot quietly take memories past this gate: whoever does must pass
	// it.
	MemoryPreload = "memory_preload"
	// ThreadHistory is earlier turns of a thread in the run's context that
	// held the member's own conversations, in which the member may have said
	// or been shown private things: a member's own run on a thread a wake-up
	// already wrote to answers with all of it in view. No tool reads it back,
	// so the caller says so when a run started with such a history.
	// This also includes earlier shared side-conversation context: those
	// words can carry private facts beyond the current question and should
	// not silently reach a wider teamspace in a first-run publication.
	ThreadHistory = "thread_history"
)

// NeedsJudging is the provenance gate: it reports whether any of the tools a
// run called, given by name, may have read private material, so that its
// answer has to be judged before another member reads it. Only names the
// code below knows read nothing private are let through: a tool this
// package has never heard of counts as private, so a new tool is judged
// until someone decides otherwise. A run that called nothing, or nothing but
// those, has nothing in its answer it did not get from the request, and
// costs no model call.
//
// Pass the tools the run actually called, from what the run recorded; the
// agent's own report of them can leave one out, which is what steering does.
// Pass tool names, not toolbox names: "apps" is not a tool. Names are read
// in any case, and a blank one is ignored.
func NeedsJudging(sources []string) bool {
	for _, source := range sources {
		name := normalizeSource(source)
		if name == "" {
			continue
		}
		if _, harmless := readsNothingPrivate[name]; !harmless {
			return true
		}
	}
	return false
}

func normalizeSource(source string) string { return strings.ToLower(strings.TrimSpace(source)) }

// readsNothingPrivate are the tools known to read nothing private, with why.
// A tool is here only when what it can return is the caller's own arguments,
// the organization's directory, or something no other member's private
// material can be in. It is a short list on purpose, and an exact one: a
// tool added to a family that is listed here is not let through with it.
var readsNothingPrivate = map[string]string{
	// The organization directory: who people are and where they sit, which
	// every member can look up.
	"directory_load":        "the people, teams and agents every member of the organization may see",
	"directory_find_people": "the directory's people, by name",
	"directory_find_teams":  "the directory's teams, by name",
	"directory_find_agents": "the directory's agents, by name or purpose",
	"directory_find_owners": "the workspace ownership hints every joined member may see",
	"directory_is_above":    "a relation in the member-visible workspace organization chart",

	// Toolbox and helper bookkeeping: names and descriptions of what could
	// be loaded or is running, and sub-agents that have no tools and see only
	// the task they are given. What such a task holds came from a tool the
	// run called, or from the request.
	"list_tools":      "the toolboxes that could be loaded",
	"load_tools":      "loads toolboxes; says which, and why one could not be",
	"list_helpers":    "describes the sub-agents",
	"spawn_helper":    "runs an agent with no tools on the task it was given",
	"create_subagent": "records a sub-agent's name and instruction",
	"list_subagents":  "lists the sub-agents this conversation made",
	"remove_subagent": "drops a sub-agent",
	"planner":         "plans from the task it was given, with no tools",

	// The sandbox is a scratch machine: computation. It only ever sees files
	// the conversation wrote into it (see the sandbox skill), so whatever
	// private there is in it came in through a tool that is itself listed
	// in the run's sources, or an earlier turn (ThreadHistory).
	"sandbox_execute_code":    "computation",
	"sandbox_execute_command": "computation",
	"sandbox_list_files":      "files the conversation wrote in the sandbox",
	"sandbox_read_files":      "files the conversation wrote in the sandbox",
	"sandbox_write_files":     "writes what it is given",
	"sandbox_remove_files":    "removes files",
	"sandbox_reset":           "clears the sandbox",
	"sandbox_save_artifact":   "keeps a sandbox file as an artifact; returns its name",

	// Writing, and control, that return what they were given or nothing:
	// saving an artifact returns its name and version, publishing to a
	// teamspace echoes the entry, filing a question with a member returns
	// the request's id and status.
	"save_artifact":      "returns the name and version it saved",
	"teamspaces_publish": "publishes what it is given and echoes it",
	"room_ask_owner":     "files a question with the member; returns its id and status",
	"transfer_to_agent":  "hands the turn to another agent, whose own calls are recorded",
	"finish_task":        "ends a sub-agent's task with what it was given",
	"exit_loop":          "ends a loop",

	// The interactions that act on one request. In a run that delivers an
	// interaction these tools are pinned to that request, which holds what
	// the member who asked wrote and nothing of the addressed member's; they
	// refuse any other request. Outside a delivery, get can read any request
	// its agent is a party to. General-run sharing surfaces must add an
	// unknown/private source for that read, rather than assume this delivery
	// restriction from the name (the publication/side run recorders do).
	// Listing the inbox or the requests sent is not here: that
	// reads other people's requests, which is private.
	"interactions_get":     "the one request a delivery is for, as its asker wrote it",
	"interactions_create":  "files a request; returns it as the agent wrote it",
	"interactions_decide":  "records a decision on the one request a delivery is for",
	"interactions_respond": "records the answer to the one request a delivery is for",
}

// privateSources are the tools known to read private material, or that may
// have, with why. They are listed to be deliberate, and so a test can tell a
// tool somebody decided about from one nobody has: NeedsJudging judges
// every tool that is not in readsNothingPrivate, listed here or not.
var privateSources = map[string]string{
	// Memory, which is what the member told Krude in earlier threads.
	"load_memory":    "what the assistant remembers of the member, from every earlier thread",
	"preload_memory": "the same, placed in the run's context instead of asked for",
	MemoryPreload:    "memories placed in the run's context before it started",
	ThreadHistory:    "the member's earlier turns in the thread, in the run's context",

	// The member's files and account.
	"load_artifacts":     "the member's artifacts, including the ones every thread of theirs shares",
	"get_user_details":   "the member's own account details: their email and what they wrote about themselves",
	"get_current_screen": "what the member had open on Oryxa when they sent the message: a page, a selected item, a document's name",

	// Other people's requests.
	"interactions_inbox": "the requests other members made of this member's agents",
	"interactions_sent":  "the requests this member's agents made of others",

	// Shared teamspaces: what teammates published there is for the team.
	"teamspaces_list":    "the teamspaces the agent takes part in, and who is in them",
	"teamspaces_entries": "what teammates published to a teamspace",
	"teamspaces_context": "a teamspace's people and what teammates published there",

	// The open web, which says whatever it says about whom: a search for a
	// person can turn up their health or their family. The browser is
	// private for the same reason (browser_, below), and the researcher reads
	// pages the same way.
	"researcher":    "the open web, by search and by address",
	"web_search":    "the open web, by search and by address, as the researcher reads it",
	"google_search": "the open web, by search",
	"url_context":   "a page on the open web",

	// Krude's todo list for the thread (single-agent Krude). It returns what
	// Krude wrote, but it reads back steps earlier runs in the thread wrote,
	// which can carry what those runs read.
	"plan": "the thread's todo list, written by earlier runs that may have read private material",
}

// privateFamilies are tool families whose every tool reads private material:
// the member's connected apps, the organization's knowledge, the browser, and
// the member's agent registry and the results of the agents in it.
var privateFamilies = map[string]string{
	"apps_":         "the member's connected apps: mail, messages, calendars, files, CRMs (Composio and Oryxa toolkits)",
	"knowledge_":    "the organization's documents, including what its members' connected apps hold",
	"browser_":      "web pages, which can say anything about anyone; and screenshots that land in the artifact store",
	"agent_system_": "the member's agents and teams, and what the specialists they delegate to read",
}

// knownPrivate reports whether a source was decided to be private, as opposed
// to unknown, which NeedsJudging treats as private all the same.
func knownPrivate(source string) bool {
	name := normalizeSource(source)
	if _, listed := privateSources[name]; listed {
		return true
	}
	for prefix := range privateFamilies {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

// classified reports whether a source was decided about at all.
func classified(source string) bool {
	_, harmless := readsNothingPrivate[normalizeSource(source)]
	return harmless || knownPrivate(source)
}
