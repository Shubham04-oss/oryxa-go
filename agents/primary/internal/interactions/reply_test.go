package interactions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/answerguard"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/policy"
)

// fakeConsents is the consent loop's side of the ledger as far as a choice
// goes: Consent decides a pending request as the store does, under the lock a
// lockedRepository holds when it shares one, and nothing else of it is there.
type fakeConsents struct {
	Consents
	repo *fakeRepository
	// mu, when set, is the lock a lockedRepository around repo takes, which the
	// store's row lock is.
	mu       *sync.Mutex
	consents int
}

func (fake *fakeConsents) Consent(_ context.Context, id string, record ConsentRecord, at time.Time) (agents.Interaction, *Lease, error) {
	if fake.mu != nil {
		fake.mu.Lock()
		defer fake.mu.Unlock()
	}
	request, ok := fake.repo.requests[id]
	switch {
	case !ok:
		return agents.Interaction{}, nil, ErrNotFound
	case request.Status != StatusPending:
		return agents.Interaction{}, nil, ErrConflict
	case !request.ExpiresAt.After(at):
		return agents.Interaction{}, nil, ErrExpired
	}
	decider := record.Decider
	request.Status = StatusRejected
	if record.Input.Decision == DecisionApproved {
		request.Status = StatusApproved
	}
	request.Decision = &agents.InteractionDecision{
		Decision: record.Input.Decision, Message: record.Input.Message,
		AllowedTools: nonNilStrings(record.Input.AllowedTools), GrantedPermissions: nonNilStrings(record.Input.GrantedPermissions),
		DecidedAt: at, DecidedBy: &decider, Consent: ConsentFor(record.Input.Decision, record.Input.GrantedPermissions),
	}
	request.Revision++
	request.UpdatedAt = at
	fake.repo.requests[id] = request
	fake.consents++
	return request, nil, nil
}

// Reply takes the lock the store's row lock stands for, as Get and Review do.
func (repo *lockedRepository) Reply(ctx context.Context, id string, reply ReplyRecord, at time.Time) (agents.Interaction, error) {
	repo.mu.Lock()
	defer repo.mu.Unlock()
	return repo.Repository.Reply(ctx, id, reply, at)
}

// maskingGuard reads what it is given as the answer guard does: the values in
// masks are the kinds it always masks, and masks them; the values in holds are
// the kinds it holds an agent's answer for, which it names and leaves as they
// are, beside a fully masked version; unchecked is a guard that could not
// read anything. It keeps what it was asked.
type maskingGuard struct {
	mu        sync.Mutex
	masks     map[string]string
	holds     map[string]string
	unchecked bool
	checked   [][]string
	relevance []*answerguard.RelevanceInput
}

func (guard *maskingGuard) CheckWith(_ context.Context, texts []string, relevance *answerguard.RelevanceInput) answerguard.Verdict {
	guard.mu.Lock()
	defer guard.mu.Unlock()
	guard.checked = append(guard.checked, slices.Clone(texts))
	guard.relevance = append(guard.relevance, relevance)
	if guard.unchecked {
		return answerguard.Verdict{State: answerguard.StateHeld, Kinds: []string{answerguard.Unchecked}, Texts: slices.Clone(texts), Given: slices.Clone(texts)}
	}
	delivered := slices.Clone(texts)
	var masked, held []string
	for index := range delivered {
		for value, kind := range guard.masks {
			if strings.Contains(delivered[index], value) {
				delivered[index] = strings.ReplaceAll(delivered[index], value, "{"+kind+"}")
				masked = append(masked, kind)
			}
		}
	}
	fully := slices.Clone(delivered)
	for index := range fully {
		for value, kind := range guard.holds {
			if strings.Contains(fully[index], value) {
				fully[index] = strings.ReplaceAll(fully[index], value, "{"+kind+"}")
				held = append(held, kind)
			}
		}
	}
	sorted := func(kinds []string) []string {
		slices.Sort(kinds)
		return slices.Compact(kinds)
	}
	switch {
	case len(held) > 0:
		return answerguard.Verdict{State: answerguard.StateHeld, Kinds: sorted(held), Texts: delivered, Masked: fully,
			Given: slices.Clone(texts), SecretKinds: sorted(masked)}
	case len(masked) > 0:
		return answerguard.Verdict{State: answerguard.StateMasked, Kinds: sorted(masked), Texts: delivered,
			Given: slices.Clone(texts), SecretKinds: sorted(masked)}
	}
	return answerguard.Verdict{State: answerguard.StateClear, Texts: slices.Clone(texts), Given: slices.Clone(texts)}
}

// slackReplyService is the ledger with the tickets of a known key, and a
// choice's side of the consent loop over its fake store.
func slackReplyService(t *testing.T, changes ...func(*Config)) (*Service, *fakeRepository) {
	t.Helper()
	repo := newFakeRepository()
	config := Config{
		Repository: repo, Directory: fakeDirectory{}, Now: func() time.Time { return testNow },
		TicketKey: ticketKey, Consents: &fakeConsents{repo: repo},
	}
	for _, change := range changes {
		change(&config)
	}
	service, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return service, repo
}

