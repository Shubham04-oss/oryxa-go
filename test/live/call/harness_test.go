package call

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/kms"
	"github.com/gorilla/websocket"

	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/serviceauth"
)

// localIdentityHeader is how test/live/call/door takes the member: only the
// local door reads it, and nothing deployed does.
const localIdentityHeader = "x-live-identity"

type member struct {
	label  string
	id     string
	name   string
	cookie string
}

// live is one run's setup: the door members reach the edge through, who
// they are, and, when seeding, a signer for the agent tokens that make rooms.
type live struct {
	roomURL   string
	local     bool
	org       string
	team      string
	room      string
	a, b      member
	outsider  *member
	agentName string
	edgeURL   string
	signer    *serviceauth.Signer
	client    *http.Client
}

func setup(t *testing.T) *live {
	t.Helper()
	template := os.Getenv("CALL_LIVE_ROOM_URL")
	if template == "" {
		t.Skip("CALL_LIVE_ROOM_URL is not set: no door to the edge to run against")
	}
	l := &live{
		roomURL:   template,
		local:     os.Getenv("CALL_LIVE_DOOR") == "local",
		org:       os.Getenv("CALL_LIVE_ORG"),
		team:      os.Getenv("CALL_LIVE_TEAM"),
		room:      os.Getenv("CALL_LIVE_ROOM"),
		agentName: envOr("CALL_LIVE_AGENT_NAME", "Krude"),
		edgeURL:   strings.TrimRight(os.Getenv("EDGE_URL"), "/"),
		client:    &http.Client{Timeout: 30 * time.Second},
	}
	refuseProduction(t, "CALL_LIVE_ROOM_URL", template)
	refuseProduction(t, "EDGE_URL", l.edgeURL)
	if !edgev1.ValidID(l.org) {
		t.Fatal("CALL_LIVE_ORG must name the organization")
	}
	l.a = l.member(t, "A", "Asha")
	l.b = l.member(t, "B", "Bilal")
	if id := os.Getenv("CALL_LIVE_OUTSIDER_ID"); id != "" || l.local {
		outsider := member{label: "outsider", id: envOr("CALL_LIVE_OUTSIDER_ID", "usr_livecall_outsider"), name: "Olu", cookie: os.Getenv("CALL_LIVE_OUTSIDER_COOKIE")}
		l.outsider = &outsider
	}
	if pemKey := os.Getenv("EDGE_SIGNING_KEY_PEM"); pemKey != "" && l.edgeURL != "" {
		l.signer = signer(t, pemKey)
	}
	if l.signer == nil && l.room == "" {
		t.Fatal("set CALL_LIVE_ROOM, or EDGE_URL and EDGE_SIGNING_KEY_PEM so the test makes its own rooms")
	}
	if l.local && l.signer == nil {
		t.Fatal("the local door needs EDGE_URL and EDGE_SIGNING_KEY_PEM: a local edge has no rooms until an agent makes one")
	}
	return l
}

// production is where real calls are: the test ends whatever call is open in
// its room and writes made-up lines into the room's log for good.
var production = []string{"app.oryxa.in", "api.oryxa.in", "edge.oryxa.in"}

// refuseProduction stops the test when name points at production, unless
// CALL_LIVE_ALLOW_PROD=1 says that is meant.
func refuseProduction(t *testing.T, name, raw string) {
	t.Helper()
	if os.Getenv("CALL_LIVE_ALLOW_PROD") == "1" {
		return
	}
	host, err := productionHost(raw)
	if err != nil {
		t.Fatalf("%s is not a URL: %v", name, err)
	}
	if host != "" {
		t.Fatalf("%s points at production (%s): this test ends live calls and writes into rooms' logs; set CALL_LIVE_ALLOW_PROD=1 if that is really meant", name, host)
	}
}

