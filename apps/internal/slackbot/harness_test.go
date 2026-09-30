package slackbot

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	appsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/apps/v1"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/authclient"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/domain"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/hooks"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/internal/secrets"
	"github.com/Shubham04-oss/oryxa-go/internal/slackfake"
)

const (
	testOrg     = "org_1"
	testBaseURL = "https://api.oryxa.test"
	testWebURL  = "https://app.oryxa.test"
)

var testStart = time.Date(2026, time.September, 29, 12, 0, 0, 0, time.UTC)

// testClock is a clock a test moves.
type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(by time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(by)
}

// memoryStore is Store in memory, keeping the rules apps' Postgres store
// keeps: one transaction per delivery, the active install unique per
// organization and per workspace, a claim taken once. It is also as much of
// hooks.Store as the ack path uses, and the hooks methods it does not
// implement panic on the embedded nil, which is the test saying the ingress
// touched something it should not.
type memoryStore struct {
	hooks.Store

	mu        sync.Mutex
	installs  map[string]Install
	flows     map[string]InstallFlow
	inbound   map[string]Inbound
	handled   map[string]bool
	events    []domain.Event
	posts     map[string]Post
	profiles  map[string]Profile
	marks     map[string]MarkedChannel
	endpoints map[string]hooks.Endpoint

	// gate, when set, is waited on inside RecordInbound before it writes, to
	// hold a delivery in the middle of its transaction, and arrivals hears of
	// each delivery that reaches it.
	gate     chan struct{}
	arrivals chan struct{}
	// deadline is the context deadline the last RecordInbound was given.
	deadline time.Time
	limited  bool
	// fail is an error the next store call answers.
	fail error
}

func newMemoryStore() *memoryStore {
	return &memoryStore{
		installs: map[string]Install{}, flows: map[string]InstallFlow{}, inbound: map[string]Inbound{},
		handled: map[string]bool{}, posts: map[string]Post{}, profiles: map[string]Profile{},
		marks: map[string]MarkedChannel{}, endpoints: map[string]hooks.Endpoint{},
	}
}

func (m *memoryStore) failure() error {
	err := m.fail
	m.fail = nil
	return err
}

// ---- hooks.Store, as far as the ingress goes ----

func (m *memoryStore) ActiveHookEndpoint(_ context.Context, scope, organizationID, toolkit string) (hooks.Endpoint, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, endpoint := range m.endpoints {
		if endpoint.Scope == scope && endpoint.OrganizationID == organizationID && endpoint.Toolkit == toolkit {
			return endpoint, nil
		}
	}
	return hooks.Endpoint{}, domain.ErrNotFound
}

func (m *memoryStore) CreateHookEndpoint(_ context.Context, endpoint hooks.Endpoint) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, other := range m.endpoints {
		if other.Scope == endpoint.Scope && other.Toolkit == endpoint.Toolkit && other.OrganizationID == endpoint.OrganizationID {
			return domain.ErrConflict
		}
	}
	m.endpoints[endpoint.ID] = endpoint
	return nil
}

func (m *memoryStore) MarkHookValidated(_ context.Context, id string, _, now time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	endpoint, ok := m.endpoints[id]
	if !ok || endpoint.ValidatedAt != nil {
		return false, nil
	}
	endpoint.ValidatedAt = &now
	m.endpoints[id] = endpoint
	return true, nil
}

func (m *memoryStore) HookDeliveryHandled(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failure(); err != nil {
		return false, err
	}
	return m.handled[key], nil
}

// ---- Store ----

func (m *memoryStore) InstallByTeam(_ context.Context, teamID string) (Install, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failure(); err != nil {
		return Install{}, err
	}
	install, ok := m.installs[teamID]
	if !ok {
		return Install{}, domain.ErrNotFound
	}
	return install, nil
}

func (m *memoryStore) InstallForOrganization(_ context.Context, organizationID string) (Install, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *Install
	for _, install := range m.installs {
		if install.OrganizationID != organizationID {
			continue
		}
		install := install
		if found == nil || (install.Active() && !found.Active()) || (install.Active() == found.Active() && install.UpdatedAt.After(found.UpdatedAt)) {
			found = &install
		}
	}
	if found == nil {
		return Install{}, domain.ErrNotFound
	}
	return *found, nil
}

func (m *memoryStore) InstallStatusForOrganization(ctx context.Context, organizationID string) (string, error) {
	m.mu.Lock()
	err := m.failure()
	m.mu.Unlock()
	if err != nil {
		return "", err
	}
	install, err := m.InstallForOrganization(ctx, organizationID)
	return install.Status, err
}