// pendingFromSlack puts in the ledger a request asked in Slack by user_a of
// user_b's primary agent, waiting for user_b, and returns it.
func pendingFromSlack(repo *fakeRepository) agents.Interaction {
	request := agents.Interaction{
		ID: "irq_slack", OrganizationID: "org_1", Approval: ApprovalOwner, Origin: OriginSlack, AskID: "iak_1",
		Requester: agents.InteractionParty{UserID: "user_a", AgentID: "agt_a", AgentName: "Crew"},
		Target:    agents.InteractionParty{UserID: "user_b", AgentID: "agt_b", AgentName: "Carl", UserDisplayName: "B"},
		Purpose:   "Named in a message a member sent in Slack.", Task: "When do we ship the launch?",
		Context: []agents.InteractionContextItem{}, RequestedTools: []string{agents.KnowledgeToolboxName}, ToolsJudgedBy: ToolsJudgedByRequester,
		RequestedPermissions: []string{PermissionConsentSummary, PermissionConsentAccess},
		Status:               StatusPending, Revision: 1,
		ExpiresAt: testNow.Add(24 * time.Hour), CreatedAt: testNow.Add(-time.Minute), UpdatedAt: testNow.Add(-time.Minute),
	}
	repo.requests[request.ID] = request
	return request
}

// theOwner is user_b in their own signed-in session, the only actor a reply on
// the web is taken from.
var theOwner = inOwnSession(targetPrimary)

const ownWords = "We ship on the 14th, and I sign it off myself."

// TestAReplyToAPendingRequestApprovesAndAnswersItInOneChange: the request is
// approved with a reply's consent, granting its agent nothing, and completed
// with the owner's words as they wrote them, trimmed, which the guard read
// without a relevance check; and both members read it as the owner's own.
func TestAReplyToAPendingRequestApprovesAndAnswersItInOneChange(t *testing.T) {
	ctx := context.Background()
	guard := &maskingGuard{}
	service, repo := slackReplyService(t, func(config *Config) { config.Guard = guard })
	pending := pendingFromSlack(repo)

	replied, err := service.ReplyAsOwner(ctx, theOwner, "member-token", pending.ID, "  "+ownWords+"\n")
	if err != nil {
		t.Fatal(err)
	}
	if replied.Status != StatusCompleted || replied.Revision != pending.Revision+2 || repo.replies != 1 {
		t.Fatalf("replied = %+v (%d replies), want completed two revisions on, once", replied, repo.replies)
	}
	decision := replied.Decision
	want := agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceWeb}
	if decision == nil || decision.Decision != DecisionApproved || decision.Consent != ConsentReply || decision.Message != replyDecided ||
		len(decision.AllowedTools) != 0 || len(decision.GrantedPermissions) != 0 || decision.DecidedBy == nil || *decision.DecidedBy != want ||
		!decision.DecidedAt.Equal(testNow) {
		t.Fatalf("decision = %+v, want the owner's reply on the web, granting nothing", decision)
	}
	result := replied.Result
	if result == nil || result.Status != StatusCompleted || result.Summary != ownWords || result.Content != "" || !result.WrittenByOwner ||
		len(result.ToolsUsed) != 0 || len(result.Artifacts) != 0 || !result.CompletedAt.After(decision.DecidedAt) {
		t.Fatalf("result = %+v, want the owner's words, written by them, after the approval", result)
	}
	if len(guard.checked) != 1 || guard.checked[0][0] != ownWords || guard.relevance[0] != nil {
		t.Fatalf("the guard read %q with %v, want the words alone and no relevance check", guard.checked, guard.relevance)
	}
	if replied.Guard == nil || replied.Guard.State != answerguard.StateClear {
		t.Fatalf("guard = %+v, want clear", replied.Guard)
	}
	// The member who asked reads the owner's words as the answer, and that they
	// are the owner's.
	asker, err := service.Get(ctx, requester, pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(asker)
	for _, part := range []string{`"written_by_owner":true`, `"consent":"reply"`, `"summary":"` + ownWords + `"`, `"status":"completed"`} {
		if !strings.Contains(string(encoded), part) {
			t.Errorf("the asker reads %s, without %s", encoded, part)
		}
	}
}

