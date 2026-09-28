package authz

import (
	"context"
	_ "embed" // enables //go:embed below
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"sync"
	"time"

	cedar "github.com/cedar-policy/cedar-go"
)

// defaultCacheTTL is how long Engine trusts an organization's compiled
// stored policies before asking PolicyStore again; see WithCacheTTL.
const defaultCacheTTL = 30 * time.Second

//go:embed policies/knowledge_bases.cedar
var baselineCedarText string

// baselineIDAnnotation is the annotation each baseline statement carries its
// stable id in; see policies/knowledge_bases.cedar.
const baselineIDAnnotation = "id"

// Engine decides Requests: the baseline permits from
// policies/knowledge_bases.cedar, narrowed by whichever of an organization's
// stored policies apply (see PolicyStore and Store). The zero Engine is not
// usable; build one with New.
type Engine struct {
	store    PolicyStore
	baseline *cedar.PolicySet
	// baselineIDs is exactly the ids baseline carries. Authorize checks a
	// Diagnostic's errors against it: an evaluation error from one of these
	// forces a denial, because the baseline is the only source of permits
	// (see doc.go); an error from a stored policy, which can only forbid, is
	// merely logged, not treated as a denial in its own right.
	baselineIDs map[cedar.PolicyID]bool
	logger      *slog.Logger
	cacheTTL    time.Duration
	// now is time.Now, overridden by tests to control the cache without
	// sleeping.
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cacheEntry
}

// cacheEntry is one organization's compiled stored policies, as of loadedAt.
type cacheEntry struct {
	loadedAt   time.Time
	statements []compiledStatement
}

// compiledStatement is one forbid statement out of one stored Policy's Text,
// already parsed and paired with the Scope it was stored under, so Engine
// can tell which requests it applies to without reparsing Text on every call.
type compiledStatement struct {
	id     cedar.PolicyID
	scope  Scope
	policy *cedar.Policy
}

// Option adjusts an Engine built by New.
type Option func(*Engine)

// WithCacheTTL overrides how long an organization's compiled stored policies
// are trusted before Engine asks PolicyStore again. The default is 30
// seconds.
func WithCacheTTL(ttl time.Duration) Option {
	return func(engine *Engine) { engine.cacheTTL = ttl }
}

// WithLogger gives Engine somewhere to report policies that fail to
// evaluate (see Authorize). The default discards them.
func WithLogger(logger *slog.Logger) Option {
	return func(engine *Engine) {
		if logger != nil {
			engine.logger = logger
		}
	}
}

// New builds an Engine over the baseline policies and, when store is not
// nil, whatever an organization has stored on top of them. A nil store
// leaves every organization at the baseline: with nothing to forbid, only
// what the baseline already permits is ever allowed.
func New(store PolicyStore, options ...Option) (*Engine, error) {
	baseline, baselineIDs, err := parseBaseline()
	if err != nil {
		return nil, err
	}
	engine := &Engine{
		store:       store,
		baseline:    baseline,
		baselineIDs: baselineIDs,
		logger:      slog.New(slog.DiscardHandler),
		cacheTTL:    defaultCacheTTL,
		now:         time.Now,
		cache:       map[string]cacheEntry{},
	}
	for _, option := range options {
		option(engine)
	}
	return engine, nil
}

// parseBaseline compiles policies/knowledge_bases.cedar once, keyed by each
// statement's own @id annotation rather than by its position in the file, so
// reordering statements there never changes a Decision's Policies.
func parseBaseline() (*cedar.PolicySet, map[cedar.PolicyID]bool, error) {
	return parseBaselineFromText(baselineCedarText)
}

// parseBaselineFromText is parseBaseline's logic over arbitrary text, so
// tests can check that a statement missing its @id, or two statements
// sharing one, are refused without editing the embedded file.
func parseBaselineFromText(text string) (*cedar.PolicySet, map[cedar.PolicyID]bool, error) {
	statements, err := cedar.NewPolicyListFromBytes("policies/knowledge_bases.cedar", []byte(text))
	if err != nil {
		return nil, nil, fmt.Errorf("authz: parse baseline policies: %w", err)
	}
	policySet := cedar.NewPolicySet()
	ids := make(map[cedar.PolicyID]bool, len(statements))
	for _, statement := range statements {
		raw, ok := statement.Annotations()[baselineIDAnnotation]
		if !ok || raw == "" {
			position := statement.Position()
			return nil, nil, fmt.Errorf("authz: baseline policy at %s:%d is missing its @id annotation", position.Filename, position.Line)
		}
		id := cedar.PolicyID(raw)
		if !policySet.Add(id, statement) {
			return nil, nil, fmt.Errorf("authz: baseline policy id %q is used more than once", id)
		}
		ids[id] = true
	}
	return policySet, ids, nil
}

// Authorize decides one Request.
func (engine *Engine) Authorize(ctx context.Context, request Request) (Decision, error) {
	statements, err := engine.statementsFor(ctx, request.OrganizationID)
	if err != nil {
		return Decision{Allowed: false}, err
	}
	return engine.evaluate(statements, request.OrganizationID, request.Principal, request.Action, request.Resource, request.Context), nil
}

// AuthorizeEach decides one action on many resources for one principal and
// context, loading the organization's stored policies once rather than once
// per resource — the shape filtering a search result list needs.
func (engine *Engine) AuthorizeEach(ctx context.Context, organizationID string, principal Principal, action Action, resources []Resource, requestContext Context) ([]bool, error) {
	allowed := make([]bool, len(resources))
	if len(resources) == 0 {
		return allowed, nil
	}
	statements, err := engine.statementsFor(ctx, organizationID)
	if err != nil {
		return allowed, err
	}
	for i, resource := range resources {
		allowed[i] = engine.evaluate(statements, organizationID, principal, action, resource, requestContext).Allowed
	}
	return allowed, nil
}