func (m *memoryStore) SaveInstall(_ context.Context, install Install, event func(bool) (domain.Event, error)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	existing, exists := m.installs[install.TeamID]
	if exists && existing.Active() && existing.OrganizationID != install.OrganizationID {
		return ErrTeamBound
	}
	for team, other := range m.installs {
		if team != install.TeamID && other.OrganizationID == install.OrganizationID && other.Active() {
			return ErrOrganizationBound
		}
	}
	reinstalled := exists && existing.Active()
	notice, err := event(reinstalled)
	if err != nil {
		return err
	}
	if reinstalled {
		install.InstalledAt = existing.InstalledAt
	}
	install.Status, install.EndReason, install.EndedAt = appsv1.SlackInstallActive, "", nil
	m.installs[install.TeamID] = install
	m.events = append(m.events, notice)
	return nil
}

func (m *memoryStore) endLocked(teamID, reason string, now time.Time) (Install, bool) {
	install, ok := m.installs[teamID]
	if !ok || !install.Active() {
		return Install{}, false
	}
	install.Status, install.EndReason, install.Sealed, install.EndedAt, install.UpdatedAt = appsv1.SlackInstallEnded, reason, nil, &now, now
	m.installs[teamID] = install
	return install, true
}

func (m *memoryStore) EndInstall(_ context.Context, teamID, reason string, now time.Time, event func(Install) (domain.Event, error)) (Install, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ended, ok := m.endLocked(teamID, reason, now)
	if !ok {
		return Install{}, false, nil
	}
	notice, err := event(ended)
	if err != nil {
		return Install{}, false, err
	}
	m.events = append(m.events, notice)
	return ended, true, nil
}

func (m *memoryStore) InsertInstallFlow(_ context.Context, flow InstallFlow) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.flows[flow.StateHash] = flow
	return nil
}

func (m *memoryStore) ConsumeInstallFlow(_ context.Context, stateHash string, now time.Time) (InstallFlow, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	flow, ok := m.flows[stateHash]
	if !ok || flow.ConsumedAt != nil || !flow.ExpiresAt.After(now) {
		return InstallFlow{}, domain.ErrNotFound
	}
	flow.ConsumedAt = &now
	m.flows[stateHash] = flow
	return flow, nil
}

func (m *memoryStore) RecordInbound(ctx context.Context, mark string, _ time.Time, inbound Inbound, event domain.Event) (bool, error) {
	m.mu.Lock()
	m.deadline, m.limited = ctx.Deadline()
	gate, arrivals := m.gate, m.arrivals
	m.mu.Unlock()
	if gate != nil {
		if arrivals != nil {
			arrivals <- struct{}{}
		}
		select {
		case <-gate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.failure(); err != nil {
		return false, err
	}
	if m.handled[mark] {
		return false, nil
	}
	m.handled[mark] = true
	m.inbound[inbound.ID] = inbound
	m.events = append(m.events, event)
	return true, nil
}

func (m *memoryStore) RecordInstallEnd(_ context.Context, mark string, at time.Time, teamID, reason string, event func(Install) (domain.Event, error)) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.handled[mark] {
		return false, nil
	}
	m.handled[mark] = true
	ended, ok := m.endLocked(teamID, reason, at)
	if !ok {
		return false, nil
	}
	notice, err := event(ended)
	if err != nil {
		return false, err
	}
	m.events = append(m.events, notice)
	return true, nil
}

func (m *memoryStore) Inbound(_ context.Context, id string) (Inbound, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	inbound, ok := m.inbound[id]
	if !ok {
		return Inbound{}, domain.ErrNotFound
	}
	return inbound, nil
}

func (m *memoryStore) ThreadSeen(_ context.Context, organizationID, teamID, channelID, ts string) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var root string
	var latest time.Time
	for _, inbound := range m.inbound {
		if inbound.OrganizationID == organizationID && inbound.TeamID == teamID && inbound.ChannelID == channelID &&
			(inbound.ThreadTS == ts || inbound.MessageTS == ts) && !inbound.ReceivedAt.Before(latest) {
			root, latest = inbound.ThreadTS, inbound.ReceivedAt
		}
	}
	return root, root != "", nil
}

func (m *memoryStore) UserSeen(_ context.Context, organizationID, teamID, userID string, since time.Time) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, inbound := range m.inbound {
		if inbound.OrganizationID == organizationID && inbound.TeamID == teamID && !inbound.ReceivedAt.Before(since) &&
			(inbound.UserID == userID || slices.Contains(inbound.Mentions, userID)) {
			return true, nil
		}
	}
	return false, nil
}