// TestAReplyReplacesTheAnswerHeldForReview: the owner's words are delivered in
// its place, the review says so, and nothing of the held answer, as it was
// held or masked, is ever what either member reads.
func TestAReplyReplacesTheAnswerHeldForReview(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	service.guard = &maskingGuard{}

	replied, err := service.ReplyAsOwner(ctx, theOwner, "member-token", request.ID, ownWords)
	if err != nil {
		t.Fatal(err)
	}
	if replied.Status != StatusCompleted || replied.Held != nil || replied.Result == nil || replied.Result.Summary != ownWords ||
		!replied.Result.WrittenByOwner || replied.Revision != request.Revision+1 {
		t.Fatalf("replied = %+v, want completed with the owner's words and nothing held", replied)
	}
	review := replied.Review
	if review == nil || review.Action != ReviewReply || review.DecidedBy != (agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceWeb}) ||
		!review.DecidedAt.Equal(testNow) {
		t.Fatalf("review = %+v, want the owner's reply on the web", review)
	}
	if replied.Decision == nil || replied.Decision.Consent != ConsentSummary {
		t.Fatalf("the decision became %+v, want the owner's earlier choice as it was", replied.Decision)
	}
	// What the guard found in the held answer went with it.
	if replied.Guard == nil || replied.Guard.State != answerguard.StateClear || len(replied.Guard.Kinds) != 0 {
		t.Fatalf("guard = %+v, want what was found in the reply alone", replied.Guard)
	}
	stored := repo.requests[request.ID]
	for _, member := range []agents.InteractionActor{requester, theOwner} {
		read, err := service.Get(ctx, member, request.ID)
		if err != nil {
			t.Fatal(err)
		}
		encoded, _ := json.Marshal(read)
		for _, held := range append(append([]string{}, textsAsHeld...), textsMasked...) {
			for _, line := range strings.Split(held, "\n") {
				if strings.Contains(string(encoded), line) {
					t.Errorf("%s reads %q of the held answer: %s", member.UserID, line, encoded)
				}
			}
		}
	}
	if stored.Held != nil || repo.reviews != 0 {
		t.Fatalf("the held answer is still there, or was reviewed: %+v, %d reviews", stored.Held, repo.reviews)
	}
	// The review is over: sending the held answer now, on either surface,
	// finds it no longer in review.
	if _, err := service.ReviewAsOwner(ctx, theOwner, "member-token", request.ID, agents.InteractionReviewInput{Action: ReviewSendMasked}); !errors.Is(err, ErrConflict) {
		t.Fatalf("sending the held answer after the reply: %v, want ErrConflict", err)
	}
}

// TestOnlyTheOwnerRepliesAndOnlyInTheirOwnSession: an agent's run, the member
// who asked, another member and another organization each reply nothing.
func TestOnlyTheOwnerRepliesAndOnlyInTheirOwnSession(t *testing.T) {
	ctx := context.Background()
	service, repo := slackReplyService(t)
	pending := pendingFromSlack(repo)
	elsewhere := theOwner
	elsewhere.OrganizationID = "org_2"
	for name, test := range map[string]struct {
		actor agents.InteractionActor
		want  error
	}{
		"the owner's agent, in a run":      {targetPrimary, ErrForbidden},
		"the owner's specialist, in a run": {target, ErrForbidden},
		"the member who asked":             {inOwnSession(requester), ErrForbidden},
		"another member":                   {inOwnSession(otherMember), ErrNotFound},
		"the owner, in another org":        {elsewhere, ErrNotFound},
	} {
		if _, err := service.ReplyAsOwner(ctx, test.actor, "token", pending.ID, ownWords); !errors.Is(err, test.want) {
			t.Errorf("%s: %v, want %v", name, err, test.want)
		}
	}
	if _, err := service.ReplyAsOwner(ctx, theOwner, "token", "irq_missing", ownWords); !errors.Is(err, ErrNotFound) {
		t.Errorf("a request that is not there: %v, want ErrNotFound", err)
	}
	if stored := repo.requests[pending.ID]; stored.Status != StatusPending || repo.replies != 0 {
		t.Fatalf("a refused reply changed the request: %+v", stored)
	}
}

// TestAReplyIsOneToFourThousandCharactersOnceTrimmed: an empty reply, or one of
// spaces, a longer one, and one with what cannot be kept are refused before
// the request is read; 4000 characters, in any script, is a reply.
func TestAReplyIsOneToFourThousandCharactersOnceTrimmed(t *testing.T) {
	ctx := context.Background()
	for name, text := range map[string]string{
		"nothing":          "",
		"spaces":           "   \n\t ",
		"4001 characters":  strings.Repeat("a", MaxReplyRunes+1),
		"4001 in Hindi":    strings.Repeat("क", MaxReplyRunes+1),
		"a NUL in it":      "yes\x00no",
		"not UTF-8 at all": "yes\xffno",
	} {
		service, repo := slackReplyService(t)
		pending := pendingFromSlack(repo)
		if _, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, text); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v, want ErrInvalid", name, err)
		}
		if repo.replies != 0 {
			t.Errorf("%s was recorded", name)
		}
	}
	for name, text := range map[string]string{
		"4000 characters": strings.Repeat("a", MaxReplyRunes),
		"4000 in Hindi":   strings.Repeat("क", MaxReplyRunes),
		"padded to 4010":  "     " + strings.Repeat("b", MaxReplyRunes) + "     ",
		"one character":   "y",
	} {
		service, repo := slackReplyService(t)
		pending := pendingFromSlack(repo)
		replied, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, text)
		if err != nil || replied.Result == nil || replied.Result.Summary != strings.TrimSpace(text) {
			t.Errorf("%s: %+v, %v", name, replied.Result, err)
		}
	}
}

