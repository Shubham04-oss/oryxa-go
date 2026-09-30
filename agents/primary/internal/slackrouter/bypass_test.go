package slackrouter

import (
	"errors"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

var (
	markedThread = consentpolicy.Bypass{Reason: consentpolicy.PriorityChannel, AnswerTo: consentpolicy.InThread}
	outranksDM   = consentpolicy.Bypass{Reason: consentpolicy.OrgChart, AnswerTo: consentpolicy.InDM}
)

func markChannel(h *harness, info ChannelInfo) {
	info.ID = testChannel
	h.slack.channels[testChannel] = info
}

func TestAMarkedPrivateChannelBypassesConsent(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	markChannel(h, ChannelInfo{Private: true, HighPriority: true})
	job := h.mention(toBot("ask <@URAYA> about the acquisition"))
	h.mustRun(job)

	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya || bypassOf(ask.Owners[0]) != markedThread {
		t.Fatalf("owners = %+v, want Raya bypassed for the channel, answered in the thread", ask.Owners)
	}
	if len(h.authority.asked) != 0 {
		t.Fatalf("the chart was read for an ask the channel already bypassed: %v", h.authority.asked)
	}
	// The asker is told who was asked, as for any ask: how it was decided is
	// the owner's notice's to say, and the answer's to show.
	if _, ack := h.ack(job); wire(t, ack) != `{"owners":[{"user_id":"URAYA"}]}` {
		t.Fatalf("ack on the wire = %s", wire(t, ack))
	}
}

// TestTheMarkIsReadAgainOnEveryUse: an administrator who unmarks a channel is
// not asked twice. The next ask in it needs consent, and marking it again
// brings the bypass back, because the channel is read for each ask and never
// remembered.
func TestTheMarkIsReadAgainOnEveryUse(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")

	markChannel(h, ChannelInfo{Private: true, HighPriority: true})
	h.mustRun(h.mention(toBot("ask <@URAYA> one")))
	markChannel(h, ChannelInfo{Private: true, HighPriority: false})
	h.mustRun(h.mention(toBot("ask <@URAYA> two")))
	markChannel(h, ChannelInfo{Private: true, HighPriority: true})
	h.mustRun(h.mention(toBot("ask <@URAYA> three")))

	if len(h.asks.created) != 3 {
		t.Fatalf("made %d asks", len(h.asks.created))
	}
	for index, want := range []*consentpolicy.Bypass{&markedThread, nil, &markedThread} {
		got := h.asks.created[index].Owners[0].Bypass
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Fatalf("ask %d bypass = %+v, want %+v", index+1, got, want)
		}
	}
	if h.slack.channelReads() != 3 {
		t.Fatalf("read the channel %d times for three asks", h.slack.channelReads())
	}
}

// TestAChannelThatBecameSharedIsNeverABypass: the mark is not enough. The
// channel is checked private and unshared each time, and a shared one is
// refused, not bypassed, whoever asks and however it is marked.
func TestAChannelThatBecameSharedIsNeverABypass(t *testing.T) {
	for name, info := range map[string]ChannelInfo{
		"marked, private and shared":          {Private: true, HighPriority: true, Shared: true},
		"marked, private and a group message": {Private: true, HighPriority: true, MPIM: true},
		"unmarked, private and shared":        {Private: true, Shared: true},
		"marked, not private and shared":      {HighPriority: true, Shared: true},
	} {
		for _, outranks := range []bool{false, true} {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			h.authority.outranks["usr_ada>usr_raya"] = outranks
			markChannel(h, info)
			job := h.mention(toBot("ask <@URAYA>"))
			h.mustRun(job)
			_, params := h.refusal(job)
			if params.Reason != consentpolicy.ExternalChannel && params.Reason != consentpolicy.GroupDM {
				t.Fatalf("%s (outranks %v): reason = %q, want the channel refused", name, outranks, params.Reason)
			}
		}
	}
}

func TestAMarkOnAChannelThatIsNotPrivateBypassesNothing(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	markChannel(h, ChannelInfo{HighPriority: true}) // marks belong to private channels only
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if got := h.ask().Owners[0].Bypass; got != nil {
		t.Fatalf("bypass = %+v, want consent asked", got)
	}
}

func TestAPrivateChannelNobodyMarkedNeedsConsent(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	markChannel(h, ChannelInfo{Private: true})
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if got := h.ask().Owners[0].Bypass; got != nil {
		t.Fatalf("bypass = %+v, want consent asked", got)
	}
}

func TestAnAskerWhoOutranksTheOwnerBypassesToTheirDM(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	h.authority.outranks["usr_ada>usr_raya"] = true
	job := h.mention(toBot("ask <@URAYA> about the roadmap"))
	h.mustRun(job)

	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya || bypassOf(ask.Owners[0]) != outranksDM {
		t.Fatalf("owners = %+v, want Raya bypassed on authority, answered in the asker's DM", ask.Owners)
	}
	// The chart is asked who outranks whom, once, of the two members.
	if len(h.authority.asked) != 1 || h.authority.asked[0] != "usr_ada>usr_raya" {
		t.Fatalf("chart questions = %v", h.authority.asked)
	}
	if _, ack := h.ack(job); wire(t, ack) != `{"owners":[{"user_id":"URAYA"}]}` {
		t.Fatalf("ack on the wire = %s", wire(t, ack))
	}
}

