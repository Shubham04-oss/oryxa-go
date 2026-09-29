package interactions

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
)

// TestForgettingAMemberForgetsTheirAsksAndLeases: a member who leaves takes
// their requests with them, and the asks and leases they were part of: the ones
// they asked, the leases they gave or held, and any ask left with no request. An
// organization that is deleted loses them all, and no other is touched.
func TestForgettingAMemberForgetsTheirAsksAndLeases(t *testing.T) {
	ledger := newSlackLedger(t)
	other := newSlackLedger(t)
	ctx := context.Background()

	// Raya and Maya were asked in one ask, and Raya gave a lease.
	both := ledger.ask("1790000000.000100", raya, maya)
	ledger.decide(raya, requestTo(t, both, raya), ChoiceAccess)
	// Maya was asked alone, and so was Raya, in threads of their own.
	onlyMaya := ledger.askIn(inAnotherThread("1790000010.000100"), maya)
	onlyRaya := ledger.askIn(SlackThread{TeamID: "T1", ChannelID: "C2", ThreadTS: "1790000020.000100", TriggerTS: "1790000020.000100"}, raya)
	// Another organization has its own.
	survivor := other.ask("1790000000.000100", raya)
	other.decide(raya, requestTo(t, survivor, raya), ChoiceAccess)

	asks := func(l *slackLedger) int {
		return l.count(`SELECT count(*) FROM agents_primary.interaction_asks WHERE organization_id=$1`, l.org)
	}
	if asks(ledger) != 3 || ledger.leases() != 1 {
		t.Fatalf("before: %d asks and %d leases", asks(ledger), ledger.leases())
	}

	// Raya leaves: her requests, her lease, and the ask that had only her.
	if err := ledger.store.Forget(ctx, ledger.org, raya); err != nil {
		t.Fatal(err)
	}
	if ledger.leases() != 0 {
		t.Errorf("%d leases left after the member who gave one was forgotten", ledger.leases())
	}
	for _, request := range []agents.Interaction{requestTo(t, both, raya), requestTo(t, onlyRaya, raya)} {
		if _, err := ledger.store.Get(ctx, request.ID); !errors.Is(err, ErrNotFound) {
			t.Errorf("Raya's request %s survived: %v", request.ID, err)
		}
	}
	if _, err := ledger.store.Get(ctx, requestTo(t, both, maya).ID); err != nil {
		t.Errorf("Maya's request in the shared ask was forgotten: %v", err)
	}
	if _, err := ledger.store.Ask(ctx, onlyRaya.Ask.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("the ask that had only Raya survived: %v", err)
	}
	if _, err := ledger.store.Ask(ctx, both.Ask.ID); err != nil {
		t.Errorf("the ask Maya is still in was forgotten: %v", err)
	}
	if asks(ledger) != 2 {
		t.Errorf("%d asks after Raya left, want the two Maya is in", asks(ledger))
	}

	// The asker leaves: everything they asked goes, and the requests with it.
	if err := ledger.store.Forget(ctx, ledger.org, asker); err != nil {
		t.Fatal(err)
	}
	if asks(ledger) != 0 || ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE organization_id=$1`, ledger.org) != 0 {
		t.Errorf("%d asks and requests remain after the asker was forgotten", asks(ledger))
	}
	_ = onlyMaya

	// The other organization is untouched, until it is deleted.
	if asks(other) != 1 || other.leases() != 1 {
		t.Fatalf("the other organization: %d asks, %d leases", asks(other), other.leases())
	}
	if err := other.store.Forget(ctx, other.org, ""); err != nil {
		t.Fatal(err)
	}
	if asks(other) != 0 || other.leases() != 0 || other.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE organization_id=$1`, other.org) != 0 {
		t.Fatal("deleting an organization left asks, leases or requests")
	}
}

