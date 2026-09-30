package surfaces

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/appsclient"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/threads"
)

// The Slack ids of the people in these tests, in the shapes apps takes.
const (
	slackAsker = "UASKER01"
	slackRaya  = "URAYA001"
	slackMaya  = "UMAYA001"
	channelID  = "C0LAUNCH1"
	threadTS   = "1700000000.000100"
)

// testNow is the moment every test's clock says.
var testNow = time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

// ---- The ledger ----

// fakeLedger is the ledger as far as the service can tell: it holds requests and
// asks, and makes tickets the way the real one does, for a request that is still
// pending.
type fakeLedger struct {
	mu          sync.Mutex
	requests    map[string]agents.Interaction
	asks        map[string]interactions.Ask
	now         func() time.Time
	ticketCalls []string
	getFailures int
	ticketErr   error
}

func newFakeLedger(now func() time.Time) *fakeLedger {
	return &fakeLedger{requests: map[string]agents.Interaction{}, asks: map[string]interactions.Ask{}, now: now}
}

func (l *fakeLedger) Get(_ context.Context, id string) (agents.Interaction, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.getFailures > 0 {
		l.getFailures--
		return agents.Interaction{}, errors.New("the ledger is unavailable")
	}
	request, ok := l.requests[id]
	if !ok {
		return agents.Interaction{}, interactions.ErrNotFound
	}
	return request, nil
}

func (l *fakeLedger) Ask(_ context.Context, id string) (interactions.Ask, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	ask, ok := l.asks[id]
	if !ok {
		return interactions.Ask{}, interactions.ErrNotFound
	}
	return ask, nil
}

// ConsentTickets is the real one's contract: only a request that is pending, and
// not past its expiry, has buttons, and the tickets say what they are for.
func (l *fakeLedger) ConsentTickets(_ context.Context, requestID, notice string) (interactions.ConsentTickets, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ticketCalls = append(l.ticketCalls, requestID+"/"+notice)
	if l.ticketErr != nil {
		return interactions.ConsentTickets{}, l.ticketErr
	}
	request, ok := l.requests[requestID]
	switch {
	case !ok:
		return interactions.ConsentTickets{}, interactions.ErrNotFound
	case request.Status != interactions.StatusPending:
		return interactions.ConsentTickets{}, interactions.ErrConflict
	case !request.ExpiresAt.After(l.now()):
		return interactions.ConsentTickets{}, interactions.ErrExpired
	}
	ticket := func(choice string) string { return "t1." + choice + "." + requestID + "." + notice }
	return interactions.ConsentTickets{
		Summary: ticket("summary"), Access: ticket("access"), Deny: ticket("deny"), ExpiresAt: request.ExpiresAt,
	}, nil
}

func (l *fakeLedger) put(request agents.Interaction) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.requests[request.ID] = request
}

func (l *fakeLedger) putAsk(ask interactions.Ask) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.asks[ask.ID] = ask
}

// change edits a request as a ledger transition would.
func (l *fakeLedger) change(id string, edit func(*agents.Interaction)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	request := l.requests[id]
	edit(&request)
	request.Revision++
	l.requests[id] = request
}

func (l *fakeLedger) tickets() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.ticketCalls)
}

// baseAsk is one Slack message that asked one owner, in a channel's thread.
func baseAsk() interactions.Ask {
	return interactions.Ask{
		ID: "iak_1", OrganizationID: "org_1", Origin: interactions.OriginSlack, TeamID: "T0ACME001",
		ChannelID: channelID, ThreadTS: threadTS, TriggerTS: threadTS, AskerUserID: "user_asker", Kind: interactions.AskQuestion,
		CreatedAt: testNow,
	}
}

