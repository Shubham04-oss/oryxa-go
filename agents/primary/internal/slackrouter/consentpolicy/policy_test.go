package consentpolicy

import "testing"

// ask is a fine ask: a member asks a member in a public channel, with nothing
// open, and the screen cleared the words. Each case changes what it is about
// and leaves the rest.
func ask(change func(*Facts)) Facts {
	facts := Facts{AskerMember: true, OwnersNamed: 1, OwnerMember: true, Screen: Cleared}
	if change != nil {
		change(&facts)
	}
	return facts
}

func TestDecideRefusesEveryReason(t *testing.T) {
	for _, test := range []struct {
		name   string
		facts  Facts
		reason Reason
	}{
		{"an asker who is not a member", ask(func(f *Facts) { f.AskerMember = false }), NotMember},
		{"a group message", ask(func(f *Facts) { f.Channel.GroupDM = true }), GroupDM},
		{"a channel shared with another organization", ask(func(f *Facts) { f.Channel.Shared = true }), ExternalChannel},
		{"a mention that names nobody", ask(func(f *Facts) { f.OwnersNamed = 0 }), NoOwner},
		{"a mention that names too many people", ask(func(f *Facts) { f.OwnersNamed = MaxOwners + 1 }), TooManyAsks},
		{"an asker at the limit of open asks", ask(func(f *Facts) { f.OpenAsks = MaxOpenAsks }), TooManyAsks},
		{"an asker past the limit of open asks", ask(func(f *Facts) { f.OpenAsks = MaxOpenAsks + 3 }), TooManyAsks},
		{"an owner who is not a member", ask(func(f *Facts) { f.OwnerMember = false }), OwnerNotMember},
		{"words nobody screened", ask(func(f *Facts) { f.Screen = Unscreened }), ScreenFailed},
		{"words the screen found are not work", ask(func(f *Facts) { f.Screen = FoundNotWork }), NotWork},
		{"words the screen found to be an injection", ask(func(f *Facts) { f.Screen = FoundInjection }), Injection},
		{"words implausible for the asker", ask(func(f *Facts) { f.Screen = FoundImplausible }), Implausible},
		{"a screen this package has not heard of", ask(func(f *Facts) { f.Screen = Screen(99) }), ScreenFailed},
		{"an owner whose department does not fit", ask(func(f *Facts) { f.OwnerMisfit = true }), OwnerDepartment},
	} {
		t.Run(test.name, func(t *testing.T) {
			decision := Decide(test.facts)
			if decision.Outcome != Deny || decision.Reason != test.reason {
				t.Fatalf("decision = %+v, want deny with %q", decision, test.reason)
			}
			if decision.Bypass != (Bypass{}) {
				t.Fatalf("a refusal carries a bypass: %+v", decision.Bypass)
			}
		})
	}
}

// TestDecideRefusalOrder: when several refusals apply, the first in the
// documented order is the one the asker hears, so what they are told does not
// depend on which fact the router happened to read first.
func TestDecideRefusalOrder(t *testing.T) {
	everything := Facts{
		AskerMember: false, OwnersNamed: 0, OwnerMember: false, OpenAsks: MaxOpenAsks,
		Channel: Channel{GroupDM: true, Shared: true}, Screen: FoundInjection, OwnerMisfit: true,
	}
	steps := []struct {
		fix    func(*Facts)
		reason Reason
	}{
		{nil, NotMember},
		{func(f *Facts) { f.AskerMember = true }, GroupDM},
		{func(f *Facts) { f.Channel.GroupDM = false }, ExternalChannel},
		{func(f *Facts) { f.Channel.Shared = false }, NoOwner},
		{func(f *Facts) { f.OwnersNamed = 1 }, TooManyAsks},
		{func(f *Facts) { f.OpenAsks = 0 }, OwnerNotMember},
		{func(f *Facts) { f.OwnerMember = true }, Injection},
		{func(f *Facts) { f.Screen = Cleared }, OwnerDepartment},
	}
	for _, step := range steps {
		if step.fix != nil {
			step.fix(&everything)
		}
		if decision := Decide(everything); decision.Outcome != Deny || decision.Reason != step.reason {
			t.Fatalf("decision = %+v, want deny with %q", decision, step.reason)
		}
	}
	everything.OwnerMisfit = false
	if decision := Decide(everything); decision.Outcome != NeedsConsent {
		t.Fatalf("with every refusal cleared, decision = %+v, want needs consent", decision)
	}
}