// TestManyThingsAtOnceOnOneAskNeitherDeadlockNorDeliverTwice: owners choosing,
// answering, a bypass, a revoke and a follow-up all race on the requests of
// one ask, in the orders they come in, and nothing deadlocks, nothing fails but
// as it says it may, and the ask ends with at most one delivered answer and
// every other request closed.
func TestManyThingsAtOnceOnOneAskNeitherDeadlockNorDeliverTwice(t *testing.T) {
	ledger := newSlackLedger(t)
	const owners = 3
	users := []string{raya, maya, "user_lee"}
	acceptable := func(err error) bool {
		return err == nil || errors.Is(err, ErrConflict) || errors.Is(err, ErrAlreadyDecided) || errors.Is(err, ErrAnsweredElsewhere) ||
			errors.Is(err, ErrRevoked) || errors.Is(err, ErrNotFound)
	}
	for round := range 12 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		thread := SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: fmt.Sprintf("1790010%03d.000100", round), TriggerTS: fmt.Sprintf("1790010%03d.000100", round)}
		result := ledger.askIn(thread, users...)
		// A lease in the thread, for the answers that ride on it.
		if round%2 == 0 {
			request := requestTo(t, result, users[0])
			ledger.decide(users[0], request, ChoiceAccess)
		}

		var wg sync.WaitGroup
		var start sync.WaitGroup
		start.Add(1)
		failures := make(chan error, 64)
		run := func(name string, call func() error) {
			wg.Add(1)
			go func() {
				defer wg.Done()
				start.Wait()
				if err := call(); !acceptable(err) {
					failures <- fmt.Errorf("round %d, %s: %w", round, name, err)
				}
			}()
		}
		for index, user := range users {
			request := requestTo(t, result, user)
			// The request may be pending or approved by now; each owner both
			// chooses and answers, and whichever finds the state it needs goes.
			run("choose "+user, func() error {
				_, err := ledger.service.ConsentAsOwner(ctx, ledger.owner(user), request.ID, []string{ChoiceSummary, ChoiceAccess, ChoiceDeny}[(index+round)%3])
				return err
			})
			run("answer "+user, func() error {
				_, err := ledger.service.Respond(ctx, ledger.agentOf(user), request.ID, agents.InteractionResponseInput{
					Status: StatusCompleted, Summary: "The keys live in the vault.", ToolsUsed: []string{"knowledge"},
				})
				return err
			})
		}
		run("bypass", func() error {
			_, err := ledger.service.Bypass(ctx, requestTo(t, result, users[owners-1]).ID, "outranks")
			return err
		})
		run("follow up", func() error {
			_, err := ledger.service.CreateAsk(ctx, AskInput{
				OrganizationID: ledger.org, AskerUserID: asker, Slack: SlackThread{TeamID: "T1", ChannelID: "C1", ThreadTS: thread.ThreadTS, TriggerTS: thread.TriggerTS + "1"},
				Question: "And the staging keys?", Owners: []AskOwner{{UserID: users[0]}, {UserID: users[1]}},
			})
			return err
		})
		run("revoke", func() error {
			leases, err := ledger.store.Leases(ctx, ledger.org, users[0], 5)
			if err != nil {
				return err
			}
			for _, lease := range leases {
				if _, err := ledger.service.RevokeLease(ctx, ledger.owner(users[0]), lease.ID); err != nil {
					return err
				}
			}
			return nil
		})
		start.Done()
		wg.Wait()
		cancel()
		close(failures)
		for err := range failures {
			t.Fatal(err)
		}

		// However it fell, the ask has at most one delivered answer, and once
		// it has one nothing else of it is left waiting.
		delivered := ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE ask_id=$1 AND status='completed'`, result.Ask.ID)
		waiting := ledger.count(`SELECT count(*) FROM agents_primary.agent_interactions WHERE ask_id=$1 AND status IN ('pending', 'approved', 'review')`, result.Ask.ID)
		var winner string
		if err := ledger.database.Pool.QueryRow(context.Background(), `SELECT winner_request_id FROM agents_primary.interaction_asks WHERE id=$1`, result.Ask.ID).Scan(&winner); err != nil {
			t.Fatal(err)
		}
		if delivered > 1 || (delivered == 1) != (winner != "") || (winner != "" && waiting != 0) {
			t.Fatalf("round %d: %d delivered, winner %q, %d still waiting", round, delivered, winner, waiting)
		}
		if winner != "" && ledger.read(winner).Status != StatusCompleted {
			t.Fatalf("round %d: the winner is %+v", round, ledger.read(winner))
		}
	}
}
