package appsclient

import (
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

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// The client is what the router calls apps through.
var _ slackrouter.Slack = (*Client)(nil)

// serviceauth's own signer is what production hands the client.
var _ Signer = serviceauth.Signer{}

type mintedToken struct{ audience, purpose string }

// fakeSigner mints tokens that say what they are for, one after another.
type fakeSigner struct {
	mu     sync.Mutex
	minted []mintedToken
	err    error
}

func (s *fakeSigner) Token(_ context.Context, audience, purpose string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return "", s.err
	}
	s.minted = append(s.minted, mintedToken{audience, purpose})
	return fmt.Sprintf("tok-%d", len(s.minted)), nil
}

type recorded struct {
	Method, Path, EscapedPath, RawQuery, Token string
	Body                                       []byte
}

func (r recorded) json(t *testing.T) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(r.Body, &decoded); err != nil {
		t.Fatalf("request body %q is not JSON: %v", r.Body, err)
	}
	return decoded
}

// fakeApps is apps' internal Slack API as far as the client can tell: it
// records what it is sent and answers as the test scripts.
type fakeApps struct {
	t       *testing.T
	server  *httptest.Server
	mu      sync.Mutex
	seen    []recorded
	respond func(n int, r recorded, w http.ResponseWriter)
}

func newFakeApps(t *testing.T, respond func(n int, r recorded, w http.ResponseWriter)) *fakeApps {
	t.Helper()
	fake := &fakeApps{t: t, respond: respond}
	fake.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		entry := recorded{
			Method: r.Method, Path: r.URL.Path, EscapedPath: r.URL.EscapedPath(), RawQuery: r.URL.RawQuery,
			Token: r.Header.Get(serviceauth.Header), Body: body,
		}
		fake.mu.Lock()
		fake.seen = append(fake.seen, entry)
		n := len(fake.seen)
		fake.mu.Unlock()
		fake.respond(n, entry, w)
	}))
	t.Cleanup(fake.server.Close)
	return fake
}

