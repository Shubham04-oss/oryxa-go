package guardpolicy

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
)

// memoryStore stands in for PostgreSQL in the tests of the rule: it keeps the
// choices with the same meaning of nil and "" the real store gives them.
type memoryStore struct {
	mu        sync.Mutex
	choices   map[[2]string]Choices
	getErr    error
	setErr    error
	forgotten [][2]string
	sets      int
}

func newMemoryStore() *memoryStore { return &memoryStore{choices: map[[2]string]Choices{}} }

func (store *memoryStore) migrate(context.Context) error { return nil }

func (store *memoryStore) get(_ context.Context, organizationID, userID string) (Choices, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.getErr != nil {
		return Choices{}, store.getErr
	}
	return store.choices[[2]string{organizationID, userID}], nil
}

func (store *memoryStore) set(_ context.Context, organizationID, userID string, owner, peer *Level) (Choices, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.setErr != nil {
		return Choices{}, store.setErr
	}
	store.sets++
	key := [2]string{organizationID, userID}
	chosen := store.choices[key]
	if owner != nil {
		chosen.Owner = *owner
	}
	if peer != nil {
		chosen.Peer = *peer
	}
	store.choices[key] = chosen
	return chosen, nil
}

func (store *memoryStore) forget(_ context.Context, organizationID, memberID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.forgotten = append(store.forgotten, [2]string{organizationID, memberID})
	return nil
}

// fakeOrgs is an organization policy source that answers what it is told to.
type fakeOrgs struct {
	mu     sync.Mutex
	policy OrgPolicy
	err    error
	asked  []string
}

func (orgs *fakeOrgs) Get(_ context.Context, organizationID string) (OrgPolicy, error) {
	orgs.mu.Lock()
	defer orgs.mu.Unlock()
	orgs.asked = append(orgs.asked, organizationID)
	if orgs.err != nil {
		return OrgPolicy{}, orgs.err
	}
	return orgs.policy, nil
}

func (orgs *fakeOrgs) set(policy OrgPolicy, err error) {
	orgs.mu.Lock()
	defer orgs.mu.Unlock()
	orgs.policy, orgs.err = policy, err
}

// records is a log handler that keeps what it was told, to count and read.
type records struct {
	mu   sync.Mutex
	list []slog.Record
}

func (log *records) Enabled(context.Context, slog.Level) bool { return true }
func (log *records) Handle(_ context.Context, record slog.Record) error {
	log.mu.Lock()
	defer log.mu.Unlock()
	log.list = append(log.list, record)
	return nil
}
func (log *records) WithAttrs([]slog.Attr) slog.Handler { return log }
func (log *records) WithGroup(string) slog.Handler      { return log }

func (log *records) warnings() []slog.Record {
	log.mu.Lock()
	defer log.mu.Unlock()
	var warned []slog.Record
	for _, record := range log.list {
		if record.Level == slog.LevelWarn {
			warned = append(warned, record)
		}
	}
	return warned
}

func (log *records) count() int {
	log.mu.Lock()
	defer log.mu.Unlock()
	return len(log.list)
}

func testService(orgs OrgPolicies, ceiling Cap) (*Service, *memoryStore, *records) {
	store := newMemoryStore()
	log := &records{}
	return newService(store, orgs, ceiling).WithLogger(slog.New(log)), store, log
}

func levelPointer(level Level) *Level { return &level }

