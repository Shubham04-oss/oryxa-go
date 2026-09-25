package httpserver

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"net/http"
	"slices"
	"testing"
	"time"

	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/session"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/events"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/pricing"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/runstore"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// TestAThreadIsCreatedWithTheOriginItNames: a new thread is a chat unless
// the body names another origin; the origin reaches the service as sent and
// comes back in the thread, and one it does not know is the caller's
// mistake.
func TestAThreadIsCreatedWithTheOriginItNames(t *testing.T) {
	for _, testCase := range []struct {
		body, sent, origin string
	}{
		{``, "", threads.OriginChat},
		{`{}`, "", threads.OriginChat},
		{`{"origin":""}`, "", threads.OriginChat},
		{`{"origin":"chat"}`, "chat", threads.OriginChat},
		{`{"origin":"side_conversation"}`, "side_conversation", threads.OriginSideConversation},
	} {
		service := &fakeThreads{}
		created := serve(newTestServer(member(), service), http.MethodPost, "/v1/organizations/org_1/threads", testCase.body, bearer)
		var thread threads.Thread
		if err := json.Unmarshal(created.Body.Bytes(), &thread); err != nil || created.Code != http.StatusCreated || thread.Origin != testCase.origin {
			t.Fatalf("POST %q = %d %s, want 201 with origin %q", testCase.body, created.Code, created.Body.String(), testCase.origin)
		}
		if len(service.createOrigins) != 1 || service.createOrigins[0] != testCase.sent {
			t.Fatalf("POST %q passed %v, want %q", testCase.body, service.createOrigins, testCase.sent)
		}
	}
	refused := serve(newTestServer(member(), &fakeThreads{}), http.MethodPost, "/v1/organizations/org_1/threads", `{"origin":"bogus"}`, bearer)
	if refused.Code != http.StatusBadRequest || errorCode(t, refused) != "invalid_request" {
		t.Fatalf("an unknown origin = %d %s, want 400 invalid_request", refused.Code, refused.Body.String())
	}
}

// TestTheThreadListIsTheMembersChatsUnlessAskedForMore: the listing is chat
// threads by default, and ?origin= picks another origin or all of them; one
// sent blank is refused before the service is asked, and one it does not
// know is refused by it.
func TestTheThreadListIsTheMembersChatsUnlessAskedForMore(t *testing.T) {
	for query, origin := range map[string]string{
		"":                          threads.OriginChat,
		"?origin=chat":              threads.OriginChat,
		"?origin=side_conversation": threads.OriginSideConversation,
		"?origin=all":               threads.OriginAll,
		"?origin=%20all%20":         threads.OriginAll,
	} {
		service := &fakeThreads{}
		listed := serve(newTestServer(member(), service), http.MethodGet, "/v1/organizations/org_1/threads"+query, "", bearer)
		if listed.Code != http.StatusOK || len(service.listOrigins) != 1 || service.listOrigins[0] != origin {
			t.Fatalf("GET threads%s = %d %s asking %v, want 200 asking for %q", query, listed.Code, listed.Body.String(), service.listOrigins, origin)
		}
	}
	for _, query := range []string{"?origin=", "?origin=%20", "?origin", "?origin=bogus"} {
		service := &fakeThreads{}
		listed := serve(newTestServer(member(), service), http.MethodGet, "/v1/organizations/org_1/threads"+query, "", bearer)
		if listed.Code != http.StatusBadRequest || errorCode(t, listed) != "invalid_request" {
			t.Fatalf("GET threads%s = %d %s, want 400 invalid_request", query, listed.Code, listed.Body.String())
		}
		if query != "?origin=bogus" && len(service.listOrigins) != 0 {
			t.Fatalf("GET threads%s reached the service with %v", query, service.listOrigins)
		}
	}
}

// silentModel is a model no test here runs.
type silentModel struct{}

func (silentModel) Name() string { return "silent" }

func (silentModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) { yield(nil, errors.New("not run in this test")) }
}

// idleRuns is a run store no test here starts a run on.
type idleRuns struct{}

