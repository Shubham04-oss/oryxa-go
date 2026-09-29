// Package slackfake is a fake Slack, for tests of anything that talks to
// Oryxa's Slack app. It plays both directions.
//
// Toward Oryxa it is the Web API: the methods apps' Slack client calls, on a
// loopback server, each answered as Slack answers it and each recorded. It
// keeps the messages it was asked to post, so a test can read what the
// bot said and check that an update replaced it, and it can be told to
// answer any method with an error or with Slack's ratelimited.
//
// From Oryxa's side of the wire it is Slack itself: it signs and builds the
// requests Slack sends (an @-mention, a button click, an uninstall, the
// url_verification challenge) with the v0 scheme and the app's signing
// secret, ready to hand to an http.Handler or to send over the network.
//
//	fake := slackfake.New(t)
//	// apps' Slack client is pointed at fake.URL(), as APPS_SLACK_API_BASE_URL is,
//	// and the ingress verifies with fake.SigningSecret().
//	recorder := slackfake.Serve(ingress, fake.Mention("", slackfake.Mention{Text: "<@UBOT> hello"}))
//	posted := fake.Calls("chat.postMessage")
//
// Only what Oryxa's surface uses is faked, and only as far as it is used.
// Where Slack's documentation leaves a shape open the fake answers what the
// surface reads, and says so. Nothing here is a Slack API reference.
package slackfake

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Paths of Oryxa's ingress, under whatever base URL it is served at.
const (
	EventsPath        = "/v1/apps/slack/events"
	InteractivityPath = "/v1/apps/slack/interactivity"
)

// The defaults of a Fake that is given no options.
const (
	DefaultClientID      = "1234567890.0987654321"
	DefaultClientSecret  = "fake-client-secret-0123456789"
	DefaultSigningSecret = "fake-signing-secret-0123456789abcdef"
	DefaultTeamID        = "T0FAKE01"
	DefaultBotUserID     = "U0FAKEBOT"
	DefaultBotToken      = "xoxb-fake-bot-token-0123456789"
)

// BotScopes are the scopes a workspace grants by default: what Oryxa's app
// asks for, as apps/internal/slackbot/manifest.yaml lists them.
var BotScopes = []string{
	"app_mentions:read", "chat:write", "im:write", "users:read", "users:read.email", "channels:read", "groups:read",
}

// Workspace is a Slack workspace that installed the app.
type Workspace struct {
	TeamID    string
	Name      string
	BotUserID string
	// BotToken is the xoxb- token the install was issued. Every Web API
	// call but oauth.v2.access must present it.
	BotToken string
	// Scopes are what the install granted; BotScopes when empty.
	Scopes []string
	// Revoked makes every call with the token answer token_revoked.
	Revoked bool
}

// User is a person in a workspace, as users.info answers them.
type User struct {
	ID       string
	TeamID   string // the default workspace's when empty
	Name     string
	RealName string
	Email    string
	Title    string
	Deleted  bool
	Bot      bool
	// Guest and UltraRestricted are Slack's is_restricted and
	// is_ultra_restricted.
	Guest, UltraRestricted bool
	// Stranger is Slack's is_stranger, a person from another workspace in a
	// shared channel.
	Stranger bool
}

// Channel is a conversation, as conversations.info and conversations.list
// answer it.
type Channel struct {
	ID      string
	Name    string
	Private bool
	// Shared is any kind of sharing: Slack Connect, or across an
	// organization.
	Shared   bool
	MPIM     bool
	IM       bool
	Archived bool
	// NotMember makes posting to the channel answer not_in_channel.
	NotMember bool
}

// Message is a message the fake was asked to post, as it stands now.
type Message struct {
	Channel  string
	TS       string
	ThreadTS string
	Text     string
	Blocks   []any
	// Ephemeral is a chat.postEphemeral, which cannot be updated; User is who
	// it was for.
	Ephemeral bool
	User      string
	// Updates counts the chat.update calls that changed it.
	Updates int
	// UnfurlLinks and UnfurlMedia are what the post asked for. Slack's
	// default when a post says nothing is true.
	UnfurlLinks bool
	UnfurlMedia bool
}