func TestEffectiveAndPreferencesCombineWhatAMemberChoseWithTheirOrganization(t *testing.T) {
	ctx := context.Background()
	orgs := &fakeOrgs{policy: OrgPolicy{MinimumPeer: LevelStrict, Relevance: RelevanceShadow, Regulated: true}}
	service, _, log := testService(orgs, Cap{})

	before, err := service.Effective(ctx, "org_1", "user_1")
	if err != nil {
		t.Fatal(err)
	}
	if want := (Effective{Owner: LevelSecrets, Peer: LevelStrict, Relevance: RelevanceShadow, Regulated: true}); before != want {
		t.Fatalf("nothing chosen = %+v, want %+v", before, want)
	}

	view, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelStrict), levelPointer(LevelSecrets))
	if err != nil {
		t.Fatal(err)
	}
	want := View{
		Owner:     OriginView{Chosen: LevelStrict, Default: LevelSecrets, Floor: LevelSecrets, Effective: LevelStrict},
		Peer:      OriginView{Chosen: LevelSecrets, Default: LevelStrict, Floor: LevelStrict, Effective: LevelStrict},
		Relevance: RelevanceShadow, Regulated: true,
	}
	if view != want {
		t.Fatalf("after choosing = %+v, want %+v (the choice below the minimum is kept, and the minimum wins)", view, want)
	}
	if read, err := service.Preferences(ctx, "org_1", "user_1"); err != nil || read != want {
		t.Fatalf("Preferences() = %+v, %v; want what SetPreferences answered", read, err)
	}
	if effective, err := service.Effective(ctx, "org_1", "user_1"); err != nil || effective != want.Effective() {
		t.Fatalf("Effective() = %+v, %v; want %+v", effective, err, want.Effective())
	}

	// The minimum was asked of the organization the member is in, and nobody else's.
	for _, asked := range orgs.asked {
		if asked != "org_1" {
			t.Errorf("the policy of %q was read, want only org_1's", asked)
		}
	}
	if len(log.warnings()) != 0 {
		t.Errorf("a policy that was read logged %d warnings", len(log.warnings()))
	}

	// Another member, and the same person elsewhere, have chosen nothing.
	for _, who := range [][2]string{{"org_1", "user_2"}, {"org_2", "user_1"}} {
		if effective, err := service.Effective(ctx, who[0], who[1]); err != nil || effective.Owner != LevelSecrets || effective.Peer != LevelStrict {
			t.Errorf("%v = %+v, %v; want the defaults", who, effective, err)
		}
	}
}

func TestSetPreferencesLeavesWhatIsNotNamedAndClearsWhatIsEmpty(t *testing.T) {
	ctx := context.Background()
	service, store, _ := testService(NoOrgPolicies{}, Cap{})
	set := func(owner, peer *Level) View {
		t.Helper()
		view, err := service.SetPreferences(ctx, "org_1", "user_1", owner, peer)
		if err != nil {
			t.Fatal(err)
		}
		return view
	}

	view := set(levelPointer(LevelStrict), nil)
	if view.Owner.Chosen != LevelStrict || view.Peer.Chosen != "" {
		t.Fatalf("owner strict, peer not named = %+v/%+v", view.Owner, view.Peer)
	}
	view = set(nil, levelPointer(LevelSecrets))
	if view.Owner.Chosen != LevelStrict || view.Peer.Chosen != LevelSecrets || view.Peer.Effective != LevelSecrets {
		t.Fatalf("peer secrets, owner not named = %+v/%+v, want owner still strict", view.Owner, view.Peer)
	}
	view = set(levelPointer(""), nil)
	if view.Owner.Chosen != "" || view.Owner.Effective != LevelSecrets || view.Peer.Chosen != LevelSecrets {
		t.Fatalf("owner cleared = %+v/%+v, want owner back to its default and peer as it was", view.Owner, view.Peer)
	}

	// Naming neither writes nothing at all.
	sets := store.sets
	if read := set(nil, nil); read != view || store.sets != sets {
		t.Fatalf("naming neither = %+v after %d writes, want %+v and no write", read, store.sets-sets, view)
	}
}

func TestSetPreferencesRefusesALevelItDoesNotKnowBeforeChangingAnything(t *testing.T) {
	ctx := context.Background()
	service, store, _ := testService(NoOrgPolicies{}, Cap{})
	for _, bad := range []Level{"off", "STRICT", "everything", " "} {
		if _, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelStrict), levelPointer(bad)); !errors.Is(err, ErrInvalid) {
			t.Errorf("peer %q = %v, want ErrInvalid", bad, err)
		}
		if _, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(bad), nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("owner %q = %v, want ErrInvalid", bad, err)
		}
	}
	if store.sets != 0 || len(store.choices) != 0 {
		t.Fatalf("a refused change wrote: %d writes, %v", store.sets, store.choices)
	}
	for _, who := range [][2]string{{"", "user_1"}, {"org_1", ""}, {"", ""}} {
		if _, err := service.SetPreferences(ctx, who[0], who[1], levelPointer(LevelStrict), nil); !errors.Is(err, ErrInvalid) {
			t.Errorf("SetPreferences(%q, %q) = %v, want ErrInvalid", who[0], who[1], err)
		}
		if _, err := service.Preferences(ctx, who[0], who[1]); !errors.Is(err, ErrInvalid) {
			t.Errorf("Preferences(%q, %q) = %v, want ErrInvalid", who[0], who[1], err)
		}
		if effective, err := service.Effective(ctx, who[0], who[1]); !errors.Is(err, ErrInvalid) || effective.Owner != LevelStrict || effective.Peer != LevelStrict {
			t.Errorf("Effective(%q, %q) = %+v, %v; want ErrInvalid with the strictest levels", who[0], who[1], effective, err)
		}
	}
}

