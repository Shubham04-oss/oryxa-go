package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const sentinel = `"@@FRAG@@"`

// Runner drives mutations of operations and judges the answers.
type Runner struct {
	W         *World
	Findings  *FindingSet
	Workers   int
	SlowAfter time.Duration
	Logf      func(format string, args ...any)
	// Stop is asked between operations; a non-empty answer ends the run with it.
	Stop func() string

	sent    atomic.Int64
	perOp   sync.Map // op name -> *atomic.Int64
	statusM sync.Map // "op|status" -> *atomic.Int64
}

// NewRunner returns a runner with sensible defaults.
func NewRunner(world *World, logf func(string, ...any)) *Runner {
	return &Runner{W: world, Findings: NewFindingSet(), Workers: 8, SlowAfter: 4 * time.Second, Logf: logf}
}

// Healthy asks the server whether it is still answering.
func (runner *Runner) Healthy(ctx context.Context) bool {
	res := runner.W.C.Do(ctx, Req{Method: "GET", Path: "/healthz", Timeout: 5 * time.Second, Op: "healthz", Route: "GET /healthz"})
	return res.Status == http.StatusNoContent
}

// leafPaths lists the path to every scalar in a decoded JSON value.
func leafPaths(node any, prefix []any) [][]any {
	switch value := node.(type) {
	case map[string]any:
		var out [][]any
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			out = append(out, leafPaths(value[key], append(append([]any{}, prefix...), key))...)
		}
		return out
	case []any:
		var out [][]any
		limit := len(value)
		if limit > 2 {
			limit = 2 // a long array (an embedding) has one kind of leaf, not 1536
		}
		for index := 0; index < limit; index++ {
			out = append(out, leafPaths(value[index], append(append([]any{}, prefix...), index))...)
		}
		return out
	default:
		return [][]any{prefix}
	}
}

func setAt(node any, path []any, value any) any {
	if len(path) == 0 {
		return value
	}
	switch container := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(container))
		for key, child := range container {
			out[key] = child
		}
		key := path[0].(string)
		out[key] = setAt(container[key], path[1:], value)
		return out
	case []any:
		out := append([]any{}, container...)
		index := path[0].(int)
		out[index] = setAt(container[index], path[1:], value)
		return out
	}
	return node
}

func describe(path []any) string {
	parts := make([]string, len(path))
	for index, part := range path {
		parts[index] = fmt.Sprint(part)
	}
	return strings.Join(parts, ".")
}

// mutations lists the hostile variants of an operation's working request.
func (runner *Runner) mutations(op Op, base Req, orgKey string, authProbe bool) []Req {
	var out []Req
	clone := func(mutation, class, target string) Req {
		req := base
		req.Header = base.Header.Clone()
		req.Mutation, req.Class, req.Target = mutation, class, target
		return req
	}

	if op.Body != nil && !op.Consumes {
		for _, named := range BodyMutations(base.Body) {
			req := clone(named.Name, "structure", "body")
			req.Body = named.Body
			if req.Header == nil {
				req.Header = http.Header{}
			}
			switch named.ContentType {
			case "":
			case "-":
				req.Header.Del("Content-Type")
			default:
				req.Header.Set("Content-Type", named.ContentType)
			}
			if named.Encoding != "" {
				req.Header.Set("Content-Encoding", named.Encoding)
			}
			req.Timeout = 30 * time.Second
			out = append(out, req)
		}
		var decoded any
		if json.Unmarshal(base.Body, &decoded) == nil {
			for _, path := range leafPaths(decoded, nil) {
				marked, _ := json.Marshal(setAt(decoded, path, "@@FRAG@@"))
				for _, fragment := range Fragments() {
					req := clone(fragment.Name, fragment.Class, describe(path))
					req.Body = []byte(strings.Replace(string(marked), sentinel, string(fragment.JSON), 1))
					req.Timeout = 30 * time.Second
					out = append(out, req)
				}
			}
		}
	}

	if len(op.Query) > 0 && !op.Consumes {
		for index, param := range op.Query {
			for _, fragment := range QueryValues() {
				values := make([]string, 0, len(op.Query))
				for other, existing := range op.Query {
					resolved, _ := runner.W.Resolve(orgKey, existing.Value)
					if other == index {
						resolved = string(fragment.JSON)
					}
					values = append(values, url.QueryEscape(existing.Name)+"="+url.QueryEscape(resolved))
				}
				req := clone(fragment.Name, "query", param.Name)
				req.Path = strings.SplitN(base.Path, "?", 2)[0] + "?" + strings.Join(values, "&")
				out = append(out, req)
			}
		}
		root := strings.SplitN(base.Path, "?", 2)[0]
		for name, query := range map[string]string{
			"unknown-param":   "zz_unknown=1",
			"duplicate-param": url.QueryEscape(op.Query[0].Name) + "=1&" + url.QueryEscape(op.Query[0].Name) + "=2",
			"empty-query-key": "=1&&=&",
			"bracket-params":  "a[b]=c&a[]=d&__proto__[x]=1",
			"thousand-params": strings.Repeat("p=1&", 1000) + "p=1",
			"huge-value-1mib": op.Query[0].Name + "=" + strings.Repeat("x", 1<<20),
		} {
			req := clone(name, "query", "query")
			req.Path = root + "?" + query
			out = append(out, req)
		}
	}

	for _, name := range placeholders(op.Path) {
		if name == "org" {
			continue
		}
		for _, fragment := range PathValues() {
			req := clone(fragment.Name, "path", name)
			current := runner.W.Get(orgKey, name)
			if current == "" {
				current = "missing-" + name // what Resolve put there
			}
			req.Path = strings.Replace(base.Path, current, string(fragment.JSON), 1)
			out = append(out, req)
		}
	}

	root := strings.SplitN(base.Path, "?", 2)[0]
	// A method or route variant that reaches a real handler with a real id does
	// what it says: DELETE on a real team path deletes the team. Anything that
	// could change or consume an object is aimed at an id that does not exist.
	dummy := runner.dummyPath(op.Path, orgKey)
	for _, method := range []string{"HEAD", "OPTIONS", "TRACE", "PUT", "PATCH", "DELETE", "POST", "GET", "PROPFIND", "lower-case-get"} {
		if method == op.Method {
			continue
		}
		req := clone("method-"+strings.ToLower(method), "method", "method")
		req.Method = method
		if method == "lower-case-get" {
			req.Method = "get"
		}
		req.Path, req.Body = root, nil
		if isMutating(req.Method) {
			req.Path = dummy
		}
		out = append(out, req)
	}
	routeRoot := root
	if op.Consumes {
		routeRoot = dummy
	}
	for name, path := range map[string]string{
		"trailing-slash": routeRoot + "/", "double-slash": strings.Replace(routeRoot, "/v1/", "//v1//", 1),
		"dot-segment": strings.Replace(routeRoot, "/v1/", "/v1/./", 1), "encoded-v1": strings.Replace(routeRoot, "/v1/", "/%76%31/", 1),
		"upper-case": strings.ToUpper(routeRoot), "semicolon-param": routeRoot + ";x=1", "fragment": routeRoot + "%23frag",
	} {
		req := clone(name, "route", "path")
		req.Path = path
		out = append(out, req)
	}

	if authProbe {
		out = append(out, authVariants(base, runner.W, orgKey)...)
	}
	return out
}