// pendingRequest is the request that ask made to Raya, waiting for her.
func pendingRequest() agents.Interaction {
	return agents.Interaction{
		ID: "irq_1", OrganizationID: "org_1", Approval: interactions.ApprovalOwner, AskID: "iak_1", Origin: interactions.OriginSlack,
		Requester: agents.InteractionParty{UserID: "user_asker", AgentID: "agt_asker", AgentName: "Krude"},
		Target:    agents.InteractionParty{UserID: "user_raya", AgentID: "agt_raya", AgentName: "Krude", UserDisplayName: "Raya"},
		Purpose:   "Named in a message a member sent in Slack.", Task: "What is the launch date for Project Falcon?",
		Context:        []agents.InteractionContextItem{},
		RequestedTools: []string{agents.KnowledgeToolboxName}, ToolsJudgedBy: interactions.ToolsJudgedByRequester,
		RequestedPermissions: []string{interactions.PermissionConsentSummary, interactions.PermissionConsentAccess},
		Status:               interactions.StatusPending, Revision: 1,
		ExpiresAt: testNow.Add(24 * time.Hour), CreatedAt: testNow.Add(-time.Minute), UpdatedAt: testNow.Add(-time.Minute),
	}
}

// decidedBy is a request approved or rejected by the given decider.
func decidedBy(request agents.Interaction, decision string, decider agents.InteractionDecider, consent string) agents.Interaction {
	granted := []string{}
	switch consent {
	case interactions.ConsentSummary:
		granted = []string{interactions.PermissionConsentSummary}
	case interactions.ConsentAccess:
		granted = []string{interactions.PermissionConsentSummary, interactions.PermissionConsentAccess}
	}
	request.Status = interactions.StatusApproved
	if decision == interactions.DecisionRejected {
		request.Status = interactions.StatusRejected
	}
	request.Decision = &agents.InteractionDecision{
		Decision: decision, GrantedPermissions: granted, AllowedTools: []string{agents.KnowledgeToolboxName},
		DecidedAt: testNow.Add(-30 * time.Second), DecidedBy: &decider, Consent: consent,
	}
	request.Revision++
	return request
}

func memberDecided(request agents.Interaction, decision, consent, surface string) agents.Interaction {
	return decidedBy(request, decision, agents.InteractionDecider{Kind: interactions.DecidedByMember, UserID: request.Target.UserID, Surface: surface}, consent)
}

func bypassed(request agents.Interaction, reason string) agents.Interaction {
	return decidedBy(request, interactions.DecisionApproved,
		agents.InteractionDecider{Kind: interactions.DecidedByBypass, Surface: interactions.SurfaceBypass, Reason: reason}, interactions.ConsentAccess)
}

func leased(request agents.Interaction) agents.Interaction {
	return decidedBy(request, interactions.DecisionApproved,
		agents.InteractionDecider{Kind: interactions.DecidedByLease, UserID: request.Target.UserID, Surface: interactions.SurfaceLease, Reason: "ils_1"}, interactions.ConsentAccess)
}

func completedWith(request agents.Interaction, content string) agents.Interaction {
	request.Status = interactions.StatusCompleted
	request.Result = &agents.InteractionResult{
		Status: interactions.StatusCompleted, Summary: "Answered.", Content: content,
		ToolsUsed: []string{}, Artifacts: []string{}, CompletedAt: testNow.Add(-10 * time.Second),
	}
	request.Revision++
	return request
}

func heldWith(request agents.Interaction, content string) agents.Interaction {
	request.Status = interactions.StatusReview
	request.Result = nil
	request.Held = &agents.InteractionHeld{Result: agents.InteractionResult{Status: interactions.StatusCompleted, Content: content, Summary: "held summary"}}
	request.Guard = &agents.InteractionGuard{State: "held", Kinds: []string{"EMAIL"}}
	request.Revision++
	return request
}

// ---- Slack users, notes, links ----

type fakeUsers struct {
	mu    sync.Mutex
	users map[string]slackrouter.Identity
	err   error
}

