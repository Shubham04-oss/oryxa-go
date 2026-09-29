package slackrouter

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

const (
	testOrg     = "org_1"
	testTeam    = "T1"
	testBot     = "UBOT"
	testChannel = "C1"
	testAsker   = "UASKER"
)

// fakeSlack is apps' internal Slack API: the inbound rows it kept, the
// profiles and channels Slack would describe, and every message posted. Like
// apps, it sends one message for one idempotency key.
type fakeSlack struct {
	mu       sync.Mutex
	inbound  map[string]Inbound
	profiles map[string]Profile
	channels map[string]ChannelInfo
	posts    []Post
	keys     map[string]bool

	inboundErr, profilesErr, channelErr, postErr error
	// postErrFor fails only posts of one template.
	postErrFor map[string]error
	profileAsk [][]string
	channelGet []string
	inboundGet []string
	// onPost, when set, is told of each message as it is posted, before it is
	// kept.
	onPost func(Post)
}

func newFakeSlack() *fakeSlack {
	return &fakeSlack{
		inbound: map[string]Inbound{}, profiles: map[string]Profile{}, channels: map[string]ChannelInfo{},
		keys: map[string]bool{}, postErrFor: map[string]error{},
	}
}

func (slack *fakeSlack) Inbound(_ context.Context, id string) (Inbound, error) {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	slack.inboundGet = append(slack.inboundGet, id)
	if slack.inboundErr != nil {
		return Inbound{}, slack.inboundErr
	}
	inbound, ok := slack.inbound[id]
	if !ok {
		return Inbound{}, ErrGone
	}
	return inbound, nil
}

func (slack *fakeSlack) Profiles(_ context.Context, organizationID, teamID string, userIDs []string) ([]Profile, error) {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	slack.profileAsk = append(slack.profileAsk, append([]string(nil), userIDs...))
	if slack.profilesErr != nil {
		return nil, slack.profilesErr
	}
	var found []Profile
	for _, id := range userIDs {
		if profile, ok := slack.profiles[id]; ok {
			found = append(found, profile)
		}
	}
	return found, nil
}

func (slack *fakeSlack) Channel(_ context.Context, organizationID, channelID string) (ChannelInfo, error) {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	slack.channelGet = append(slack.channelGet, channelID)
	if slack.channelErr != nil {
		return ChannelInfo{}, slack.channelErr
	}
	info, ok := slack.channels[channelID]
	if !ok {
		return ChannelInfo{}, ErrGone
	}
	return info, nil
}

func (slack *fakeSlack) Post(_ context.Context, post Post) error {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	if slack.onPost != nil {
		slack.onPost(post)
	}
	if err := slack.postErrFor[post.Template]; err != nil {
		return err
	}
	if slack.postErr != nil {
		return slack.postErr
	}
	if slack.keys[post.IdempotencyKey] {
		return nil
	}
	slack.keys[post.IdempotencyKey] = true
	slack.posts = append(slack.posts, post)
	return nil
}

func (slack *fakeSlack) sent() []Post {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	return append([]Post(nil), slack.posts...)
}

func (slack *fakeSlack) channelReads() int {
	slack.mu.Lock()
	defer slack.mu.Unlock()
	return len(slack.channelGet)
}

// fakeMembers is auth's tables: members by organization and address.
type fakeMembers struct {
	mu      sync.Mutex
	members map[string]Member
	err     error
	asked   []string
	// orgNames answers OrganizationName; nameErr fails it.
	orgNames map[string]string
	nameErr  error
}

func (members *fakeMembers) add(organizationID string, member Member) {
	members.mu.Lock()
	defer members.mu.Unlock()
	if members.members == nil {
		members.members = map[string]Member{}
	}
	members.members[organizationID+"|"+strings.ToLower(member.Email)] = member
}