func (idleRuns) Start(context.Context, runstore.Run, time.Duration) error { return nil }
func (idleRuns) Renew(context.Context, string, time.Duration) error       { return nil }
func (idleRuns) Finish(context.Context, string, runstore.Outcome, events.Event) error {
	return nil
}
func (idleRuns) Cancel(context.Context, string) error { return nil }

// TestASideConversationsThreadStaysOutOfTheMembersChats runs the real run
// API behind the routes. A side conversation opens a thread to run the
// member's own agent on an internal prompt; that thread must not become the
// newest entry of their chat list, which the web app opens, while it stays
// reachable by id and by asking for its origin.
func TestASideConversationsThreadStaysOutOfTheMembersChats(t *testing.T) {
	krude, err := llmagent.New(llmagent.Config{Name: "krude", Model: silentModel{}, Instruction: "You are Krude."})
	if err != nil {
		t.Fatal(err)
	}
	prices, err := pricing.NewTable(nil)
	if err != nil {
		t.Fatal(err)
	}
	service, err := threads.New(threads.Config{AppName: "krude", Agent: krude, Sessions: session.InMemoryService(), Runs: idleRuns{}, Prices: prices})
	if err != nil {
		t.Fatal(err)
	}
	server := newTestServerWith(member(), ForThreads(service))
	create := func(body string) threads.Thread {
		t.Helper()
		created := serve(server, http.MethodPost, "/v1/organizations/org_1/threads", body, bearer)
		var thread threads.Thread
		if err := json.Unmarshal(created.Body.Bytes(), &thread); err != nil || created.Code != http.StatusCreated {
			t.Fatalf("POST %s = %d %s", body, created.Code, created.Body.String())
		}
		return thread
	}
	list := func(query string) []string {
		t.Helper()
		listed := serve(server, http.MethodGet, "/v1/organizations/org_1/threads"+query, "", bearer)
		var body struct {
			Threads []threads.Thread `json:"threads"`
		}
		if err := json.Unmarshal(listed.Body.Bytes(), &body); err != nil || listed.Code != http.StatusOK {
			t.Fatalf("GET threads%s = %d %s", query, listed.Code, listed.Body.String())
		}
		ids := make([]string, 0, len(body.Threads))
		for _, thread := range body.Threads {
			ids = append(ids, thread.ID+":"+thread.Origin)
		}
		slices.Sort(ids)
		return ids
	}

	chat := create(`{}`)
	side := create(`{"origin":"side_conversation"}`)
	if chat.Origin != threads.OriginChat || side.Origin != threads.OriginSideConversation {
		t.Fatalf("created %+v and %+v", chat, side)
	}
	wantChat, wantSide := []string{chat.ID + ":chat"}, []string{side.ID + ":side_conversation"}
	wantAll := append(slices.Clone(wantChat), wantSide...)
	slices.Sort(wantAll)
	if got := list(""); !slices.Equal(got, wantChat) {
		t.Fatalf("the member's chat list = %v, want only %v", got, wantChat)
	}
	if got := list("?origin=side_conversation"); !slices.Equal(got, wantSide) {
		t.Fatalf("the side conversations = %v, want %v", got, wantSide)
	}
	if got := list("?origin=all"); !slices.Equal(got, wantAll) {
		t.Fatalf("every thread = %v, want %v", got, wantAll)
	}
	read := serve(server, http.MethodGet, "/v1/organizations/org_1/threads/"+side.ID, "", bearer)
	var detail threads.Detail
	if err := json.Unmarshal(read.Body.Bytes(), &detail); err != nil || read.Code != http.StatusOK || detail.ID != side.ID || detail.Origin != threads.OriginSideConversation {
		t.Fatalf("GET the side thread by id = %d %s", read.Code, read.Body.String())
	}
}

// newTestServerWith is newTestServer over any run API.
func newTestServerWith(directory *fakeDirectory, service Threads) *Server {
	managed := &fakeManagedAgents{definitions: []managedagents.Definition{{ID: "agt_primary", Kind: managedagents.KindPrimary, Name: "Carl", Status: managedagents.StatusActive}}, run: &fakeRun{}}
	return New(directory, service, Config{AllowedOrigins: []string{"https://app.oryxa.test"}, RunTimeout: time.Minute, Agents: managed}, nil)
}