// TestAReplyAfterAnotherDecisionIsAlreadyDecided: a request decided, answered,
// replied to, reviewed, closed or withheld is not the owner's to reply to any
// more, and the reply says who won; one past its expiry is expired.
func TestAReplyAfterAnotherDecisionIsAlreadyDecided(t *testing.T) {
	ctx := context.Background()
	for name, edit := range map[string]func(*agents.Interaction){
		"approved, the agent at work": func(r *agents.Interaction) {
			r.Status = StatusApproved
			r.Decision = &agents.InteractionDecision{Decision: DecisionApproved, Consent: ConsentSummary,
				DecidedBy: &agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceSlack}, DecidedAt: testNow.Add(-time.Second)}
		},
		"denied": func(r *agents.Interaction) {
			r.Status = StatusRejected
			r.Decision = &agents.InteractionDecision{Decision: DecisionRejected, DecidedBy: &agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceWeb}}
		},
		"answered":                     func(r *agents.Interaction) { r.Status = StatusCompleted },
		"failed":                       func(r *agents.Interaction) { r.Status = StatusFailed },
		"withheld":                     func(r *agents.Interaction) { r.Status = StatusWithheld },
		"in review, with nothing held": func(r *agents.Interaction) { r.Status = StatusReview },
	} {
		service, repo := slackReplyService(t)
		pending := pendingFromSlack(repo)
		stored := repo.requests[pending.ID]
		edit(&stored)
		repo.requests[pending.ID] = stored
		lost, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords)
		if !errors.Is(err, ErrAlreadyDecided) {
			t.Errorf("%s: %v, want ErrAlreadyDecided", name, err)
			continue
		}
		if lost.ID != pending.ID || lost.Status != stored.Status {
			t.Errorf("%s: the reply that lost reads %+v, want the request as it stands", name, lost)
		}
		if repo.replies != 0 {
			t.Errorf("%s: the reply was recorded", name)
		}
	}
	service, repo := slackReplyService(t)
	pending := pendingFromSlack(repo)
	service.now = func() time.Time { return pending.ExpiresAt }
	if _, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords); !errors.Is(err, ErrExpired) {
		t.Fatalf("a reply to an expired request: %v, want ErrExpired", err)
	}
}

// TestTheGuardsTakeSecretsOutOfAReplyAndHoldNothing: the kinds the answer guard
// always masks are masked, credentials are taken out, and what it would hold
// an agent's answer for, an address the owner chose to give, is left as they
// wrote it; nothing holds the reply, not a kind, not a guard that could not
// read it, and not an organization that holds every answer for its owner.
func TestTheGuardsTakeSecretsOutOfAReplyAndHoldNothing(t *testing.T) {
	ctx := context.Background()
	const token = "xoxb-123456789012-1234567890123-AbCdEfGhIjKlMnOpQrStUvWx"
	words := "Write to priya@example.com; the PIN is 4921 and the bot token is " + token + "."
	guard := &maskingGuard{masks: map[string]string{"4921": "PIN"}, holds: map[string]string{"priya@example.com": "EMAIL"}}
	var logs bytes.Buffer
	service, repo := slackReplyService(t, func(config *Config) {
		config.Guard = guard
		config.Policy = fakeWorkspacePolicy{settings: policy.Settings{HoldForReview: true}}
		config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	pending := pendingFromSlack(repo)
	replied, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, words)
	if err != nil {
		t.Fatal(err)
	}
	const want = "Write to priya@example.com; the PIN is {PIN} and the bot token is {SLACK_TOKEN}."
	if replied.Status != StatusCompleted || replied.Held != nil || replied.Result == nil || replied.Result.Summary != want {
		t.Fatalf("replied = %+v, want %q delivered", replied.Result, want)
	}
	if replied.Guard == nil || replied.Guard.State != answerguard.StateMasked || !slices.Equal(replied.Guard.Kinds, []string{"PIN", "SLACK_TOKEN"}) {
		t.Fatalf("guard = %+v, want masked for the PIN and the token, and nothing held", replied.Guard)
	}

	// A guard that could not read it holds nothing either: the reply goes with
	// its credentials taken out, and the log says the guard could not read it,
	// by kind, never with a word of it.
	guard.unchecked = true
	service, repo = slackReplyService(t, func(config *Config) {
		config.Guard = guard
		config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	pending = pendingFromSlack(repo)
	replied, err = service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, words)
	if err != nil {
		t.Fatal(err)
	}
	const stripped = "Write to priya@example.com; the PIN is 4921 and the bot token is {SLACK_TOKEN}."
	if replied.Status != StatusCompleted || replied.Result == nil || replied.Result.Summary != stripped ||
		replied.Guard == nil || replied.Guard.State != answerguard.StateMasked || !slices.Equal(replied.Guard.Kinds, []string{"SLACK_TOKEN"}) {
		t.Fatalf("an unread reply = %+v, guard %+v; want it delivered with the token taken out", replied.Result, replied.Guard)
	}
	if !strings.Contains(logs.String(), "the answer guard could not read an owner's reply") {
		t.Fatalf("the log does not say the guard could not read it:\n%s", logs.String())
	}
	for _, secret := range []string{"priya@example.com", "4921", token, "Write to"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("the log carries %q:\n%s", secret, logs.String())
		}
	}

	// With no guard at all, credentials are still taken out, and a reply with
	// none in it records no guard.
	service, repo = slackReplyService(t)
	pending = pendingFromSlack(repo)
	replied, err = service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, "the bot token is "+token)
	if err != nil || replied.Result.Summary != "the bot token is {SLACK_TOKEN}" || replied.Guard == nil || replied.Guard.State != answerguard.StateMasked {
		t.Fatalf("with no guard: %+v, %+v, %v", replied.Result, replied.Guard, err)
	}
	service, repo = slackReplyService(t)
	pending = pendingFromSlack(repo)
	if replied, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords); err != nil || replied.Guard != nil {
		t.Fatalf("with no guard and nothing to take out: guard %+v, %v; want none", replied.Guard, err)
	}
}