// authVariants tries to be someone else, or no one, in every way a header allows.
// Each is marked Deny: a 2xx to any of them is a hole.
func authVariants(base Req, world *World, orgKey string) []Req {
	token := base.Token
	root := base
	root.Token = ""
	variant := func(name string, header http.Header, deny bool) Req {
		req := root
		req.Header = base.Header.Clone()
		if req.Header == nil {
			req.Header = http.Header{}
		}
		for key, values := range header {
			req.Header[key] = values
		}
		req.Mutation, req.Class, req.Target, req.Deny = name, "auth", "header", deny
		return req
	}
	origin := "http://localhost:3000"
	org := world.Org(orgKey)
	cookie := "oryxa_access=" + token
	mutating := base.Method != "GET" && base.Method != "HEAD"
	return []Req{
		variant("no-credentials", nil, true),
		variant("bearer-empty", http.Header{"Authorization": {"Bearer"}}, true),
		variant("bearer-blank", http.Header{"Authorization": {"Bearer "}}, true),
		variant("bearer-unknown-token", http.Header{"Authorization": {"Bearer not-a-real-token"}}, true),
		variant("basic-scheme", http.Header{"Authorization": {"Basic dG9rOg=="}}, true),
		variant("token-scheme", http.Header{"Authorization": {"Token " + token}}, true),
		variant("two-tokens-one-header", http.Header{"Authorization": {"Bearer " + token + " " + token}}, true),
		variant("token-with-nul", http.Header{"Authorization": {"Bearer " + token + "\x00"}}, true),
		variant("token-sql", http.Header{"Authorization": {"Bearer ' OR '1'='1"}}, true),
		variant("token-alg-none-jwt", http.Header{"Authorization": {"Bearer eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4Iiwicm9sZSI6Im93bmVyIn0."}}, true),
		variant("token-10k", http.Header{"Authorization": {"Bearer " + strings.Repeat("A", 10_000)}}, true),
		variant("token-200k", http.Header{"Authorization": {"Bearer " + strings.Repeat("A", 200_000)}}, true),
		variant("two-authorization-headers", http.Header{"Authorization": {"Bearer " + token, "Bearer not-a-real-token"}}, false),
		variant("ghost-bearer-owner-cookie", http.Header{"Authorization": {"Bearer " + world.Ghost.Token}, "Cookie": {cookie}}, true),
		variant("cookie-blank", http.Header{"Cookie": {"oryxa_access="}}, true),
		variant("cookie-unknown", http.Header{"Cookie": {"oryxa_access=not-a-real-token"}}, true),
		variant("cookie-nul", http.Header{"Cookie": {"oryxa_access=" + token + "\x00"}}, true),
		variant("cookie-no-origin", http.Header{"Cookie": {cookie}}, mutating),
		variant("cookie-foreign-origin", http.Header{"Cookie": {cookie}, "Origin": {"https://evil.example"}}, mutating),
		variant("cookie-null-origin", http.Header{"Cookie": {cookie}, "Origin": {"null"}}, mutating),
		variant("cookie-lookalike-origin", http.Header{"Cookie": {cookie}, "Origin": {"http://localhost:3000.evil.example"}}, mutating),
		variant("cookie-origin-userinfo", http.Header{"Cookie": {cookie}, "Origin": {"http://localhost:3000@evil.example"}}, mutating),
		variant("cookie-origin-trailing-dot", http.Header{"Cookie": {cookie}, "Origin": {"http://localhost.:3000"}}, mutating),
		variant("cookie-origin-other-port", http.Header{"Cookie": {cookie}, "Origin": {"http://localhost:3001"}}, mutating),
		variant("cookie-referer-only", http.Header{"Cookie": {cookie}, "Referer": {origin + "/"}}, mutating),
		variant("host-spoof", http.Header{"X-Forwarded-Host": {"evil.example"}, "X-Forwarded-For": {"127.0.0.1"}, "X-Real-Ip": {"127.0.0.1"}, "X-Original-Url": {"/healthz"}}, true),
		variant("other-org-id-header", http.Header{"X-Organization-Id": {org.ID}, "X-Oryxa-Org": {org.ID}, "X-User-Id": {org.Owner.UserID}}, true),
	}
}