func (f *fakeApps) requests() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func reply(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refuse(w http.ResponseWriter, status int, code, message string) {
	reply(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// waits records the waits a client is asked to sit out, and sits none.
type waits struct {
	mu    sync.Mutex
	slept []time.Duration
	err   error
}

func (w *waits) sleep(_ context.Context, d time.Duration) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.slept = append(w.slept, d)
	return w.err
}

func (w *waits) all() []time.Duration {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone(w.slept)
}

func clientFor(t *testing.T, fake *fakeApps, signer Signer, w *waits, mutate ...func(*Config)) *Client {
	t.Helper()
	config := Config{BaseURL: fake.server.URL, Signer: signer, Sleep: w.sleep}
	for _, change := range mutate {
		change(&config)
	}
	client, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func ok(w http.ResponseWriter, body any) { reply(w, http.StatusOK, body) }

func TestEveryCallCarriesAFreshServiceTokenForAppsAndTheSlackPurpose(t *testing.T) {
	fake := newFakeApps(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case strings.Contains(r.Path, "/inbound/"):
			ok(w, map[string]any{"id": "asi_1", "kind": "message"})
		case strings.Contains(r.Path, "/profiles"):
			ok(w, map[string]any{"profiles": []any{}, "missing": []any{}})
		case strings.Contains(r.Path, "/channels/"):
			ok(w, map[string]any{"channel_id": "C123"})
		default:
			ok(w, map[string]any{"channel_id": "C123", "ts": "1700000000.000100"})
		}
	})
	signer := &fakeSigner{}
	client := clientFor(t, fake, signer, &waits{})
	ctx := context.Background()
	_, _ = client.Inbound(ctx, "asi_1")
	_, _ = client.Profiles(ctx, "org_1", "T1", []string{"U1"})
	_, _ = client.Channel(ctx, "org_1", "C123")
	_ = client.Post(ctx, slackrouter.Post{OrganizationID: "org_1", IdempotencyKey: "surfaces:irq_1:notice", Target: slackrouter.Target{UserID: "U1"}, Template: "status"})
	_ = client.Update(ctx, Update{OrganizationID: "org_1", ChannelID: "D123", TS: "1700000000.000100", Template: "notice_settled"})

	seen := fake.requests()
	if len(seen) != 5 {
		t.Fatalf("got %d requests, want 5", len(seen))
	}
	tokens := map[string]bool{}
	for _, request := range seen {
		if request.Token == "" || tokens[request.Token] {
			t.Fatalf("request %s %s carried token %q, want a token of its own", request.Method, request.Path, request.Token)
		}
		tokens[request.Token] = true
	}
	for _, minted := range signer.minted {
		if minted != (mintedToken{"apps", "slack"}) {
			t.Fatalf("minted a token for %+v, want audience apps and purpose slack", minted)
		}
	}
	wantPaths := []string{
		"/v1/internal/apps/slack/inbound/asi_1", "/v1/internal/apps/slack/profiles", "/v1/internal/apps/slack/channels/C123",
		"/v1/internal/apps/slack/post", "/v1/internal/apps/slack/update",
	}
	for i, request := range seen {
		if request.Path != wantPaths[i] {
			t.Errorf("request %d went to %s, want %s", i, request.Path, wantPaths[i])
		}
	}
}

func TestInboundReadsAMessageAndAClickAsTheRouterDescribesThem(t *testing.T) {
	fake := newFakeApps(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case strings.HasSuffix(r.Path, "/asi_msg"):
			ok(w, map[string]any{
				"id": "asi_msg", "organization_id": "org_1", "team_id": "T1", "kind": "message",
				"received_at": "2026-09-29T10:00:00Z", "channel_id": "C1", "user_id": "U1", "message_ts": "1700000000.000100",
				"thread_ts": "1700000000.000050", "bot_user_id": "UBOT", "event_id": "Ev1", "text": "<@UBOT> ask <@U2> about launch",
				"mentions": []string{"U2"}, "channel_type": "channel", "ext_shared": true,
			})
		case strings.HasSuffix(r.Path, "/asi_click"):
			ok(w, map[string]any{
				"id": "asi_click", "organization_id": "org_1", "team_id": "T1", "kind": "action",
				"received_at": "2026-09-29T10:01:00Z", "channel_id": "D1", "user_id": "U2", "message_ts": "1700000001.000200",
				"action_id": "consent_summary", "action_value": "t1.abc.def",
			})
		default:
			refuse(w, http.StatusNotFound, "not_found", "resource not found")
		}
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	ctx := context.Background()

	message, err := client.Inbound(ctx, "asi_msg")
	if err != nil {
		t.Fatal(err)
	}
	wantAt, _ := time.Parse(time.RFC3339, "2026-09-29T10:00:00Z")
	wantMention := &slackrouter.Mention{
		ChannelID: "C1", ChannelType: "channel", UserID: "U1", Text: "<@UBOT> ask <@U2> about launch", TS: "1700000000.000100",
		ThreadTS: "1700000000.000050", BotUserID: "UBOT", SharedExternally: true,
	}
	if message.ID != "asi_msg" || message.OrganizationID != "org_1" || message.TeamID != "T1" || message.Kind != slackrouter.KindMessage ||
		!message.ReceivedAt.Equal(wantAt) || message.Click != nil || message.Mention == nil || *message.Mention != *wantMention {
		t.Fatalf("message = %+v, mention %+v", message, message.Mention)
	}

	click, err := client.Inbound(ctx, "asi_click")
	if err != nil {
		t.Fatal(err)
	}
	wantClick := &slackrouter.Click{UserID: "U2", ChannelID: "D1", MessageTS: "1700000001.000200", ActionID: "consent_summary", Value: "t1.abc.def"}
	if click.Kind != slackrouter.KindAction || click.Mention != nil || click.Click == nil || *click.Click != *wantClick {
		t.Fatalf("click = %+v, %+v", click, click.Click)
	}

	// Purged, or never there: it is gone, and not to be tried again.
	if _, err := client.Inbound(ctx, "asi_purged"); !errors.Is(err, slackrouter.ErrGone) || slackrouter.IsPermanent(err) {
		t.Fatalf("a purged row's error = %v, want ErrGone", err)
	}
	if _, err := client.Inbound(ctx, " "); !slackrouter.IsPermanent(err) {
		t.Fatalf("an empty id's error = %v, want Permanent", err)
	}
}

func TestProfilesAreAskedForInBatchesAppsTakesAndMissingOnesAreLeftOut(t *testing.T) {
	fake := newFakeApps(t, func(_ int, r recorded, w http.ResponseWriter) {
		var body struct {
			OrganizationID string   `json:"organization_id"`
			UserIDs        []string `json:"user_ids"`
		}
		_ = json.Unmarshal(r.Body, &body)
		profiles := []map[string]any{}
		for _, id := range body.UserIDs {
			if id == "UGONE" {
				continue
			}
			profiles = append(profiles, map[string]any{
				"user_id": id, "team_id": "T1", "email": strings.ToLower(id) + "@acme.test", "email_confirmed": id != "U3",
				"name": "Name " + id, "is_guest": id == "U4", "is_bot": id == "U5", "is_deleted": id == "U6", "is_stranger": id == "U7",
			})
			if id == "U1" {
				profiles[len(profiles)-1]["title"] = "Staff engineer"
			}
		}
		ok(w, map[string]any{"profiles": profiles, "missing": []string{"UGONE"}})
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	ctx := context.Background()

	got, err := client.Profiles(ctx, "org_1", "T1", []string{"U1", "U2", "U3", "U4", "U5", "U6", "U7", "UGONE", "U1", " "})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 7 {
		t.Fatalf("got %d profiles, want the seven Slack knows: %+v", len(got), got)
	}
	byID := map[string]slackrouter.Profile{}
	for _, profile := range got {
		byID[profile.UserID] = profile
	}
	if p := byID["U1"]; p.Email != "u1@acme.test" || !p.EmailConfirmed || p.Name != "Name U1" || p.TeamID != "T1" || p.Guest || p.Bot || p.Deleted || p.Stranger {
		t.Fatalf("U1 = %+v", p)
	}
	// The title Slack has for a person reaches the router, which shows it to the
	// screen as one more hint at what they do; one who set none has none.
	if byID["U1"].Title != "Staff engineer" || byID["U2"].Title != "" {
		t.Fatalf("titles = %q and %q, want the one U1 set and none for U2", byID["U1"].Title, byID["U2"].Title)
	}
	if byID["U3"].EmailConfirmed {
		t.Fatalf("U3 = %+v, want an unconfirmed address kept as such", byID["U3"])
	}
	if !byID["U4"].Guest || !byID["U5"].Bot || !byID["U6"].Deleted || !byID["U7"].Stranger {
		t.Fatalf("flags lost: %+v", byID)
	}

	// Many people are asked for 20 at a time, each once.
	fake.mu.Lock()
	fake.seen = nil
	fake.mu.Unlock()
	ids := make([]string, 0, 45)
	for i := range 45 {
		ids = append(ids, fmt.Sprintf("U%03d", i))
	}
	if _, err := client.Profiles(ctx, "org_1", "T1", ids); err != nil {
		t.Fatal(err)
	}
	var sizes []int
	for _, request := range fake.requests() {
		body := request.json(t)
		if body["organization_id"] != "org_1" {
			t.Fatalf("body = %v, want the organization named", body)
		}
		sizes = append(sizes, len(body["user_ids"].([]any)))
	}
	if fmt.Sprint(sizes) != "[20 20 5]" {
		t.Fatalf("batch sizes = %v, want [20 20 5]", sizes)
	}

	// Nobody to ask about is no call.
	fake.mu.Lock()
	fake.seen = nil
	fake.mu.Unlock()
	if profiles, err := client.Profiles(ctx, "org_1", "T1", nil); err != nil || len(profiles) != 0 || len(fake.requests()) != 0 {
		t.Fatalf("Profiles(nil) = %v, %v with %d calls", profiles, err, len(fake.requests()))
	}
}

func TestChannelIsReadNowAndSaysWhatAppsSays(t *testing.T) {
	fake := newFakeApps(t, func(_ int, r recorded, w http.ResponseWriter) {
		switch {
		case strings.HasSuffix(r.Path, "/CPRIORITY"):
			ok(w, map[string]any{"channel_id": "CPRIORITY", "name": "launch", "is_private": true, "is_shared": false, "high_priority": true})
		case strings.HasSuffix(r.Path, "/CSHARED"):
			ok(w, map[string]any{"channel_id": "CSHARED", "is_private": false, "is_shared": true})
		case strings.HasSuffix(r.Path, "/GGROUP"):
			refuse(w, http.StatusUnprocessableEntity, "channel_unreadable", "slack would not describe that conversation to the bot")
		case strings.HasSuffix(r.Path, "/CGONE"):
			refuse(w, http.StatusNotFound, "not_found", "resource not found")
		default:
			ok(w, map[string]any{"channel_id": "DDM", "is_private": true, "is_im": true, "is_mpim": false})
		}
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	ctx := context.Background()

	info, err := client.Channel(ctx, "org_1", "CPRIORITY")
	if err != nil || info != (slackrouter.ChannelInfo{ID: "CPRIORITY", Private: true, HighPriority: true}) {
		t.Fatalf("Channel() = %+v, %v", info, err)
	}
	details, err := client.ChannelDetails(ctx, "org_1", "CPRIORITY")
	if err != nil || details.Name != "launch" || !details.HighPriority {
		t.Fatalf("ChannelDetails() = %+v, %v", details, err)
	}
	if info, err := client.Channel(ctx, "org_1", "CSHARED"); err != nil || !info.Shared || info.Private || info.HighPriority {
		t.Fatalf("Channel(shared) = %+v, %v", info, err)
	}
	if info, err := client.Channel(ctx, "org_1", "DDM"); err != nil || !info.IM || !info.Private {
		t.Fatalf("Channel(dm) = %+v, %v", info, err)
	}
	if _, err := client.Channel(ctx, "org_1", "GGROUP"); !errors.Is(err, slackrouter.ErrUnreadable) || slackrouter.IsPermanent(err) {
		t.Fatalf("a group message's error = %v, want ErrUnreadable", err)
	}
	if _, err := client.Channel(ctx, "org_1", "CGONE"); !errors.Is(err, slackrouter.ErrGone) {
		t.Fatalf("an unknown channel's error = %v, want ErrGone", err)
	}
	// The organization is named in the query, and the channel is one path segment.
	for _, request := range fake.requests() {
		if request.Method != http.MethodGet || request.RawQuery != "organization_id=org_1" || len(request.Body) != 0 {
			t.Fatalf("request = %+v, want a GET naming the organization", request)
		}
	}
	if _, err := client.Channel(ctx, "org_1", "C/../../post"); err != nil {
		t.Fatal(err)
	}
	if last := fake.requests()[len(fake.requests())-1]; last.EscapedPath != "/v1/internal/apps/slack/channels/C%2F..%2F..%2Fpost" {
		t.Fatalf("a channel id walked out of its path: %s", last.EscapedPath)
	}
}

func TestPostSendsItsKeyAsGivenAndSaysWhereItWent(t *testing.T) {
	fake := newFakeApps(t, func(_ int, _ recorded, w http.ResponseWriter) {
		ok(w, map[string]any{"channel_id": "D9", "ts": "1700000000.000300", "ephemeral": false, "replayed": true})
	})
	client := clientFor(t, fake, &fakeSigner{}, &waits{})
	post := slackrouter.Post{
		OrganizationID: "org_1", IdempotencyKey: "surfaces:irq_1:notice",
		Target:   slackrouter.Target{UserID: "U2"},
		Template: "owner_notice",
		Params:   map[string]any{"asker_user_id": "U1", "question": "what is the launch date?", "tickets": map[string]string{"summary": "t1.a", "access": "t1.b", "deny": "t1.c"}},
	}
	posted, err := client.Send(context.Background(), post)
	if err != nil {
		t.Fatal(err)
	}
	if posted != (Posted{ChannelID: "D9", TS: "1700000000.000300", Replayed: true}) {
		t.Fatalf("Send() = %+v", posted)
	}
	// The router's own method sends the same and drops the answer.
	if err := client.Post(context.Background(), post); err != nil {
		t.Fatal(err)
	}
	seen := fake.requests()
	if len(seen) != 2 || string(seen[0].Body) != string(seen[1].Body) {
		t.Fatalf("Send and Post sent different requests: %s / %s", seen[0].Body, seen[len(seen)-1].Body)
	}
	body := seen[0].json(t)
	if body["organization_id"] != "org_1" || body["idempotency_key"] != "surfaces:irq_1:notice" || body["template"] != "owner_notice" {
		t.Fatalf("body = %v", body)
	}
	target, _ := body["target"].(map[string]any)
	if len(target) != 1 || target["user_id"] != "U2" {
		t.Fatalf("target = %v, want only the person, as a direct message", target)
	}
	params, _ := body["params"].(map[string]any)
	tickets, _ := params["tickets"].(map[string]any)
	if params["asker_user_id"] != "U1" || tickets["access"] != "t1.b" {
		t.Fatalf("params = %v", params)
	}
	if fields := len(body); fields != 5 {
		t.Fatalf("body has %d fields, want the five apps knows (it refuses others): %v", fields, body)
	}

	// A thread and a person in it, and typed params, go as apps reads them.
	if _, err := client.Send(context.Background(), slackrouter.Post{
		OrganizationID: "org_1", IdempotencyKey: "surfaces:irq_1:status",
		Target: slackrouter.Target{ChannelID: "C1", ThreadTS: "1700000000.000100", UserID: "U1"}, Template: slackrouter.TemplateRefusal,
		Params: slackrouter.RefusalParams{Reason: consentpolicy.NotMember, OrganizationName: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}
	last := fake.requests()[2].json(t)
	if want := map[string]any{"channel_id": "C1", "thread_ts": "1700000000.000100", "user_id": "U1"}; fmt.Sprint(last["target"]) != fmt.Sprint(want) {
		t.Fatalf("target = %v, want %v", last["target"], want)
	}
	if params, _ := last["params"].(map[string]any); params["reason"] != "not_member" || params["organization_name"] != "Acme" {
		t.Fatalf("params = %v", params)
	}
	// No params is an empty object, which is what apps reads it as.
	if _, err := client.Send(context.Background(), slackrouter.Post{
		OrganizationID: "org_1", IdempotencyKey: "surfaces:irq_1:note",
		Target: slackrouter.Target{ChannelID: "C1", ThreadTS: "1700000000.000100"}, Template: "private_note",
	}); err != nil {
		t.Fatal(err)
	}
	if params, ok := fake.requests()[3].json(t)["params"].(map[string]any); !ok || len(params) != 0 {
		t.Fatalf("params = %v, want {}", fake.requests()[3].json(t)["params"])
	}
}
