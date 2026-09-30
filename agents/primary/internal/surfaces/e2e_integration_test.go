package surfaces

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/interactions"
	agentsv1 "github.com/Shubham04-oss/oryxa-go/contracts/events/agents/v1"
	"github.com/Shubham04-oss/oryxa-go/internal/postgreskit"
)

// loop is the whole service over PostgreSQL: the real store and worker, and the
// service over them, reading a fake ledger and posting through the real apps
// client to a fake of apps' API.
type loop struct {
	*harness
	store     *Store
	database  *postgreskit.Database
	worker    *Worker
	org       string
	requestID string
	askID     string
}

func newLoop(t *testing.T) *loop {
	t.Helper()
	store, database, org := openTestStore(t)
	h := newHarness(t)
	l := &loop{harness: h, store: store, database: database, org: org, requestID: org + "_irq_1", askID: "iak_" + org}
	service, err := NewService(store, Deps{
		Requests: h.ledger, Asks: h.ledger, Tickets: h.ledger, Slack: h.client, Users: h.users, Notes: h.notes,
		Now: func() time.Time { return h.now },
	})
	if err != nil {
		t.Fatal(err)
	}
	l.worker = NewWorker(store, service, WorkerConfig{Interval: time.Hour}, nil)
	l.worker.now = func() time.Time { return h.now }
	ask := baseAsk()
	ask.ID, ask.OrganizationID = l.askID, org
	h.ledger.putAsk(ask)
	return l
}

// request is the ledger's request in the state edit leaves it, in this loop's
// organization.
func (l *loop) request(edit func(agents.Interaction) agents.Interaction) agents.Interaction {
	request := pendingRequest()
	request.ID, request.OrganizationID, request.AskID = l.requestID, l.org, l.askID
	if edit != nil {
		request = edit(request)
	}
	return request
}

// hear delivers the event of a change, as agents' consumer would, and runs what
// it woke.
func (l *loop) hear(t *testing.T, detailType, eventID string) {
	t.Helper()
	data := changeEvent(t, func(c *agentsv1.InteractionChanged) {
		c.RequestID, c.OrganizationID, c.AskID = l.requestID, l.org, l.askID
	})
	if err := l.worker.HandleEvent(context.Background(), detailType, eventID, data); err != nil {
		t.Fatal(err)
	}
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
}

type jobRow struct {
	wanted, handled int64
	attempts        int
	gaveUp          bool
	lastError       string
	availableIn     time.Duration
}

func (l *loop) job(t *testing.T) jobRow {
	t.Helper()
	var row jobRow
	var seconds float64
	err := l.database.Pool.QueryRow(context.Background(), `
		SELECT wanted, handled, attempts, gave_up, last_error, extract(epoch FROM (available_at - now()))
		FROM agents_primary.surface_jobs WHERE request_id = $1`, l.requestID).
		Scan(&row.wanted, &row.handled, &row.attempts, &row.gaveUp, &row.lastError, &seconds)
	if err != nil {
		t.Fatalf("read the job: %v", err)
	}
	row.availableIn = time.Duration(seconds * float64(time.Second))
	return row
}

func (l *loop) linkState(t *testing.T, role string) (string, bool) {
	t.Helper()
	link, found, err := l.store.Link(context.Background(), l.requestID, role)
	if err != nil {
		t.Fatal(err)
	}
	return link.State, found
}