func newFakeUsers() *fakeUsers {
	return &fakeUsers{users: map[string]slackrouter.Identity{
		"user_asker": {OrganizationID: "org_1", TeamID: "T0ACME001", SlackUserID: slackAsker, MemberUserID: "user_asker", DisplayName: "Ada"},
		"user_raya":  {OrganizationID: "org_1", TeamID: "T0ACME001", SlackUserID: slackRaya, MemberUserID: "user_raya", DisplayName: "Raya S."},
		"user_maya":  {OrganizationID: "org_1", TeamID: "T0ACME001", SlackUserID: slackMaya, MemberUserID: "user_maya", DisplayName: "Maya"},
	}}
}

func (u *fakeUsers) SlackUserFor(_ context.Context, _, memberUserID string) (slackrouter.Identity, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.err != nil {
		return slackrouter.Identity{}, u.err
	}
	identity, ok := u.users[memberUserID]
	if !ok {
		return slackrouter.Identity{}, slackrouter.ErrNoIdentity
	}
	return identity, nil
}

func (u *fakeUsers) forget(memberUserID string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.users, memberUserID)
}

type writtenNote struct {
	Member threads.Member
	Note   threads.Note
}

// fakeNotes records the notes written, once for each request and state as the
// real ones are, and can be made to fail.
type fakeNotes struct {
	mu       sync.Mutex
	written  []writtenNote
	calls    int
	failures int
	failWith error
}

func (n *fakeNotes) Note(_ context.Context, member threads.Member, note threads.Note) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls++
	if n.failures > 0 {
		n.failures--
		if n.failWith != nil {
			return n.failWith
		}
		return errors.New("the thread service is unavailable")
	}
	for _, existing := range n.written {
		if existing.Note.RequestID == note.RequestID && existing.Note.State == note.State {
			return nil
		}
	}
	n.written = append(n.written, writtenNote{Member: member, Note: note})
	return nil
}

func (n *fakeNotes) all() []writtenNote {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Clone(n.written)
}

func (n *fakeNotes) texts() []string {
	var texts []string
	for _, written := range n.all() {
		texts = append(texts, written.Note.State+": "+written.Note.Text)
	}
	return texts
}

// memLinks is the store's links, in memory, that can be made to fail.
type memLinks struct {
	mu          sync.Mutex
	links       map[string]Link
	putFailures int
}

func newMemLinks() *memLinks { return &memLinks{links: map[string]Link{}} }

func (m *memLinks) Link(_ context.Context, requestID, role string) (Link, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[requestID+"/"+role]
	return link, ok, nil
}

func (m *memLinks) PutLink(_ context.Context, link Link) error {
	if err := link.validate(); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putFailures > 0 {
		m.putFailures--
		return errors.New("the database is unavailable")
	}
	m.links[link.RequestID+"/"+link.Role] = link
	return nil
}

func (m *memLinks) SetLinkState(_ context.Context, requestID, role, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[requestID+"/"+role]
	if !ok {
		return errors.New("no such link")
	}
	link.State, link.ExpiresAt = state, nil
	m.links[requestID+"/"+role] = link
	return nil
}

func (m *memLinks) get(requestID, role string) (Link, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[requestID+"/"+role]
	return link, ok
}

// ---- Apps' internal API ----

// message is one Slack message apps was asked to make, as it was asked.
type message struct {
	Key       string
	Template  string
	Target    slackrouter.Target
	Params    map[string]any
	ChannelID string
	TS        string
	Ephemeral bool
	Body      string
	Updates   []update
}

type update struct {
	Template string
	Params   map[string]any
}

// fakeAPI is apps' internal Slack API as far as the client can tell: it makes a
// message once for an idempotency key, refuses what apps refuses, and can be
// told to rate limit or to refuse. It refuses to be asked for ask_ack, which is
// the router's alone.
type fakeAPI struct {
	t      *testing.T
	server *httptest.Server
	mu     sync.Mutex
	// messages are the messages made, in order, and requests every post the
	// client asked for, replays and refusals included.
	messages []*message
	byKey    map[string]*message
	requests int
	// updates counts every update the client asked for.
	updates int
	// channels are what the conversations read as, by id.
	channels map[string]channelBehaviour
	// forbidden are strings that no request may carry: what must never reach Slack.
	forbidden []string
	// respond, when set, may answer a post before apps would: returning true has
	// answered it.
	respond func(n int, body map[string]any, w http.ResponseWriter) bool
	// updateRespond is the same for updates.
	updateRespond func(n int, w http.ResponseWriter) bool
	tsCounter     int
}