func TestAMemberWhoseChoicesCannotBeReadIsReadAsStrictly(t *testing.T) {
	ctx := context.Background()
	service, store, _ := testService(&fakeOrgs{policy: OrgPolicy{Relevance: RelevanceOff, Regulated: true}}, Cap{})
	store.choices[[2]string{"org_1", "user_1"}] = Choices{Owner: LevelSecrets, Peer: LevelSecrets}
	store.getErr = errors.New("database down")

	effective, err := service.Effective(ctx, "org_1", "user_1")
	if !errors.Is(err, store.getErr) {
		t.Fatalf("err = %v, want the store's", err)
	}
	if want := (Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: RelevanceOff, Regulated: true}); effective != want {
		t.Errorf("Effective() on a failure = %+v, want the strictest levels, and the organization's own settings: %+v", effective, want)
	}
	if _, err := service.Preferences(ctx, "org_1", "user_1"); !errors.Is(err, store.getErr) {
		t.Errorf("Preferences() = %v, want the store's error", err)
	}

	store.getErr, store.setErr = nil, errors.New("database down")
	if _, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelStrict), nil); !errors.Is(err, store.setErr) {
		t.Errorf("SetPreferences() = %v, want the store's error", err)
	}
}

func TestAnUnreadableOrganizationPolicyIsConservativeUntilRecoveryAndLogsEachOutageOnce(t *testing.T) {
	ctx := context.Background()
	orgs := &fakeOrgs{err: errors.New("workspace store down")}
	service, store, log := testService(orgs, Cap{Relevance: RelevanceShadow})
	store.choices[[2]string{"org_1", "user_1"}] = Choices{Owner: LevelSecrets, Peer: LevelSecrets}

	fallback := Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: RelevanceShadow, Regulated: true}
	for range 3 {
		effective, err := service.Effective(ctx, "org_1", "user_1")
		if err != nil || effective != fallback {
			t.Fatalf("Effective() with the policy down = %+v, %v; want the capped conservative fallback %+v", effective, err, fallback)
		}
	}
	if view, err := service.Preferences(ctx, "org_1", "user_1"); err != nil || view.Owner.Floor != LevelStrict || view.Peer.Floor != LevelStrict || view.Peer.Chosen != LevelSecrets || view.Effective() != fallback {
		t.Fatalf("Preferences() with the policy down = %+v, %v; want strict floors, retained choices and the capped fallback", view, err)
	}
	if view, err := service.SetPreferences(ctx, "org_1", "user_1", levelPointer(LevelSecrets), levelPointer(LevelSecrets)); err != nil || view.Owner.Chosen != LevelSecrets || view.Peer.Chosen != LevelSecrets || view.Effective() != fallback {
		t.Fatalf("SetPreferences() with the policy down = %+v, %v; want saved choices under strict floors", view, err)
	}
	warnings := log.warnings()
	if len(warnings) != 1 {
		t.Fatalf("logged %d warnings over five checks while the policy stayed down, want 1", len(warnings))
	}
	if text := warnings[0].Message; !strings.Contains(text, "workspace policy") {
		t.Errorf("warning = %q, want it to say the policy could not be read", text)
	}
	attributes := map[string]string{}
	warnings[0].Attrs(func(attribute slog.Attr) bool {
		attributes[attribute.Key] = attribute.Value.String()
		return true
	})
	if attributes["organization_id"] != "org_1" || !strings.Contains(attributes["error"], "workspace store down") {
		t.Errorf("warning attributes = %v, want the organization and the error", attributes)
	}

	// Another organization failing is its own outage.
	if _, err := service.Effective(ctx, "org_2", "user_1"); err != nil {
		t.Fatal(err)
	}
	if len(log.warnings()) != 2 {
		t.Fatalf("logged %d warnings after a second organization failed, want 2", len(log.warnings()))
	}

	// Once it can be read, it is used; the next time it fails is reported again.
	orgs.set(OrgPolicy{}, nil)
	effective, err := service.Effective(ctx, "org_1", "user_1")
	if want := (Effective{Owner: LevelSecrets, Peer: LevelSecrets, Relevance: RelevanceShadow}); err != nil || effective != want {
		t.Fatalf("Effective() once the policy is back = %+v, %v; want saved choices to resume %+v", effective, err, want)
	}
	orgs.set(OrgPolicy{}, errors.New("workspace store down again"))
	for range 2 {
		if _, err := service.Effective(ctx, "org_1", "user_1"); err != nil {
			t.Fatal(err)
		}
	}
	if len(log.warnings()) != 3 {
		t.Fatalf("logged %d warnings after the policy failed again, want 3", len(log.warnings()))
	}

	// An organization that is forgotten starts afresh.
	if err := service.Forget(ctx, "org_1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Effective(ctx, "org_1", "user_1"); err != nil {
		t.Fatal(err)
	}
	if len(log.warnings()) != 4 {
		t.Fatalf("logged %d warnings after the organization was forgotten and failed again, want 4", len(log.warnings()))
	}
}