func (members *fakeMembers) ByEmail(_ context.Context, organizationID, email string) (Member, error) {
	members.mu.Lock()
	defer members.mu.Unlock()
	members.asked = append(members.asked, email)
	if members.err != nil {
		return Member{}, members.err
	}
	member, ok := members.members[organizationID+"|"+strings.ToLower(email)]
	if !ok {
		return Member{}, ErrNotMember
	}
	return member, nil
}

func (members *fakeMembers) OrganizationName(_ context.Context, organizationID string) (string, error) {
	members.mu.Lock()
	defer members.mu.Unlock()
	if members.nameErr != nil {
		return "", members.nameErr
	}
	name, ok := members.orgNames[organizationID]
	if !ok {
		return "", ErrGone
	}
	return name, nil
}

// fakeIdentities keeps the matches, as slack_identities does: one per Slack
// user, and one Slack user per member of a workspace.
type fakeIdentities struct {
	mu      sync.Mutex
	kept    map[string]Identity
	deleted []string
	saveErr error
	delErr  error
}

func (identities *fakeIdentities) SaveIdentity(_ context.Context, identity Identity) error {
	identities.mu.Lock()
	defer identities.mu.Unlock()
	if identities.saveErr != nil {
		return identities.saveErr
	}
	if identities.kept == nil {
		identities.kept = map[string]Identity{}
	}
	for key, other := range identities.kept {
		if other.TeamID == identity.TeamID && other.MemberUserID == identity.MemberUserID && other.SlackUserID != identity.SlackUserID {
			delete(identities.kept, key)
		}
	}
	identities.kept[identity.TeamID+"|"+identity.SlackUserID] = identity
	return nil
}

func (identities *fakeIdentities) DeleteIdentity(_ context.Context, teamID, slackUserID string) error {
	identities.mu.Lock()
	defer identities.mu.Unlock()
	if identities.delErr != nil {
		return identities.delErr
	}
	delete(identities.kept, teamID+"|"+slackUserID)
	identities.deleted = append(identities.deleted, teamID+"|"+slackUserID)
	return nil
}

func (identities *fakeIdentities) get(teamID, slackUserID string) (Identity, bool) {
	identities.mu.Lock()
	defer identities.mu.Unlock()
	identity, ok := identities.kept[teamID+"|"+slackUserID]
	return identity, ok
}

type consentCall struct{ Ticket, Clicker, Surface string }

// fakeAsks is the consent core: it keeps each ask it made, and makes the same
// one again for the same origin, as CreateAsk does.
type fakeAsks struct {
	mu      sync.Mutex
	created []AskInput
	byKey   map[string]Ask
	open    map[string]int
	consent []consentCall

	createCalls, countCalls int
	createErr, countErr     error
	consentErr              error
	// onCreate, when set, is told of each ask as it is filed.
	onCreate func(AskInput)
}

func (asks *fakeAsks) CreateAsk(_ context.Context, input AskInput) (Ask, error) {
	asks.mu.Lock()
	defer asks.mu.Unlock()
	asks.createCalls++
	if asks.onCreate != nil {
		asks.onCreate(input)
	}
	if asks.createErr != nil {
		return Ask{}, asks.createErr
	}
	key := input.Origin.TeamID + ":" + input.Origin.ChannelID + ":" + input.Origin.TriggerTS
	if existing, ok := asks.byKey[key]; ok {
		return existing, nil
	}
	if asks.byKey == nil {
		asks.byKey = map[string]Ask{}
	}
	ask := Ask{ID: fmt.Sprintf("ask_%d", len(asks.byKey)+1)}
	asks.byKey[key] = ask
	asks.created = append(asks.created, input)
	return ask, nil
}

func (asks *fakeAsks) CountOpenAsks(_ context.Context, organizationID, askerUserID string) (int, error) {
	asks.mu.Lock()
	defer asks.mu.Unlock()
	asks.countCalls++
	if asks.countErr != nil {
		return 0, asks.countErr
	}
	return asks.open[askerUserID], nil
}

