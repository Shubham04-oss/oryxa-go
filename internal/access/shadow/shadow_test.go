package shadow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/access"
	"github.com/Shubham04-oss/oryxa-go/internal/access/spicedb"
)

type checker func(context.Context) (bool, error)

func (check checker) Check(ctx context.Context, _ string, _ []spicedb.Check) (bool, error) {
	return check(ctx)
}

type recorder chan access.Disagreement

func (record recorder) Disagree(_ context.Context, disagreement access.Disagreement) {
	record <- disagreement
}

var ask = access.Ask{
	OrganizationID: "org_1", Service: "files", Action: access.ActionRead,
	ResourceKind: "file", ResourceID: "file_1", UserID: "user_1",
	Actor: access.Actor{Role: access.Member},
}

// started is a shadow whose Checks always asks one check, as the real
// mapping will.
func started(t *testing.T, check checker) (*Shadow, recorder) {
	t.Helper()
	disagreements := make(recorder, 8)
	shadow := New(check, disagreements, nil)
	shadow.timeout = 20 * time.Millisecond
	shadow.checks = func(_ access.Actor, userID, _, action string, resource spicedb.Object, _ bool) ([]spicedb.Check, bool) {
		return []spicedb.Check{{Resource: resource, Permission: action, Subject: spicedb.Object{Type: "member", ID: userID}}}, true
	}
	t.Cleanup(shadow.Close)
	return shadow, disagreements
}

func TestDisagreementIsRecorded(t *testing.T) {
	shadow, disagreements := started(t, func(context.Context) (bool, error) { return false, nil })
	shadow.Compare(context.Background(), ask, true)
	select {
	case got := <-disagreements:
		if !got.Code || got.SpiceDB || got.ResourceID != "file_1" {
			t.Fatalf("disagreement = %+v", got)
		}
		if len(got.Checks) != 1 || got.Checks[0] != "file:file_1#read@member:user_1" {
			t.Fatalf("checks = %v", got.Checks)
		}
	case <-time.After(time.Second):
		t.Fatal("no disagreement recorded")
	}
}

func TestAgreementRecordsNothing(t *testing.T) {
	answered := make(chan struct{}, 1)
	shadow, disagreements := started(t, func(context.Context) (bool, error) { answered <- struct{}{}; return true, nil })
	shadow.Compare(context.Background(), ask, true)
	<-answered
	shadow.Close()
	if len(disagreements) != 0 {
		t.Fatalf("recorded %v", <-disagreements)
	}
}

// An error or a timeout is no answer: nothing recorded, and Compare never
// waits for it.
func TestNoAnswerRecordsNothingAndNeverBlocks(t *testing.T) {
	for name, check := range map[string]checker{
		"error":   func(context.Context) (bool, error) { return false, errors.New("unavailable") },
		"timeout": func(ctx context.Context) (bool, error) { <-ctx.Done(); return false, ctx.Err() },
	} {
		t.Run(name, func(t *testing.T) {
			shadow, disagreements := started(t, check)
			began := time.Now()
			for range queueSize * 2 { // more than the queue holds: the rest drop
				shadow.Compare(context.Background(), ask, true)
			}
			if waited := time.Since(began); waited > 50*time.Millisecond {
				t.Fatalf("Compare blocked for %s", waited)
			}
			time.Sleep(50 * time.Millisecond)
			shadow.Close()
			if len(disagreements) != 0 {
				t.Fatalf("recorded %v", <-disagreements)
			}
		})
	}
}

func TestSkippedWhenChecksHasNoMapping(t *testing.T) {
	shadow, disagreements := started(t, func(context.Context) (bool, error) {
		t.Error("asked SpiceDB for an unmapped decision")
		return false, nil
	})
	shadow.checks = func(access.Actor, string, string, string, spicedb.Object, bool) ([]spicedb.Check, bool) {
		return nil, false
	}
	shadow.Compare(context.Background(), ask, true)
	time.Sleep(20 * time.Millisecond)
	shadow.Close()
	if len(disagreements) != 0 {
		t.Fatal("recorded a disagreement for an unmapped decision")
	}
}

func TestOffIsANoOp(t *testing.T) {
	var shadow *Shadow
	shadow.Compare(context.Background(), ask, true)
	shadow.Close()
	off, err := Open(spicedb.Config{}, nil, nil, nil)
	if off != nil || err != nil {
		t.Fatalf("Open without an endpoint = %v, %v", off, err)
	}
}

// Against a real SpiceDB with the schema loaded, a file nobody wrote is
// readable by nobody.
func TestRealSpiceDB(t *testing.T) {
	config := spicedb.FromEnv()
	if !config.Enabled() {
		t.Skip("SPICEDB_ENDPOINT is not set")
	}
	client, err := spicedb.Dial(config)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	allowed, err := Client{SpiceDB: client}.Check(ctx, "org_1", []spicedb.Check{{
		Resource: spicedb.Object{Type: "file", ID: "shadow_test_missing"}, Permission: "read",
		Subject: spicedb.Object{Type: "member", ID: "shadow_test_nobody"},
	}})
	if err != nil {
		t.Skip("spicedb not reachable, or without the schema:", err)
	}
	if allowed {
		t.Fatal("an unknown file is readable")
	}
}
