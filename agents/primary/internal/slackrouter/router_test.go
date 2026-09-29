package slackrouter

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/slackrouter/consentpolicy"
)

func TestNewRouterNeedsWhatItReadsFrom(t *testing.T) {
	full := Deps{Slack: newFakeSlack(), Members: &fakeMembers{}, Identities: &fakeIdentities{}, Asks: &fakeAsks{}}
	if _, err := NewRouter(full); err != nil {
		t.Fatal(err)
	}
	for name, deps := range map[string]Deps{
		"no Slack API":  {Members: full.Members, Identities: full.Identities, Asks: full.Asks},
		"no members":    {Slack: full.Slack, Identities: full.Identities, Asks: full.Asks},
		"no identities": {Slack: full.Slack, Members: full.Members, Asks: full.Asks},
		"no consent":    {Slack: full.Slack, Members: full.Members, Identities: full.Identities},
	} {
		if _, err := NewRouter(deps); err == nil {
			t.Errorf("%s: NewRouter accepted it", name)
		}
	}
}

// job2ts is the ts of the message a job is about.
func job2ts(h *harness, job Job) string { return h.slack.inbound[job.InboundID].Mention.TS }

func TestAMentionBecomesAnAsk(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	h.router.deps.OpenURL = func(askID string) string { return "https://app.oryxa.test/agents/requests?ask_id=" + askID }
	job := h.mention(toBot("ask <@URAYA> for the Q3 numbers"))
	h.mustRun(job)

	ask := h.ask()
	if ask.OrganizationID != testOrg || ask.Asker.UserID != "usr_ada" || ask.Asker.SlackUserID != testAsker ||
		ask.Asker.Email != "ada@acme.com" || ask.Asker.Name != "Ada" {
		t.Fatalf("asker = %+v in %q", ask.Asker, ask.OrganizationID)
	}
	if ask.Question != "ask @Raya for the Q3 numbers" {
		t.Fatalf("question = %q", ask.Question)
	}
	// A top-level mention starts its own thread, and is its own trigger.
	if want := (Origin{TeamID: testTeam, ChannelID: testChannel, ThreadTS: job2ts(h, job), TriggerTS: job2ts(h, job), AskerSlackUserID: testAsker}); ask.Origin != want {
		t.Fatalf("origin = %+v, want %+v", ask.Origin, want)
	}
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya || ask.Owners[0].Bypass != nil {
		t.Fatalf("owners = %+v, want %+v asked for consent", ask.Owners, raya)
	}

	// The asker, and only the asker, hears who was asked, in the thread of
	// their message.
	post, ack := h.ack(job)
	if want := (Target{ChannelID: testChannel, ThreadTS: job2ts(h, job), UserID: testAsker}); post.Target != want {
		t.Fatalf("ack goes to %+v, want the asker alone: %+v", post.Target, want)
	}
	if len(h.slack.sent()) != 1 || len(ack.Owners) != 1 || ack.Owners[0].UserID != "URAYA" {
		t.Fatalf("ack = %+v among %d posts", ack, len(h.slack.sent()))
	}
	if got, want := wire(t, ack), `{"owners":[{"user_id":"URAYA"}],"open_url":"https://app.oryxa.test/agents/requests?ask_id=ask_1"}`; got != want {
		t.Fatalf("ack on the wire = %s, want %s", got, want)
	}
	// The mention is recorded as having made its ask, and both people were
	// resolved in one profile read.
	if h.asked.asks[job.InboundID] != "ask_1" {
		t.Fatalf("recorded asks = %+v", h.asked.asks)
	}
	if len(h.slack.profileAsk) != 1 || strings.Join(h.slack.profileAsk[0], ",") != testAsker+",URAYA" {
		t.Fatalf("profile reads = %+v", h.slack.profileAsk)
	}
	// The matches were kept for whoever must go back from a member to their
	// Slack user, such as to send them a direct message.
	if identity, ok := h.identities.get(testTeam, "URAYA"); !ok || identity.MemberUserID != "usr_raya" || identity.Email != "raya@acme.com" || identity.OrganizationID != testOrg {
		t.Fatalf("Raya's match = %+v, %v", identity, ok)
	}
	if identity, ok := h.identities.get(testTeam, testAsker); !ok || identity.MemberUserID != "usr_ada" {
		t.Fatalf("the asker's match = %+v, %v", identity, ok)
	}
}