// Call is one Web API call, as the fake received it.
type Call struct {
	Method string
	// Token is the bearer token the call presented, "" if none.
	Token string
	// BasicUser and BasicPassword are HTTP Basic credentials, as
	// oauth.v2.access takes the client's.
	BasicUser, BasicPassword string
	// Args are the call's arguments, from its JSON body, its form or its
	// query, whichever it used.
	Args map[string]any
	At   time.Time
}

// Fake is a fake Slack. Its methods are safe for concurrent use.
type Fake struct {
	server *httptest.Server
	now    func() time.Time

	clientID, clientSecret, signingSecret string

	mu         sync.Mutex
	workspaces map[string]*Workspace
	users      map[string]User
	channels   map[string]Channel
	messages   []*Message
	calls      []Call
	failures   map[string][]failure
	codes      map[string]string // authorization code -> team id; "" once spent
	spent      map[string]bool
	tweak      func(map[string]any)
	sequence   int
	down       bool
}

type failure struct {
	code       string
	retryAfter time.Duration
	rateLimit  bool
	left       int // <= 0 means for good
}

// Option adjusts a Fake.
type Option func(*Fake)

// WithSigningSecret sets the secret requests are signed with.
func WithSigningSecret(secret string) Option { return func(f *Fake) { f.signingSecret = secret } }

// WithClient sets the OAuth client oauth.v2.access accepts.
func WithClient(id, secret string) Option {
	return func(f *Fake) { f.clientID, f.clientSecret = id, secret }
}

// WithClock sets the clock requests are signed by and messages are stamped
// with.
func WithClock(now func() time.Time) Option { return func(f *Fake) { f.now = now } }

// New starts a fake Slack with one workspace, DefaultTeamID, and closes it
// when the test ends.
func New(t testing.TB, options ...Option) *Fake {
	t.Helper()
	f := &Fake{
		now: time.Now, clientID: DefaultClientID, clientSecret: DefaultClientSecret, signingSecret: DefaultSigningSecret,
		workspaces: map[string]*Workspace{}, users: map[string]User{}, channels: map[string]Channel{},
		failures: map[string][]failure{}, codes: map[string]string{}, spent: map[string]bool{},
	}
	for _, option := range options {
		option(f)
	}
	f.AddWorkspace(Workspace{TeamID: DefaultTeamID, Name: "Acme", BotUserID: DefaultBotUserID, BotToken: DefaultBotToken})
	f.server = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.server.Close)
	return f
}

// URL is the Web API's base: what APPS_SLACK_API_BASE_URL is set to.
func (f *Fake) URL() string { return f.server.URL + "/api" }

// SigningSecret is what the ingress must verify with.
func (f *Fake) SigningSecret() string { return f.signingSecret }

// ClientID and ClientSecret are the OAuth client oauth.v2.access accepts.
func (f *Fake) ClientID() string     { return f.clientID }
func (f *Fake) ClientSecret() string { return f.clientSecret }