func TestDecideAtTheEdgeOfTheLimits(t *testing.T) {
	// The fifth open ask is allowed to be made; with five open, the sixth is not.
	if got := Decide(ask(func(f *Facts) { f.OpenAsks = MaxOpenAsks - 1 })); got.Outcome != NeedsConsent {
		t.Fatalf("with %d open, decision = %+v, want needs consent", MaxOpenAsks-1, got)
	}
	if got := Decide(ask(func(f *Facts) { f.OpenAsks = MaxOpenAsks })); got.Outcome != Deny || got.Reason != TooManyAsks {
		t.Fatalf("with %d open, decision = %+v, want too_many_asks", MaxOpenAsks, got)
	}
	if got := Decide(ask(func(f *Facts) { f.OwnersNamed = MaxOwners })); got.Outcome != NeedsConsent {
		t.Fatalf("naming %d people, decision = %+v, want needs consent", MaxOwners, got)
	}
	if got := Decide(ask(func(f *Facts) { f.OwnersNamed = MaxOwners + 1 })); got.Outcome != Deny || got.Reason != TooManyAsks {
		t.Fatalf("naming %d people, decision = %+v, want too_many_asks", MaxOwners+1, got)
	}
}

func TestDecideNeedsConsentWhereNothingElseApplies(t *testing.T) {
	for name, change := range map[string]func(*Facts){
		"a public channel":                   nil,
		"a private channel nobody marked":    func(f *Facts) { f.Channel.Private = true },
		"a mark on a channel that is public": func(f *Facts) { f.Channel.HighPriority = true },
	} {
		t.Run(name, func(t *testing.T) {
			decision := Decide(ask(change))
			if decision.Outcome != NeedsConsent || decision.Reason != "" || decision.Bypass != (Bypass{}) {
				t.Fatalf("decision = %+v, want plain needs consent", decision)
			}
		})
	}
}

func TestDecideBypassesForAMarkedPrivateChannel(t *testing.T) {
	decision := Decide(ask(func(f *Facts) {
		f.Channel = Channel{Private: true, HighPriority: true}
	}))
	want := Decision{Outcome: Allow, Bypass: Bypass{Reason: PriorityChannel, AnswerTo: InThread}}
	if decision != want {
		t.Fatalf("decision = %+v, want %+v", decision, want)
	}
}

// TestASharedChannelIsNeverABypass: however it is marked, and whoever asks,
// a channel with people from another organization in it is refused, never
// bypassed; and so is a group message.
func TestASharedChannelIsNeverABypass(t *testing.T) {
	for name, channel := range map[string]Channel{
		"marked and private":           {Private: true, HighPriority: true, Shared: true},
		"marked, private, and a DM":    {Private: true, HighPriority: true, GroupDM: true},
		"private, shared and unmarked": {Private: true, Shared: true},
	} {
		for _, outranks := range []bool{false, true} {
			decision := Decide(ask(func(f *Facts) { f.Channel, f.Outranks = channel, outranks }))
			if decision.Outcome != Deny {
				t.Fatalf("%s (outranks %v): decision = %+v, want a refusal", name, outranks, decision)
			}
		}
	}
	// The mark's own conditions hold apart from the refusals before it.
	if (Channel{Private: true, HighPriority: true, Shared: true}).bypasses() {
		t.Fatal("a shared channel bypasses on its own")
	}
	if (Channel{Private: true, HighPriority: true, GroupDM: true}).bypasses() {
		t.Fatal("a group message bypasses on its own")
	}
	if (Channel{HighPriority: true}).bypasses() || (Channel{Private: true}).bypasses() {
		t.Fatal("a channel bypasses without being both private and marked")
	}
}

func TestDecideBypassesForAskerWhoOutranks(t *testing.T) {
	for name, channel := range map[string]Channel{
		"a public channel":                {},
		"a private channel nobody marked": {Private: true},
	} {
		decision := Decide(ask(func(f *Facts) { f.Outranks, f.Channel = true, channel }))
		want := Decision{Outcome: Allow, Bypass: Bypass{Reason: OrgChart, AnswerTo: InDM}}
		if decision != want {
			t.Fatalf("%s: decision = %+v, want %+v", name, decision, want)
		}
	}
	// Outranking says nothing about a person who is not a member.
	if got := Decide(ask(func(f *Facts) { f.Outranks, f.OwnerMember = true, false })); got.Outcome != Deny || got.Reason != OwnerNotMember {
		t.Fatalf("outranking a non-member: decision = %+v, want owner_not_member", got)
	}
	// Nor lifts the limit on open asks.
	if got := Decide(ask(func(f *Facts) { f.Outranks, f.OpenAsks = true, MaxOpenAsks })); got.Outcome != Deny || got.Reason != TooManyAsks {
		t.Fatalf("outranking at the limit: decision = %+v, want too_many_asks", got)
	}
}

