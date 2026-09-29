package slack

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
)

// The stand-ins for everything the two services reach that is not AWS and not
// Slack: the model the owner's agent answers with, Jev, the knowledge the
// agent searches, and the organization directory. None is smart. Each answers
// what the loop asks of it, deterministically, and a test that wants it to
// answer otherwise says how.

// ---- Vertex AI: the model behind the owner's agent, and the relevance judge ----

// answerPlan is what the fake model does for one request it is woken to answer:
// it searches the knowledge for Query, and then answers with Summary and Content,
// as the two steps the interactions toolbox asks of it. A plan with NoSearch
// answers without reading anything, which the consent guard holds.
type answerPlan struct {
	Query    string
	Summary  string
	Content  string
	NoSearch bool
}

// passage is what the fake rag holds, and the words of it the default plan does
// not copy: a summary shares no run of 20 words with what it was read from.
const passage = "Q3 revenue was 4.2 million dollars, up 21 percent on Q2, which the finance team puts down to the enterprise tier. " +
	"The launch is scheduled for the 14th of October, once security review signs it off."

func defaultPlan(string) answerPlan {
	return answerPlan{
		Query:   "Q3 numbers and the launch date",
		Summary: "Revenue grew by about a fifth in Q3, and the launch is set for mid October.",
		Content: "Revenue grew by about a fifth in Q3 compared with Q2, thanks to the enterprise tier. The launch is planned for the middle of October, after security review.",
	}
}

type fakeVertex struct {
	t      testing.TB
	server *httptest.Server

	mu    sync.Mutex
	plan  func(requestID string) answerPlan
	calls []string
}

func newFakeVertex(t testing.TB) *fakeVertex {
	t.Helper()
	v := &fakeVertex{t: t, plan: defaultPlan}
	v.server = httptest.NewServer(http.HandlerFunc(v.serve))
	t.Cleanup(v.server.Close)
	return v
}

func (v *fakeVertex) URL() string { return v.server.URL }

// setPlan replaces what the model answers requests with, and returns what puts
// the default back.
func (v *fakeVertex) setPlan(plan func(requestID string) answerPlan) (restore func()) {
	v.mu.Lock()
	previous := v.plan
	v.plan = plan
	v.mu.Unlock()
	return func() {
		v.mu.Lock()
		v.plan = previous
		v.mu.Unlock()
	}
}

// requestID finds the request a run was woken for in the message that woke it.
var requestIDPattern = regexp.MustCompile(`\birq_[A-Za-z0-9_-]+`)

type vertexPart struct {
	Text         string `json:"text"`
	FunctionCall *struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args"`
	} `json:"functionCall"`
	FunctionResponse *struct {
		Name     string         `json:"name"`
		Response map[string]any `json:"response"`
	} `json:"functionResponse"`
}

type vertexRequest struct {
	Contents []struct {
		Role  string       `json:"role"`
		Parts []vertexPart `json:"parts"`
	} `json:"contents"`
	Tools []struct {
		FunctionDeclarations []struct {
			Name string `json:"name"`
		} `json:"functionDeclarations"`
	} `json:"tools"`
	GenerationConfig struct {
		ResponseMIMEType string `json:"responseMimeType"`
	} `json:"generationConfig"`
}

// summary is what a request says, for a person debugging: the conversation as
// roles and the shape of each part, and the tools it offers.
func (r vertexRequest) summary() string {
	var out strings.Builder
	for index, content := range r.Contents {
		fmt.Fprintf(&out, "  [%d %s]", index, content.Role)
		for _, part := range content.Parts {
			switch {
			case part.FunctionCall != nil:
				fmt.Fprintf(&out, " call(%s)", part.FunctionCall.Name)
			case part.FunctionResponse != nil:
				encoded, _ := json.Marshal(part.FunctionResponse.Response)
				fmt.Fprintf(&out, " response(%s: %s)", part.FunctionResponse.Name, tail(string(encoded), 500))
			default:
				fmt.Fprintf(&out, " text(%s)", tail(strings.ReplaceAll(part.Text, "\n", " "), 160))
			}
		}
		out.WriteString("\n")
	}
	var tools []string
	for _, group := range r.Tools {
		for _, declaration := range group.FunctionDeclarations {
			tools = append(tools, declaration.Name)
		}
	}
	fmt.Fprintf(&out, "  mime=%q tools=%v", r.GenerationConfig.ResponseMIMEType, tools)
	return out.String()
}

func (r vertexRequest) offers(tool string) bool {
	for _, group := range r.Tools {
		for _, declaration := range group.FunctionDeclarations {
			if declaration.Name == tool {
				return true
			}
		}
	}
	return false
}

// answered says whether a tool's result is in the conversation already.
func (r vertexRequest) answered(tool string) bool {
	for _, content := range r.Contents {
		for _, part := range content.Parts {
			if part.FunctionResponse != nil && part.FunctionResponse.Name == tool {
				return true
			}
		}
	}
	return false
}