// TestAReplyFromSlackIsTheOwnersWithTheirTicket: the notice's Reply yourself
// ticket, sent by the member it was made for, records the reply as made in
// Slack; anyone else, a forged ticket, one that ran out, and a request that
// was not asked in Slack record nothing.
func TestAReplyFromSlackIsTheOwnersWithTheirTicket(t *testing.T) {
	ctx := context.Background()
	service, repo := slackReplyService(t)
	pending := pendingFromSlack(repo)
	tickets, err := service.ConsentTickets(ctx, pending.ID, "notice_"+pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	for name, clicker := range map[string]string{
		"the member who asked": "user_a", "another member": "user_x", "nobody": "", "the owner, padded": " user_b",
	} {
		if _, err := service.ReplyFromSlack(ctx, tickets.Reply, clicker, ownWords); !errors.Is(err, ErrForbidden) {
			t.Errorf("%s sent it: %v, want ErrForbidden", name, err)
		}
	}
	other, err := NewTickets([]byte("another-key-of-at-least-32-bytes-long!!"))
	if err != nil {
		t.Fatal(err)
	}
	forged, err := other.Mint(TicketClaims{RequestID: pending.ID, OwnerUserID: "user_b", NoticeID: "notice_" + pending.ID, Kind: TicketReply, Choice: StatusPending, ExpiresAt: pending.ExpiresAt})
	if err != nil {
		t.Fatal(err)
	}
	for name, ticket := range map[string]string{"signed under another key": forged, "nothing": "", "not a ticket": "reply"} {
		if _, err := service.ReplyFromSlack(ctx, ticket, "user_b", ownWords); !errors.Is(err, ErrTicketInvalid) {
			t.Errorf("%s: %v, want ErrTicketInvalid", name, err)
		}
	}
	if _, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", "   "); !errors.Is(err, ErrInvalid) {
		t.Errorf("an empty reply from Slack: %v, want ErrInvalid", err)
	}
	later := *service
	later.now = func() time.Time { return pending.ExpiresAt }
	if _, err := later.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords); !errors.Is(err, ErrTicketExpired) {
		t.Errorf("a ticket that ran out: %v, want ErrTicketExpired", err)
	}
	byAnAgent := repo.requests[pending.ID]
	byAnAgent.Origin = OriginAgent
	repo.requests[pending.ID] = byAnAgent
	if _, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords); !errors.Is(err, ErrInvalid) {
		t.Errorf("a reply from Slack to an agent's request: %v, want ErrInvalid", err)
	}
	byAnAgent.Origin = OriginSlack
	repo.requests[pending.ID] = byAnAgent
	if repo.replies != 0 {
		t.Fatalf("a refused reply was recorded")
	}
	noKey := *service
	noKey.tickets = nil
	if _, err := noKey.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords); !errors.Is(err, ErrConsentUnavailable) {
		t.Errorf("a ledger with no ticket key: %v, want ErrConsentUnavailable", err)
	}

	replied, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords)
	if err != nil {
		t.Fatal(err)
	}
	want := agents.InteractionDecider{Kind: DecidedByMember, UserID: "user_b", Surface: SurfaceSlack}
	if replied.Status != StatusCompleted || replied.Decision == nil || replied.Decision.Consent != ConsentReply || *replied.Decision.DecidedBy != want ||
		replied.Result == nil || replied.Result.Summary != ownWords || !replied.Result.WrittenByOwner {
		t.Fatalf("replied = %+v, want the owner's reply, made in Slack", replied)
	}
	// Sent twice, the modal's second submission replies nothing more.
	if _, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", "Something else."); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("a second reply: %v, want ErrAlreadyDecided", err)
	}
	if repo.replies != 1 || repo.requests[pending.ID].Result.Summary != ownWords {
		t.Fatalf("the second reply changed the answer: %+v", repo.requests[pending.ID].Result)
	}
}