func (asks *fakeAsks) Consent(_ context.Context, ticket, clickerUserID, surface string) error {
	asks.mu.Lock()
	defer asks.mu.Unlock()
	asks.consent = append(asks.consent, consentCall{ticket, clickerUserID, surface})
	return asks.consentErr
}

// fakeAuthority answers Outranks from a table of "asker>owner", and records
// each question.
type fakeAuthority struct {
	mu       sync.Mutex
	outranks map[string]bool
	err      error
	asked    []string
}

func (authority *fakeAuthority) Outranks(_ context.Context, organizationID string, asker, owner Person) (bool, error) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.asked = append(authority.asked, asker.UserID+">"+owner.UserID)
	if authority.err != nil {
		return false, authority.err
	}
	return authority.outranks[asker.UserID+">"+owner.UserID], nil
}

// fakeScreen is the screen: it clears every ask unless told otherwise, says
// which owners' departments do not fit, or fails, and remembers what it was
// asked to judge.
type fakeScreen struct {
	mu      sync.Mutex
	verdict consentpolicy.Screen
	misfits map[string]bool
	err     error
	judged  []ScreenInput
}

func newFakeScreen() *fakeScreen { return &fakeScreen{verdict: consentpolicy.Cleared} }

func (screen *fakeScreen) Judge(_ context.Context, input ScreenInput) (ScreenResult, error) {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	screen.judged = append(screen.judged, input)
	if screen.err != nil {
		return ScreenResult{}, screen.err
	}
	return ScreenResult{Verdict: screen.verdict, Misfits: screen.misfits}, nil
}

func (screen *fakeScreen) calls() []ScreenInput {
	screen.mu.Lock()
	defer screen.mu.Unlock()
	return append([]ScreenInput(nil), screen.judged...)
}

// fakeDirectory is the organization directory as one asker sees it: the teams
// each member is on that the asker may see, and which askers have given agents
// standing access. Nobody is on any team unless a test says so, which is a
// department nobody knows.
type fakeDirectory struct {
	mu      sync.Mutex
	teams   map[string][]string
	noGrant map[string]bool
	err     error
	reads   []directoryRead
	// onRead, when set, is told of each read before it is answered.
	onRead func()
}

// directoryRead is one read of the directory: who it was read as, and about whom.
type directoryRead struct {
	Organization, Asker string
	UserIDs             []string
}

func (directory *fakeDirectory) Teams(_ context.Context, organizationID string, asker Person, userIDs []string) (map[string][]string, error) {
	directory.mu.Lock()
	defer directory.mu.Unlock()
	if directory.onRead != nil {
		directory.onRead()
	}
	directory.reads = append(directory.reads, directoryRead{organizationID, asker.UserID, append([]string(nil), userIDs...)})
	if directory.noGrant[asker.UserID] {
		return nil, fmt.Errorf("agent token: %w", ErrNoGrant)
	}
	if directory.err != nil {
		return nil, directory.err
	}
	found := map[string][]string{}
	for _, id := range userIDs {
		if teams, ok := directory.teams[id]; ok {
			found[id] = append([]string(nil), teams...)
		}
	}
	return found, nil
}

func (directory *fakeDirectory) read() []directoryRead {
	directory.mu.Lock()
	defer directory.mu.Unlock()
	return append([]directoryRead(nil), directory.reads...)
}

type fakeAsked struct {
	mu    sync.Mutex
	asks  map[string]string
	err   error
	calls int
}

func (asked *fakeAsked) MarkAsked(_ context.Context, inboundID, askID string) error {
	asked.mu.Lock()
	defer asked.mu.Unlock()
	asked.calls++
	if asked.err != nil {
		return asked.err
	}
	if asked.asks == nil {
		asked.asks = map[string]string{}
	}
	asked.asks[inboundID] = askID
	return nil
}