type channelBehaviour struct {
	view   map[string]any
	status int
	code   string
}

func newFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	api := &fakeAPI{t: t, byKey: map[string]*message{}, channels: map[string]channelBehaviour{
		channelID: {view: map[string]any{"channel_id": channelID, "name": "launch", "is_private": true, "is_shared": false, "high_priority": true}},
	}}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) refusal(w http.ResponseWriter, status int, code, text string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": text}})
}

func (a *fakeAPI) ok(w http.ResponseWriter, body any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(body)
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	for _, forbidden := range a.forbidden {
		if strings.Contains(string(body), forbidden) {
			a.t.Errorf("a request to apps carried %q, which must never reach Slack: %s", forbidden, body)
		}
	}
	if r.Header.Get("X-Oryxa-Service-Token") == "" {
		a.refusal(w, http.StatusUnauthorized, "unauthorized", "a service token is required")
		return
	}
	switch {
	case r.URL.Path == "/v1/internal/apps/slack/post":
		a.post(w, body)
	case r.URL.Path == "/v1/internal/apps/slack/update":
		a.update(w, body)
	case strings.HasPrefix(r.URL.Path, "/v1/internal/apps/slack/channels/"):
		a.channel(w, strings.TrimPrefix(r.URL.Path, "/v1/internal/apps/slack/channels/"))
	default:
		a.refusal(w, http.StatusNotFound, "not_found", "resource not found")
	}
}

func (a *fakeAPI) channel(w http.ResponseWriter, id string) {
	a.mu.Lock()
	behaviour, ok := a.channels[id]
	a.mu.Unlock()
	switch {
	case !ok:
		a.refusal(w, http.StatusNotFound, "not_found", "resource not found")
	case behaviour.status != 0:
		a.refusal(w, behaviour.status, behaviour.code, "refused")
	default:
		a.ok(w, behaviour.view)
	}
}

func (a *fakeAPI) post(w http.ResponseWriter, raw []byte) {
	var request struct {
		OrganizationID string             `json:"organization_id"`
		IdempotencyKey string             `json:"idempotency_key"`
		Target         slackrouter.Target `json:"target"`
		Template       string             `json:"template"`
		Params         map[string]any     `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		a.refusal(w, http.StatusBadRequest, "invalid_request", "request body must be one JSON object with known fields")
		return
	}
	if request.Template == "ask_ack" {
		a.t.Errorf("the surfaces service posted an ask_ack, which is the router's alone")
	}
	if len(request.IdempotencyKey) < 8 || len(request.IdempotencyKey) > 128 {
		a.refusal(w, http.StatusBadRequest, "invalid_request", "an idempotency key is 8 to 128 characters")
		return
	}
	a.mu.Lock()
	a.requests++
	n := a.requests
	respond := a.respond
	a.mu.Unlock()
	var params map[string]any = request.Params
	if respond != nil && respond(n, params, w) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if first, ok := a.byKey[request.IdempotencyKey]; ok {
		if first.Template != request.Template || first.Target != request.Target || fmt.Sprint(first.Params) != fmt.Sprint(request.Params) {
			a.refusal(w, http.StatusConflict, "conflict", "that idempotency key was used for a different message")
			return
		}
		a.ok(w, map[string]any{"channel_id": first.ChannelID, "ts": first.TS, "ephemeral": first.Ephemeral, "replayed": true})
		return
	}
	a.tsCounter++
	created := &message{
		Key: request.IdempotencyKey, Template: request.Template, Target: request.Target, Params: request.Params,
		ChannelID: request.Target.ChannelID, TS: fmt.Sprintf("1700001000.%06d", a.tsCounter), Body: string(raw),
	}
	switch {
	case request.Target.ChannelID == "" && request.Target.UserID != "":
		created.ChannelID = "D" + strings.TrimPrefix(request.Target.UserID, "U")
	case request.Target.UserID != "":
		created.Ephemeral = true
	}
	a.messages = append(a.messages, created)
	a.byKey[request.IdempotencyKey] = created
	a.ok(w, map[string]any{"channel_id": created.ChannelID, "ts": created.TS, "ephemeral": created.Ephemeral})
}

func (a *fakeAPI) update(w http.ResponseWriter, raw []byte) {
	var request struct {
		OrganizationID string         `json:"organization_id"`
		ChannelID      string         `json:"channel_id"`
		TS             string         `json:"ts"`
		Template       string         `json:"template"`
		Params         map[string]any `json:"params"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		a.refusal(w, http.StatusBadRequest, "invalid_request", "request body must be one JSON object with known fields")
		return
	}
	a.mu.Lock()
	a.updates++
	n := a.updates
	respond := a.updateRespond
	a.mu.Unlock()
	if respond != nil && respond(n, w) {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, existing := range a.messages {
		if existing.ChannelID == request.ChannelID && existing.TS == request.TS && !existing.Ephemeral {
			existing.Updates = append(existing.Updates, update{Template: request.Template, Params: request.Params})
			a.ok(w, map[string]any{"channel_id": request.ChannelID, "ts": request.TS})
			return
		}
	}
	a.refusal(w, http.StatusNotFound, "not_found", "resource not found")
}

// sent is the messages made, in order.
func (a *fakeAPI) sent() []*message {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.messages)
}