func TestTheAckHasNoLinkWithoutAWebAddress(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA>"))
	h.mustRun(job)
	_, ack := h.ack(job)
	if got, want := wire(t, ack), `{"owners":[{"user_id":"URAYA"}]}`; got != want {
		t.Fatalf("ack on the wire = %s, want %s", got, want)
	}
}

// TestTheAckIsLeftToTheSurfacesWhenTheyDoIt: with SkipAck the ask is made and
// the asker is told nothing by the router, but still told who was left out.
func TestTheAckIsLeftToTheSurfacesWhenTheyDoIt(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, Name: "Maya"}
	h.router.deps.SkipAck = true
	job := h.mention(toBot("ask <@URAYA> or <@UMAYA>"))
	h.mustRun(job)
	if len(h.asks.created) != 1 {
		t.Fatal("the ask was not made")
	}
	posts := h.slack.sent()
	if len(posts) != 1 || posts[0].Template != TemplateRefusal || posts[0].Params.(RefusalParams).UserID != "UMAYA" {
		t.Fatalf("posts = %+v, want only the word about Maya", posts)
	}
}

func TestAMentionInAThreadIsAnsweredThere(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA> about pricing"), func(m *Mention) { m.ThreadTS = "1699999999.000100" })
	h.mustRun(job)
	ask := h.ask()
	if ask.Origin.ThreadTS != "1699999999.000100" || ask.Origin.TriggerTS != job2ts(h, job) {
		t.Fatalf("origin = %+v: the answer belongs in the thread the mention was in", ask.Origin)
	}
	if post := h.only(); post.Target.ThreadTS != "1699999999.000100" {
		t.Fatalf("ack target = %+v", post.Target)
	}
}