func isMutating(method string) bool {
	switch strings.ToUpper(method) {
	case "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

// dummyPath is an operation's path with every object id replaced by one that
// does not exist, keeping the organization, so it reaches the handler and
// finds nothing to change.
func (runner *Runner) dummyPath(template, orgKey string) string {
	out := template
	for _, name := range placeholders(template) {
		value := "no_such_object"
		if name == "org" {
			value = runner.W.Org(orgKey).ID
		}
		out = strings.Replace(out, "{"+name+"}", value, 1)
	}
	return out
}

func placeholders(template string) []string {
	var names []string
	rest := template
	for {
		start := strings.Index(rest, "{")
		if start < 0 {
			return names
		}
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			return names
		}
		names = append(names, rest[start+1:start+end])
		rest = rest[start+end+1:]
	}
}

// FuzzOp mutates one operation and judges every answer. It reports how many
// requests it sent, and an error only if the server stopped answering.
func (runner *Runner) FuzzOp(ctx context.Context, op Op, orgKey string, authProbe bool) (int, error) {
	if runner.Stop != nil {
		if reason := runner.Stop(); reason != "" {
			return 0, fmt.Errorf("%s", reason)
		}
	}
	base, missing := runner.W.Baseline(op, orgKey)
	if len(missing) > 0 && !op.Shallow {
		return 0, nil
	}
	reqs := runner.mutations(op, base, orgKey, authProbe)
	jobs := make(chan Req)
	var wait sync.WaitGroup
	for range runner.Workers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for req := range jobs {
				runner.exec(ctx, req)
			}
		}()
	}
	for _, req := range reqs {
		select {
		case jobs <- req:
		case <-ctx.Done():
		}
	}
	close(jobs)
	wait.Wait()
	if !runner.Healthy(ctx) {
		// Give it a moment before calling it dead: a slow drain is not a crash.
		time.Sleep(3 * time.Second)
		if !runner.Healthy(ctx) {
			runner.Findings.Add(Finding{
				Kind: "server-down", Severity: Critical, Op: op.Name, Route: op.Route(), Class: "health",
				Detail: "healthz stopped answering after this operation's mutations",
			}, "")
			return len(reqs), fmt.Errorf("the server stopped answering during %s", op.Name)
		}
	}
	return len(reqs), nil
}

func (runner *Runner) exec(ctx context.Context, req Req) {
	res := runner.W.C.Do(ctx, req)
	runner.sent.Add(1)
	if req.Deny && res.Status >= 200 && res.Status < 300 {
		runner.Findings.Add(Finding{
			Kind: "authz-bypass", Severity: Critical, Op: req.Op, Route: req.Route, Class: req.Class, Status: res.Status,
			Detail:  fmt.Sprintf("%s was accepted and answered %d; it must be refused", req.Mutation, res.Status),
			Snippet: snippet(res), Curl: Curl(runner.W.C.base, req),
		}, req.Mutation)
	}
	for _, finding := range Judge(req, res, runner.SlowAfter) {
		if req.Op == "" {
			continue
		}
		finding.Curl = Curl(runner.W.C.base, req)
		runner.Findings.Add(finding, req.Mutation+"@"+req.Target)
	}
}
