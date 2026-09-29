package guardpolicy

import (
	"errors"
	"fmt"
	"testing"
)

// TestTheEffectiveLevelForEveryCombination writes out, by hand, what applies
// for every organization minimum (none, secrets, strict) against every choice
// (none, secrets, strict), for each origin, whose default differs: the
// stricter of the minimum and the choice, or of the minimum and the default
// when nothing was chosen. It fails if a combination is missing from the table.
func TestTheEffectiveLevelForEveryCombination(t *testing.T) {
	const none = Level("")
	rows := []struct {
		origin          Origin
		minimum, chosen Level
		want            Level
	}{
		// An answer the member started: the default is secrets.
		{OriginOwner, none, none, LevelSecrets},
		{OriginOwner, none, LevelSecrets, LevelSecrets},
		{OriginOwner, none, LevelStrict, LevelStrict},
		{OriginOwner, LevelSecrets, none, LevelSecrets},
		{OriginOwner, LevelSecrets, LevelSecrets, LevelSecrets},
		{OriginOwner, LevelSecrets, LevelStrict, LevelStrict},
		{OriginOwner, LevelStrict, none, LevelStrict},
		{OriginOwner, LevelStrict, LevelSecrets, LevelStrict},
		{OriginOwner, LevelStrict, LevelStrict, LevelStrict},
		// An answer another member reads: the default is strict.
		{OriginPeer, none, none, LevelStrict},
		{OriginPeer, none, LevelSecrets, LevelSecrets},
		{OriginPeer, none, LevelStrict, LevelStrict},
		{OriginPeer, LevelSecrets, none, LevelStrict},
		{OriginPeer, LevelSecrets, LevelSecrets, LevelSecrets},
		{OriginPeer, LevelSecrets, LevelStrict, LevelStrict},
		{OriginPeer, LevelStrict, none, LevelStrict},
		{OriginPeer, LevelStrict, LevelSecrets, LevelStrict},
		{OriginPeer, LevelStrict, LevelStrict, LevelStrict},
	}
	covered := map[[3]string]bool{}
	for _, row := range rows {
		covered[[3]string{string(row.origin), string(row.minimum), string(row.chosen)}] = true
		t.Run(fmt.Sprintf("%s/minimum=%q/chosen=%q", row.origin, row.minimum, row.chosen), func(t *testing.T) {
			var org OrgPolicy
			var chosen Choices
			untouched := OriginView{Default: DefaultLevel(otherOrigin(row.origin)), Floor: LevelSecrets, Effective: DefaultLevel(otherOrigin(row.origin))}
			if row.origin == OriginOwner {
				org.MinimumOwner, chosen.Owner = row.minimum, row.chosen
			} else {
				org.MinimumPeer, chosen.Peer = row.minimum, row.chosen
			}

			view := Resolve("org_1", org, chosen, Cap{})
			got, other := view.Owner, view.Peer
			if row.origin == OriginPeer {
				got, other = view.Peer, view.Owner
			}
			floor := LevelSecrets
			if row.minimum != none {
				floor = row.minimum
			}
			if want := (OriginView{Chosen: row.chosen, Default: DefaultLevel(row.origin), Floor: floor, Effective: row.want}); got != want {
				t.Errorf("view = %+v, want %+v", got, want)
			}
			if other != untouched {
				t.Errorf("the other origin's view = %+v, want it left as its defaults %+v", other, untouched)
			}
			if applied := view.Effective().For(row.origin); applied != row.want {
				t.Errorf("Effective().For(%s) = %q, want %q", row.origin, applied, row.want)
			}
		})
	}
	for _, origin := range []Origin{OriginOwner, OriginPeer} {
		for _, minimum := range []Level{none, LevelSecrets, LevelStrict} {
			for _, chosen := range []Level{none, LevelSecrets, LevelStrict} {
				if !covered[[3]string{string(origin), string(minimum), string(chosen)}] {
					t.Errorf("no row for %s, minimum %q, chosen %q", origin, minimum, chosen)
				}
			}
		}
	}
}

func otherOrigin(origin Origin) Origin {
	if origin == OriginOwner {
		return OriginPeer
	}
	return OriginOwner
}