func TestRefusals(t *testing.T) {
	notInOryxa := func(h *harness, slackID, name string) {
		h.slack.profiles[slackID] = Profile{UserID: slackID, TeamID: testTeam, Email: strings.ToLower(name) + "@acme.com", EmailConfirmed: true, Name: name}
	}
	for _, test := range []struct {
		name   string
		text   string
		setup  func(h *harness) []func(*Mention)
		reason consentpolicy.Reason
		// the params on the wire
		wire string
	}{
		{
			name: "an asker who is a guest",
			text: toBot("ask <@URAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.profile(testAsker, func(p *Profile) { p.Guest = true })
				return nil
			},
			reason: consentpolicy.NotMember, wire: `{"reason":"not_member","organization_name":"Acme"}`,
		},
		{
			name: "an asker whose address no member uses",
			text: toBot("ask <@URAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.profile(testAsker, func(p *Profile) { p.Email = "ada@elsewhere.example" })
				return nil
			},
			reason: consentpolicy.NotMember, wire: `{"reason":"not_member","organization_name":"Acme"}`,
		},
		{
			name: "an asker refused without an organization name to give",
			text: toBot("ask <@URAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.profile(testAsker, func(p *Profile) { p.Guest = true })
				h.members.nameErr = errors.New("database down")
				return nil
			},
			reason: consentpolicy.NotMember, wire: `{"reason":"not_member"}`,
		},
		{
			name: "an owner who is not in Oryxa",
			text: toBot("ask <@UMAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				notInOryxa(h, "UMAYA", "Maya")
				return nil
			},
			reason: consentpolicy.OwnerNotMember, wire: `{"reason":"owner_not_member","user_id":"UMAYA"}`,
		},
		{
			name: "a group message",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channels[testChannel] = ChannelInfo{ID: testChannel, Private: true, MPIM: true}
				return nil
			},
			reason: consentpolicy.GroupDM, wire: `{"reason":"group_dm"}`,
		},
		{
			name: "a direct message",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channels[testChannel] = ChannelInfo{ID: testChannel, Private: true, IM: true}
				return nil
			},
			reason: consentpolicy.GroupDM, wire: `{"reason":"group_dm"}`,
		},
		{
			name: "a conversation Slack will not describe to the bot",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channelErr = ErrUnreadable
				return nil
			},
			reason: consentpolicy.GroupDM, wire: `{"reason":"group_dm"}`,
		},
		{
			name: "a group message the delivery says so of",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				return []func(*Mention){func(m *Mention) { m.ChannelType = "mpim" }}
			},
			reason: consentpolicy.GroupDM, wire: `{"reason":"group_dm"}`,
		},
		{
			name: "a channel shared with another organization",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channels[testChannel] = ChannelInfo{ID: testChannel, Shared: true}
				return nil
			},
			reason: consentpolicy.ExternalChannel, wire: `{"reason":"external_channel"}`,
		},
		{
			name: "a channel the delivery says is shared",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				return []func(*Mention){func(m *Mention) { m.SharedExternally = true }}
			},
			reason: consentpolicy.ExternalChannel, wire: `{"reason":"external_channel"}`,
		},
		{
			name:   "a mention that names nobody",
			text:   toBot("what's the Q3 number?"),
			reason: consentpolicy.NoOwner, wire: `{"reason":"no_owner"}`,
		},
		{
			name:   "a mention that names only the asker",
			text:   toBot("ask <@" + testAsker + "> for the Q3 numbers"),
			reason: consentpolicy.NoOwner, wire: `{"reason":"no_owner"}`,
		},
		{
			name:   "a mention of a group and of everyone",
			text:   toBot("ask <!subteam^S123|@eng> and <!channel> for the Q3 numbers"),
			reason: consentpolicy.NoOwner, wire: `{"reason":"no_owner"}`,
		},
		{
			name: "an asker with five asks open",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.asks.open["usr_ada"] = consentpolicy.MaxOpenAsks
				return nil
			},
			reason: consentpolicy.TooManyAsks, wire: `{"reason":"too_many_asks"}`,
		},
		{
			name: "a mention that names six people",
			text: toBot("ask <@UP001> <@UP002> <@UP003> <@UP004> <@UP005> <@UP006>"),
			setup: func(h *harness) []func(*Mention) {
				for _, id := range []string{"UP001", "UP002", "UP003", "UP004", "UP005", "UP006"} {
					h.member(id, "Person"+id)
				}
				return nil
			},
			reason: consentpolicy.TooManyAsks, wire: `{"reason":"too_many_asks"}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			h := newHarness(t)
			var changes []func(*Mention)
			if test.setup != nil {
				changes = test.setup(h)
			}
			job := h.mention(test.text, changes...)
			h.mustRun(job)

			post, params := h.refusal(job)
			if params.Reason != test.reason || wire(t, params) != test.wire {
				t.Fatalf("refusal on the wire = %s, want reason %q: %s", wire(t, params), test.reason, test.wire)
			}
			// Told to the asker in the thread of the mention, in nobody else's
			// hearing.
			want := Target{ChannelID: testChannel, ThreadTS: job2ts(h, job), UserID: testAsker}
			if post.Target != want || post.Template != TemplateRefusal {
				t.Fatalf("refusal = %+v, want it for the asker alone: %+v", post, want)
			}
		})
	}
}

// TestEveryPersonLeftOutIsToldOf: apps' refusal names one person, so each
// named person who is not in Oryxa is told of on their own, when nobody is
// asked at all.
func TestEveryPersonLeftOutIsToldOf(t *testing.T) {
	h := newHarness(t)
	for id, name := range map[string]string{"UMAYA": "Maya", "UDAVE": "Dave"} {
		h.slack.profiles[id] = Profile{UserID: id, TeamID: testTeam, Email: strings.ToLower(name) + "@acme.com", EmailConfirmed: true, Name: name}
	}
	job := h.mention(toBot("ask <@UMAYA> and <@UDAVE>"))
	h.mustRun(job)
	h.noAsk()
	got := h.refusals(job)
	if len(got) != 2 || got[0] != (RefusalParams{Reason: consentpolicy.OwnerNotMember, UserID: "UMAYA"}) ||
		got[1] != (RefusalParams{Reason: consentpolicy.OwnerNotMember, UserID: "UDAVE"}) {
		t.Fatalf("refusals = %+v, want one for each, in the order named", got)
	}
}

// TestARefusalIsRetriedUntilItIsTold: the refusal is the whole outcome of the
// job, so a job whose refusal could not be sent has not finished; and one
// that apps will never send is dropped, not retried.
func TestARefusalIsRetriedUntilItIsTold(t *testing.T) {
	h := newHarness(t)
	h.profile(testAsker, func(p *Profile) { p.Guest = true })
	job := h.mention(toBot("ask <@URAYA>"))
	h.slack.postErr = errors.New("apps is down")
	err := h.run(job)
	if err == nil || IsPermanent(err) {
		t.Fatalf("a refusal that was not sent finished the job: %v", err)
	}
	h.slack.postErr = Permanent(errors.New("apps will not post into that thread"))
	if err := h.run(job); !IsPermanent(err) {
		t.Fatalf("a refusal apps will never send is retried: %v", err)
	}
	h.slack.postErr = nil
	h.mustRun(job)
	if len(h.slack.sent()) != 1 {
		t.Fatalf("told %d times", len(h.slack.sent()))
	}
}

// TestOnePersonNotInOryxaIsLeftOutNotRefused: the rest are asked, and the
// asker is told, once for the ask and once for the person left out, who was
// not asked.
func TestOnePersonNotInOryxaIsLeftOutNotRefused(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, Name: "Maya"}
	job := h.mention(toBot("ask <@UMAYA> or <@URAYA>"))
	h.mustRun(job)

	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya {
		t.Fatalf("owners = %+v, want Raya alone", ask.Owners)
	}
	_, ack := h.ack(job)
	if len(ack.Owners) != 1 || ack.Owners[0].UserID != "URAYA" {
		t.Fatalf("ack = %+v, want Raya asked", ack)
	}
	var refusals []RefusalParams
	for _, post := range h.slack.sent() {
		if post.Template == TemplateRefusal {
			refusals = append(refusals, post.Params.(RefusalParams))
			if post.IdempotencyKey != "slackrouter:"+job.InboundID+":refusal:UMAYA" {
				t.Fatalf("refusal key = %q", post.IdempotencyKey)
			}
		}
	}
	if len(refusals) != 1 || refusals[0] != (RefusalParams{Reason: consentpolicy.OwnerNotMember, UserID: "UMAYA"}) {
		t.Fatalf("refusals = %+v, want Maya left out", refusals)
	}
}

// TestNotBeingToldWhoWasLeftOutLeavesTheAskStanding: the ask is the outcome.
func TestNotBeingToldWhoWasLeftOutLeavesTheAskStanding(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, Name: "Maya"}
	h.slack.postErrFor[TemplateRefusal] = errors.New("apps is down")
	job := h.mention(toBot("ask <@UMAYA> or <@URAYA>"))
	if err := h.run(job); err != nil {
		t.Fatalf("a refusal that was not sent failed a job whose ask was made: %v", err)
	}
	if len(h.asks.created) != 1 {
		t.Fatal("the ask was not made")
	}
}

func TestParallelOwnersAreOneAsk(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	maya := h.member("UMAYA", "Maya")
	job := h.mention(toBot("ask the dev keys from <@URAYA> or <@UMAYA> (engineering)"))
	h.mustRun(job)

	if h.asks.createCalls != 1 {
		t.Fatalf("CreateAsk called %d times, want once for one ask", h.asks.createCalls)
	}
	ask := h.ask()
	if len(ask.Owners) != 2 || ask.Owners[0].Person != raya || ask.Owners[1].Person != maya {
		t.Fatalf("owners = %+v, want Raya then Maya, in the order named", ask.Owners)
	}
	if ask.Question != "ask the dev keys from @Raya or @Maya (engineering)" {
		t.Fatalf("question = %q", ask.Question)
	}
	_, ack := h.ack(job)
	if len(ack.Owners) != 2 || ack.Owners[0].UserID != "URAYA" || ack.Owners[1].UserID != "UMAYA" {
		t.Fatalf("ack = %+v", ack)
	}
}

func TestAnOwnerNamedTwiceOrTheAskerNamedIsNotAskedTwice(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	h.mustRun(h.mention(toBot("ask <@URAYA> and <@URAYA|raya> and <@" + testAsker + "> about the offsite")))
	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya {
		t.Fatalf("owners = %+v, want Raya once and the asker not at all", ask.Owners)
	}
}

// TestTheSamePersonUnderAnotherAccountIsNotAnotherOwner: two Slack users who
// are one member, and a Slack user who is the asker, are one person each.
func TestTheSamePersonUnderAnotherAccountIsNotAnotherOwner(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	// A second Slack user with Raya's address, and one with the asker's.
	h.slack.profiles["URAYA2"] = Profile{UserID: "URAYA2", TeamID: testTeam, Email: "raya@acme.com", EmailConfirmed: true, Name: "Raya (old account)"}
	h.slack.profiles["UADA2"] = Profile{UserID: "UADA2", TeamID: testTeam, Email: "ada@acme.com", EmailConfirmed: true, Name: "Ada (old account)"}
	h.mustRun(h.mention(toBot("ask <@URAYA> and <@URAYA2> and <@UADA2>")))
	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person.UserID != raya.UserID || ask.Owners[0].Person.SlackUserID != "URAYA" {
		t.Fatalf("owners = %+v, want Raya once, on the account first named", ask.Owners)
	}

	// Named only under the asker's other account, nobody is asked.
	h2 := newHarness(t)
	h2.slack.profiles["UADA2"] = Profile{UserID: "UADA2", TeamID: testTeam, Email: "ada@acme.com", EmailConfirmed: true, Name: "Ada (old account)"}
	job := h2.mention(toBot("ask <@UADA2>"))
	h2.mustRun(job)
	if _, params := h2.refusal(job); params.Reason != consentpolicy.NoOwner {
		t.Fatalf("reason = %q", params.Reason)
	}
}

func TestTheOpenAskLimitIsFiveAtOnce(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.asks.open["usr_ada"] = consentpolicy.MaxOpenAsks - 1
	h.mustRun(h.mention(toBot("ask <@URAYA> once")))
	if len(h.asks.created) != 1 {
		t.Fatal("the fifth open ask was refused")
	}

	h.slack.posts, h.slack.keys = nil, map[string]bool{}
	h.asks.open["usr_ada"] = consentpolicy.MaxOpenAsks
	job := h.mention(toBot("ask <@URAYA> again"))
	h.mustRun(job)
	if len(h.asks.created) != 1 {
		t.Fatal("a sixth ask was made")
	}
	post := h.only()
	if params, ok := post.Params.(RefusalParams); !ok || params.Reason != consentpolicy.TooManyAsks {
		t.Fatalf("posted %+v, want the refusal", post)
	}
	// The limit is the asker's own, as a member of the organization.
	if h.asks.countCalls != 2 {
		t.Fatalf("counted %d times", h.asks.countCalls)
	}
}

func TestTheOpenAskCountIsNotReadForSomeoneWhoIsNotAMember(t *testing.T) {
	h := newHarness(t)
	h.profile(testAsker, func(p *Profile) { p.Deleted = true })
	h.mustRun(h.mention(toBot("ask <@URAYA>")))
	if h.asks.countCalls != 0 {
		t.Fatal("counted the open asks of someone who is not a member")
	}
}

func TestTheQuestionIsPlainText(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.member("UMAYA", "Maya")
	text := "<@UBOT>  ask <@URAYA|raya> and <@UMAYA>\n\n\n\nabout <#C99|billing> &amp; <https://example.com/a|the doc> " +
		"<!here> <mailto:x@y.example|x@y.example> &lt;script&gt; <@UNKNOWN>"
	h.mustRun(h.mention(text))
	want := "ask @Raya and @Maya\n\nabout #billing & the doc (https://example.com/a) @here x@y.example <script> @someone"
	if got := h.ask().Question; got != want {
		t.Fatalf("question = %q, want %q", got, want)
	}
}

func TestTransientFailuresAreRetriedAndDoNothing(t *testing.T) {
	for name, fail := range map[string]func(h *harness){
		"the inbound row":   func(h *harness) { h.slack.inboundErr = errors.New("apps timed out") },
		"the profiles":      func(h *harness) { h.slack.profilesErr = errors.New("apps timed out") },
		"the channel":       func(h *harness) { h.slack.channelErr = errors.New("apps timed out") },
		"the members":       func(h *harness) { h.members.err = errors.New("database down") },
		"the open asks":     func(h *harness) { h.asks.countErr = errors.New("database down") },
		"the match to keep": func(h *harness) { h.identities.saveErr = errors.New("database down") },
		"the ask":           func(h *harness) { h.asks.createErr = errors.New("database down") },
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			h.member("URAYA", "Raya")
			job := h.mention(toBot("ask <@URAYA>"))
			fail(h)
			err := h.run(job)
			if err == nil || IsPermanent(err) {
				t.Fatalf("a failure that may pass finished or dropped the job: %v", err)
			}
			if len(h.slack.sent()) != 0 {
				t.Fatalf("something was said before the ask was made: %+v", h.slack.sent())
			}
		})
	}
}

// TestAnAskThatCannotBeFiledIsDropped: a request the consent core refuses for
// good is not tried again.
func TestAnAskThatCannotBeFiledIsDropped(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.asks.createErr = Permanent(errors.New("invalid agent request"))
	if err := h.run(h.mention(toBot("ask <@URAYA>"))); !IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
}

func TestAFailureToAcknowledgeLeavesTheAskStanding(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA>"))
	h.slack.postErr = errors.New("apps is down")
	if err := h.run(job); err != nil {
		t.Fatalf("an acknowledgement that was not sent failed a job whose ask was made: %v", err)
	}
	if len(h.asks.created) != 1 || h.asked.asks[job.InboundID] == "" {
		t.Fatal("the ask was not made and recorded")
	}
}

func TestAnAskThatCouldNotBeRecordedIsStillMade(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.asked.err = errors.New("database down")
	job := h.mention(toBot("ask <@URAYA>"))
	if err := h.run(job); err != nil {
		t.Fatalf("failing to record the ask failed the job: %v", err)
	}
	h.ack(job)
}

// TestAnAskIsMadeOnceNoMatterHowOftenTheJobRuns is the idempotency that lives
// in the router: a job that already made its ask does nothing, and one that
// did not know it had makes the same ask again, not another.
func TestAnAskIsMadeOnceNoMatterHowOftenTheJobRuns(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA>"))
	h.mustRun(job)
	askID := h.asked.asks[job.InboundID]
	if askID == "" {
		t.Fatal("the ask was not recorded against the job")
	}

	// A retry told which ask its first attempt made does nothing at all, and
	// does not so much as count the asker's open asks, of which that one is
	// now one.
	h.asks.open["usr_ada"] = consentpolicy.MaxOpenAsks
	job.AskID = askID
	reads, counts := len(h.slack.inboundGet), h.asks.countCalls
	h.mustRun(job)
	if len(h.slack.inboundGet) != reads || h.asks.countCalls != counts || len(h.slack.sent()) != 1 {
		t.Fatalf("the finished job did something again: %d reads, %d counts, %d posts",
			len(h.slack.inboundGet)-reads, h.asks.countCalls-counts, len(h.slack.sent()))
	}

	// One that could not be told, because its first attempt ended before it
	// could say, asks for the same ask and is given it; and the asker is told
	// once, since apps sends one message for one key.
	h.asks.open["usr_ada"] = 0
	job.AskID = ""
	h.mustRun(job)
	if len(h.asks.created) != 1 || h.asks.createCalls != 2 {
		t.Fatalf("made %d asks in %d calls, want the one ask asked for twice", len(h.asks.created), h.asks.createCalls)
	}
	if posts := h.slack.sent(); len(posts) != 1 || posts[0].Template != TemplateAskAck {
		t.Fatalf("posts = %+v, want the one acknowledgement", posts)
	}
}

func TestWhatCanNeverBeRoutedIsDropped(t *testing.T) {
	for name, test := range map[string]struct {
		build func(h *harness) Job
		why   string
	}{
		"an inbound row apps purged": {
			func(h *harness) Job {
				return Job{InboundID: "in_gone", Kind: KindMessage, OrganizationID: testOrg, TeamID: testTeam}
			},
			"gone",
		},
		"a row of another organization": {
			func(h *harness) Job {
				job := h.mention(toBot("ask <@URAYA>"))
				job.OrganizationID = "org_other"
				return job
			},
			"not the one its event named",
		},
		"a row of another workspace": {
			func(h *harness) Job {
				job := h.mention(toBot("ask <@URAYA>"))
				job.TeamID = "T9"
				return job
			},
			"not the one its event named",
		},
		"a row of another kind": {
			func(h *harness) Job {
				job := h.mention(toBot("ask <@URAYA>"))
				job.Kind = KindAction
				return job
			},
			"not the one its event named",
		},
		"a message with no message in it": {
			func(h *harness) Job {
				job := h.mention("")
				inbound := h.slack.inbound[job.InboundID]
				inbound.Mention = nil
				h.slack.inbound[job.InboundID] = inbound
				return job
			},
			"no message",
		},
		"another kind of Slack event": {
			func(h *harness) Job {
				return h.mention("hi", func(m *Mention) { m.EventType = "message.im" })
			},
			"dropped",
		},
		"a message a bot wrote": {
			func(h *harness) Job { return h.mention("automated", func(m *Mention) { m.UserID = "" }) },
			"no human sender",
		},
		"a message the bot itself wrote": {
			func(h *harness) Job {
				return h.mention(toBot("hello"), func(m *Mention) { m.UserID = testBot })
			},
			"written by the bot",
		},
		"a message from another app": {
			func(h *harness) Job {
				h.slack.profiles["UOTHERBOT"] = Profile{UserID: "UOTHERBOT", TeamID: testTeam, Bot: true, Name: "Other"}
				return h.mention(toBot("ask <@URAYA>"), func(m *Mention) { m.UserID = "UOTHERBOT" })
			},
			"written by a bot",
		},
		"a delivery that does not say which user is the bot": {
			func(h *harness) Job { return h.mention("ask <@URAYA>", func(m *Mention) { m.BotUserID = "" }) },
			"bot",
		},
		"a message with no place": {
			func(h *harness) Job { return h.mention(toBot("hi"), func(m *Mention) { m.ChannelID = "" }) },
			"where it was written",
		},
		"a channel Slack does not know": {
			func(h *harness) Job {
				h.member("URAYA", "Raya")
				return h.mention(toBot("ask <@URAYA>"), func(m *Mention) { m.ChannelID = "CNOTFOUND" })
			},
			"not known to Slack",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			job := test.build(h)
			err := h.run(job)
			if !IsPermanent(err) || !strings.Contains(err.Error(), test.why) {
				t.Fatalf("err = %v, want a permanent failure about %q", err, test.why)
			}
			if len(h.slack.sent()) != 0 || len(h.asks.created) != 0 {
				t.Fatalf("something was done for a job that cannot be routed: posts %+v, asks %+v", h.slack.sent(), h.asks.created)
			}
		})
	}
}

// TestABotIsNeverAnswered: a refusal to a program would start a conversation
// between two programs.
func TestABotIsNeverAnswered(t *testing.T) {
	h := newHarness(t)
	h.slack.profiles["UOTHERBOT"] = Profile{UserID: "UOTHERBOT", TeamID: testTeam, Bot: true, Name: "Other"}
	job := h.mention(toBot("hello"), func(m *Mention) { m.UserID = "UOTHERBOT" })
	if err := h.run(job); !IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if len(h.slack.sent()) != 0 {
		t.Fatalf("a bot was answered: %+v", h.slack.sent())
	}
}

// TestASlackNameIsNeverPutInAMessage: a name is a person's own words. A
// refusal names the person by their Slack id, which apps shows as a mention;
// the name itself only ever goes into the question and to the consent core,
// cleaned of the markup Slack reads.
func TestASlackNameIsNeverPutInAMessage(t *testing.T) {
	evil := "<https://phish.example|Click>\n<!channel> & co\u202e" + strings.Repeat("x", 100)
	t.Run("in a refusal", func(t *testing.T) {
		h := newHarness(t)
		h.slack.profiles["UEVIL"] = Profile{UserID: "UEVIL", TeamID: testTeam, Email: "evil@acme.com", EmailConfirmed: true, Name: evil}
		job := h.mention(toBot("ask <@UEVIL>"))
		h.mustRun(job)
		_, params := h.refusal(job)
		if got := wire(t, params); got != `{"reason":"owner_not_member","user_id":"UEVIL"}` {
			t.Fatalf("refusal on the wire = %s", got)
		}
	})
	t.Run("to the consent core and in the question", func(t *testing.T) {
		h := newHarness(t)
		h.member("UEVIL", "Placeholder")
		h.slack.profiles["UEVIL"] = Profile{UserID: "UEVIL", TeamID: testTeam, Email: "placeholder@acme.com", EmailConfirmed: true, Name: evil}
		h.mustRun(h.mention(toBot("ask <@UEVIL>")))
		ask := h.ask()
		name := ask.Owners[0].Name
		if strings.ContainsAny(name, "<>&|\n\u202e") || len([]rune(name)) > maxNameRunes || name == "" {
			t.Fatalf("owner name %q carries markup, or is too long", name)
		}
		if strings.ContainsAny(ask.Question, "\u202e|") || strings.Contains(ask.Question, "<https") {
			t.Fatalf("question %q carries what the name did", ask.Question)
		}
	})
}

func TestContextIsHonouredMidJob(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	job := h.mention(toBot("ask <@URAYA>"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h.slack.inboundErr = ctx.Err()
	if err := h.router.Handle(ctx, job); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
}

// TestRefusalOnTheWire pins what an adapter encodes for the refusal template,
// which apps decodes strictly: any other field is refused.
func TestRefusalOnTheWire(t *testing.T) {
	for _, test := range []struct {
		params RefusalParams
		want   string
	}{
		{RefusalParams{Reason: consentpolicy.NotMember, OrganizationName: "Acme"}, `{"reason":"not_member","organization_name":"Acme"}`},
		{RefusalParams{Reason: consentpolicy.OwnerNotMember, UserID: "UMAYA"}, `{"reason":"owner_not_member","user_id":"UMAYA"}`},
		{RefusalParams{Reason: consentpolicy.NoOwner}, `{"reason":"no_owner"}`},
	} {
		if got := wire(t, test.params); got != test.want {
			t.Fatalf("on the wire: %s, want %s", got, test.want)
		}
	}
	if got := wire(t, Target{ChannelID: "C1", ThreadTS: "1700000000.000100", UserID: "U1"}); got != `{"channel_id":"C1","thread_ts":"1700000000.000100","user_id":"U1"}` {
		t.Fatalf("target on the wire: %s", got)
	}
}

// appsRefusal and appsAskAck are the fields apps' templates decode, copied from
// apps' own renderers (apps/internal/slackbot/templates.go), which decode with
// DisallowUnknownFields: a field they do not name is refused, and the message
// is never sent. This package cannot import them, so this is the contract as
// the router last read it; a change in apps' templates is a change here.
type appsRefusal struct {
	Reason           string `json:"reason"`
	OrganizationName string `json:"organization_name"`
	UserID           string `json:"user_id"`
	Name             string `json:"name"`
}

type appsAskAck struct {
	Owners []struct {
		UserID string `json:"user_id"`
		Name   string `json:"name"`
	} `json:"owners"`
	OpenURL string `json:"open_url"`
}

func strictDecode(t *testing.T, encoded string, into any) {
	t.Helper()
	decoder := json.NewDecoder(strings.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		t.Fatalf("apps would refuse %s: %v", encoded, err)
	}
}

// TestWhatTheRouterPostsIsWhatAppsTemplatesTake sends every kind of message the
// router posts through a strict decoder of apps' parameters.
func TestWhatTheRouterPostsIsWhatAppsTemplatesTake(t *testing.T) {
	h := newHarness(t)
	h.member("URAYA", "Raya")
	h.router.deps.OpenURL = func(askID string) string { return "https://app.oryxa.test/agents/requests?ask_id=" + askID }
	h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, Name: "Maya"}
	h.mustRun(h.mention(toBot("ask <@URAYA> or <@UMAYA>"))) // an ack, and Maya left out
	h.mustRun(h.mention(toBot("nobody named")))             // no_owner
	h.profile(testAsker, func(p *Profile) { p.Guest = true })
	h.mustRun(h.mention(toBot("ask <@URAYA>"))) // not_member, with the organization

	seen := map[string]int{}
	for _, post := range h.slack.sent() {
		encoded := wire(t, post.Params)
		switch post.Template {
		case TemplateRefusal:
			var decoded appsRefusal
			strictDecode(t, encoded, &decoded)
			if decoded.Reason == "" {
				t.Fatalf("a refusal without a reason: %s", encoded)
			}
		case TemplateAskAck:
			var decoded appsAskAck
			strictDecode(t, encoded, &decoded)
			if len(decoded.Owners) == 0 || len(decoded.Owners) > 5 {
				t.Fatalf("an ack of %d owners: apps takes one to five", len(decoded.Owners))
			}
			for _, owner := range decoded.Owners {
				if owner.UserID == "" {
					t.Fatalf("an owner with no user id: %s", encoded)
				}
			}
		default:
			t.Fatalf("posted a %q, which apps takes no such params of here", post.Template)
		}
		// A message to one person in a thread: channel, thread and user, which is
		// how apps tells an ephemeral message from a reply and from a DM.
		if post.Target.ChannelID == "" || post.Target.ThreadTS == "" || post.Target.UserID == "" {
			t.Fatalf("target %+v is not a person in a thread", post.Target)
		}
		seen[post.Template]++
	}
	if seen[TemplateRefusal] != 3 || seen[TemplateAskAck] != 1 {
		t.Fatalf("posted %v, want 1 ack and 3 refusals", seen)
	}
}
