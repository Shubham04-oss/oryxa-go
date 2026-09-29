package slackrouter

import (
	"context"
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

func TestAMentionBecomesAnAsk(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
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

	// The asker, and only the asker, hears who was asked.
	ack := h.ack(job)
	post := h.only()
	wantTarget := Target{ChannelID: testChannel, ThreadTS: job2ts(h, job), UserID: testAsker}
	if post.Target != wantTarget || post.OrganizationID != testOrg {
		t.Fatalf("ack goes to %+v in %q, want the asker alone: %+v", post.Target, post.OrganizationID, wantTarget)
	}
	if ack.AskID != "ask_1" || len(ack.Owners) != 1 || ack.Owners[0] != (AskedOwner{Name: "Raya"}) || len(ack.Skipped) != 0 {
		t.Fatalf("ack = %+v", ack)
	}
	if wire(t, ack) != `{"ask_id":"ask_1","owners":[{"name":"Raya"}]}` {
		t.Fatalf("ack on the wire = %s", wire(t, ack))
	}
	// The mention is recorded as having made its ask, and both people were
	// resolved in one profile read.
	if h.asked.asks[job.InboundID] != "ask_1" {
		t.Fatalf("recorded asks = %+v", h.asked.asks)
	}
	if len(h.slack.profileAsk) != 1 || strings.Join(h.slack.profileAsk[0], ",") != testAsker+",URAYA" {
		t.Fatalf("profile reads = %+v", h.slack.profileAsk)
	}
	// The matches were kept for whoever must go back from a member.
	if identity, ok := h.identities.get(testTeam, "URAYA"); !ok || identity.MemberUserID != "usr_raya" || identity.Email != "raya@acme.com" || identity.OrganizationID != testOrg {
		t.Fatalf("Raya's match = %+v, %v", identity, ok)
	}
	if identity, ok := h.identities.get(testTeam, testAsker); !ok || identity.MemberUserID != "usr_ada" {
		t.Fatalf("the asker's match = %+v, %v", identity, ok)
	}
}

// job2ts is the ts of the message a job is about.
func job2ts(h *harness, job Job) string { return h.slack.inbound[job.InboundID].Mention.TS }

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
	for _, test := range []struct {
		name   string
		text   string
		setup  func(h *harness) []func(*Mention)
		reason consentpolicy.Reason
		names  []string
		hint   string
	}{
		{
			name: "an asker who is a guest",
			text: toBot("ask <@URAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.profile(testAsker, func(p *Profile) { p.Guest = true })
				return nil
			},
			reason: consentpolicy.NotMember, hint: "Only members",
		},
		{
			name: "an asker whose address no member uses",
			text: toBot("ask <@URAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.profile(testAsker, func(p *Profile) { p.Email = "ada@elsewhere.example" })
				return nil
			},
			reason: consentpolicy.NotMember, hint: "Only members",
		},
		{
			name: "an owner who is not in Oryxa",
			text: toBot("ask <@UMAYA> for the Q3 numbers"),
			setup: func(h *harness) []func(*Mention) {
				h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, DisplayName: "Maya"}
				return nil
			},
			reason: consentpolicy.OwnerNotMember, names: []string{"Maya"}, hint: "Maya isn't in Oryxa, so I didn't ask them.",
		},
		{
			name: "every owner named is not in Oryxa",
			text: toBot("ask <@UMAYA> and <@UDAVE>"),
			setup: func(h *harness) []func(*Mention) {
				h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, DisplayName: "Maya"}
				h.slack.profiles["UDAVE"] = Profile{UserID: "UDAVE", TeamID: testTeam, Email: "dave@acme.com", EmailConfirmed: true, DisplayName: "Dave"}
				return nil
			},
			reason: consentpolicy.OwnerNotMember, names: []string{"Maya", "Dave"}, hint: "Maya and Dave aren't in Oryxa",
		},
		{
			name: "a group message",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channels[testChannel] = ChannelInfo{ID: testChannel, Private: true, GroupDM: true}
				return nil
			},
			reason: consentpolicy.GroupDM, hint: "group messages",
		},
		{
			name: "a group message the delivery says so of",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				return []func(*Mention){func(m *Mention) { m.ChannelType = "mpim" }}
			},
			reason: consentpolicy.GroupDM, hint: "group messages",
		},
		{
			name: "a channel shared with another organization",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.slack.channels[testChannel] = ChannelInfo{ID: testChannel, Shared: true}
				return nil
			},
			reason: consentpolicy.ExternalChannel, hint: "shared with other organizations",
		},
		{
			name: "a channel the delivery says is shared",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				return []func(*Mention){func(m *Mention) { m.SharedExternally = true }}
			},
			reason: consentpolicy.ExternalChannel, hint: "shared with other organizations",
		},
		{
			name:   "a mention that names nobody",
			text:   toBot("what's the Q3 number?"),
			reason: consentpolicy.NoOwner, hint: "Say who to ask",
		},
		{
			name:   "a mention that names only the asker",
			text:   toBot("ask <@" + testAsker + "> for the Q3 numbers"),
			reason: consentpolicy.NoOwner, hint: "Say who to ask",
		},
		{
			name:   "a mention of a group and of everyone",
			text:   toBot("ask <!subteam^S123|@eng> and <!channel> for the Q3 numbers"),
			reason: consentpolicy.NoOwner, hint: "Say who to ask",
		},
		{
			name: "an asker with five asks open",
			text: toBot("ask <@URAYA>"),
			setup: func(h *harness) []func(*Mention) {
				h.member("URAYA", "Raya")
				h.asks.open["usr_ada"] = consentpolicy.MaxOpenAsks
				return nil
			},
			reason: consentpolicy.TooManyAsks, hint: "already have 5 requests waiting",
		},
		{
			name: "a mention that names six people",
			text: toBot("ask <@U1> <@U2> <@U3> <@U4> <@U5> <@U6>"),
			setup: func(h *harness) []func(*Mention) {
				for _, id := range []string{"U1", "U2", "U3", "U4", "U5", "U6"} {
					h.member(id, "Person"+id)
				}
				return nil
			},
			reason: consentpolicy.TooManyAsks, hint: "at most 5 people",
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
			if params.Reason != test.reason {
				t.Fatalf("reason = %q, want %q (hint %q)", params.Reason, test.reason, params.Hint)
			}
			if !strings.Contains(params.Hint, test.hint) || strings.Contains(params.Hint, "\n") {
				t.Fatalf("hint = %q, want one line holding %q", params.Hint, test.hint)
			}
			if strings.Join(params.Names, ",") != strings.Join(test.names, ",") {
				t.Fatalf("names = %v, want %v", params.Names, test.names)
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

func TestOnePersonNotInOryxaIsLeftOutNotRefused(t *testing.T) {
	h := newHarness(t)
	raya := h.member("URAYA", "Raya")
	h.slack.profiles["UMAYA"] = Profile{UserID: "UMAYA", TeamID: testTeam, Email: "maya@acme.com", EmailConfirmed: true, DisplayName: "Maya"}
	job := h.mention(toBot("ask <@UMAYA> or <@URAYA>"))
	h.mustRun(job)

	ask := h.ask()
	if len(ask.Owners) != 1 || ask.Owners[0].Person != raya {
		t.Fatalf("owners = %+v, want Raya alone", ask.Owners)
	}
	ack := h.ack(job)
	if len(ack.Owners) != 1 || ack.Owners[0].Name != "Raya" || len(ack.Skipped) != 1 || ack.Skipped[0] != "Maya" {
		t.Fatalf("ack = %+v, want Raya asked and Maya left out", ack)
	}
	if want := `{"ask_id":"ask_1","owners":[{"name":"Raya"}],"skipped":["Maya"]}`; wire(t, ack) != want {
		t.Fatalf("ack on the wire = %s, want %s", wire(t, ack), want)
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
	ack := h.ack(job)
	if len(ack.Owners) != 2 || ack.Owners[0].Name != "Raya" || ack.Owners[1].Name != "Maya" {
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
	if _, params := h.refusalAfterAsks(job, 1); params.Reason != consentpolicy.TooManyAsks {
		t.Fatalf("reason = %q", params.Reason)
	}
	// The limit is the asker's own, as a member of the organization.
	if len(h.asks.created) != 1 || h.asks.countCalls != 2 {
		t.Fatalf("counted %d times", h.asks.countCalls)
	}
}

// refusalAfterAsks is refusal for a harness that has already made some asks.
func (h *harness) refusalAfterAsks(job Job, asks int) (Post, RefusalParams) {
	h.t.Helper()
	post := h.only()
	if post.Template != TemplateRefusal {
		h.t.Fatalf("posted %q, want a refusal", post.Template)
	}
	if len(h.asks.created) != asks {
		h.t.Fatalf("made %d asks, want %d", len(h.asks.created), asks)
	}
	return post, post.Params.(RefusalParams)
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
				h.slack.profiles["UOTHERBOT"] = Profile{UserID: "UOTHERBOT", TeamID: testTeam, Bot: true, DisplayName: "Other"}
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
		"a channel Slack will not describe": {
			func(h *harness) Job {
				h.member("URAYA", "Raya")
				return h.mention(toBot("ask <@URAYA>"), func(m *Mention) { m.ChannelID = "CNOTFOUND" })
			},
			"not readable",
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
	h.slack.profiles["UOTHERBOT"] = Profile{UserID: "UOTHERBOT", TeamID: testTeam, Bot: true, DisplayName: "Other"}
	job := h.mention(toBot("hello"), func(m *Mention) { m.UserID = "UOTHERBOT" })
	if err := h.run(job); !IsPermanent(err) {
		t.Fatalf("err = %v", err)
	}
	if len(h.slack.sent()) != 0 {
		t.Fatalf("a bot was answered: %+v", h.slack.sent())
	}
}

// TestNamesInMessagesAreCleaned: a name is a person's own words and reaches a
// message about them; the markup Slack reads is removed before it does.
func TestNamesInMessagesAreCleaned(t *testing.T) {
	h := newHarness(t)
	h.slack.profiles["UEVIL"] = Profile{
		UserID: "UEVIL", TeamID: testTeam, Email: "evil@acme.com", EmailConfirmed: true,
		DisplayName: "<https://phish.example|Click>\n<!channel> & co\u202e" + strings.Repeat("x", 100),
	}
	job := h.mention(toBot("ask <@UEVIL>"))
	h.mustRun(job)
	_, params := h.refusal(job)
	if len(params.Names) != 1 {
		t.Fatalf("names = %v", params.Names)
	}
	name := params.Names[0]
	if strings.ContainsAny(name, "<>&|\n\u202e") || len([]rune(name)) > maxNameRunes {
		t.Fatalf("name %q carries markup, or is too long", name)
	}
	if strings.ContainsAny(params.Hint, "<>&|") {
		t.Fatalf("hint %q carries markup", params.Hint)
	}
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

func TestEveryRefusalReasonHasAHint(t *testing.T) {
	for _, reason := range []consentpolicy.Reason{
		consentpolicy.NotMember, consentpolicy.OwnerNotMember, consentpolicy.GroupDM,
		consentpolicy.ExternalChannel, consentpolicy.NoOwner, consentpolicy.TooManyAsks,
	} {
		hint := refusalHint(reason, []string{"Maya"}, false)
		if hint == "" || strings.Contains(hint, "\n") || hint == refusalHint("unknown", nil, false) {
			t.Errorf("%s: hint %q", reason, hint)
		}
	}
	if refusalHint(consentpolicy.TooManyAsks, nil, true) == refusalHint(consentpolicy.TooManyAsks, nil, false) {
		t.Error("too many people and too many open asks read the same")
	}
	if got := refusalHint(consentpolicy.OwnerNotMember, []string{"A", "B", "C"}, false); !strings.HasPrefix(got, "A, B and C aren't") {
		t.Errorf("hint = %q", got)
	}
}

// TestRefusalOnTheWire pins what an adapter encodes for the refusal template,
// which apps reads: the reason codes are its contract.
func TestRefusalOnTheWire(t *testing.T) {
	got := wire(t, RefusalParams{Reason: consentpolicy.OwnerNotMember, Hint: "Maya isn't in Oryxa, so I didn't ask them.", Names: []string{"Maya"}})
	want := `{"reason":"owner_not_member","hint":"Maya isn't in Oryxa, so I didn't ask them.","names":["Maya"]}`
	if got != want {
		t.Fatalf("on the wire: %s, want %s", got, want)
	}
	if got := wire(t, RefusalParams{Reason: consentpolicy.NoOwner, Hint: "h"}); got != `{"reason":"no_owner","hint":"h"}` {
		t.Fatalf("on the wire: %s", got)
	}
}