// requestIDs are the requests the conversation names, in order.
func (r vertexRequest) requestID() string {
	found := ""
	for _, content := range r.Contents {
		for _, part := range content.Parts {
			if match := requestIDPattern.FindString(part.Text); match != "" {
				found = match
			}
		}
	}
	return found
}

func (v *fakeVertex) serve(writer http.ResponseWriter, request *http.Request) {
	body, _ := io.ReadAll(request.Body)
	switch {
	case strings.HasSuffix(request.URL.Path, "/token"):
		writeJSON(writer, map[string]any{"access_token": "fake-access-token", "token_type": "Bearer", "expires_in": 3600})
		return
	case !strings.Contains(request.URL.Path, ":generateContent") && !strings.Contains(request.URL.Path, ":streamGenerateContent"):
		v.t.Logf("fake Vertex: no such call %s %s", request.Method, request.URL.Path)
		http.NotFound(writer, request)
		return
	}
	var parsed vertexRequest
	if err := json.Unmarshal(body, &parsed); err != nil {
		v.t.Logf("fake Vertex: cannot read a request: %v\n%s", err, tail(string(body), 2000))
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	if os.Getenv("SLACK_E2E_DEBUG") != "" {
		v.t.Logf("fake Vertex: %s %s\n%s", request.Method, request.URL.Path, parsed.summary())
	}
	var parts []any
	switch {
	case parsed.GenerationConfig.ResponseMIMEType == "application/json" && len(parsed.Tools) == 0:
		// The relevance judge: the answer asks nothing more than its question did.
		parts = []any{map[string]any{"text": `{"asked":["question"],"extras":[],"certainty":0.9}`}}
		v.record("relevance judge")
	default:
		parts = v.runStep(parsed)
	}
	response := map[string]any{
		"candidates": []any{map[string]any{
			"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP",
		}},
		"usageMetadata": map[string]any{"promptTokenCount": 120, "candidatesTokenCount": 24, "totalTokenCount": 144},
	}
	encoded, _ := json.Marshal(response)
	if strings.Contains(request.URL.Path, ":streamGenerateContent") {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(writer, "data: %s\n\n", encoded)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	_, _ = writer.Write(encoded)
}

// runStep is what the owner's agent does next, given what it has been told and
// what its tools have said: search the knowledge, then answer, alone, once the
// search has returned, and then say it is done.
func (v *fakeVertex) runStep(request vertexRequest) []any {
	call := func(name string, args map[string]any) []any {
		v.record("call " + name)
		return []any{map[string]any{"functionCall": map[string]any{"name": name, "args": args}}}
	}
	requestID := request.requestID()
	v.mu.Lock()
	plan := v.plan(requestID)
	v.mu.Unlock()
	answer := func() []any {
		return call("interactions_respond", map[string]any{
			"request_id": requestID, "status": "completed", "summary": plan.Summary, "content": plan.Content,
			// The toolboxes the approval allowed, which are what an answer reports.
			"tools_used": []string{"knowledge"},
		})
	}
	switch {
	case request.answered("interactions_respond"):
		v.record("done")
		return []any{map[string]any{"text": "Done."}}
	case request.answered("knowledge_search") && request.offers("interactions_respond"):
		return answer()
	case request.offers("knowledge_search") && !plan.NoSearch:
		return call("knowledge_search", map[string]any{"query": plan.Query})
	case request.offers("interactions_respond"):
		return answer()
	}
	v.record("text")
	return []any{map[string]any{"text": "Understood."}}
}

func (v *fakeVertex) record(what string) {
	v.mu.Lock()
	v.calls = append(v.calls, what)
	v.mu.Unlock()
}

func (v *fakeVertex) recorded() []string {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]string(nil), v.calls...)
}

func writeJSON(writer http.ResponseWriter, value any) {
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(value)
}