// TestAReplyLandsOnlyOnTheStageItsButtonWasOn: the notice's Reply yourself,
// sent once the request has been approved and its answer held, is already
// decided, since the owner chose first; the review's, sent while the request is
// pending, which only a leaked key could make, is too.
func TestAReplyLandsOnlyOnTheStageItsButtonWasOn(t *testing.T) {
	ctx := context.Background()
	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	fromTheNotice, err := service.tickets.Mint(TicketClaims{
		RequestID: request.ID, OwnerUserID: "user_b", NoticeID: "notice_" + request.ID, Kind: TicketReply, Choice: StatusPending, ExpiresAt: request.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplyFromSlack(ctx, fromTheNotice, "user_b", ownWords); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("the notice's reply to an answer in review: %v, want ErrAlreadyDecided", err)
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusReview || stored.Held == nil || repo.replies != 0 {
		t.Fatalf("the notice's reply changed the request in review: %+v", stored)
	}

	slack, slackRepo := slackReplyService(t)
	pending := pendingFromSlack(slackRepo)
	fromAReview, err := slack.tickets.Mint(TicketClaims{
		RequestID: pending.ID, OwnerUserID: "user_b", NoticeID: "review_" + pending.ID, Kind: TicketReply, Choice: StatusReview, ExpiresAt: pending.ExpiresAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := slack.ReplyFromSlack(ctx, fromAReview, "user_b", ownWords); !errors.Is(err, ErrAlreadyDecided) {
		t.Fatalf("a review's reply to a pending request: %v, want ErrAlreadyDecided", err)
	}
	if slackRepo.replies != 0 {
		t.Fatal("a review's reply landed on a pending request")
	}
	// The web's reply names no stage, and lands on either.
	if _, err := service.ReplyAsOwner(ctx, theOwner, "token", request.ID, ownWords); err != nil {
		t.Fatalf("a reply on the web to an answer in review: %v", err)
	}
	if _, err := slack.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords); err != nil {
		t.Fatalf("a reply on the web to a pending request: %v", err)
	}
}

// TestTheFirstOfAReplyAndAChoiceWins: whichever of the owner's reply and their
// choice, on either surface, comes first decides the request; the second finds
// it decided, is told who won, and changes nothing.
func TestTheFirstOfAReplyAndAChoiceWins(t *testing.T) {
	ctx := context.Background()

	// The choice first: the reply is already decided, and the winner says so.
	service, repo := slackReplyService(t)
	pending := pendingFromSlack(repo)
	tickets, err := service.ConsentTickets(ctx, pending.ID, "notice_"+pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Consent(ctx, tickets.Summary, "user_b", SurfaceSlack); err != nil {
		t.Fatal(err)
	}
	for name, reply := range map[string]func() (agents.Interaction, error){
		"on the web": func() (agents.Interaction, error) {
			return service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords)
		},
		"in Slack": func() (agents.Interaction, error) {
			return service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords)
		},
	} {
		lost, err := reply()
		if !errors.Is(err, ErrAlreadyDecided) || lost.Decision == nil || lost.Decision.Consent != ConsentSummary || lost.Decision.DecidedBy.Surface != SurfaceSlack {
			t.Errorf("a reply %s after a choice: %+v, %v; want already decided, by the choice in Slack", name, lost.Decision, err)
		}
	}
	if stored := repo.requests[pending.ID]; stored.Status != StatusApproved || stored.Result != nil || repo.replies != 0 {
		t.Fatalf("a reply after a choice changed the request: %+v", stored)
	}

	// The reply first: a choice, on either surface, is already decided, and the
	// winner is the reply.
	service, repo = slackReplyService(t)
	pending = pendingFromSlack(repo)
	tickets, err = service.ConsentTickets(ctx, pending.ID, "notice_"+pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords); err != nil {
		t.Fatal(err)
	}
	if lost, err := service.Consent(ctx, tickets.Access, "user_b", SurfaceSlack); !errors.Is(err, ErrAlreadyDecided) || lost.Request.Decision.Consent != ConsentReply {
		t.Errorf("a click after a reply: %+v, %v; want already decided, by the reply", lost.Request.Decision, err)
	}
	if lost, err := service.ConsentAsOwner(ctx, theOwner, pending.ID, ChoiceDeny); !errors.Is(err, ErrAlreadyDecided) || lost.Request.Decision.Consent != ConsentReply {
		t.Errorf("a choice on the web after a reply: %+v, %v; want already decided, by the reply", lost.Request.Decision, err)
	}
	if lost, err := service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, "Another answer."); !errors.Is(err, ErrAlreadyDecided) || lost.Result.Summary != ownWords {
		t.Errorf("a second reply on the web: %+v, %v; want already decided, by the first", lost.Result, err)
	}
	if stored := repo.requests[pending.ID]; stored.Result == nil || stored.Result.Summary != ownWords || repo.replies != 1 ||
		service.consents.(*fakeConsents).consents != 0 {
		t.Fatalf("a choice after a reply changed the request: %+v", stored)
	}
}

// TestTheFirstOfAReplyAndAReviewWins: the same, for an answer held for review:
// the owner's review on either surface and their reply are one decision.
func TestTheFirstOfAReplyAndAReviewWins(t *testing.T) {
	ctx := context.Background()

	service, repo, request := heldFromSlack(t, heldVerdict, nil)
	tickets := reviewTickets(t, service, request)
	if _, err := service.ReviewFromSlack(ctx, tickets.Withhold, "user_b"); err != nil {
		t.Fatal(err)
	}
	if lost, err := service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords); !errors.Is(err, ErrAlreadyDecided) || lost.Status != StatusWithheld {
		t.Errorf("a reply in Slack after a review: %+v, %v; want already decided, withheld", lost.Status, err)
	}
	if _, err := service.ReplyAsOwner(ctx, theOwner, "token", request.ID, ownWords); !errors.Is(err, ErrAlreadyDecided) {
		t.Errorf("a reply on the web after a review: %v, want already decided", err)
	}
	if stored := repo.requests[request.ID]; stored.Status != StatusWithheld || stored.Result != nil || repo.replies != 0 {
		t.Fatalf("a reply after the review changed the request: %+v", stored)
	}

	service, repo, request = heldFromSlack(t, heldVerdict, nil)
	service.guard = &maskingGuard{}
	tickets = reviewTickets(t, service, request)
	if _, err := service.ReplyAsOwner(ctx, theOwner, "token", request.ID, ownWords); err != nil {
		t.Fatal(err)
	}
	for name, review := range map[string]func() error{
		"Send in Slack":        func() error { _, err := service.ReviewFromSlack(ctx, tickets.Send, "user_b"); return err },
		"Send masked in Slack": func() error { _, err := service.ReviewFromSlack(ctx, tickets.SendMasked, "user_b"); return err },
		"withhold on the web": func() error {
			_, err := service.ReviewAsOwner(ctx, theOwner, "token", request.ID, agents.InteractionReviewInput{Action: ReviewWithhold})
			return err
		},
	} {
		if err := review(); !errors.Is(err, ErrConflict) {
			t.Errorf("%s after a reply: %v, want ErrConflict", name, err)
		}
	}
	if stored := repo.requests[request.ID]; stored.Result == nil || stored.Result.Summary != ownWords || repo.reviews != 0 {
		t.Fatalf("a review after the reply changed the request: %+v", stored)
	}
}