func TestTheWholeLoopOverPostgresSaysEachThingOnceHoweverOftenItIsToldOfIt(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))

	// The request is made; its event arrives twice, as the bus may deliver it.
	l.hear(t, RequestedDetailType, "evt_requested")
	l.hear(t, RequestedDetailType, "evt_requested")
	if got := fmt.Sprint(templates(l.api.sent())); got != "[owner_notice]" || l.api.postRequests() != 1 {
		t.Fatalf("posted %s after %d post requests, want the owner's notice once", got, l.api.postRequests())
	}
	if state, found := l.linkState(t, roleNotice); !found || state != statePending {
		t.Fatalf("notice link = %q, %v", state, found)
	}
	if job := l.job(t); job.handled != job.wanted || job.wanted != 2 {
		t.Fatalf("job = %+v, want both events covered", job)
	}

	// The owner decides in Slack; the event is delivered three times.
	l.ledger.put(l.request(func(r agents.Interaction) agents.Interaction {
		return memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack)
	}))
	for range 3 {
		l.hear(t, DecidedDetailType, "evt_decided")
	}
	notice := l.api.sent()[0]
	if len(notice.Updates) != 1 || notice.Updates[0].Params["decision"] != "approved_summary" || l.api.postRequests() != 1 {
		t.Fatalf("updates = %+v after %d post requests, want the notice replaced once", notice.Updates, l.api.postRequests())
	}
	if state, _ := l.linkState(t, roleNotice); state != stateApprovedSummary {
		t.Fatalf("notice link = %q", state)
	}

	// The agent answers.
	l.ledger.put(l.request(func(r agents.Interaction) agents.Interaction {
		return completedWith(memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceSlack), "The launch is on the 14th.")
	}))
	l.hear(t, CompletedDetailType, "evt_completed")
	if got := fmt.Sprint(templates(l.api.sent())); got != "[owner_notice answer]" {
		t.Fatalf("posted %s", got)
	}
	if got := l.api.sentWith("answer")[0]; got.Target != l.threadTarget() || got.param("text") != "The launch is on the 14th." || got.param("mode") != "summary" {
		t.Fatalf("answer = %+v", got)
	}
	if got := l.notes.texts(); len(got) != 2 {
		t.Fatalf("notes = %q", got)
	}

	// The bus replays the whole history, as a queue can after an outage.
	posts, updates, ticketCalls, noteCalls := l.api.postRequests(), l.api.updateRequests(), len(l.ledger.tickets()), l.notes.calls
	for _, event := range []struct{ detailType, id string }{
		{RequestedDetailType, "evt_requested"}, {DecidedDetailType, "evt_decided"}, {CompletedDetailType, "evt_completed"},
	} {
		l.hear(t, event.detailType, event.id)
	}
	if l.api.postRequests() != posts || l.api.updateRequests() != updates || len(l.ledger.tickets()) != ticketCalls || l.notes.calls != noteCalls {
		t.Fatalf("a replay said things again: %d posts (was %d), %d updates (was %d), %d ticket calls (was %d), %d note calls (was %d)",
			l.api.postRequests(), posts, l.api.updateRequests(), updates, len(l.ledger.tickets()), ticketCalls, l.notes.calls, noteCalls)
	}
}

func TestAFailureIsBackedOffFromAndTheRequestSaidOnceTheDelayHasPassed(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	down := true
	l.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		if down {
			l.api.refusal(w, http.StatusServiceUnavailable, "unavailable", "apps is down")
			return true
		}
		return false
	}
	l.hear(t, RequestedDetailType, "evt_requested")
	job := l.job(t)
	if job.attempts != 1 || job.gaveUp || job.availableIn < 5*time.Second || job.availableIn > 12*time.Second || job.lastError == "" || job.handled >= job.wanted {
		t.Fatalf("job = %+v, want the failure counted, and the job put back for about 10 seconds", job)
	}
	// Not due yet: another pass finds nothing to run.
	requests := l.api.postRequests()
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.api.postRequests() != requests {
		t.Fatal("a job put back for later was run at once")
	}
	// Time passes, and apps is back.
	down = false
	if _, err := l.database.Pool.Exec(context.Background(), `UPDATE agents_primary.surface_jobs SET available_at = now() WHERE request_id = $1`, l.requestID); err != nil {
		t.Fatal(err)
	}
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := templates(l.api.sent()); fmt.Sprint(got) != "[owner_notice]" {
		t.Fatalf("posted %v", got)
	}
	if job := l.job(t); job.attempts != 0 || job.handled != job.wanted || job.lastError != "" {
		t.Fatalf("job = %+v, want it done with the failures forgotten", job)
	}
}

func TestARateLimitTheClientWillNotSitOutSetsTheJobsDelayToItsWord(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	l.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		w.Header().Set("Retry-After", "45")
		l.api.refusal(w, http.StatusTooManyRequests, "rate_limited", "too many requests; retry")
		return true
	}
	l.hear(t, RequestedDetailType, "evt_requested")
	job := l.job(t)
	if job.attempts != 1 || job.availableIn < 40*time.Second || job.availableIn > 50*time.Second {
		t.Fatalf("job = %+v, want it put back for the 45 seconds apps asked for", job)
	}
	if len(l.waits) != 0 {
		t.Fatalf("the client sat out %v when the limit asked for longer than it holds a slot for", l.waits)
	}
}