func (m *memoryStore) ClaimPost(_ context.Context, post Post, staleBefore time.Time) (Post, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := post.OrganizationID + "/" + post.Key
	held, ok := m.posts[key]
	if ok && !(held.CompletedAt == nil && held.ClaimedAt.Before(staleBefore) && held.RequestHash == post.RequestHash) {
		return held, false, nil
	}
	m.posts[key] = post
	return post, true, nil
}

func (m *memoryStore) CompletePost(_ context.Context, organizationID, key, channelID, ts string, at time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	post, ok := m.posts[organizationID+"/"+key]
	if !ok {
		return domain.ErrNotFound
	}
	post.ChannelID, post.TS, post.CompletedAt = channelID, ts, &at
	m.posts[organizationID+"/"+key] = post
	return nil
}

func (m *memoryStore) ReleasePost(_ context.Context, organizationID, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if post, ok := m.posts[organizationID+"/"+key]; ok && post.CompletedAt == nil {
		delete(m.posts, organizationID+"/"+key)
	}
	return nil
}

func (m *memoryStore) PostAt(_ context.Context, organizationID, channelID, ts string) (Post, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, post := range m.posts {
		if post.OrganizationID == organizationID && post.ChannelID == channelID && post.TS == ts && post.CompletedAt != nil {
			return post, nil
		}
	}
	return Post{}, domain.ErrNotFound
}

func (m *memoryStore) Profiles(_ context.Context, teamID string, userIDs []string, fetchedAfter time.Time) ([]Profile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []Profile
	for _, id := range userIDs {
		if profile, ok := m.profiles[teamID+"/"+id]; ok && profile.FetchedAt.After(fetchedAfter) {
			found = append(found, profile)
		}
	}
	return found, nil
}

func (m *memoryStore) SaveProfile(_ context.Context, profile Profile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.profiles[profile.TeamID+"/"+profile.UserID] = profile
	return nil
}

func (m *memoryStore) HighPriorityChannels(_ context.Context, organizationID, teamID string) ([]MarkedChannel, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var marked []MarkedChannel
	for key, channel := range m.marks {
		if key == organizationID+"/"+teamID+"/"+channel.ChannelID {
			marked = append(marked, channel)
		}
	}
	slices.SortFunc(marked, func(a, b MarkedChannel) int { return compareStrings(a.ChannelID, b.ChannelID) })
	return marked, nil
}

func compareStrings(a, b string) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (m *memoryStore) IsHighPriority(_ context.Context, organizationID, teamID, channelID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.marks[organizationID+"/"+teamID+"/"+channelID]
	return ok, nil
}

func (m *memoryStore) ReplaceHighPriority(_ context.Context, organizationID, teamID, actor string, channels []MarkedChannel, now time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prefix := organizationID + "/" + teamID + "/"
	next := map[string]MarkedChannel{}
	for _, channel := range channels {
		key := prefix + channel.ChannelID
		if before, ok := m.marks[key]; ok {
			channel.MarkedBy, channel.MarkedAt = before.MarkedBy, before.MarkedAt
		}
		next[key] = channel
	}
	for key := range m.marks {
		if len(key) >= len(prefix) && key[:len(prefix)] == prefix {
			delete(m.marks, key)
		}
	}
	maps.Copy(m.marks, next)
	return nil
}

func (m *memoryStore) Purge(_ context.Context, cutoffs Cutoffs) (Purged, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var purged Purged
	for id, inbound := range m.inbound {
		if inbound.ReceivedAt.Before(cutoffs.Inbound) {
			delete(m.inbound, id)
			purged.Inbound++
		}
	}
	for hash, flow := range m.flows {
		if flow.ExpiresAt.Before(cutoffs.Flows) {
			delete(m.flows, hash)
			purged.Flows++
		}
	}
	for key, post := range m.posts {
		if post.ClaimedAt.Before(cutoffs.Posts) {
			delete(m.posts, key)
			purged.Posts++
		}
	}
	for key, profile := range m.profiles {
		if profile.FetchedAt.Before(cutoffs.Profiles) {
			delete(m.profiles, key)
			purged.Profiles++
		}
	}
	return purged, nil
}

// ---- Views on what was recorded ----

func (m *memoryStore) inboundRows() []Inbound {
	m.mu.Lock()
	defer m.mu.Unlock()
	rows := make([]Inbound, 0, len(m.inbound))
	for _, inbound := range m.inbound {
		rows = append(rows, inbound)
	}
	slices.SortFunc(rows, func(a, b Inbound) int { return a.ReceivedAt.Compare(b.ReceivedAt) })
	return rows
}