// Invalidate drops an organization's cached compiled policies, so the next
// Authorize or AuthorizeEach for it reads PolicyStore again instead of
// waiting out the cache TTL. Callers use it right after CreatePolicy or
// DeletePolicy so a change takes effect immediately.
func (engine *Engine) Invalidate(organizationID string) {
	engine.mu.Lock()
	defer engine.mu.Unlock()
	delete(engine.cache, organizationID)
}

// statementsFor is an organization's compiled stored policies, from cache
// when it is fresh and from PolicyStore otherwise. A nil store, or an
// organization with nothing stored, both come back as no statements: every
// request then rests on the baseline alone.
func (engine *Engine) statementsFor(ctx context.Context, organizationID string) ([]compiledStatement, error) {
	if engine.store == nil {
		return nil, nil
	}
	engine.mu.Lock()
	defer engine.mu.Unlock()
	if entry, ok := engine.cache[organizationID]; ok && engine.now().Sub(entry.loadedAt) < engine.cacheTTL {
		return entry.statements, nil
	}
	policies, err := engine.store.OrganizationPolicies(ctx, organizationID)
	if err != nil {
		return nil, fmt.Errorf("authz: load organization policies: %w", err)
	}
	statements, err := compileStatements(policies)
	if err != nil {
		return nil, err
	}
	engine.cache[organizationID] = cacheEntry{loadedAt: engine.now(), statements: statements}
	return statements, nil
}

// compileStatements parses each stored Policy's Text, giving every statement
// in it a unique id built from the Policy's own id and its position in the
// text — "org/<policy id>/<n>" — since a stored Policy, unlike the baseline,
// is not expected to carry its own @id annotations: ValidatePolicy never
// requires an admin to write one.
func compileStatements(policies []Policy) ([]compiledStatement, error) {
	var statements []compiledStatement
	for _, policy := range policies {
		parsed, err := cedar.NewPolicyListFromBytes(policy.ID, []byte(policy.Text))
		if err != nil {
			return nil, fmt.Errorf("authz: parse stored policy %s: %w", policy.ID, err)
		}
		for n, statement := range parsed {
			statements = append(statements, compiledStatement{
				id:     cedar.PolicyID(fmt.Sprintf("org/%s/%d", policy.ID, n)),
				scope:  policy.Scope,
				policy: statement,
			})
		}
	}
	return statements, nil
}

// evaluate authorizes one action on one resource, given an organization's
// already-loaded compiled statements.
func (engine *Engine) evaluate(all []compiledStatement, organizationID string, principal Principal, action Action, resource Resource, requestContext Context) Decision {
	applicable := applicableStatements(all, resource.scopes())

	principalUID, principals := principalEntities(organizationID, principal)
	resources := resource.entities()
	entities := make(cedar.EntityMap, len(principals)+len(resources))
	for _, entity := range principals {
		entities[entity.UID] = entity
	}
	for _, entity := range resources {
		entities[entity.UID] = entity
	}

	request := cedar.Request{
		Principal: principalUID,
		Action:    actionUID(action),
		Resource:  resource.entityUID(),
		Context:   contextRecord(requestContext),
	}

	decision, diagnostic := cedar.Authorize(policyIterator{baseline: engine.baseline, stored: applicable}, entities, request)

	failClosed := false
	for _, evaluationError := range diagnostic.Errors {
		engine.logger.Warn("authz: a policy failed to evaluate; Cedar treats it as not matching",
			"policy", string(evaluationError.PolicyID), "error", evaluationError.Message)
		if engine.baselineIDs[evaluationError.PolicyID] {
			failClosed = true
		}
	}
	if failClosed {
		return Decision{Allowed: false}
	}

	ids := make([]string, 0, len(diagnostic.Reasons))
	for _, reason := range diagnostic.Reasons {
		ids = append(ids, string(reason.PolicyID))
	}
	return Decision{Allowed: bool(decision), Policies: ids}
}

// applicableStatements is the subset of an organization's compiled
// statements whose Scope is one of scopes — the organization's own plus
// whichever the resource and its ancestors carry (see Resource.scopes).
func applicableStatements(all []compiledStatement, scopes []Scope) []compiledStatement {
	if len(all) == 0 {
		return nil
	}
	applicable := make([]compiledStatement, 0, len(all))
	for _, statement := range all {
		if slices.Contains(scopes, statement.scope) {
			applicable = append(applicable, statement)
		}
	}
	return applicable
}

// contextRecord is a Context as the Cedar context record schema.cedarschema
// documents: acts_as_admin and team_member, nothing else.
func contextRecord(requestContext Context) cedar.Record {
	return cedar.NewRecord(cedar.RecordMap{
		"acts_as_admin": cedar.Boolean(requestContext.ActsAsAdmin),
		"team_member":   cedar.Boolean(requestContext.TeamMember),
	})
}

// policyIterator combines the baseline PolicySet with one request's
// applicable stored statements without copying either into a new PolicySet:
// cedar.Authorize needs only something it can range over.
type policyIterator struct {
	baseline *cedar.PolicySet
	stored   []compiledStatement
}

func (policies policyIterator) All() iter.Seq2[cedar.PolicyID, *cedar.Policy] {
	return func(yield func(cedar.PolicyID, *cedar.Policy) bool) {
		for id, policy := range policies.baseline.All() {
			if !yield(id, policy) {
				return
			}
		}
		for _, statement := range policies.stored {
			if !yield(statement.id, statement.policy) {
				return
			}
		}
	}
}