// TestRepliesChoicesAndReviewsAtOnceDecideOnce: the owner replying on the web
// and in Slack, and choosing on either, at the same moment, decides a pending
// request once, and an answer in review is ended once by replies and reviews at
// the same moment; every other call is told it lost and changes nothing. Run
// with -race.
func TestRepliesChoicesAndReviewsAtOnceDecideOnce(t *testing.T) {
	ctx := context.Background()
	const attempts = 24
	for round := range 3 {
		service, repo := slackReplyService(t)
		pending := pendingFromSlack(repo)
		tickets, err := service.ConsentTickets(ctx, pending.ID, "notice_"+pending.ID)
		if err != nil {
			t.Fatal(err)
		}
		locked := &lockedRepository{Repository: repo}
		consents := &fakeConsents{repo: repo, mu: &locked.mu}
		service.repository, service.consents = locked, consents
		errs := make([]error, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				start.Wait()
				switch (index + round) % 4 {
				case 0:
					_, errs[index] = service.ReplyAsOwner(ctx, theOwner, "token", pending.ID, ownWords)
				case 1:
					_, errs[index] = service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords)
				case 2:
					_, errs[index] = service.Consent(ctx, tickets.Summary, "user_b", SurfaceSlack)
				default:
					_, errs[index] = service.ConsentAsOwner(ctx, theOwner, pending.ID, ChoiceDeny)
				}
			})
		}
		start.Done()
		done.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrAlreadyDecided):
				t.Fatalf("round %d: a call failed with %v, want it to win or find the request decided", round, err)
			}
		}
		if won != 1 || repo.replies+consents.consents != 1 {
			t.Fatalf("round %d: %d won, %d replies and %d choices recorded, want exactly one", round, won, repo.replies, consents.consents)
		}
	}
	for round := range 3 {
		service, repo, request := heldFromSlack(t, heldVerdict, nil)
		service.guard = nil
		tickets := reviewTickets(t, service, request)
		service.repository = &lockedRepository{Repository: repo}
		errs := make([]error, attempts)
		var start, done sync.WaitGroup
		start.Add(1)
		for index := range attempts {
			done.Go(func() {
				start.Wait()
				switch (index + round) % 4 {
				case 0:
					_, errs[index] = service.ReplyAsOwner(ctx, theOwner, "token", request.ID, ownWords)
				case 1:
					_, errs[index] = service.ReplyFromSlack(ctx, tickets.Reply, "user_b", ownWords)
				case 2:
					_, errs[index] = service.ReviewFromSlack(ctx, tickets.Send, "user_b")
				default:
					_, errs[index] = service.ReviewAsOwner(ctx, theOwner, "token", request.ID, agents.InteractionReviewInput{Action: ReviewWithhold})
				}
			})
		}
		start.Done()
		done.Wait()
		won := 0
		for _, err := range errs {
			switch {
			case err == nil:
				won++
			case !errors.Is(err, ErrAlreadyDecided) && !errors.Is(err, ErrConflict):
				t.Fatalf("round %d: a call failed with %v, want it to win or find the answer reviewed", round, err)
			}
		}
		if won != 1 || repo.replies+repo.reviews != 1 {
			t.Fatalf("round %d: %d won, %d replies and %d reviews recorded, want exactly one", round, won, repo.replies, repo.reviews)
		}
		if stored := repo.requests[request.ID]; stored.Status == StatusReview || stored.Held != nil {
			t.Fatalf("round %d: the request is %+v, want its review over", round, stored)
		}
	}
}

