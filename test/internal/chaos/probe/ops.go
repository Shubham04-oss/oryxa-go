package probe

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// QP is one query parameter an operation understands, with a working value.
type QP struct{ Name, Value string }

// Op is one API operation with a request that works, which the mutation
// engine then breaks.
type Op struct {
	Name   string // "teams.create"
	Method string
	Path   string // "/v1/organizations/{org}/teams/{team}"
	Query  []QP
	Body   any // baseline body, a map; {name} inside a string is filled from the world
	// RawBody replaces Body for a request that is not JSON (multipart, form).
	RawBody     []byte
	ContentType string
	Actor       string // who sends the baseline: "a.owner"
	// Capture names variables to remember from the response: var -> dotted path.
	Capture map[string]string
	// Consumes marks an operation that uses up what it acts on, so it runs
	// last and its body and query are not fuzzed against a corpse.
	Consumes bool
	// Streams marks a long-lived response the fuzzer must not wait out.
	Streams bool
	// Slow marks an operation allowed to take seconds.
	Slow bool
	// External marks an operation whose success depends on a fake upstream.
	External string
	// Public needs no credentials.
	Public bool
	// Skip is a reason not to run the operation, shown in the report.
	Skip string
	// Shallow marks an operation with no hand-written baseline: it is sent with
	// a body of {} and ids that do not exist, and a 4xx is what it is expected
	// to answer, so it is not reported as a broken baseline.
	Shallow bool
}

// Route is the METHOD /template form used in reports.
func (op Op) Route() string { return op.Method + " " + op.Path }

// substitute walks a body template and fills {name} tokens in its strings.
func (world *World) substitute(orgKey string, node any) any {
	switch value := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			out[key] = world.substitute(orgKey, child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for index, child := range value {
			out[index] = world.substitute(orgKey, child)
		}
		return out
	case string:
		if strings.Contains(value, "{") {
			resolved, _ := world.Resolve(orgKey, value)
			return resolved
		}
		return value
	default:
		return node
	}
}

// Baseline builds the working request for an operation in an organization.
func (world *World) Baseline(op Op, orgKey string) (Req, []string) {
	path, missing := world.Resolve(orgKey, op.Path)
	if len(op.Query) > 0 {
		values := url.Values{}
		for _, param := range op.Query {
			resolved, _ := world.Resolve(orgKey, param.Value)
			values.Set(param.Name, resolved)
		}
		path += "?" + values.Encode()
	}
	req := Req{Method: op.Method, Path: path, Op: op.Name, Route: op.Route(), Mutation: "baseline", Class: "baseline"}
	actor := op.Actor
	if actor == "" {
		actor = orgKey + ".owner"
	} else if strings.HasPrefix(actor, "a.") || strings.HasPrefix(actor, "b.") {
		actor = orgKey + actor[1:]
	}
	if !op.Public {
		req.Token = world.Identity(actor).Token
	}
	switch {
	case op.RawBody != nil:
		req.Body = op.RawBody
		req.Header = http.Header{"Content-Type": {op.ContentType}}
	case op.Body != nil:
		encoded, err := json.Marshal(world.substitute(orgKey, op.Body))
		if err != nil {
			panic(fmt.Sprintf("op %s: body does not marshal: %v", op.Name, err))
		}
		req.Body = encoded
		req.Header = http.Header{"Content-Type": {"application/json"}}
	}
	return req, missing
}

// capture reads variables out of a JSON response.
func (world *World) capture(orgKey string, capture map[string]string, body []byte) {
	if len(capture) == 0 {
		return
	}
	var decoded any
	if json.Unmarshal(body, &decoded) != nil {
		return
	}
	for name, path := range capture {
		node := decoded
		for _, part := range strings.Split(path, ".") {
			object, ok := node.(map[string]any)
			if !ok {
				node = nil
				break
			}
			node = object[part]
		}
		if text, ok := node.(string); ok && text != "" {
			world.Set(orgKey, name, text)
		}
	}
}

// BaselineResult is how one operation did on its working request.
type BaselineResult struct {
	Op      Op
	Org     string
	Status  int
	Body    string
	Missing []string
	OK      bool
}

// RunBaselines sends every operation's working request once, in order, for
// both organizations, so each has real objects for the fuzzer and the
// cross-tenant checks to aim at. It is also a smoke test of every route.
//
// Operations that consume what they act on (a delete) run only when consuming
// is true, and belong at the very end: run first, they would leave everything
// after them aimed at objects that no longer exist.
func (world *World) RunBaselines(ctx context.Context, ops []Op, consuming bool) []BaselineResult {
	var results []BaselineResult
	for _, org := range []string{"a", "b"} {
		for _, op := range ops {
			if op.Skip != "" || op.Consumes != consuming {
				continue
			}
			req, missing := world.Baseline(op, org)
			result := BaselineResult{Op: op, Org: org, Missing: missing}
			if op.Shallow {
				missing = nil // stand-in ids are the point
			}
			if len(missing) > 0 {
				result.Body = "skipped: no value for " + strings.Join(missing, ", ")
				results = append(results, result)
				continue
			}
			if op.Streams {
				req.Timeout = 8 * time.Second
			}
			res := world.C.Do(ctx, req)
			result.Status, result.Body = res.Status, snippet(res)
			result.OK = res.Status >= 200 && res.Status < 300 || op.Shallow
			if result.OK {
				world.capture(org, op.Capture, res.Body)
			}
			results = append(results, result)
		}
	}
	return results
}