// AddWorkspace adds a workspace that installed the app, or replaces one. The
// zero fields take defaults derived from TeamID.
func (f *Fake) AddWorkspace(w Workspace) {
	if w.TeamID == "" {
		w.TeamID = DefaultTeamID
	}
	if w.Name == "" {
		w.Name = "Workspace " + w.TeamID
	}
	if w.BotUserID == "" {
		w.BotUserID = "UBOT" + strings.TrimPrefix(w.TeamID, "T")
	}
	if w.BotToken == "" {
		w.BotToken = "xoxb-" + strings.ToLower(w.TeamID) + "-token-0123456789"
	}
	if len(w.Scopes) == 0 {
		w.Scopes = slices.Clone(BotScopes)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.workspaces[w.TeamID] = &w
}

// Workspace answers a workspace as it stands.
func (f *Fake) Workspace(teamID string) Workspace {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.workspaces[teamID]; ok {
		return *w
	}
	return Workspace{}
}

// RevokeToken makes a workspace's token answer token_revoked from now on, as
// if an admin had uninstalled the app.
func (f *Fake) RevokeToken(teamID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if w, ok := f.workspaces[teamID]; ok {
		w.Revoked = true
	}
}

// AddUser adds a person. Unset fields take defaults derived from ID.
func (f *Fake) AddUser(u User) {
	if u.TeamID == "" {
		u.TeamID = DefaultTeamID
	}
	if u.Name == "" {
		u.Name = strings.ToLower(strings.TrimLeft(u.ID, "UW"))
	}
	if u.RealName == "" {
		u.RealName = strings.ToUpper(u.Name[:1]) + u.Name[1:]
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.users[u.ID] = u
}

// AddChannel adds a conversation.
func (f *Fake) AddChannel(c Channel) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channels[c.ID] = c
}

// DMChannel is the id conversations.open answers for a user's direct message
// with the bot, so a test can name it without asking.
func DMChannel(userID string) string { return "D" + strings.TrimLeft(userID, "UW") }

// IssueCode makes an authorization code for a workspace, as Slack does when
// an admin approves the install: the callback's ?code=, which
// oauth.v2.access exchanges once. The workspace is added if it is new.
func (f *Fake) IssueCode(teamID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	code := fmt.Sprintf("code-%s-%d", strings.ToLower(teamID), f.sequence)
	f.codes[code] = teamID
	return code
}

// ModifyOAuth changes what oauth.v2.access answers, before it is sent: to
// turn token rotation on, mark an Enterprise install, or drop a scope.
// Pass nil to stop.
func (f *Fake) ModifyOAuth(change func(answer map[string]any)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tweak = change
}

// Fail makes the next times calls of a method answer {"ok": false, "error":
// code} with HTTP 200, as Slack refuses. With times <= 0 it does so until
// Reset. Method "*" is every method.
func (f *Fake) Fail(method, code string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[method] = append(f.failures[method], failure{code: code, left: times})
}

// RateLimit makes the next times calls of a method answer HTTP 429 with
// Retry-After, as Slack does when a method is called too fast. With times
// <= 0 it does so until Reset.
func (f *Fake) RateLimit(method string, retryAfter time.Duration, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures[method] = append(f.failures[method], failure{rateLimit: true, retryAfter: retryAfter, left: times})
}

// Down makes every call answer HTTP 503, as if Slack were unreachable, until
// it is called with false.
func (f *Fake) Down(down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down = down
}

// Reset forgets the recorded calls, the messages and every injected failure.
// Workspaces, users and channels stay.
func (f *Fake) Reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls, f.messages, f.failures, f.down = nil, nil, map[string][]failure{}, false
}

// Calls answers the recorded calls, oldest first: all of them, or only those
// of the methods named.
func (f *Fake) Calls(methods ...string) []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []Call
	for _, call := range f.calls {
		if len(methods) == 0 || slices.Contains(methods, call.Method) {
			found = append(found, call)
		}
	}
	return found
}

// Messages answers what was posted, oldest first, as it stands after any
// updates.
func (f *Fake) Messages() []Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	messages := make([]Message, 0, len(f.messages))
	for _, message := range f.messages {
		messages = append(messages, *message)
	}
	return messages
}

// Message finds one posted message.
func (f *Fake) Message(channel, ts string) (Message, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, message := range f.messages {
		if message.Channel == channel && message.TS == ts {
			return *message, true
		}
	}
	return Message{}, false
}

