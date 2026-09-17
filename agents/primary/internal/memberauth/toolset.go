package memberauth

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/genai"

	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/toolutils"
)

// WrapToolset returns inner with the acting member attached to the context
// every call it makes uses: its Tools listing and each tool's Run. Both are
// live, authenticated calls to a remote MCP endpoint — Tools lists its
// tools, Run calls one — and CredentialProvider only ever sees an
// HTTP request's plain context, never ADK's session state, so this is how
// the member reaches it.
//
// Context.WithAgentContext looks like the built-in way to swap in a new
// context, but the concrete Context ADK hands a tool's Run
// (agent.NewToolContext's wrapper, used by the real request flow) stubs
// WithAgentContext to log "not supported" and return nil, so it cannot be
// used here. Instead, the two small context types below embed whatever ADK
// context they wrap and override only the four context.Context methods
// (Deadline, Done, Err, Value): every ADK-specific method — state, session,
// tool actions, confirmation — is promoted, unchanged, from the real
// context, while a lookup by key sees whatever this package attached.
func WrapToolset(inner tool.Toolset) tool.Toolset {
	return &memberToolset{inner: inner}
}

type memberToolset struct {
	inner tool.Toolset
}

func (w *memberToolset) Name() string { return w.inner.Name() }

// Tools lists inner's tools with the session's member attached to the
// context inner uses to do it — for an MCP toolset, a live "list tools" call.
func (w *memberToolset) Tools(ctx agent.ReadonlyContext) ([]tool.Tool, error) {
	member, err := MemberFromSession(ctx)
	if err != nil {
		return nil, err
	}
	memberCtx := readonlyContext{ReadonlyContext: ctx, ctx: withMember(ctx, member)}
	tools, err := w.inner.Tools(memberCtx)
	if explained := Explain(err); explained != nil {
		return nil, explained
	}
	if err != nil {
		return nil, err
	}
	wrapped := make([]tool.Tool, len(tools))
	for i, t := range tools {
		wrapped[i] = &memberTool{inner: t}
	}
	return wrapped, nil
}

// readonlyContext presents ctx's ADK-specific surface unchanged while
// serving Deadline, Done, Err and Value from a different context.Context —
// see WrapToolset.
type readonlyContext struct {
	agent.ReadonlyContext
	ctx context.Context
}

func (c readonlyContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c readonlyContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c readonlyContext) Err() error                  { return c.ctx.Err() }
func (c readonlyContext) Value(key any) any           { return c.ctx.Value(key) }

// toolContext is readonlyContext's counterpart for agent.Context, used when
// wrapping a tool's Run.
type toolContext struct {
	agent.Context
	ctx context.Context
}

func (c toolContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c toolContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c toolContext) Err() error                  { return c.ctx.Err() }
func (c toolContext) Value(key any) any           { return c.ctx.Value(key) }

// memberTool wraps one tool from the inner toolset so Run attaches the
// member to the context the call uses, exactly as Tools does above.
type memberTool struct {
	inner tool.Tool
}

func (t *memberTool) Name() string        { return t.inner.Name() }
func (t *memberTool) Description() string { return t.inner.Description() }
func (t *memberTool) IsLongRunning() bool { return t.inner.IsLongRunning() }

// Declaration implements toolutils.Tool, so this tool can still be packed
// into a model request, and satisfies the toolbox catalog's check that every
// tool it dynamically loads is declarative.
func (t *memberTool) Declaration() *genai.FunctionDeclaration {
	if declared, ok := t.inner.(toolutils.Tool); ok {
		return declared.Declaration()
	}
	return nil
}

// ProcessRequest mirrors an MCP tool's own: it packs this wrapper — not the
// inner tool — into the request, so a call the model makes comes back to
// this Run, not straight to the inner tool.
func (t *memberTool) ProcessRequest(ctx agent.Context, req *model.LLMRequest) error {
	return toolutils.PackTool(req, t)
}

// Run attaches the session's member to the context the inner tool's Run
// uses, so an MCP call it makes carries who is calling.
func (t *memberTool) Run(ctx agent.Context, args any) (map[string]any, error) {
	runner, ok := t.inner.(runnableTool)
	if !ok {
		return nil, fmt.Errorf("memberauth: tool %q cannot be run", t.inner.Name())
	}
	member, err := MemberFromSession(ctx)
	if err != nil {
		return nil, err
	}
	memberCtx := toolContext{Context: ctx, ctx: withMember(ctx, member)}
	result, err := runner.Run(memberCtx, args)
	// The model reads a tool's error: a failure to act for the member
	// reaches it in plain words, never with the request that carried it.
	if explained := Explain(err); explained != nil {
		return nil, explained
	}
	return result, err
}

// runnableTool is what an inner tool needs so memberTool can call it.
// Declared locally and matched structurally, because ADK's own equivalent
// (tool.runnableTool) is private to package tool.
type runnableTool interface {
	Run(ctx agent.Context, args any) (map[string]any, error)
}

var (
	_ tool.Toolset   = (*memberToolset)(nil)
	_ tool.Tool      = (*memberTool)(nil)
	_ toolutils.Tool = (*memberTool)(nil)
)
