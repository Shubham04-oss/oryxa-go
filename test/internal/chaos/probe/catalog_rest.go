package probe

import "strings"

// pathVars maps the placeholder names each service's routes use to the
// variable names the world remembers them under.
var pathVars = map[string]string{
	"organizationID": "org", "agentID": "agent", "workflowID": "workflow", "executionID": "execution",
	"teamspaceID": "teamspace", "taskID": "task", "requestID": "request", "threadID": "thread", "name": "artifact",
	"corpusID": "corpus", "documentID": "document", "sourceID": "source",
	"connectionID": "connection", "triggerID": "trigger", "toolkit": "toolkit", "tool": "tool",
}

// shallowRoutes are routes with no hand-written baseline. Each gets a request
// with a body of {} and object ids that do not exist, which is enough to test
// what does not depend on a valid body: authentication, tenant checks, the
// handling of hostile ids and query strings, body framing and size, and the
// methods and paths around it.
var shallowRoutes = map[string][]string{
	"agents": {
		"GET /v1/organizations/{organizationID}/agents", "GET /v1/organizations/{organizationID}/agents/{agentID}",
		"PATCH /v1/organizations/{organizationID}/agents/{agentID}",
		"GET /v1/organizations/{organizationID}/agent-teams", "POST /v1/organizations/{organizationID}/agent-teams",
		"GET /v1/organizations/{organizationID}/agent-teams/{agentteamID}", "PATCH /v1/organizations/{organizationID}/agent-teams/{agentteamID}",
		"GET /v1/organizations/{organizationID}/agent-workflows", "POST /v1/organizations/{organizationID}/agent-workflows",
		"GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}", "PATCH /v1/organizations/{organizationID}/agent-workflows/{workflowID}",
		"GET /v1/organizations/{organizationID}/agent-workflows/{workflowID}/executions",
		"GET /v1/organizations/{organizationID}/agent-workflow-executions/{executionID}",
		"GET /v1/organizations/{organizationID}/agent-requests",
		"GET /v1/organizations/{organizationID}/agent-requests/inbox", "GET /v1/organizations/{organizationID}/agent-requests/sent",
		"GET /v1/organizations/{organizationID}/agent-requests/{requestID}", "POST /v1/organizations/{organizationID}/agent-requests",
		"POST /v1/organizations/{organizationID}/agent-requests/{requestID}/decision", "POST /v1/organizations/{organizationID}/agent-requests/{requestID}/response",
		"GET /v1/organizations/{organizationID}/organization-agent", "PATCH /v1/organizations/{organizationID}/organization-agent",
		"GET /v1/organizations/{organizationID}/organization-agent/context", "GET /v1/organizations/{organizationID}/organization-agent/follow-ups",
		"GET /v1/organizations/{organizationID}/preferences", "PUT /v1/organizations/{organizationID}/preferences",
		"GET /v1/organizations/{organizationID}/teamspaces", "GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}",
		"PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}",
		"GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries", "POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/entries",
		"POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents", "PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}",
		"POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/read",
		"GET /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/tasks", "POST /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/tasks",
		"PATCH /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/tasks/{taskID}",
		"GET /v1/organizations/{organizationID}/threads", "GET /v1/organizations/{organizationID}/threads/{threadID}",
		"PATCH /v1/organizations/{organizationID}/threads/{threadID}",
		"PATCH /v1/organizations/{organizationID}/agents/{agentID}/threads/{threadID}",
		"GET /v1/organizations/{organizationID}/threads/{threadID}/artifacts/{name}",
		// consuming
		"DELETE /v1/organizations/{organizationID}/agents/{agentID}", "DELETE /v1/organizations/{organizationID}/agent-teams/{agentteamID}",
		"DELETE /v1/organizations/{organizationID}/agent-workflows/{workflowID}", "DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}",
		"DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/agents/{agentID}", "DELETE /v1/organizations/{organizationID}/threads/{threadID}",
		"DELETE /v1/organizations/{organizationID}/teamspaces/{teamspaceID}/tasks/{taskID}",
		// The edge's route: an edge→agents token, not a member's, so a shallow
		// call is refused 401, or 404 where the edge is not configured.
		"POST /v1/edge/teamspaces/{teamspaceID}/parley-notes",
	},
	"rag": {
		"GET /v1/capabilities",
		"GET /v1/organizations/{organizationID}/corpora", "GET /v1/organizations/{organizationID}/corpora/{corpusID}",
		"GET /v1/organizations/{organizationID}/corpora/{corpusID}/documents",
		"GET /v1/organizations/{organizationID}/corpora/{corpusID}/documents/{documentID}",
		"POST /v1/organizations/{organizationID}/corpora/{corpusID}/answer",
		"DELETE /v1/organizations/{organizationID}/corpora/{corpusID}/documents/{documentID}", "DELETE /v1/organizations/{organizationID}/corpora/{corpusID}",
	},
	"knowledge": {
		"GET /v1/sources/providers", "GET /v1/sources/kinds",
		"GET /v1/organizations/{organizationID}/sources", "POST /v1/organizations/{organizationID}/sources",
		"GET /v1/organizations/{organizationID}/sources/{sourceID}", "GET /v1/organizations/{organizationID}/sources/{sourceID}/runs",
		"POST /v1/organizations/{organizationID}/sources/{sourceID}/sync", "PUT /v1/organizations/{organizationID}/sources/{sourceID}/kinds",
		"GET /v1/organizations/{organizationID}/documents/{documentID}",
		"GET /v1/internal/organizations/{organizationID}/documents/{documentID}/body",
		"DELETE /v1/organizations/{organizationID}/sources/{sourceID}",
	},
	"apps": {
		"GET /v1/apps/catalog", "GET /v1/apps/catalog/{toolkit}", "GET /v1/apps/catalog/{toolkit}/tools", "GET /v1/apps/catalog/{toolkit}/triggers",
		"GET /v1/apps/connect/callback",
		"GET /v1/organizations/{organizationID}/apps/connections", "POST /v1/organizations/{organizationID}/apps/connections",
		"GET /v1/organizations/{organizationID}/apps/connections/{connectionID}", "GET /v1/organizations/{organizationID}/apps/connections/{connectionID}/targets",
		"GET /v1/organizations/{organizationID}/apps/triggers", "POST /v1/organizations/{organizationID}/apps/triggers",
		"POST /v1/organizations/{organizationID}/apps/read", "POST /v1/organizations/{organizationID}/apps/tools/{tool}/run",
		"DELETE /v1/organizations/{organizationID}/apps/triggers/{triggerID}", "DELETE /v1/organizations/{organizationID}/apps/connections/{connectionID}",
	},
	"billing": {
		"GET /v1/billing/plans", "GET /v1/billing/organizations/{organizationID}",
		"POST /v1/billing/organizations/{organizationID}/checkout", "POST /v1/billing/organizations/{organizationID}/portal",
	},
	// The Worker's route: signed with REALTIME_PURGE_SECRET, not a member's
	// token, so a shallow call is refused 401 before its body is read.
	"realtime": {"POST /v1/realtime/events"},
}