func TestTheDefaultsAreSecretsForOwnAnswersAndStrictForThoseOthersRead(t *testing.T) {
	view := Resolve("org_1", OrgPolicy{}, Choices{}, Cap{})
	if view.Owner.Effective != LevelSecrets || view.Peer.Effective != LevelStrict {
		t.Fatalf("nothing chosen and no policy = owner %q, peer %q; want secrets, strict", view.Owner.Effective, view.Peer.Effective)
	}
	if DefaultLevel(OriginOwner) != LevelSecrets || DefaultLevel(OriginPeer) != LevelStrict || DefaultLevel("elsewhere") != LevelStrict {
		t.Errorf("defaults = %q, %q, %q for owner, peer and an unknown origin", DefaultLevel(OriginOwner), DefaultLevel(OriginPeer), DefaultLevel("elsewhere"))
	}
	effective := view.Effective()
	if effective.For(OriginOwner) != LevelSecrets || effective.For(OriginPeer) != LevelStrict || effective.For("elsewhere") != LevelStrict {
		t.Errorf("For() = %q, %q, %q for owner, peer and an unknown origin", effective.For(OriginOwner), effective.For(OriginPeer), effective.For("elsewhere"))
	}
}

func TestAMinimumBelowWhatIsChosenLeavesTheChoiceStanding(t *testing.T) {
	// The choice is kept, and shown, when the minimum passes it; lowering the
	// minimum again lets the choice apply.
	chosen := Choices{Peer: LevelSecrets}
	raised := Resolve("org_1", OrgPolicy{MinimumPeer: LevelStrict}, chosen, Cap{}).Peer
	if raised.Chosen != LevelSecrets || raised.Floor != LevelStrict || raised.Effective != LevelStrict {
		t.Errorf("under a strict minimum = %+v, want chosen secrets, floor strict, effective strict", raised)
	}
	lowered := Resolve("org_1", OrgPolicy{}, chosen, Cap{}).Peer
	if lowered.Chosen != LevelSecrets || lowered.Floor != LevelSecrets || lowered.Effective != LevelSecrets {
		t.Errorf("with the minimum gone = %+v, want the choice to apply again", lowered)
	}
}

func TestAValueThisCodeDoesNotKnowNeverLoosensAnything(t *testing.T) {
	for _, unknown := range []Level{"off", "STRICT", "lenient", " secrets"} {
		view := Resolve("org_1", OrgPolicy{MinimumOwner: unknown, MinimumPeer: unknown}, Choices{}, Cap{})
		if view.Owner.Floor != LevelStrict || view.Owner.Effective != LevelStrict || view.Peer.Floor != LevelStrict || view.Peer.Effective != LevelStrict {
			t.Errorf("an organization minimum of %q = %+v, want it read as strict", unknown, view)
		}
		view = Resolve("org_1", OrgPolicy{}, Choices{Owner: unknown, Peer: unknown}, Cap{})
		if view.Owner.Chosen != LevelStrict || view.Owner.Effective != LevelStrict || view.Peer.Chosen != LevelStrict || view.Peer.Effective != LevelStrict {
			t.Errorf("a member's choice of %q = %+v, want it read as strict", unknown, view)
		}
	}
}

func TestRegulatedIsCarriedAndChangesNothingHere(t *testing.T) {
	view := Resolve("org_1", OrgPolicy{Regulated: true}, Choices{}, Cap{})
	plain := Resolve("org_1", OrgPolicy{}, Choices{}, Cap{})
	if !view.Regulated || !view.Effective().Regulated || plain.Regulated || plain.Effective().Regulated {
		t.Fatalf("regulated = %t/%t for a regulated organization, %t/%t for another; want it carried as given", view.Regulated, view.Effective().Regulated, plain.Regulated, plain.Effective().Regulated)
	}
	same := view
	same.Regulated = false
	if same != plain {
		t.Errorf("a regulated organization's view = %+v, want it the same as any other's apart from regulated: %+v", view, plain)
	}
}