// TestTheChannelBypassWinsWhenBothApply: the marked channel's answer goes to
// its thread even when the asker also outranks the owner, because the
// administrator's mark is the wider trust, and the recorded reason is the
// channel's.
func TestTheChannelBypassWinsWhenBothApply(t *testing.T) {
	decision := Decide(ask(func(f *Facts) {
		f.Outranks = true
		f.Channel = Channel{Private: true, HighPriority: true}
	}))
	want := Decision{Outcome: Allow, Bypass: Bypass{Reason: PriorityChannel, AnswerTo: InThread}}
	if decision != want {
		t.Fatalf("decision = %+v, want %+v", decision, want)
	}
}

// TestFailsClosed: nothing known is nothing allowed.
func TestFailsClosed(t *testing.T) {
	if decision := Decide(Facts{}); decision.Outcome != Deny || decision.Reason != NotMember {
		t.Fatalf("Decide(Facts{}) = %+v, want a refusal", decision)
	}
	if (Decision{}).Outcome != Deny {
		t.Fatal("the zero Decision is not a refusal")
	}
	if Outcome(0) != Deny {
		t.Fatal("the zero Outcome is not Deny")
	}
	// Outranks alone, without a member asking, allows nothing.
	if decision := Decide(Facts{Outranks: true, OwnersNamed: 1, OwnerMember: true}); decision.Outcome != Deny {
		t.Fatalf("authority without membership: %+v", decision)
	}
	// Everything else in order, but no screen: an ask nobody screened is
	// refused, and its zero value is the reason, not a pass.
	unscreened := Facts{AskerMember: true, OwnersNamed: 1, OwnerMember: true}
	if decision := Decide(unscreened); decision.Outcome != Deny || decision.Reason != ScreenFailed {
		t.Fatalf("an ask nobody screened: decision = %+v, want screen_failed", decision)
	}
	if (Facts{}).Screen != Unscreened {
		t.Fatal("the zero Screen is not Unscreened")
	}
}

// TestNothingBypassesTheScreen: a marked private channel, an asker above the
// owner, and both together allow nothing when the screen did not clear the
// words. The screen's refusal is the ask's, in any channel and whoever asks.
func TestNothingBypassesTheScreen(t *testing.T) {
	for name, channel := range map[string]Channel{
		"a public channel":       {},
		"a marked private one":   {Private: true, HighPriority: true},
		"a private unmarked one": {Private: true},
	} {
		for _, outranks := range []bool{false, true} {
			for _, screen := range []Screen{Unscreened, FoundNotWork, FoundInjection, FoundImplausible, Screen(-1)} {
				decision := Decide(ask(func(f *Facts) { f.Channel, f.Outranks, f.Screen = channel, outranks, screen }))
				if decision.Outcome != Deny || decision.Reason != screen.refusal() || decision.Bypass != (Bypass{}) {
					t.Fatalf("%s, outranks %v, screen %v: decision = %+v, want %q", name, outranks, screen, decision, screen.refusal())
				}
			}
		}
	}
	// Cleared, the same facts bypass as they did.
	if got := Decide(ask(func(f *Facts) { f.Channel = Channel{Private: true, HighPriority: true} })); got.Outcome != Allow {
		t.Fatalf("a cleared ask in a marked channel: decision = %+v", got)
	}
}