// publicRoutes need no credentials.
var publicRoutes = map[string]bool{
	"GET /v1/capabilities": true, "GET /v1/sources/providers": true, "GET /v1/sources/kinds": true,
	"GET /v1/apps/catalog": true, "GET /v1/apps/catalog/{toolkit}": true, "GET /v1/apps/catalog/{toolkit}/tools": true,
	"GET /v1/apps/catalog/{toolkit}/triggers": true, "GET /v1/billing/plans": true,
	"POST /v1/realtime/events": true,
}

// shallowOp turns a route pattern into a shallow operation.
func shallowOp(service, pattern string) Op {
	method, path, _ := strings.Cut(pattern, " ")
	for placeholder, variable := range pathVars {
		path = strings.ReplaceAll(path, "{"+placeholder+"}", "{"+variable+"}")
	}
	path = strings.ReplaceAll(path, "{agentteamID}", "{agentteam}")
	tail := strings.TrimPrefix(path, "/v1/organizations/{org}")
	op := Op{
		Name: service + "." + strings.ToLower(method) + " " + tail, Method: method, Path: path,
		Consumes: method == "DELETE", Public: publicRoutes[pattern], Shallow: true,
	}
	if method == "POST" || method == "PUT" || method == "PATCH" {
		op.Body = M{}
	}
	return op
}

func shallow(service string) []Op {
	var ops []Op
	for _, pattern := range shallowRoutes[service] {
		ops = append(ops, shallowOp(service, pattern))
	}
	return ops
}

// withCreates puts hand-written operations that make objects ahead of the
// shallow ones, and drops any shallow operation a hand-written one covers.
func withCreates(service string, creates []Op) []Op {
	covered := map[string]bool{}
	for _, op := range creates {
		covered[op.Route()] = true
	}
	ops := append([]Op{}, creates...)
	for _, op := range shallow(service) {
		if !covered[op.Route()] {
			ops = append(ops, op)
		}
	}
	return ops
}

// AgentsOps is the agents service, apart from the streaming run routes.
func AgentsOps() []Op {
	base := orgPath
	return withCreates("agents", []Op{
		{Name: "agents.create", Method: "POST", Path: base + "/agents", Capture: map[string]string{"agent": "id"},
			Body: M{"name": "Researcher", "description": "Reads things", "instructions": "Be brief."}},
		{Name: "agents.update", Method: "PATCH", Path: base + "/agents/{agent}", Body: M{"description": "Reads more things"}},
		{Name: "teamspaces.create", Method: "POST", Path: base + "/teamspaces", Capture: map[string]string{"teamspace": "id"},
			Body: M{"team_id": "{team}", "name": "Launch room", "description": "Shared space"}},
		{Name: "threads.create", Method: "POST", Path: base + "/threads", Actor: "a.member", Capture: map[string]string{"thread": "id"}},
	})
}

// RAGOps is rag: corpora, documents, retrieval.
func RAGOps() []Op {
	base := orgPath + "/corpora"
	return withCreates("rag", []Op{
		{Name: "rag.corpora.create", Method: "POST", Path: base, Capture: map[string]string{"corpus": "id"},
			Body: M{"slug": "handbook", "name": "Handbook", "scope": "organization"}},
		{Name: "rag.documents.create", Method: "POST", Path: base + "/{corpus}/documents", Capture: map[string]string{"document": "id"},
			Body: M{"source": M{"kind": "inline"}, "title": "Onboarding", "content_type": "text/plain",
				"text": "New hires get a laptop on day one. Expenses are reimbursed within ten days.", "metadata": M{"team": "people"}}},
		{Name: "rag.search", Method: "POST", Path: base + "/{corpus}/search", Body: M{"query": "when are expenses reimbursed", "top_k": 3, "mode": "hybrid"}},
	})
}

// KnowledgeOps, AppsOps, BillingOps and RealtimeOps are shallow until their
// objects are worth making.
func KnowledgeOps() []Op { return withCreates("knowledge", nil) }
func AppsOps() []Op      { return withCreates("apps", nil) }
func BillingOps() []Op   { return withCreates("billing", nil) }
func RealtimeOps() []Op  { return withCreates("realtime", nil) }