func TestAJobThatKeepsFailingIsGivenUpOnAndTheNextEventStartsItOver(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	l.api.respond = func(_ int, _ map[string]any, w http.ResponseWriter) bool {
		l.api.refusal(w, http.StatusServiceUnavailable, "unavailable", "apps is down")
		return true
	}
	l.hear(t, RequestedDetailType, "evt_requested")
	for range maxAttempts - 1 {
		if _, err := l.database.Pool.Exec(context.Background(), `UPDATE agents_primary.surface_jobs SET available_at = now() WHERE request_id = $1`, l.requestID); err != nil {
			t.Fatal(err)
		}
		if err := l.worker.WakeDue(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	job := l.job(t)
	if !job.gaveUp || job.handled != job.wanted || job.lastError == "" {
		t.Fatalf("job = %+v, want it given up on after %d failures", job, maxAttempts)
	}
	// Nothing is due, however often the worker looks.
	requests := l.api.postRequests()
	if err := l.worker.WakeDue(context.Background()); err != nil || l.api.postRequests() != requests {
		t.Fatalf("a job given up on ran again (%v)", err)
	}
	// A later event for the request starts it over.
	l.api.respond = nil
	l.hear(t, DecidedDetailType, "evt_later")
	if got := templates(l.api.sent()); fmt.Sprint(got) != "[owner_notice]" {
		t.Fatalf("posted %v, want the notice sent once apps answered", got)
	}
	if job := l.job(t); job.gaveUp || job.attempts != 0 {
		t.Fatalf("job = %+v", job)
	}
}

func TestANoticeWhoseRequestLapsedIsSettledByTheSweepWithNoEventToWakeIt(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	l.hear(t, RequestedDetailType, "evt_requested")
	notice := l.api.sent()[0]

	// A day passes and nothing happens: no event announces a request lapsing.
	l.now = testNow.Add(25 * time.Hour)
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(notice.Updates) != 1 || notice.Updates[0].Params["decision"] != "expired" {
		t.Fatalf("updates = %+v, want the notice to say it expired", notice.Updates)
	}
	statuses := l.api.sentWith("status")
	if len(statuses) != 1 || statuses[0].param("state") != "expired" || statuses[0].Target.UserID != slackAsker {
		t.Fatalf("statuses = %+v, want the asker told alone", statuses)
	}
	if state, _ := l.linkState(t, roleNotice); state != stateExpired {
		t.Fatalf("notice link = %q", state)
	}
	// Settled, it is left alone.
	posts, updates := l.api.postRequests(), l.api.updateRequests()
	l.now = testNow.Add(72 * time.Hour)
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.api.postRequests() != posts || l.api.updateRequests() != updates {
		t.Fatal("a settled notice was woken again")
	}
}

func TestForgettingTheOwnerLeavesNothingToReplaceAndNothingToFail(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	l.hear(t, RequestedDetailType, "evt_requested")
	if err := l.store.Forget(context.Background(), l.org, "user_raya"); err != nil {
		t.Fatal(err)
	}
	if _, found := l.linkState(t, roleNotice); found {
		t.Fatal("the owner's link outlived them")
	}
	// The request is decided later, on the web. Nothing is left to replace, and
	// nothing fails: the notice stays in the workspace as Slack keeps it.
	l.ledger.put(l.request(func(r agents.Interaction) agents.Interaction {
		return memberDecided(r, interactions.DecisionApproved, interactions.ConsentSummary, interactions.SurfaceWeb)
	}))
	l.hear(t, DecidedDetailType, "evt_decided")
	if l.api.updateRequests() != 0 {
		t.Fatalf("%d updates for a member whose rows were forgotten", l.api.updateRequests())
	}
}

func TestUninstallingTheAppDropsTheWorkspacesMessagesAndThePassesNotRun(t *testing.T) {
	l := newLoop(t)
	l.ledger.put(l.request(nil))
	l.hear(t, RequestedDetailType, "evt_requested")
	// Another event is recorded and has not run when the workspace is uninstalled.
	data := changeEvent(t, func(c *agentsv1.InteractionChanged) {
		c.RequestID, c.OrganizationID, c.AskID = l.requestID, l.org, l.askID
	})
	if err := l.worker.HandleEvent(context.Background(), DecidedDetailType, "evt_decided", data); err != nil {
		t.Fatal(err)
	}
	if err := l.store.ForgetTeam(context.Background(), l.org, "T0ACME001"); err != nil {
		t.Fatal(err)
	}
	if _, found := l.linkState(t, roleNotice); found {
		t.Fatal("a message of the uninstalled workspace is still remembered")
	}
	requests := l.api.postRequests()
	if err := l.worker.WakeDue(context.Background()); err != nil {
		t.Fatal(err)
	}
	if l.api.postRequests() != requests || l.api.updateRequests() != 0 {
		t.Fatal("a pass ran for a workspace that was uninstalled")
	}
}
