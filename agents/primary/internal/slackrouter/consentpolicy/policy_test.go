package consentpolicy

import "testing"

// ask is a fine ask: a member asks a member in a public channel, with nothing
// open. Each case changes what it is about and leaves the rest.
func ask(change func(*Facts)) Facts {
	facts := Facts{AskerMember: true, OwnersNamed: 1, OwnerMember: true}
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
		Channel: Channel{GroupDM: true, Shared: true},
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
	}
	for _, step := range steps {
		if step.fix != nil {
			step.fix(&everything)
		}
		if decision := Decide(everything); decision.Outcome != Deny || decision.Reason != step.reason {
			t.Fatalf("decision = %+v, want deny with %q", decision, step.reason)
		}
	}
	everything.OwnerMember = true
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
	want := Decision{Outcome: Allow, Bypass: Bypass{Reason: HighPriorityChannel, AnswerTo: InThread}}
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
		want := Decision{Outcome: Allow, Bypass: Bypass{Reason: AskerOutranks, AnswerTo: InDM}}
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
	want := Decision{Outcome: Allow, Bypass: Bypass{Reason: HighPriorityChannel, AnswerTo: InThread}}
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
	} {
		if string(reason) != want {
			t.Fatalf("reason %q, want %q", reason, want)
		}
	}
	for reason, want := range map[BypassReason]string{HighPriorityChannel: "high_priority_channel", AskerOutranks: "outranks"} {
		if string(reason) != want {
			t.Fatalf("bypass reason %q, want %q", reason, want)
		}
	}
	if InThread != "thread" || InDM != "dm" {
		t.Fatalf("answer destinations = %q, %q", InThread, InDM)
	}
}
