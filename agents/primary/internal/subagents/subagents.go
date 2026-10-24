// Package subagents runs the sub-agents single-agent Krude starts for
// itself (AGENTS_PRIMARY_SINGLE_AGENT; docs/design/agent-runtime.md,
// sections 2 to 6). Krude briefs one with spawn_agent, answers its questions
// and sends it follow-ups with message_agent, reads it with agent_status and
// stops it with stop_agent. A sub-agent asks Krude, never the member, with
// ask_parent.
//
// What Krude reads of a sub-agent is its Record, which a Store keeps: the
// objective, the state, the question it asked, a progress note and a
// condensed result. Its transcript is an ADK session of its own (AppName,
// session id <thread>/<sub-agent>), so it never enters Krude's context and
// it can be resumed.
//
// A sub-agent runs as the member, on the context of the run of Krude's that
// set it working: the member's token, the run's usage counter, which prices
// its model calls into that run, and the run's record of what it read. Its
// tools are a fixed set (Worker): reads of the organization's knowledge, the
// member's apps and the web, which send, post and change nothing; or, on its
// own, the conversation's sandbox, which runs code with network access and
// so is never given beside a read (checkBrief), so that nothing a sub-agent
// read can leave through it. It never starts sub-agents of its own, and it
// neither reads nor writes memory. A running sub-agent stops when the run that set it working ends;
// a done or waiting one is kept, for the thread's next runs, until its TTL
// after its last activity runs out.
//
// Nothing here emits an outbox event: each state change is a log line and
// each turn of a sub-agent's an OpenTelemetry span. The agents consumer
// refuses a detail type it does not know (inbound/consumer.go), so outbox
// events wait until a consumer handles them.
package subagents

import (
	"time"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
)

// AppName is the ADK app every sub-agent's session belongs to.
const AppName = "krude_sub"

// WorkerName is the agent name a sub-agent answers to in its own session.
const WorkerName = "subagent"

// The tool families a brief may allow, and nothing else. The three reads
// send, post and change nothing; the sandbox runs code, network included,
// so a brief has it alone or not at all.
const (
	// ToolKnowledge is the organization's documents, searched as the member.
	ToolKnowledge = "knowledge"
	// ToolAppsRead is the three reads of the member's apps:
	// apps_connections, apps_search_tools and apps_run_read_tool.
	ToolAppsRead = "apps_read"
	// ToolWebSearch is web_search, Krude's own search-only agent.
	ToolWebSearch = "web_search"
	// ToolSandbox is the conversation's sandbox, the one Krude uses, for
	// running code over scratch files. It is not read-only: code there can
	// reach the network. A brief that allows it allows nothing else.
	ToolSandbox = "sandbox"
)

// Families are the tool families a brief may name, in the order they are
// described.
var Families = []string{ToolKnowledge, ToolAppsRead, ToolWebSearch, ToolSandbox}

// The tools Krude manages its sub-agents with, and the one a sub-agent asks
// Krude a question with.
const (
	SpawnToolName   = "spawn_agent"
	MessageToolName = "message_agent"
	StatusToolName  = "agent_status"
	StopToolName    = "stop_agent"
	AskToolName     = "ask_parent"
)

// Budgets: what a brief gets when it names none, and the most it may name.
// A budget is per assignment: spawn_agent's, and each message_agent's that
// sets the sub-agent working again, starts afresh.
const (
	DefaultModelCalls = 30
	DefaultMinutes    = 5
	MaxModelCalls     = 100
	MaxMinutes        = 15
)

// Defaults for Config's limits.
const (
	DefaultPerRun     = 4
	DefaultPerProcess = 32
	DefaultTTL        = 24 * time.Hour
)

// Worker is what Builder builds a sub-agent from: its instruction, the tool
// families its brief allowed, the thread whose sandbox it shares, its own
// tools (ask_parent) and the callbacks that count and bound its model calls.
type Worker struct {
	Instruction string
	Tools       []string
	Thread      Thread
	Own         []tool.Tool
	BeforeModel []llmagent.BeforeModelCallback
	AfterModel  []llmagent.AfterModelCallback
	BeforeTool  []llmagent.BeforeToolCallback
	AfterTool   []llmagent.AfterToolCallback
	// Meter wraps the model of any agent the sub-agent's tools run, such as
	// web_search's, so its calls count against the sub-agent's budget and
	// its totals as the sub-agent's own do. Nil leaves them as they are.
	Meter func(model.LLM) model.LLM
}

// Builder builds sub-agents: internal/agents' SubagentBuilder, over the
// lite model and the toolboxes it may have.
type Builder interface {
	// Families are the tool families this deployment can give a sub-agent,
	// of Families.
	Families() []string
	// Build returns the agent, named WorkerName, with Worker's tools and
	// nothing else.
	Build(Worker) (agent.Agent, error)
}