func (m *memoryStore) published() []domain.Event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.events)
}

func (m *memoryStore) publishedOf(detailType string) []domain.Event {
	var found []domain.Event
	for _, event := range m.published() {
		if event.DetailType == detailType {
			found = append(found, event)
		}
	}
	return found
}

func (m *memoryStore) install(teamID string) Install {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.installs[teamID]
}

// ---- KMS and the members' directory ----

// fakeKeys stands in for KMS: a data key unwraps only with the encryption
// context it was made for, and every unwrap is counted.
type fakeKeys struct {
	mu       sync.Mutex
	decrypts int
	broken   bool
}

type wrappedKey struct {
	Context map[string]string `json:"context"`
	Key     []byte            `json:"key"`
}

func (k *fakeKeys) GenerateDataKey(_ context.Context, _ string, encryptionContext map[string]string) ([]byte, []byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.broken {
		return nil, nil, errors.New("kms is down")
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, nil, err
	}
	wrapped, err := json.Marshal(wrappedKey{Context: maps.Clone(encryptionContext), Key: key})
	return append([]byte(nil), key...), wrapped, err
}

func (k *fakeKeys) DecryptDataKey(_ context.Context, encrypted []byte, encryptionContext map[string]string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.decrypts++
	if k.broken {
		return nil, errors.New("kms is down")
	}
	var wrapped wrappedKey
	if err := json.Unmarshal(encrypted, &wrapped); err != nil || !maps.Equal(wrapped.Context, encryptionContext) {
		return nil, errors.New("kms context mismatch")
	}
	return wrapped.Key, nil
}

func (k *fakeKeys) opened() int {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.decrypts
}

// fakeDirectory is auth: it knows who each credential is.
type fakeDirectory struct {
	members map[string]domain.OrgMembership
}

func (d *fakeDirectory) Membership(_ context.Context, organizationID string, credentials authclient.Credentials) (domain.OrgMembership, error) {
	if !credentials.Present() {
		return domain.OrgMembership{}, domain.ErrUnauthorized
	}
	member, ok := d.members[credentials.Authorization]
	if !ok || member.OrganizationID != organizationID {
		return domain.OrgMembership{}, domain.ErrForbidden
	}
	return member, nil
}

// bearer is credentials for one of the directory's people.
func bearer(name string) authclient.Credentials {
	return authclient.Credentials{Authorization: name}
}

// noSubscriptions is hooks' subscriptions, for a surface that has none.
type noSubscriptions struct{}

func (noSubscriptions) ForEndpoint(context.Context, hooks.Endpoint, string) ([]hooks.Subscription, error) {
	return nil, nil
}

func (noSubscriptions) ForAccount(context.Context, string, string, string, string) ([]hooks.Subscription, error) {
	return nil, nil
}

// ---- The harness ----

// harness is the surface assembled on fakes and mounted as apps mounts it:
// hooks in front, the service behind, the fake Slack on the other side.
type harness struct {
	t         *testing.T
	fake      *slackfake.Fake
	store     *memoryStore
	keys      *fakeKeys
	clock     *testClock
	directory *fakeDirectory
	service   *Service
	hooks     *hooks.Service
	mux       *http.ServeMux
	logs      *bytes.Buffer
}

type harnessOptions struct {
	surfaceDeadline time.Duration
	config          func(*Config)
}