// TestAReplyToARequestMadeInATeamspaceReadsItAgainFirst: with the owner's own
// token, as approving it would; one that no longer stands is closed instead,
// and a reply that carries no token, as one from Slack would not, replies
// nothing.
func TestAReplyToARequestMadeInATeamspaceReadsItAgainFirst(t *testing.T) {
	ctx := context.Background()
	service, repo := teamspaceService(t, teamspaceSeats())
	created := createInTeamspace(t, service, "agt_b")
	if _, err := service.ReplyAsOwner(ctx, theOwner, "", created.ID, ownWords); !errors.Is(err, ErrInvalid) {
		t.Fatalf("a reply with no token: %v, want ErrInvalid", err)
	}
	if repo.requests[created.ID].Status != StatusPending {
		t.Fatal("a reply with no token changed the request")
	}
	replied, err := service.ReplyAsOwner(ctx, theOwner, "member-token", created.ID, ownWords)
	if err != nil || replied.Status != StatusCompleted || replied.Decision.Consent != ConsentReply {
		t.Fatalf("a reply to a request that still stands: %+v, %v", replied, err)
	}

	spaces := teamspaceSeats()
	service, repo = teamspaceService(t, spaces)
	created = createInTeamspace(t, service, "agt_b")
	spaces.lapsed = "the member who asked left the team"
	if _, err := service.ReplyAsOwner(ctx, theOwner, "member-token", created.ID, ownWords); !errors.Is(err, ErrClosed) {
		t.Fatalf("a reply to a request that no longer stands: %v, want ErrClosed", err)
	}
	if stored := repo.requests[created.ID]; stored.Status != StatusRejected || stored.Result != nil || repo.replies != 0 {
		t.Fatalf("a request that no longer stands is %+v, want it closed by Oryxa with no answer", stored)
	}
}

// TestAMemberAnsweringInTheirOwnSessionIsTheirOwnWords: a response recorded in
// the owner's session says it was written by them, as a reply does, whether it
// is delivered or held; an agent's never does.
func TestAMemberAnsweringInTheirOwnSessionIsTheirOwnWords(t *testing.T) {
	ctx := context.Background()
	service, _, approved := guardedService(t, &fakeGuard{}, nil)
	answered, err := service.Respond(ctx, inOwnSession(target), approved.ID, answerGiven)
	if err != nil || answered.Result == nil || !answered.Result.WrittenByOwner {
		t.Fatalf("a response in the owner's session: %+v, %v; want it written by them", answered.Result, err)
	}
	byTheAgent, _, approved := guardedService(t, &fakeGuard{}, nil)
	answered, err = byTheAgent.Respond(ctx, target, approved.ID, answerGiven)
	if err != nil || answered.Result == nil || answered.Result.WrittenByOwner {
		t.Fatalf("an agent's response: %+v, %v; want it not written by the owner", answered.Result, err)
	}
	held, repo, approved := guardedService(t, &fakeGuard{verdict: &heldVerdict}, nil)
	if _, err := held.Respond(ctx, inOwnSession(target), approved.ID, answerGiven); err != nil {
		t.Fatal(err)
	}
	stored := repo.requests[approved.ID]
	if stored.Held == nil || !stored.Held.Result.WrittenByOwner || stored.Held.MaskedResult == nil || !stored.Held.MaskedResult.WrittenByOwner {
		t.Fatalf("held = %+v, want both versions written by the owner", stored.Held)
	}
}

// TestAReplyLogsNeitherItsWordsNorItsTicket: the log says who replied to what,
// by id, and never what they wrote or the ticket that let them.
func TestAReplyLogsNeitherItsWordsNorItsTicket(t *testing.T) {
	var logs bytes.Buffer
	service, repo := slackReplyService(t, func(config *Config) {
		config.Logger = slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	pending := pendingFromSlack(repo)
	tickets, err := service.ConsentTickets(context.Background(), pending.ID, "notice_"+pending.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReplyFromSlack(context.Background(), tickets.Reply, "user_b", ownWords); err != nil {
		t.Fatal(err)
	}
	logged := logs.String()
	if !strings.Contains(logged, "an owner replied to a request themself") || !strings.Contains(logged, pending.ID) {
		t.Fatalf("the reply was not logged:\n%s", logged)
	}
	for _, secret := range []string{tickets.Reply, ownWords, "14th"} {
		if strings.Contains(logged, secret) {
			t.Fatalf("the log carries %q:\n%s", secret, logged)
		}
	}
}

// TestRepliedByOwnerSaysWhoAnswered: a reply's approval, or a reply's review,
// and nothing else.
func TestRepliedByOwnerSaysWhoAnswered(t *testing.T) {
	for name, test := range map[string]struct {
		request agents.Interaction
		want    bool
	}{
		"a reply to a pending request":    {agents.Interaction{Decision: &agents.InteractionDecision{Consent: ConsentReply}}, true},
		"a reply instead of a review":     {agents.Interaction{Review: &agents.InteractionReview{Action: ReviewReply}}, true},
		"a summary":                       {agents.Interaction{Decision: &agents.InteractionDecision{Consent: ConsentSummary}}, false},
		"a review that sent the answer":   {agents.Interaction{Review: &agents.InteractionReview{Action: ReviewSend}}, false},
		"a request nobody has decided on": {agents.Interaction{}, false},
	} {
		if got := RepliedByOwner(test.request); got != test.want {
			t.Errorf("%s: RepliedByOwner = %v, want %v", name, got, test.want)
		}
	}
}