func TestACallerThatWentAwayIsNotAWorkspaceServiceThatFailed(t *testing.T) {
	orgs := &fakeOrgs{err: context.Canceled}
	service, store, log := testService(orgs, Cap{})
	store.choices[[2]string{"org_1", "user_1"}] = Choices{Owner: LevelSecrets, Peer: LevelSecrets}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	effective, err := service.Effective(ctx, "org_1", "user_1")
	if want := (Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: RelevanceHold, Regulated: true}); err != nil || effective != want {
		t.Fatalf("Effective() = %+v, %v; want a conservative result even if the member store returns after cancellation", effective, err)
	}
	if log.count() != 0 {
		t.Errorf("a cancelled request logged %d records, want none", log.count())
	}
	// Nor did it start an outage that would keep a real one from being reported.
	orgs.set(OrgPolicy{}, errors.New("workspace store down"))
	if _, err := service.Effective(context.Background(), "org_1", "user_1"); err != nil {
		t.Fatal(err)
	}
	if len(log.warnings()) != 1 {
		t.Errorf("logged %d warnings for a real failure after a cancelled request, want 1", len(log.warnings()))
	}
	// A cancelled request during that outage must not reset its suppression.
	if _, err := service.Effective(ctx, "org_1", "user_1"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Effective(context.Background(), "org_1", "user_1"); err != nil {
		t.Fatal(err)
	}
	if len(log.warnings()) != 1 {
		t.Errorf("a cancelled request reset outage suppression: %d warnings, want 1", len(log.warnings()))
	}
}

func TestUnreadablePolicyStillHonorsTheDeploymentRelevanceCeiling(t *testing.T) {
	for name, test := range map[string]struct {
		cap  Cap
		want RelevanceMode
	}{
		"hold":        {Cap{}, RelevanceHold},
		"shadow":      {Cap{Relevance: RelevanceShadow}, RelevanceShadow},
		"off":         {Cap{Relevance: RelevanceOff}, RelevanceOff},
		"unknown cap": {Cap{Relevance: "future"}, RelevanceShadow},
		"hold org":    {Cap{Relevance: RelevanceOff, HoldOrgs: map[string]bool{"org_1": true}}, RelevanceHold},
	} {
		t.Run(name, func(t *testing.T) {
			service, store, _ := testService(&fakeOrgs{err: errors.New("workspace down")}, test.cap)
			store.choices[[2]string{"org_1", "user_1"}] = Choices{Owner: LevelSecrets, Peer: LevelSecrets}
			effective, err := service.Effective(context.Background(), "org_1", "user_1")
			want := Effective{Owner: LevelStrict, Peer: LevelStrict, Relevance: test.want, Regulated: true}
			if err != nil || effective != want {
				t.Fatalf("Effective() = %+v, %v; want %+v", effective, err, want)
			}
		})
	}
}

// nilOrgs is what a policy source that was never built looks like once it is
// boxed: a nil pointer that is not a nil interface.
type nilOrgs struct{}

func (*nilOrgs) Get(context.Context, string) (OrgPolicy, error) {
	panic("a source that was never built was asked")
}

func TestNoSourceOfOrganizationPoliciesMeansTheDefaultsAndIsLoggedOnce(t *testing.T) {
	ctx := context.Background()
	for name, orgs := range map[string]OrgPolicies{
		"nil":           nil,
		"a nil pointer": (*nilOrgs)(nil),
	} {
		t.Run(name, func(t *testing.T) {
			service, _, log := testService(orgs, Cap{Relevance: RelevanceShadow, HoldOrgs: map[string]bool{"org_hold": true}})
			for range 3 {
				effective, err := service.Effective(ctx, "org_1", "user_1")
				if err != nil || effective != (Effective{Owner: LevelSecrets, Peer: LevelStrict, Relevance: RelevanceShadow}) {
					t.Fatalf("Effective() = %+v, %v; want the defaults", effective, err)
				}
			}
			if held, err := service.Effective(ctx, "org_hold", "user_1"); err != nil || held.Relevance != RelevanceHold {
				t.Fatalf("an organization exempt from the ceiling = %+v, %v; want hold", held, err)
			}
			warnings := log.warnings()
			if len(warnings) != 1 || !strings.Contains(warnings[0].Message, "no organization policy source") {
				t.Fatalf("logged %d warnings, want one saying there is no source", len(warnings))
			}
		})
	}
}