func newHarness(t *testing.T, options ...func(*harnessOptions)) *harness {
	t.Helper()
	var settings harnessOptions
	for _, option := range options {
		option(&settings)
	}
	h := &harness{
		t: t, store: newMemoryStore(), keys: &fakeKeys{}, clock: &testClock{now: testStart}, logs: &bytes.Buffer{},
		directory: &fakeDirectory{members: map[string]domain.OrgMembership{
			"admin":  {OrganizationID: testOrg, UserID: "user-admin", Role: domain.OrgRoleAdmin},
			"owner":  {OrganizationID: testOrg, UserID: "user-owner", Role: domain.OrgRoleOwner},
			"member": {OrganizationID: testOrg, UserID: "user-member", Role: domain.OrgRoleMember},
			"agent":  {OrganizationID: testOrg, UserID: "user-admin", Role: domain.OrgRoleAdmin, Agent: true},
		}},
	}
	h.fake = slackfake.New(t, slackfake.WithClock(h.clock.Now))
	logger := slog.New(slog.NewJSONHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	envelope := secrets.Envelope{Keys: h.keys, KeyID: "alias/test", Service: "oryxa-apps"}
	config := Config{
		PublicBaseURL: testBaseURL, APIBaseURL: h.fake.URL(), WebOrigins: []string{testWebURL},
		App: App{
			ClientID: h.fake.ClientID(), ClientSecret: secrets.Secret(h.fake.ClientSecret()),
			SigningSecret: secrets.Secret(h.fake.SigningSecret()),
		},
		ReturnAllowed: func(target string) bool { return target == testWebURL+"/settings" },
	}
	if settings.config != nil {
		settings.config(&config)
	}
	service, err := New(Dependencies{
		Store: h.store, Sealer: envelope, Directory: h.directory, Logger: logger, Clock: h.clock.Now,
	}, config)
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	h.service = service
	hookService, err := hooks.New(hooks.Dependencies{
		Store: h.store, Sealer: envelope, Integrations: integration.NewRegistry(), Subscriptions: noSubscriptions{},
		Surfaces: service, Logger: logger, Clock: h.clock.Now,
	}, hooks.Config{
		PublicBaseURL:     testBaseURL,
		DeploymentSecrets: map[string][]secrets.Secret{Toolkit: {secrets.Secret(h.fake.SigningSecret())}},
		SurfaceDeadline:   settings.surfaceDeadline,
	})
	if err != nil {
		t.Fatalf("hooks.New() = %v", err)
	}
	h.hooks = hookService
	h.mux = http.NewServeMux()
	h.mux.Handle("POST "+EventsPath, hookService.Alias(Toolkit, KindEvents))
	h.mux.Handle("POST "+InteractivityPath, hookService.Alias(Toolkit, KindInteractivity))
	return h
}

// serve sends a request Slack would send to the ingress.
func (h *harness) serve(request *http.Request) *httptest.ResponseRecorder {
	h.t.Helper()
	return slackfake.Serve(h.mux, request)
}

// installFor makes the fake's default workspace the organization's active
// install, as a finished install leaves it, without going through Slack.
func (h *harness) installFor(organizationID string) Install {
	h.t.Helper()
	return h.installTeam(organizationID, slackfake.DefaultTeamID)
}

func (h *harness) installTeam(organizationID, teamID string) Install {
	h.t.Helper()
	workspace := h.fake.Workspace(teamID)
	sealed, err := h.service.sealer.Seal(context.Background(), tokenBinding(teamID), []byte(workspace.BotToken))
	if err != nil {
		h.t.Fatal(err)
	}
	install := Install{
		TeamID: teamID, OrganizationID: organizationID, Status: appsv1.SlackInstallActive, TeamName: workspace.Name,
		BotUserID: workspace.BotUserID, Scopes: slices.Clone(BotScopes), Sealed: &sealed, InstalledBy: "user-admin",
		InstalledAt: h.clock.Now(), UpdatedAt: h.clock.Now(),
	}
	if err := h.store.SaveInstall(context.Background(), install, func(bool) (domain.Event, error) {
		return domain.Event{ID: "evt_setup_" + teamID, DetailType: appsv1.SlackInstallChangedDetailType, OccurredAt: h.clock.Now(), Data: json.RawMessage(`{}`)}, nil
	}); err != nil {
		h.t.Fatal(err)
	}
	h.store.mu.Lock()
	h.store.events = nil
	h.store.mu.Unlock()
	return install
}

// mention delivers an @-mention and answers what the ingress said.
func (h *harness) mention(m slackfake.Mention) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.serve(h.fake.Mention("", m))
}

// lastInbound is the newest inbound record.
func (h *harness) lastInbound() Inbound {
	h.t.Helper()
	rows := h.store.inboundRows()
	if len(rows) == 0 {
		h.t.Fatal("nothing was recorded")
	}
	return rows[len(rows)-1]
}

// seenThread records a mention, as an ingress delivery would, so posts have a
// thread to go into, and answers its record.
func (h *harness) seenThread(m slackfake.Mention) Inbound {
	h.t.Helper()
	if response := h.mention(m); response.Code != http.StatusOK {
		h.t.Fatalf("mention = %d %s", response.Code, response.Body)
	}
	return h.lastInbound()
}

// assertLogsLack fails if anything a delivery carried reached the logs.
func (h *harness) assertLogsLack(values ...string) {
	h.t.Helper()
	logs := h.logs.String()
	for _, value := range values {
		if value != "" && bytes.Contains([]byte(logs), []byte(value)) {
			h.t.Fatalf("the logs hold %q:\n%s", value, logs)
		}
	}
}