// sentWith is the messages made from a template.
func (a *fakeAPI) sentWith(template string) []*message {
	var found []*message
	for _, made := range a.sent() {
		if made.Template == template {
			found = append(found, made)
		}
	}
	return found
}

func (a *fakeAPI) postRequests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.requests
}

func (a *fakeAPI) updateRequests() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.updates
}

func (a *fakeAPI) forbid(values ...string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.forbidden = append(a.forbidden, values...)
}

func (a *fakeAPI) setChannel(id string, behaviour channelBehaviour) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.channels[id] = behaviour
}

// ---- The service, built as the process would build it ----

type fakeSigner struct{}

func (fakeSigner) Token(context.Context, string, string) (string, error) { return "test-token", nil }

type harness struct {
	t      *testing.T
	ledger *fakeLedger
	api    *fakeAPI
	users  *fakeUsers
	notes  *fakeNotes
	links  *memLinks
	client *appsclient.Client
	// waits are the rate-limit waits the client was asked to sit out.
	waits   []time.Duration
	service *Service
	now     time.Time
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, api: newFakeAPI(t), users: newFakeUsers(), notes: &fakeNotes{}, links: newMemLinks(), now: testNow}
	h.ledger = newFakeLedger(func() time.Time { return h.now })
	client, err := appsclient.New(appsclient.Config{
		BaseURL: h.api.server.URL, Signer: fakeSigner{},
		Sleep: func(_ context.Context, d time.Duration) error { h.waits = append(h.waits, d); return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.client = client
	h.service, err = NewService(h.links, Deps{
		Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users, Notes: h.notes,
		Now: func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	h.ledger.putAsk(baseAsk())
	return h
}

// pass does the pass a job for the request asks.
func (h *harness) pass(requestID string) error {
	h.t.Helper()
	return h.service.Handle(context.Background(), Job{RequestID: requestID, OrganizationID: "org_1"})
}

// mustPass does a pass that is expected to succeed.
func (h *harness) mustPass(requestID string) {
	h.t.Helper()
	if err := h.pass(requestID); err != nil {
		h.t.Fatalf("pass for %s = %v", requestID, err)
	}
}

func templates(messages []*message) []string {
	names := make([]string, 0, len(messages))
	for _, made := range messages {
		names = append(names, made.Template)
	}
	return names
}

// param reads a string param of a message.
func (m *message) param(name string) string {
	value, _ := m.Params[name].(string)
	return value
}