// TestTheRelevanceModeUnderTheDeploymentCap writes out the mode each
// organization gets under each ceiling: the organization's own, or hold when
// it has none, never more than the ceiling, except for the organizations the
// ceiling does not apply to.
func TestTheRelevanceModeUnderTheDeploymentCap(t *testing.T) {
	const none = RelevanceMode("")
	oryxa := map[string]bool{"org_oryxa": true}
	rows := []struct {
		name         string
		ceiling      Cap
		organization string
		asked        RelevanceMode
		want         RelevanceMode
	}{
		// No ceiling.
		{"no ceiling, hold", Cap{}, "org_1", RelevanceHold, RelevanceHold},
		{"no ceiling, shadow", Cap{}, "org_1", RelevanceShadow, RelevanceShadow},
		{"no ceiling, off", Cap{}, "org_1", RelevanceOff, RelevanceOff},
		{"no ceiling, nothing asked is hold", Cap{}, "org_1", none, RelevanceHold},
		{"no ceiling, an unknown mode asks for nothing", Cap{}, "org_1", "sometimes", RelevanceHold},
		// A ceiling of hold is no ceiling.
		{"ceiling hold, hold", Cap{Relevance: RelevanceHold}, "org_1", RelevanceHold, RelevanceHold},
		{"ceiling hold, off", Cap{Relevance: RelevanceHold}, "org_1", RelevanceOff, RelevanceOff},
		// Shadow: everyone at most shadow.
		{"ceiling shadow, hold", Cap{Relevance: RelevanceShadow}, "org_1", RelevanceHold, RelevanceShadow},
		{"ceiling shadow, shadow", Cap{Relevance: RelevanceShadow}, "org_1", RelevanceShadow, RelevanceShadow},
		{"ceiling shadow, off", Cap{Relevance: RelevanceShadow}, "org_1", RelevanceOff, RelevanceOff},
		{"ceiling shadow, nothing asked", Cap{Relevance: RelevanceShadow}, "org_1", none, RelevanceShadow},
		// ... except the organizations named.
		{"ceiling shadow, oryxa holds", Cap{Relevance: RelevanceShadow, HoldOrgs: oryxa}, "org_oryxa", RelevanceHold, RelevanceHold},
		{"ceiling shadow, oryxa with nothing asked holds", Cap{Relevance: RelevanceShadow, HoldOrgs: oryxa}, "org_oryxa", none, RelevanceHold},
		{"ceiling shadow, oryxa may still choose off", Cap{Relevance: RelevanceShadow, HoldOrgs: oryxa}, "org_oryxa", RelevanceOff, RelevanceOff},
		{"ceiling shadow, another org is still capped", Cap{Relevance: RelevanceShadow, HoldOrgs: oryxa}, "org_1", RelevanceHold, RelevanceShadow},
		{"ceiling shadow, an org not marked true is capped", Cap{Relevance: RelevanceShadow, HoldOrgs: map[string]bool{"org_1": false}}, "org_1", RelevanceHold, RelevanceShadow},
		// Off: nothing runs but where the ceiling does not apply.
		{"ceiling off, hold", Cap{Relevance: RelevanceOff}, "org_1", RelevanceHold, RelevanceOff},
		{"ceiling off, shadow", Cap{Relevance: RelevanceOff}, "org_1", RelevanceShadow, RelevanceOff},
		{"ceiling off, oryxa holds", Cap{Relevance: RelevanceOff, HoldOrgs: oryxa}, "org_oryxa", RelevanceHold, RelevanceHold},
		// A ceiling this code does not know is shadow.
		{"unknown ceiling, hold", Cap{Relevance: "some"}, "org_1", RelevanceHold, RelevanceShadow},
		{"unknown ceiling, off", Cap{Relevance: "some"}, "org_1", RelevanceOff, RelevanceOff},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			view := Resolve(row.organization, OrgPolicy{Relevance: row.asked}, Choices{}, row.ceiling)
			if view.Relevance != row.want || view.Effective().Relevance != row.want {
				t.Errorf("relevance = %q, want %q", view.Relevance, row.want)
			}
		})
	}
}

func TestLevelsAndModesAreParsedExactly(t *testing.T) {
	for _, text := range []string{"secrets", "strict"} {
		if level, err := ParseLevel(text); err != nil || level != Level(text) || !level.Valid() {
			t.Errorf("ParseLevel(%q) = %q, %v", text, level, err)
		}
	}
	for _, text := range []string{"", "off", "Secrets", "STRICT", " strict", "strict ", "hold"} {
		if level, err := ParseLevel(text); !errors.Is(err, ErrInvalid) || level != "" {
			t.Errorf("ParseLevel(%q) = %q, %v; want ErrInvalid", text, level, err)
		}
	}
	for _, text := range []string{"hold", "shadow", "off"} {
		if mode, err := ParseRelevanceMode(text); err != nil || mode != RelevanceMode(text) || !mode.Valid() {
			t.Errorf("ParseRelevanceMode(%q) = %q, %v", text, mode, err)
		}
	}
	for _, text := range []string{"", "on", "Hold", "secrets", "shadow "} {
		if mode, err := ParseRelevanceMode(text); !errors.Is(err, ErrInvalid) || mode != "" {
			t.Errorf("ParseRelevanceMode(%q) = %q, %v; want ErrInvalid", text, mode, err)
		}
	}
	if Level("").Valid() || RelevanceMode("").Valid() {
		t.Error("no choice was taken for a level or a mode")
	}
}