// productionHost returns raw's host when it is production's, or "".
func productionHost(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	// The room URL is a template: its {org}, {kind} and {id} aren't a URL's.
	parsed, err := url.Parse(strings.NewReplacer("{", "", "}", "").Replace(raw))
	if err != nil {
		return "", err
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if slices.Contains(production, host) {
		return host, nil
	}
	return "", nil
}

func (l *live) member(t *testing.T, label, name string) member {
	t.Helper()
	m := member{label: label, id: os.Getenv("CALL_LIVE_" + label + "_ID"), name: name, cookie: os.Getenv("CALL_LIVE_" + label + "_COOKIE")}
	if m.id == "" {
		t.Fatalf("CALL_LIVE_%s_ID must name member %s", label, label)
	}
	if !l.local && m.cookie == "" {
		t.Fatalf("CALL_LIVE_%s_COOKIE must hold member %s's web-next session", label, label)
	}
	return m
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

// localKMS signs the way KMS does for an ECC_NIST_P256 key, so serviceauth
// issues the same tokens agents do.
type localKMS struct{ key *ecdsa.PrivateKey }

func (local localKMS) Sign(_ context.Context, input *kms.SignInput, _ ...func(*kms.Options)) (*kms.SignOutput, error) {
	digest := sha256.Sum256(input.Message)
	signature, err := ecdsa.SignASN1(rand.Reader, local.key, digest[:])
	return &kms.SignOutput{Signature: signature}, err
}

func signer(t *testing.T, pemKey string) *serviceauth.Signer {
	t.Helper()
	block, _ := pem.Decode([]byte(pemKey))
	if block == nil {
		t.Fatal("EDGE_SIGNING_KEY_PEM must hold a PEM private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		key, ecErr := x509.ParseECPrivateKey(block.Bytes)
		if ecErr != nil {
			t.Fatalf("parse EDGE_SIGNING_KEY_PEM: %v", err)
		}
		parsed = key
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok {
		t.Fatal("EDGE_SIGNING_KEY_PEM must be an ECDSA P-256 key")
	}
	return &serviceauth.Signer{Client: localKMS{key: key}, KeyID: "live-call", Service: "agents"}
}

// room is the call's room for one test: a fresh one when seeding, so tests
// never see each other's calls, or the configured one, cleared of any call
// a failed run left going.
type room struct {
	*live
	t    *testing.T
	room edgev1.Room
}

func (l *live) newRoom(t *testing.T) *room {
	t.Helper()
	r := &room{live: l, t: t}
	if l.signer != nil {
		suffix := make([]byte, 6)
		_, _ = rand.Read(suffix)
		kind := edgev1.RoomTeamspace
		if l.room != "" {
			kind = edgev1.RoomKind(strings.SplitN(l.room, "/", 2)[0])
		}
		r.room = edgev1.Room{Kind: kind, ID: "livecall_" + hex.EncodeToString(suffix)}
		for _, m := range []member{l.a, l.b} {
			r.seed(m)
		}
	} else {
		kind, id, ok := strings.Cut(l.room, "/")
		if !ok {
			t.Fatal("CALL_LIVE_ROOM must be kind/id")
		}
		r.room = edgev1.Room{Kind: edgev1.RoomKind(kind), ID: id}
		if r.admits() {
			for _, m := range []member{l.a, l.b} {
				if status := r.admit(&m); status != http.StatusNoContent {
					t.Fatalf("Go would not admit %s to %s: %d", m.label, l.room, status)
				}
			}
		}
	}
	if err := r.room.Validate(); err != nil {
		t.Fatalf("room: %v", err)
	}
	r.endAnyCall()
	t.Cleanup(r.endAnyCall)
	return r
}

// admits says whether members are admitted through Go before opening the
// room, as the page does (A18): on the web door, with the room configured
// rather than seeded, unless CALL_LIVE_ADMIT=off.
func (r *room) admits() bool {
	return !r.local && r.signer == nil && os.Getenv("CALL_LIVE_ADMIT") != "off"
}

// admit asks Go, as m, to make them a member of the room on the edge:
// POST /v1/organizations/{org}/edge/rooms/{kind}/{id}/admit on the app's
// origin, 204 when they may be in it and 404 when it isn't theirs.
func (r *room) admit(m *member) int {
	r.t.Helper()
	parsed, err := url.Parse(r.roomURL)
	if err != nil {
		r.t.Fatal(err)
	}
	address := parsed.Scheme + "://" + parsed.Host + "/v1/organizations/" + url.PathEscape(r.org) +
		"/edge/rooms/" + url.PathEscape(string(r.room.Kind)) + "/" + url.PathEscape(r.room.ID) + "/admit"
	request, _ := http.NewRequest(http.MethodPost, address, nil)
	r.credentials(request.Header, m)
	response, err := r.client.Do(request)
	if err != nil {
		r.t.Fatalf("admit %s: %v", m.label, err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	response.Body.Close()
	return response.StatusCode
}

// seed makes m a member of the room the way their agent acting there does:
// the edge adds whoever an agent token names.
func (r *room) seed(m member) {
	r.t.Helper()
	path, _ := edgev1.RoomPath(r.room)
	token, err := r.signer.EdgeToken(context.Background(), edgev1.TokenClaims{
		Org: r.org, Team: r.team, Room: r.room, Member: m.id, Agent: "agt_livecall", Run: "run_livecall",
		Scopes: []edgev1.Scope{edgev1.ScopeLogRead},
	})
	if err != nil {
		r.t.Fatalf("sign a seeding token: %v", err)
	}
	request, _ := http.NewRequest(http.MethodGet, r.edgeURL+path+"/log", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := r.client.Do(request)
	if err != nil {
		r.t.Fatalf("seed %s into %s: %v", m.label, r.room.ID, err)
	}
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		r.t.Fatalf("seed %s into %s: %d %s", m.label, r.room.ID, response.StatusCode, body)
	}
}

func (r *room) endAnyCall() {
	var answer struct {
		Call *callView `json:"call"`
	}
	if status, _ := r.do(r.a, http.MethodGet, "/call", nil, &answer); status == http.StatusOK && answer.Call != nil {
		r.do(r.a, http.MethodPost, "/call/end", map[string]any{}, nil)
	}
}

// url is the room's address behind the door, with rest after it.
func (r *room) url(rest string) string {
	replacer := strings.NewReplacer("{org}", url.PathEscape(r.org), "{kind}", url.PathEscape(string(r.room.Kind)), "{id}", url.PathEscape(r.room.ID))
	return strings.TrimRight(replacer.Replace(r.roomURL), "/") + rest
}

// credentials sets who m is on a request through the door.
func (r *room) credentials(header http.Header, m *member) {
	if m == nil {
		return
	}
	if r.local {
		identity, _ := json.Marshal(map[string]string{"org": r.org, "member": m.id})
		header.Set(localIdentityHeader, string(identity))
		return
	}
	header.Set("Cookie", m.cookie)
	if parsed, err := url.Parse(r.roomURL); err == nil {
		header.Set("Origin", parsed.Scheme+"://"+parsed.Host)
	}
}

// do sends one member request and decodes a 2xx answer into out. It returns
// the status, and the body of an answer that wasn't 2xx.
func (r *room) do(m member, method, rest string, body, out any) (int, string) {
	return r.doAs(&m, method, rest, body, out)
}

func (r *room) doAs(m *member, method, rest string, body, out any) (int, string) {
	r.t.Helper()
	var reader io.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, r.url(rest), reader)
	if err != nil {
		r.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	r.credentials(request.Header, m)
	response, err := r.client.Do(request)
	if err != nil {
		r.t.Fatalf("%s %s: %v", method, rest, err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	if response.StatusCode/100 != 2 {
		return response.StatusCode, string(raw)
	}
	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			r.t.Fatalf("%s %s: decode %s: %v", method, rest, raw, err)
		}
	}
	return response.StatusCode, ""
}

// must is do for a request that has to work.
func (r *room) must(m member, method, rest string, body, out any) {
	r.t.Helper()
	if status, text := r.do(m, method, rest, body, out); status/100 != 2 {
		r.t.Fatalf("%s %s %s: %d %s", m.label, method, rest, status, text)
	}
}

// ---- the call API's shapes (edge/src/call/contracts.ts) ----

type joined struct {
	CallID        string `json:"callId"`
	MeetingID     string `json:"meetingId"`
	ParticipantID string `json:"participantId"`
	Token         string `json:"token"`
	Preset        string `json:"preset"`
}

// String leaves the participant's token out of failure messages: it lets
// whoever holds it into the meeting.
func (j joined) String() string {
	token := "<none>"
	if j.Token != "" {
		token = "<redacted>"
	}
	return fmt.Sprintf("{CallID:%s MeetingID:%s ParticipantID:%s Token:%s Preset:%s}", j.CallID, j.MeetingID, j.ParticipantID, token, j.Preset)
}

type callView struct {
	CallID       string `json:"callId"`
	StartedBy    string `json:"startedBy"`
	Participants []struct {
		Member string `json:"member"`
		Name   string `json:"name"`
	} `json:"participants"`
	Recording struct {
		On bool    `json:"on"`
		By *string `json:"by"`
	} `json:"recording"`
	Agent struct {
		Present bool   `json:"present"`
		Name    string `json:"name"`
	} `json:"agent"`
}

func (v *callView) members() []string {
	if v == nil {
		return nil
	}
	ids := make([]string, 0, len(v.Participants))
	for _, p := range v.Participants {
		ids = append(ids, p.Member)
	}
	return ids
}

func (r *room) join(m member) joined {
	r.t.Helper()
	var answer joined
	r.must(m, http.MethodPost, "/call/join", map[string]any{"name": m.name}, &answer)
	return answer
}

func (r *room) call(m member) *callView {
	r.t.Helper()
	var answer struct {
		Call *callView `json:"call"`
	}
	r.must(m, http.MethodGet, "/call", nil, &answer)
	return answer.Call
}

type segment struct {
	ID      string `json:"id"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	Partial bool   `json:"partial"`
	At      int64  `json:"at"`
}

func said(speaker member, text string, partial bool) segment {
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return segment{ID: "seg_" + hex.EncodeToString(id), Speaker: speaker.id, Text: text, Partial: partial, At: time.Now().UnixMilli()}
}

// say forwards m's own words, as their client does with meeting.ai's
// transcript, and returns how many final segments the room took.
func (r *room) say(m member, callID string, segments ...segment) int {
	r.t.Helper()
	var answer struct {
		Accepted int `json:"accepted"`
	}
	r.must(m, http.MethodPost, "/call/transcript", map[string]any{"callId": callID, "segments": segments}, &answer)
	return answer.Accepted
}

// log is the room's log as m may read it.
func (r *room) log(m member) []edgev1.Event {
	r.t.Helper()
	var events []edgev1.Event
	after := int64(0)
	for {
		var page edgev1.LogResponse
		r.must(m, http.MethodGet, fmt.Sprintf("/log?after=%d", after), nil, &page)
		events = append(events, page.Events...)
		if !page.More || page.Next <= after {
			return events
		}
		after = page.Next
	}
}

func ofType(events []edgev1.Event, kind string) []edgev1.Event {
	var matched []edgev1.Event
	for _, event := range events {
		if event.Type == kind {
			matched = append(matched, event)
		}
	}
	return matched
}

func forCall(events []edgev1.Event, callID string) []edgev1.Event {
	var matched []edgev1.Event
	for _, event := range events {
		if event.Data["callId"] == callID {
			matched = append(matched, event)
		}
	}
	return matched
}

// eventually polls m's log until match finds what it is waiting for.
func (r *room) eventually(m member, within time.Duration, what string, match func([]edgev1.Event) bool) []edgev1.Event {
	r.t.Helper()
	deadline := time.Now().Add(within)
	for {
		events := r.log(m)
		if match(events) {
			return events
		}
		if time.Now().After(deadline) {
			r.t.Fatalf("waited %s for %s; the log has %v", within, what, types(events))
		}
		time.Sleep(time.Second)
	}
}

func types(events []edgev1.Event) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Type)
	}
	return names
}

// ---- the agent's voice ----

// frame is one voice.* frame on /voice/live (VoiceFrame in contracts.ts).
type frame struct {
	Type        string `json:"type"`
	UtteranceID string `json:"utteranceId"`
	Text        string `json:"text"`
	Seq         int    `json:"seq"`
	Format      string `json:"format"`
	Data        string `json:"data"`
	Last        bool   `json:"last"`
	Reason      string `json:"reason"`
	Present     *bool  `json:"present"`
	Name        string `json:"name"`
	DispatchID  string `json:"dispatchId"`
	at          time.Time
}

// ear is a member's /voice/live socket and everything it has heard.
type ear struct {
	t      *testing.T
	who    string
	conn   *websocket.Conn
	mu     sync.Mutex
	frames []frame
	read   int
	closed chan struct{}
	signal chan struct{}
}

func (r *room) dial(m member) (*websocket.Conn, int, error) {
	address := r.url("/voice/live")
	address = "ws" + strings.TrimPrefix(address, "http")
	header := http.Header{}
	r.credentials(header, &m)
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, response, err := dialer.Dial(address, header)
	status := 0
	if response != nil {
		status = response.StatusCode
	}
	return conn, status, err
}

// listen opens m's /voice/live socket.
func (r *room) listen(m member) *ear {
	r.t.Helper()
	conn, status, err := r.dial(m)
	if err != nil {
		r.t.Fatalf("%s /voice/live: %d %v", m.label, status, err)
	}
	e := &ear{t: r.t, who: m.label, conn: conn, closed: make(chan struct{}), signal: make(chan struct{}, 1)}
	go e.run()
	r.t.Cleanup(func() { _ = conn.Close() })
	return e
}

func (e *ear) run() {
	defer close(e.closed)
	for {
		_, raw, err := e.conn.ReadMessage()
		if err != nil {
			return
		}
		var f frame
		if json.Unmarshal(raw, &f) != nil {
			continue
		}
		f.at = time.Now()
		e.mu.Lock()
		e.frames = append(e.frames, f)
		e.mu.Unlock()
		select {
		case e.signal <- struct{}{}:
		default:
		}
	}
}

// next returns the first frame from here on that match accepts, consuming
// what comes before it, or false after within.
func (e *ear) next(within time.Duration, match func(frame) bool) (frame, bool) {
	deadline := time.After(within)
	for {
		e.mu.Lock()
		for e.read < len(e.frames) {
			f := e.frames[e.read]
			e.read++
			if match(f) {
				e.mu.Unlock()
				return f, true
			}
		}
		e.mu.Unlock()
		select {
		case <-e.signal:
		case <-e.closed:
			e.mu.Lock()
			pending := e.read < len(e.frames)
			e.mu.Unlock()
			if !pending {
				return frame{}, false
			}
		case <-deadline:
			return frame{}, false
		}
	}
}

func (e *ear) expect(within time.Duration, what string, match func(frame) bool) frame {
	e.t.Helper()
	f, ok := e.next(within, match)
	if !ok {
		e.t.Fatalf("%s waited %s for %s", e.who, within, what)
	}
	return f
}

func (e *ear) never(within time.Duration, what string, match func(frame) bool) {
	e.t.Helper()
	if f, ok := e.next(within, match); ok {
		e.t.Fatalf("%s got %s: %+v", e.who, what, f)
	}
}

func isType(kind string) func(frame) bool { return func(f frame) bool { return f.Type == kind } }

// utterance waits for one spoken answer: voice.speaking, then its audio
// frames in seq order until the last.
func (e *ear) utterance(within time.Duration) (speaking frame, audio []frame) {
	e.t.Helper()
	speaking = e.expect(within, "the agent to start speaking", isType("voice.speaking"))
	for {
		f := e.expect(within, "the answer's audio", func(f frame) bool {
			return f.UtteranceID == speaking.UtteranceID && (f.Type == "voice.audio" || f.Type == "voice.stop")
		})
		if f.Type == "voice.stop" {
			e.t.Fatalf("%s: the answer stopped (%s) before its last audio", e.who, f.Reason)
		}
		if f.Seq != len(audio) || f.Format != "mp3" || f.Data == "" {
			e.t.Fatalf("%s: audio frame %d out of order or empty: seq %d format %q", e.who, len(audio), f.Seq, f.Format)
		}
		audio = append(audio, f)
		if f.Last {
			return speaking, audio
		}
	}
}

// question asks the agent something it should answer without handing off.
func (l *live) question(text string) string { return l.agentName + ", " + text }