func TestNoOrgPoliciesIsTheExplicitChoiceAndSaysNothing(t *testing.T) {
	ctx := context.Background()
	service, _, log := testService(NoOrgPolicies{}, Cap{})
	for range 2 {
		effective, err := service.Effective(ctx, "org_1", "user_1")
		if err != nil || effective != (Effective{Owner: LevelSecrets, Peer: LevelStrict, Relevance: RelevanceHold}) {
			t.Fatalf("Effective() = %+v, %v; want the defaults", effective, err)
		}
	}
	if log.count() != 0 {
		t.Errorf("logged %d records, want none for a deployment that chose to have no workspace service", log.count())
	}
}

func TestTheCeilingAppliesThroughTheService(t *testing.T) {
	ctx := context.Background()
	orgs := &fakeOrgs{policy: OrgPolicy{Relevance: RelevanceHold}}
	holdOrgs := map[string]bool{"org_oryxa": true}
	service, _, _ := testService(orgs, Cap{Relevance: RelevanceShadow, HoldOrgs: holdOrgs})
	// A change to the caller's map afterwards changes nothing here.
	holdOrgs["org_1"] = true

	for organization, want := range map[string]RelevanceMode{"org_1": RelevanceShadow, "org_oryxa": RelevanceHold} {
		effective, err := service.Effective(ctx, organization, "user_1")
		if err != nil || effective.Relevance != want {
			t.Errorf("%s = %q, %v; want %q", organization, effective.Relevance, err, want)
		}
		view, err := service.Preferences(ctx, organization, "user_1")
		if err != nil || view.Relevance != want {
			t.Errorf("%s view = %q, %v; want %q", organization, view.Relevance, err, want)
		}
	}
}

func TestForgetHandsTheStoreWhoIsToBeForgotten(t *testing.T) {
	ctx := context.Background()
	service, store, _ := testService(NoOrgPolicies{}, Cap{})
	if err := service.Forget(ctx, "org_1", "user_1"); err != nil {
		t.Fatal(err)
	}
	if err := service.Forget(ctx, "org_1", ""); err != nil {
		t.Fatal(err)
	}
	if want := [][2]string{{"org_1", "user_1"}, {"org_1", ""}}; len(store.forgotten) != 2 || store.forgotten[0] != want[0] || store.forgotten[1] != want[1] {
		t.Errorf("forgot %v, want %v", store.forgotten, want)
	}
	if err := service.Forget(ctx, "", "user_1"); err == nil {
		t.Error("Forget without an organization was accepted")
	}
	if len(store.forgotten) != 2 {
		t.Errorf("forgot %v, want nothing for a call that named no organization", store.forgotten)
	}
	// It has the shape of inbound.Forgetter, which the consumer takes.
	var _ func(context.Context, string, string) error = service.Forget
}

func TestTheServiceIsSafeForConcurrentUse(t *testing.T) {
	ctx := context.Background()
	orgs := &fakeOrgs{err: errors.New("workspace store down")}
	service, _, log := testService(orgs, Cap{Relevance: RelevanceShadow, HoldOrgs: map[string]bool{"org_0": true}})

	var wait sync.WaitGroup
	for worker := range 16 {
		wait.Go(func() {
			organizationID := "org_" + string(rune('0'+worker%4))
			for round := range 25 {
				if worker == 0 && round%5 == 0 {
					// The policy source comes and goes while checks are made.
					if round%10 == 0 {
						orgs.set(OrgPolicy{MinimumPeer: LevelStrict}, nil)
					} else {
						orgs.set(OrgPolicy{}, errors.New("workspace store down"))
					}
				}
				owner := levelPointer(LevelStrict)
				if round%2 == 0 {
					owner = levelPointer("")
				}
				if _, err := service.SetPreferences(ctx, organizationID, "user_1", owner, nil); err != nil {
					t.Errorf("SetPreferences() = %v", err)
				}
				if _, err := service.Effective(ctx, organizationID, "user_1"); err != nil {
					t.Errorf("Effective() = %v", err)
				}
				if _, err := service.Preferences(ctx, organizationID, "user_1"); err != nil {
					t.Errorf("Preferences() = %v", err)
				}
			}
		})
	}
	wait.Wait()
	if len(log.warnings()) == 0 {
		t.Error("a policy source that was failing was never reported")
	}
}