// serve is the Web API.
func (f *Fake) serve(writer http.ResponseWriter, request *http.Request) {
	method := strings.TrimPrefix(request.URL.Path, "/api/")
	args := readArgs(request)
	basicUser, basicPassword, _ := request.BasicAuth()
	token, _ := strings.CutPrefix(request.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		if value, ok := args["token"].(string); ok {
			token = value
		}
	}

	f.mu.Lock()
	f.calls = append(f.calls, Call{
		Method: method, Token: token, BasicUser: basicUser, BasicPassword: basicPassword, Args: args, At: f.now(),
	})
	down := f.down
	injected, hit := f.takeFailureLocked(method)
	f.mu.Unlock()

	switch {
	case down:
		http.Error(writer, "slack is down", http.StatusServiceUnavailable)
		return
	case hit && injected.rateLimit:
		writer.Header().Set("Retry-After", strconv.Itoa(max(1, int((injected.retryAfter+time.Second-1)/time.Second))))
		reply(writer, http.StatusTooManyRequests, map[string]any{"ok": false, "error": "ratelimited"})
		return
	case hit:
		reply(writer, http.StatusOK, map[string]any{"ok": false, "error": injected.code})
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	answer := f.handleLocked(method, token, basicUser, basicPassword, args)
	reply(writer, http.StatusOK, answer)
}

func (f *Fake) takeFailureLocked(method string) (failure, bool) {
	for _, key := range []string{method, "*"} {
		queue := f.failures[key]
		if len(queue) == 0 {
			continue
		}
		next := queue[0]
		if next.left > 0 {
			next.left--
			if next.left == 0 {
				f.failures[key] = queue[1:]
			} else {
				queue[0] = next
			}
		}
		return next, true
	}
	return failure{}, false
}

func fail(code string) map[string]any { return map[string]any{"ok": false, "error": code} }

// handleLocked answers one method. The fake's lock is held.
func (f *Fake) handleLocked(method, token, basicUser, basicPassword string, args map[string]any) map[string]any {
	if method == "oauth.v2.access" {
		return f.oauthLocked(basicUser, basicPassword, args)
	}
	workspace := f.byTokenLocked(token)
	switch {
	case token == "":
		return fail("not_authed")
	case workspace == nil:
		return fail("invalid_auth")
	case workspace.Revoked:
		return fail("token_revoked")
	}
	switch method {
	case "auth.test":
		return map[string]any{
			"ok": true, "url": "https://" + strings.ToLower(workspace.Name) + ".slack.com/", "team": workspace.Name,
			"user": "oryxa", "team_id": workspace.TeamID, "user_id": workspace.BotUserID, "bot_id": "B0FAKE",
			"is_enterprise_install": false,
		}
	case "apps.uninstall", "auth.revoke":
		workspace.Revoked = true
		return map[string]any{"ok": true, "revoked": true}
	case "users.info":
		return f.usersInfoLocked(workspace, str(args, "user"))
	case "conversations.info":
		return f.conversationsInfoLocked(workspace, str(args, "channel"))
	case "conversations.list":
		return f.conversationsListLocked(str(args, "types"))
	case "conversations.open":
		user := str(args, "users")
		if user == "" {
			return fail("users_list_not_supplied")
		}
		if u, ok := f.users[user]; ok && (u.Deleted || u.Bot) {
			if u.Deleted {
				return fail("user_disabled")
			}
			return fail("cannot_dm_bot")
		}
		return map[string]any{"ok": true, "channel": map[string]any{"id": DMChannel(user)}, "already_open": true}
	case "chat.postMessage", "chat.postEphemeral":
		return f.postLocked(method == "chat.postEphemeral", args)
	case "chat.update":
		return f.updateLocked(args)
	}
	return fail("unknown_method")
}

func (f *Fake) byTokenLocked(token string) *Workspace {
	for _, w := range f.workspaces {
		if w.BotToken == token {
			return w
		}
	}
	return nil
}

func (f *Fake) oauthLocked(basicUser, basicPassword string, args map[string]any) map[string]any {
	id, secret := basicUser, basicPassword
	if id == "" {
		id, secret = str(args, "client_id"), str(args, "client_secret")
	}
	if id != f.clientID {
		return fail("invalid_client_id")
	}
	if secret != f.clientSecret {
		return fail("bad_client_secret")
	}
	code := str(args, "code")
	teamID, ok := f.codes[code]
	switch {
	case !ok:
		return fail("invalid_code")
	case f.spent[code]:
		return fail("code_already_used")
	}
	f.spent[code] = true
	workspace, ok := f.workspaces[teamID]
	if !ok {
		f.workspaces[teamID] = &Workspace{
			TeamID: teamID, Name: "Workspace " + teamID, BotUserID: "UBOT" + strings.TrimPrefix(teamID, "T"),
			BotToken: "xoxb-" + strings.ToLower(teamID) + "-token-0123456789", Scopes: slices.Clone(BotScopes),
		}
		workspace = f.workspaces[teamID]
	}
	// Installing over an install issues a working token again.
	workspace.Revoked = false
	answer := map[string]any{
		"ok": true, "access_token": workspace.BotToken, "token_type": "bot", "scope": strings.Join(workspace.Scopes, ","),
		"bot_user_id": workspace.BotUserID, "app_id": "A0FAKEAPP",
		"team":        map[string]any{"id": workspace.TeamID, "name": workspace.Name},
		"authed_user": map[string]any{"id": "U0INSTALLER"}, "is_enterprise_install": false,
	}
	if f.tweak != nil {
		f.tweak(answer)
	}
	return answer
}

func (f *Fake) usersInfoLocked(workspace *Workspace, id string) map[string]any {
	user, ok := f.users[id]
	if !ok {
		return fail("user_not_found")
	}
	profile := map[string]any{"display_name": user.Name, "title": user.Title}
	if slices.Contains(workspace.Scopes, "users:read.email") && user.Email != "" {
		profile["email"] = user.Email
	}
	return map[string]any{"ok": true, "user": map[string]any{
		"id": user.ID, "team_id": user.TeamID, "name": user.Name, "real_name": user.RealName, "deleted": user.Deleted,
		"is_bot": user.Bot, "is_app_user": false, "is_restricted": user.Guest, "is_ultra_restricted": user.UltraRestricted,
		"is_stranger": user.Stranger, "profile": profile,
	}}
}

func channelObject(c Channel) map[string]any {
	object := map[string]any{
		"id": c.ID, "name": c.Name, "is_channel": !c.Private && !c.MPIM && !c.IM, "is_group": c.Private && !c.MPIM,
		"is_im": c.IM, "is_mpim": c.MPIM, "is_private": c.Private || c.MPIM || c.IM, "is_archived": c.Archived,
		"is_shared": c.Shared, "is_ext_shared": c.Shared, "is_org_shared": false, "is_pending_ext_shared": false,
	}
	if c.IM || c.MPIM {
		delete(object, "name")
	}
	return object
}

func (f *Fake) conversationsInfoLocked(workspace *Workspace, id string) map[string]any {
	channel, ok := f.channels[id]
	if !ok {
		return fail("channel_not_found")
	}
	// A conversation's type needs its own read scope, as in Slack.
	switch {
	case channel.MPIM && !slices.Contains(workspace.Scopes, "mpim:read"),
		channel.IM && !slices.Contains(workspace.Scopes, "im:read"),
		channel.Private && !channel.MPIM && !channel.IM && !slices.Contains(workspace.Scopes, "groups:read"),
		!channel.Private && !channel.MPIM && !channel.IM && !slices.Contains(workspace.Scopes, "channels:read"):
		return map[string]any{"ok": false, "error": "missing_scope", "needed": "channels:read,groups:read,im:read,mpim:read"}
	}
	return map[string]any{"ok": true, "channel": channelObject(channel)}
}

func (f *Fake) conversationsListLocked(types string) map[string]any {
	wanted := map[string]bool{}
	for _, kind := range strings.Split(types, ",") {
		wanted[strings.TrimSpace(kind)] = true
	}
	if len(wanted) == 1 && wanted[""] {
		wanted["public_channel"] = true
	}
	ids := make([]string, 0, len(f.channels))
	for id := range f.channels {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	channels := []any{}
	for _, id := range ids {
		c := f.channels[id]
		switch {
		case c.IM || c.MPIM || c.NotMember:
			continue
		case c.Private && wanted["private_channel"], !c.Private && wanted["public_channel"]:
			channels = append(channels, channelObject(c))
		}
	}
	return map[string]any{"ok": true, "channels": channels, "response_metadata": map[string]any{"next_cursor": ""}}
}

func (f *Fake) nextTSLocked() string {
	f.sequence++
	return fmt.Sprintf("%d.%06d", f.now().Unix(), f.sequence)
}

func (f *Fake) postLocked(ephemeral bool, args map[string]any) map[string]any {
	channel := str(args, "channel")
	switch c, known := f.channels[channel]; {
	case channel == "":
		return fail("channel_not_found")
	case known && c.Archived:
		return fail("is_archived")
	case known && c.NotMember:
		if ephemeral {
			return fail("no_permission")
		}
		return fail("not_in_channel")
	}
	if str(args, "text") == "" && blocksOf(args) == nil {
		return fail("no_text")
	}
	if ephemeral && str(args, "user") == "" {
		return fail("invalid_arguments")
	}
	message := &Message{
		Channel: channel, TS: f.nextTSLocked(), ThreadTS: str(args, "thread_ts"), Text: str(args, "text"),
		Blocks: blocksOf(args), Ephemeral: ephemeral, User: str(args, "user"),
		UnfurlLinks: boolArg(args, "unfurl_links", true), UnfurlMedia: boolArg(args, "unfurl_media", true),
	}
	f.messages = append(f.messages, message)
	if ephemeral {
		return map[string]any{"ok": true, "message_ts": message.TS}
	}
	return map[string]any{"ok": true, "channel": channel, "ts": message.TS, "message": map[string]any{"text": message.Text}}
}

func (f *Fake) updateLocked(args map[string]any) map[string]any {
	channel, ts := str(args, "channel"), str(args, "ts")
	for _, message := range f.messages {
		if message.Channel != channel || message.TS != ts {
			continue
		}
		if message.Ephemeral {
			return fail("cant_update_message")
		}
		message.Text = str(args, "text")
		// As Slack does: blocks omitted, the blocks are removed.
		message.Blocks = blocksOf(args)
		message.Updates++
		return map[string]any{"ok": true, "channel": channel, "ts": ts, "text": message.Text}
	}
	return fail("message_not_found")
}

func reply(writer http.ResponseWriter, status int, body map[string]any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(body)
}

// readArgs reads a call's arguments from its body, JSON or form, and its
// query, as Slack reads them.
func readArgs(request *http.Request) map[string]any {
	args := map[string]any{}
	for name, values := range request.URL.Query() {
		args[name] = values[0]
	}
	body, _ := io.ReadAll(io.LimitReader(request.Body, 1<<20))
	switch mediaType, _, _ := strings.Cut(request.Header.Get("Content-Type"), ";"); strings.TrimSpace(mediaType) {
	case "application/json":
		var decoded map[string]any
		if json.Unmarshal(body, &decoded) == nil {
			for name, value := range decoded {
				args[name] = value
			}
		}
	case "application/x-www-form-urlencoded":
		if form, err := url.ParseQuery(string(body)); err == nil {
			for name, values := range form {
				args[name] = values[0]
			}
		}
	}
	return args
}

func str(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

func blocksOf(args map[string]any) []any {
	switch blocks := args["blocks"].(type) {
	case []any:
		return blocks
	case string: // a form call sends its blocks as a JSON string
		var decoded []any
		if json.Unmarshal([]byte(blocks), &decoded) == nil {
			return decoded
		}
	}
	return nil
}

func boolArg(args map[string]any, name string, fallback bool) bool {
	switch value := args[name].(type) {
	case bool:
		return value
	case string:
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
}

// ---- What Slack sends to Oryxa ----

// Mention is an @-mention of the bot.
type Mention struct {
	TeamID   string // DefaultTeamID when empty
	Channel  string // "C0GENERAL" when empty
	User     string // "U0ASKER" when empty
	Text     string
	TS       string // the message's ts; made up when empty
	ThreadTS string // set for a mention inside a thread
	// EventID is Slack's event id, which the ingress dedupes on; made up
	// when empty. Two mentions with the same id are one delivery.
	EventID string
	// ExtShared marks the delivery as coming from a Slack Connect channel.
	ExtShared bool
}

// Click is a click on a button of a message the bot posted.
type Click struct {
	TeamID    string // DefaultTeamID when empty
	User      string // who clicked; "U0OWNER" when empty
	Channel   string // where the message is; the user's DM with the bot when empty
	MessageTS string // the message's ts; made up when empty
	ActionID  string
	// Value is the button's value: for Oryxa's notices, an opaque ticket.
	Value string
	// TriggerID and ActionTS are made up when empty; they differ between
	// clicks, as they do in Slack, so two clicks are two deliveries.
	TriggerID, ActionTS string
}

func (f *Fake) nextID(prefix string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sequence++
	return fmt.Sprintf("%s%08d", prefix, f.sequence)
}

// Mention builds a signed app_mention delivery for the ingress at baseURL,
// which may be "" for a path alone.
func (f *Fake) Mention(baseURL string, m Mention) *http.Request {
	if m.TeamID == "" {
		m.TeamID = DefaultTeamID
	}
	if m.Channel == "" {
		m.Channel = "C0GENERAL"
	}
	if m.User == "" {
		m.User = "U0ASKER"
	}
	if m.TS == "" {
		m.TS = f.freshTS()
	}
	if m.EventID == "" {
		m.EventID = f.nextID("Ev")
	}
	event := map[string]any{
		"type": "app_mention", "user": m.User, "text": m.Text, "ts": m.TS, "channel": m.Channel,
		"event_ts": m.TS,
	}
	if m.ThreadTS != "" {
		event["thread_ts"] = m.ThreadTS
	}
	envelope := f.envelope(m.TeamID, m.EventID, event)
	if m.ExtShared {
		envelope["is_ext_shared_channel"] = true
	}
	return f.Event(baseURL, envelope)
}

// AppUninstalled builds a signed app_uninstalled delivery.
func (f *Fake) AppUninstalled(baseURL, teamID string) *http.Request {
	return f.Event(baseURL, f.envelope(teamID, f.nextID("Ev"), map[string]any{"type": "app_uninstalled"}))
}

// TokensRevoked builds a signed tokens_revoked delivery naming the bot users
// whose bot tokens were revoked; with none, only user tokens were.
func (f *Fake) TokensRevoked(baseURL, teamID string, botUserIDs ...string) *http.Request {
	tokens := map[string]any{"oauth": []string{"U0SOMEONE"}, "bot": []string{}}
	if len(botUserIDs) > 0 {
		tokens["bot"] = botUserIDs
	}
	return f.Event(baseURL, f.envelope(teamID, f.nextID("Ev"), map[string]any{"type": "tokens_revoked", "tokens": tokens}))
}

// Event builds a signed delivery to the events URL from a whole envelope,
// such as one of the other Events API types the ingress drops.
func (f *Fake) Event(baseURL string, envelope map[string]any) *http.Request {
	body, err := json.Marshal(envelope)
	if err != nil {
		panic(err)
	}
	return f.Signed(baseURL+EventsPath, "application/json", body)
}

// URLVerification builds the signed challenge Slack sends when the events
// URL is set.
func (f *Fake) URLVerification(baseURL, challenge string) *http.Request {
	body, _ := json.Marshal(map[string]any{"token": "legacy", "challenge": challenge, "type": "url_verification"})
	return f.Signed(baseURL+EventsPath, "application/json", body)
}

// Click builds a signed block_actions delivery for the interactivity URL:
// form-encoded, with the JSON in its payload field.
func (f *Fake) Click(baseURL string, c Click) *http.Request {
	if c.TeamID == "" {
		c.TeamID = DefaultTeamID
	}
	if c.User == "" {
		c.User = "U0OWNER"
	}
	if c.Channel == "" {
		c.Channel = DMChannel(c.User)
	}
	if c.MessageTS == "" {
		c.MessageTS = f.freshTS()
	}
	if c.TriggerID == "" {
		c.TriggerID = f.nextID("trigger.")
	}
	if c.ActionTS == "" {
		c.ActionTS = f.freshTS()
	}
	payload, err := json.Marshal(map[string]any{
		"type": "block_actions", "user": map[string]any{"id": c.User, "team_id": c.TeamID, "name": "someone"},
		"api_app_id": "A0FAKEAPP", "token": "legacy",
		"container":  map[string]any{"type": "message", "message_ts": c.MessageTS, "channel_id": c.Channel, "is_ephemeral": false},
		"trigger_id": c.TriggerID, "team": map[string]any{"id": c.TeamID, "domain": "acme"},
		"channel":      map[string]any{"id": c.Channel, "name": "directmessage"},
		"message":      map[string]any{"type": "message", "ts": c.MessageTS, "text": "notice"},
		"response_url": "https://hooks.slack.com/actions/A0FAKEAPP/1/never-used",
		"actions": []any{map[string]any{
			"action_id": c.ActionID, "block_id": "consent", "value": c.Value, "type": "button", "action_ts": c.ActionTS,
		}},
	})
	if err != nil {
		panic(err)
	}
	body := url.Values{"payload": {string(payload)}}.Encode()
	return f.Signed(baseURL+InteractivityPath, "application/x-www-form-urlencoded", []byte(body))
}

// Signed builds a POST of body to target, signed as Slack signs it, now.
func (f *Fake) Signed(target, contentType string, body []byte) *http.Request {
	request, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		panic(err)
	}
	request.Header.Set("Content-Type", contentType)
	f.Sign(request, f.now())
	return request
}

// Sign sets a request's timestamp and v0 signature for its body as it stands,
// as of at. Signing with a time from the past makes a stale delivery.
func (f *Fake) Sign(request *http.Request, at time.Time) {
	body := requestBody(request)
	stamp := strconv.FormatInt(at.Unix(), 10)
	request.Header.Set("X-Slack-Request-Timestamp", stamp)
	request.Header.Set("X-Slack-Signature", Signature(f.signingSecret, stamp, body))
}

// Signature is Slack's v0 signature: "v0=" and the hex HMAC-SHA256 of
// "v0:{timestamp}:{body}" under the signing secret.
func Signature(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("v0:" + timestamp + ":"))
	mac.Write(body)
	return "v0=" + hex.EncodeToString(mac.Sum(nil))
}

// requestBody reads a request's body without spending it.
func requestBody(request *http.Request) []byte {
	if request.GetBody == nil {
		return nil
	}
	body, err := request.GetBody()
	if err != nil {
		panic(err)
	}
	defer body.Close()
	content, _ := io.ReadAll(body)
	return content
}

func (f *Fake) freshTS() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nextTSLocked()
}

// envelope is the Events API wrapper Slack puts around an event.
func (f *Fake) envelope(teamID, eventID string, event map[string]any) map[string]any {
	return map[string]any{
		"token": "legacy", "team_id": teamID, "api_app_id": "A0FAKEAPP", "event": event, "type": "event_callback",
		"event_id": eventID, "event_time": f.now().Unix(),
		"authorizations": []any{map[string]any{
			"enterprise_id": nil, "team_id": teamID, "user_id": f.Workspace(teamID).BotUserID, "is_bot": true,
			"is_enterprise_install": false,
		}},
	}
}

// Serve sends a request to a handler and answers what it wrote, without a
// network.
func Serve(handler http.Handler, request *http.Request) *httptest.ResponseRecorder {
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// Send sends a request over the network, with client, or the default one.
func Send(client *http.Client, request *http.Request) (*http.Response, error) {
	if client == nil {
		client = http.DefaultClient
	}
	return client.Do(request)
}