func TestAnAskerWhoDoesNotOutrankTheOwnerNeedsConsent(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.authority.outranks["usr_raya>usr_ada"] = true // the other way round
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if got := h.ask().Owners[0].Bypass; got != nil {
		t.Fatalf("bypass = %+v, want consent asked", got)
	}
	if len(h.authority.asked) != 1 {
		t.Fatalf("chart questions = %v", h.authority.asked)
	}
}

func TestWithoutAnAuthorityNobodyOutranksAnybody(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.authority.outranks["usr_ada>usr_raya"] = true
	h.router.deps.Authority = nil
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if got := h.ask().Owners[0].Bypass; got != nil {
		t.Fatalf("bypass = %+v without an authority", got)
	}
}

// TestTheChartIsReadOnlyWhereItCouldChangeTheAnswer: it is another service's
// call, made as the asker, so it is made only when nothing else has settled
// the ask.
func TestTheChartIsReadOnlyWhereItCouldChangeTheAnswer(t *testing.T) {
	t.Run("not for a channel that already bypassed", func(t *testing.T) {
		h := newHarness(t)
		h.member("URAYA", "Raya")
		markChannel(h, ChannelInfo{Private: true, HighPriority: true})
		h.mustRun(h.mention(toBot("ask <@URAYA>")))
		if len(h.authority.asked) != 0 {
			t.Fatalf("chart questions = %v", h.authority.asked)
		}
	})
	t.Run("not for a refusal", func(t *testing.T) {
		h := newHarness(t)
		h.member("URAYA", "Raya")
		markChannel(h, ChannelInfo{Shared: true})
		h.mustRun(h.mention(toBot("ask <@URAYA>")))
		if len(h.authority.asked) != 0 {
			t.Fatalf("chart questions = %v", h.authority.asked)
		}
	})
	t.Run("not for a person who is not in Oryxa", func(t *testing.T) {
		h := newHarness(t)
		h.member("URAYA", "Raya")
		h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, Name: "Maya"}
		h.mustRun(h.mention(toBot("ask <@UMAYA> or <@URAYA>")))
		if len(h.authority.asked) != 1 || h.authority.asked[0] != "usr_ada>usr_raya" {
			t.Fatalf("chart questions = %v, want Raya's alone", h.authority.asked)
		}
	})
}

func TestTheMarkedChannelWinsWhenTheAskerAlsoOutranks(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.authority.outranks["usr_ada>usr_raya"] = true
	markChannel(h, ChannelInfo{Private: true, HighPriority: true})
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if got := bypassOf(h.ask().Owners[0]); got != markedThread {
		t.Fatalf("bypass = %+v, want the channel's, answered in the thread", got)
	}
}

// TestOneOwnerBypassedAndOneAskedForConsent: the asker outranks one of two
// people they name. Both are asked, in one ask, so the first answer wins as
// it does for any two; one is bypassed and the other decides for themself.
func TestOneOwnerBypassedAndOneAskedForConsent(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.member("UMAYA", "Maya")
	h.authority.outranks["usr_ada>usr_raya"] = true
	job := h.mention(toBot("ask <@URAYA> or <@UMAYA> for the plan"))
	h.mustRun(job)

	ask := h.ask()
	if len(ask.Owners) != 2 || bypassOf(ask.Owners[0]) != outranksDM || ask.Owners[1].Bypass != nil {
		t.Fatalf("owners = %+v, want Raya bypassed and Maya asked", ask.Owners)
	}
	if h.asks.createCalls != 1 {
		t.Fatalf("%d asks for one ask", h.asks.createCalls)
	}
	if _, ack := h.ack(job); len(ack.Owners) != 2 || ack.Owners[0].UserID != "URAYA" || ack.Owners[1].UserID != "UMAYA" {
		t.Fatalf("ack = %+v", ack)
	}
}

// TestAChartThatCannotBeReadIsRetriedThenTheOwnerDecides: waiting for a
// bypass is worth a few tries, and a chart that stays unreadable never
// becomes a bypass by being unreadable.
func TestAChartThatCannotBeReadIsRetriedThenTheOwnerDecides(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.authority.err = errors.New("the workspace service is down")
	job := h.mention(toBot("ask <@URAYA>"))

	for attempts := range maxAttempts - 1 {
		job.Attempts = attempts
		err := h.run(job)
		if err == nil || IsPermanent(err) {
			t.Fatalf("attempt %d: err = %v, want a failure to retry", attempts+1, err)
		}
		if len(h.asks.created) != 0 || len(h.slack.sent()) != 0 {
			t.Fatalf("attempt %d did something", attempts+1)
		}
	}
	job.Attempts = maxAttempts - 1
	h.mustRun(job)
	if got := h.ask().Owners[0].Bypass; got != nil {
		t.Fatalf("bypass = %+v, want the owner asked", got)
	}
}