// writeGoogleCredentials makes a service account key that only this test
// knows, a real RSA key so that Google's library parses it, whose token URI is
// the fake's own. It is the Application Default Credentials the agents find.
func writeGoogleCredentials(t testing.TB, tokenURL string) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(map[string]string{
		"type": "service_account", "project_id": "oryxa-e2e", "private_key_id": "fake-key-id",
		"private_key":  string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email": "fake@oryxa-e2e.iam.gserviceaccount.com", "client_id": "000000000000000000000",
		"auth_uri": tokenURL + "/o/oauth2/auth", "token_uri": tokenURL + "/token", "universe_domain": "googleapis.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "fake-service-account.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// ---- Jev: the screen of what people ask ----

// fakeJev answers TypeSafe's System One as the screen asks it: a yes-or-no
// probability for each question, by the ids the screen's own code names them
// (work, injection, plausible, credential, fit_N). A clean ask is work,
// plausible and fitting, and neither an injection nor a request for a
// credential; words that ask for "the staging API key" ask for a credential,
// and a "sing me a song" is not work.
type fakeJev struct {
	t      testing.TB
	server *httptest.Server

	mu      sync.Mutex
	states  []string
	answers func(state string) map[string]float64
}

func newFakeJev(t testing.TB) *fakeJev {
	t.Helper()
	j := &fakeJev{t: t, answers: cleanAsk}
	j.server = httptest.NewServer(http.HandlerFunc(j.serve))
	t.Cleanup(j.server.Close)
	return j
}

func (j *fakeJev) URL() string { return j.server.URL }

// cleanAsk is the probabilities of a plain work question: work 0.95,
// plausible 0.9, injection and credential 0.02, and every owner fitting at 0.9.
func cleanAsk(state string) map[string]float64 {
	answers := map[string]float64{"work": 0.95, "injection": 0.02, "plausible": 0.9, "credential": 0.02}
	lower := strings.ToLower(state)
	if strings.Contains(lower, "staging api key") {
		answers["credential"] = 0.95
	}
	if strings.Contains(lower, "sing me a song") {
		answers["work"] = 0.03
	}
	return answers
}

func (j *fakeJev) serve(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/systemone" || request.Header.Get("Authorization") != "Bearer test-typesafe-key" {
		http.Error(writer, "no", http.StatusUnauthorized)
		return
	}
	var in struct {
		State     json.RawMessage            `json:"state"`
		Questions map[string]json.RawMessage `json:"questions"`
	}
	if err := json.NewDecoder(request.Body).Decode(&in); err != nil {
		http.Error(writer, err.Error(), http.StatusBadRequest)
		return
	}
	j.mu.Lock()
	j.states = append(j.states, string(in.State))
	answers := j.answers(string(in.State))
	j.mu.Unlock()
	out := map[string]any{}
	for id := range in.Questions {
		probability, ok := answers[id]
		if !ok {
			probability = 0.9
			if !strings.HasPrefix(id, "fit_") {
				probability = 0.5
			}
		}
		out[id] = map[string]any{"type": "noul", "noul": probability}
	}
	writeJSON(writer, map[string]any{"model": "jev-e2e", "answers": out, "usage": map[string]any{"input_tokens": 100}})
}

// asked is what Jev was shown, in order, as the JSON of each state.
func (j *fakeJev) asked() []string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]string(nil), j.states...)
}

// ---- rag: the knowledge the owner's agent searches ----

// fakeRag holds one collection with one passage, and remembers who asked, since
// the owner's agent must read as the owner.
type fakeRag struct {
	server *httptest.Server

	mu      sync.Mutex
	bearers []string
	queries []string
}

func newFakeRag(t testing.TB) *fakeRag {
	t.Helper()
	r := &fakeRag{}
	r.server = httptest.NewServer(http.HandlerFunc(r.serve))
	t.Cleanup(r.server.Close)
	return r
}

func (r *fakeRag) URL() string { return r.server.URL }

func (r *fakeRag) serve(writer http.ResponseWriter, request *http.Request) {
	r.mu.Lock()
	r.bearers = append(r.bearers, request.Header.Get("Authorization"))
	r.mu.Unlock()
	switch {
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/corpora"):
		writeJSON(writer, map[string]any{"corpora": []any{map[string]any{
			"id": "corp_finance", "name": "Finance", "slug": "finance", "scope": "organization", "document_count": 3,
		}}})
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/search"):
		var in struct {
			Query string `json:"query"`
		}
		_ = json.NewDecoder(request.Body).Decode(&in)
		r.mu.Lock()
		r.queries = append(r.queries, in.Query)
		r.mu.Unlock()
		writeJSON(writer, map[string]any{"matches": []any{map[string]any{
			"chunk":    map[string]any{"text": passage, "heading_path": []string{"Plan", "Q3"}},
			"document": map[string]any{"id": "doc_plan", "title": "Q3 plan", "source": map[string]any{"kind": "file", "ref": "files:doc_plan"}},
			"score":    0.9,
		}}})
	default:
		http.NotFound(writer, request)
	}
}

// readers are the credentials each read of the knowledge was made with.
func (r *fakeRag) readers() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.bearers...)
}

// ---- Teams: the organization directory ----

// fakeTeams answers the directory as it stands in the test organization: the
// members, each on no team, so no department is known and every owner is asked.
type fakeTeams struct {
	server *httptest.Server
}

func newFakeTeams(t testing.TB, organizationID string, members []person) *fakeTeams {
	t.Helper()
	teams := &fakeTeams{}
	teams.server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1/organizations/"+organizationID+"/directory" {
			http.NotFound(writer, request)
			return
		}
		listed := make([]any, 0, len(members))
		for _, member := range members {
			listed = append(listed, map[string]any{
				"user_id": member.UserID, "email": member.Email, "display_name": member.Name, "org_role": member.Role,
				"teams": []any{}, "agents": []any{},
			})
		}
		writeJSON(writer, map[string]any{
			"organization": map[string]any{"id": organizationID, "name": "Acme"}, "teams": []any{}, "members": listed,
		})
	}))
	t.Cleanup(teams.server.Close)
	return teams
}

func (t *fakeTeams) URL() string { return t.server.URL }