// harness is a router wired to fakes, with a workspace of members already in
// it. Every Slack user in it belongs to team T1 and every member to org_1.
type harness struct {
	t          *testing.T
	slack      *fakeSlack
	members    *fakeMembers
	identities *fakeIdentities
	asks       *fakeAsks
	authority  *fakeAuthority
	asked      *fakeAsked
	screen     *fakeScreen
	directory  *fakeDirectory
	router     *Router
	seq        int
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{
		t: t, slack: newFakeSlack(), members: &fakeMembers{}, identities: &fakeIdentities{},
		asks: &fakeAsks{open: map[string]int{}}, authority: &fakeAuthority{outranks: map[string]bool{}},
		asked: &fakeAsked{}, screen: newFakeScreen(),
		directory: &fakeDirectory{teams: map[string][]string{}, noGrant: map[string]bool{}},
	}
	h.slack.channels[testChannel] = ChannelInfo{ID: testChannel}
	h.members.orgNames = map[string]string{testOrg: "Acme"}
	h.rebuild()
	h.member(testAsker, "Ada")
	return h
}

func (h *harness) rebuild() {
	router, err := NewRouter(Deps{
		Slack: h.slack, Members: h.members, Identities: h.identities, Asks: h.asks,
		Authority: h.authority, Asked: h.asked, Screen: h.screen, Directory: h.directory,
	})
	if err != nil {
		h.t.Fatal(err)
	}
	h.router = router
}

// member adds a person who is a full member of the workspace with a confirmed
// address, and a member of the organization on that address. Their member id
// is "usr_" and their name in lower case; their address is that at acme.com.
func (h *harness) member(slackID, name string) Person {
	h.t.Helper()
	lower := strings.ToLower(name)
	email := lower + "@acme.com"
	h.slack.profiles[slackID] = Profile{
		UserID: slackID, TeamID: testTeam, Email: email, EmailConfirmed: true, Name: name,
	}
	member := Member{UserID: "usr_" + lower, Email: email, DisplayName: name, Role: "member"}
	h.members.add(testOrg, member)
	return Person{UserID: member.UserID, Email: email, Name: name, SlackUserID: slackID}
}

// profile changes what Slack says of a user.
func (h *harness) profile(slackID string, change func(*Profile)) {
	h.t.Helper()
	profile, ok := h.slack.profiles[slackID]
	if !ok {
		h.t.Fatalf("no profile for %s", slackID)
	}
	change(&profile)
	h.slack.profiles[slackID] = profile
}