// TestAnOwnerWhoDoesNotFitIsLeftOutWhoeverAsks: a department that plainly does
// not fit is not overridden by the channel or by the asker's authority.
func TestAnOwnerWhoDoesNotFitIsLeftOutWhoeverAsks(t *testing.T) {
	for name, change := range map[string]func(*Facts){
		"a marked private channel": func(f *Facts) { f.Channel = Channel{Private: true, HighPriority: true} },
		"an asker who outranks":    func(f *Facts) { f.Outranks = true },
		"both":                     func(f *Facts) { f.Channel, f.Outranks = Channel{Private: true, HighPriority: true}, true },
	} {
		decision := Decide(ask(func(f *Facts) { change(f); f.OwnerMisfit = true }))
		if decision.Outcome != Deny || decision.Reason != OwnerDepartment {
			t.Fatalf("%s: decision = %+v, want owner_department", name, decision)
		}
	}
	// A person who is not a member is left out for that first.
	if got := Decide(ask(func(f *Facts) { f.OwnerMember, f.OwnerMisfit = false, true })); got.Reason != OwnerNotMember {
		t.Fatalf("a non-member who also does not fit: decision = %+v", got)
	}
	// A refusal of the whole ask is not about one owner, and one about a
	// person is.
	for reason, want := range map[Reason]bool{
		OwnerNotMember: true, OwnerDepartment: true,
		NotMember: false, GroupDM: false, ExternalChannel: false, NoOwner: false, TooManyAsks: false,
		ScreenFailed: false, NotWork: false, Injection: false, Implausible: false, NoGrant: false,
		NotLinked: false, OwnerNotLinked: false,
	} {
		if reason.AboutOneOwner() != want {
			t.Errorf("%q.AboutOneOwner() = %v, want %v", reason, reason.AboutOneOwner(), want)
		}
	}
}

func TestScreenStringsAndRefusals(t *testing.T) {
	for screen, want := range map[Screen]struct {
		text   string
		reason Reason
	}{
		Unscreened:       {"unscreened", ScreenFailed},
		FoundNotWork:     {"not_work", NotWork},
		FoundInjection:   {"injection", Injection},
		FoundImplausible: {"implausible", Implausible},
		Screen(99):       {"unscreened", ScreenFailed},
	} {
		if screen.String() != want.text || screen.refusal() != want.reason {
			t.Errorf("Screen(%d) prints %q and refuses %q, want %q and %q", screen, screen.String(), screen.refusal(), want.text, want.reason)
		}
	}
	if Cleared.String() != "cleared" {
		t.Fatalf("Cleared prints %q", Cleared.String())
	}
}

func TestOutcomeStrings(t *testing.T) {
	for outcome, want := range map[Outcome]string{Deny: "deny", NeedsConsent: "needs_consent", Allow: "allow"} {
		if outcome.String() != want {
			t.Fatalf("%d prints as %q, want %q", outcome, outcome.String(), want)
		}
	}
}

// TestReasonCodesAreTheContract: apps renders the refusal message from these
// codes and A/B/C record them, so a rename here is a break there.
func TestReasonCodesAreTheContract(t *testing.T) {
	for reason, want := range map[Reason]string{
		NotMember: "not_member", OwnerNotMember: "owner_not_member", GroupDM: "group_dm",
		ExternalChannel: "external_channel", NoOwner: "no_owner", TooManyAsks: "too_many_asks",
		ScreenFailed: "screen_failed", NotWork: "not_work", Injection: "injection",
		Implausible: "implausible", NoGrant: "no_grant", OwnerDepartment: "owner_department",
		NotLinked: "not_linked", OwnerNotLinked: "owner_not_linked",
	} {
		if string(reason) != want {
			t.Fatalf("reason %q, want %q", reason, want)
		}
	}
	for reason, want := range map[BypassReason]string{PriorityChannel: "priority_channel", OrgChart: "org_chart"} {
		if string(reason) != want {
			t.Fatalf("bypass reason %q, want %q", reason, want)
		}
	}
	if InThread != "thread" || InDM != "dm" {
		t.Fatalf("answer destinations = %q, %q", InThread, InDM)
	}
}

// TestAnswerForFollowsTheReason: a decision's destination is a function of its
// recorded reason, so a service reading only the reason agrees with the
// router, and a reason it has never heard of is answered privately.
func TestAnswerForFollowsTheReason(t *testing.T) {
	for reason, want := range map[BypassReason]AnswerTo{
		PriorityChannel: InThread, OrgChart: InDM, "something_new": InDM, "": InDM,
	} {
		if got := AnswerFor(reason); got != want {
			t.Errorf("AnswerFor(%q) = %q, want %q", reason, got, want)
		}
	}
	for _, facts := range []Facts{
		ask(func(f *Facts) { f.Channel = Channel{Private: true, HighPriority: true} }),
		ask(func(f *Facts) { f.Outranks = true }),
		ask(func(f *Facts) { f.Outranks, f.Channel = true, Channel{Private: true} }),
	} {
		decision := Decide(facts)
		if decision.Outcome != Allow || decision.Bypass.AnswerTo != AnswerFor(decision.Bypass.Reason) {
			t.Errorf("Decide(%+v) = %+v", facts, decision)
		}
	}
}