// mention records a message by the asker that mentions the bot and returns
// its job, as apps and the bus would have.
func (h *harness) mention(text string, changes ...func(*Mention)) Job {
	h.t.Helper()
	h.seq++
	id := fmt.Sprintf("in_%d", h.seq)
	mention := &Mention{
		EventType: appMention, ChannelID: testChannel, UserID: testAsker, Text: text,
		TS: fmt.Sprintf("1700000000.%06d", h.seq), BotUserID: testBot,
	}
	for _, change := range changes {
		change(mention)
	}
	h.slack.inbound[id] = Inbound{
		ID: id, OrganizationID: testOrg, TeamID: testTeam, Kind: KindMessage,
		ReceivedAt: time.Unix(1700000000, 0).UTC(), Mention: mention,
	}
	return Job{
		InboundID: id, Kind: KindMessage, OrganizationID: testOrg, TeamID: testTeam,
		EventID: "evt_" + id, ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
}

// click records a click by a Slack user and returns its job.
func (h *harness) click(userID, ticket string) Job {
	h.t.Helper()
	h.seq++
	id := fmt.Sprintf("in_%d", h.seq)
	h.slack.inbound[id] = Inbound{
		ID: id, OrganizationID: testOrg, TeamID: testTeam, Kind: KindAction,
		ReceivedAt: time.Unix(1700000000, 0).UTC(),
		Click:      &Click{UserID: userID, ChannelID: "D1", MessageTS: "1700000001.000001", ActionID: "consent", Value: ticket},
	}
	return Job{
		InboundID: id, Kind: KindAction, OrganizationID: testOrg, TeamID: testTeam,
		EventID: "evt_" + id, ReceivedAt: time.Unix(1700000000, 0).UTC(),
	}
}

func (h *harness) run(job Job) error {
	h.t.Helper()
	return h.router.Handle(context.Background(), job)
}

// mustRun runs a job that must finish.
func (h *harness) mustRun(job Job) {
	h.t.Helper()
	if err := h.run(job); err != nil {
		h.t.Fatalf("job failed: %v", err)
	}
}

// only returns the one message posted, whatever it was, and fails if there
// was not exactly one.
func (h *harness) only() Post {
	h.t.Helper()
	posts := h.slack.sent()
	if len(posts) != 1 {
		h.t.Fatalf("posted %d messages, want 1: %+v", len(posts), posts)
	}
	return posts[0]
}

// refusal returns the one refusal posted, and fails unless it was the only
// message and nothing was asked.
func (h *harness) refusal(job Job) (Post, RefusalParams) {
	h.t.Helper()
	post := h.only()
	if post.Template != TemplateRefusal {
		h.t.Fatalf("posted %q, want a refusal", post.Template)
	}
	h.noAsk()
	params, ok := post.Params.(RefusalParams)
	if !ok {
		h.t.Fatalf("refusal params are %T", post.Params)
	}
	wantKey := "slackrouter:" + job.InboundID + ":refusal"
	if params.UserID != "" {
		wantKey += ":" + params.UserID
	}
	if post.OrganizationID != testOrg || post.IdempotencyKey != wantKey {
		h.t.Fatalf("refusal = %+v, want key %q", post, wantKey)
	}
	return post, params
}

// refusals returns every refusal posted, in order, and fails if anything else
// was.
func (h *harness) refusals(job Job) []RefusalParams {
	h.t.Helper()
	var all []RefusalParams
	for _, post := range h.slack.sent() {
		if post.Template != TemplateRefusal {
			h.t.Fatalf("posted %q, want refusals only", post.Template)
		}
		params := post.Params.(RefusalParams)
		if post.IdempotencyKey != "slackrouter:"+job.InboundID+":refusal:"+params.UserID {
			h.t.Fatalf("refusal key %q for %+v", post.IdempotencyKey, params)
		}
		all = append(all, params)
	}
	return all
}

func (h *harness) noAsk() {
	h.t.Helper()
	if len(h.asks.created) != 0 {
		h.t.Fatalf("an ask was made: %+v", h.asks.created)
	}
}

// ask returns the one ask made.
func (h *harness) ask() AskInput {
	h.t.Helper()
	if len(h.asks.created) != 1 {
		h.t.Fatalf("made %d asks, want 1: %+v", len(h.asks.created), h.asks.created)
	}
	return h.asks.created[0]
}

// ack returns the acknowledgement posted for an ask, whatever else was
// posted with it.
func (h *harness) ack(job Job) (Post, AskAckParams) {
	h.t.Helper()
	var found []Post
	for _, post := range h.slack.sent() {
		if post.Template == TemplateAskAck {
			found = append(found, post)
		}
	}
	if len(found) != 1 {
		h.t.Fatalf("posted %d acknowledgements, want 1: %+v", len(found), h.slack.sent())
	}
	params, ok := found[0].Params.(AskAckParams)
	if !ok {
		h.t.Fatalf("ack params are %T", found[0].Params)
	}
	if found[0].IdempotencyKey != "slackrouter:"+job.InboundID+":ask_ack" || found[0].OrganizationID != testOrg {
		h.t.Fatalf("ack = %+v", found[0])
	}
	return found[0], params
}

// wire is what an adapter would put on the wire for a post's parameters.
func wire(t *testing.T, params any) string {
	t.Helper()
	encoded, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func bypassOf(owner AskOwner) consentpolicy.Bypass {
	if owner.Bypass == nil {
		return consentpolicy.Bypass{}
	}
	return *owner.Bypass
}

// toBot is a message addressed to the bot, as Slack writes it.
func toBot(text string) string { return "<@" + testBot + "> " + text }

func nopLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }
