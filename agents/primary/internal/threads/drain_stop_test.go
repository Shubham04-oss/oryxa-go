package threads

import (
	"context"
	"errors"
	"testing"
)

func TestDrainStopsOnlyTheMembersOwnRun(t *testing.T) {
	drain := NewDrain()
	run := &Run{id: "run_1", member: Member{OrganizationID: "org_1", UserID: "user_1"}}
	ctx, cancel := context.WithCancelCause(context.Background())
	if err := drain.begin(run, cancel); err != nil {
		t.Fatal(err)
	}
	for _, other := range [][3]string{{"org_1", "user_2", "run_1"}, {"org_2", "user_1", "run_1"}, {"org_1", "user_1", "run_2"}} {
		if drain.Stop(other[0], other[1], other[2]) {
			t.Fatalf("Stop%v stopped someone else's run", other)
		}
	}
	if ctx.Err() != nil {
		t.Fatal("a refused stop canceled the run")
	}
	if !drain.Stop("org_1", "user_1", "run_1") {
		t.Fatal("the member could not stop their own run")
	}
	if !errors.Is(context.Cause(ctx), ErrStopped) {
		t.Fatalf("cause = %v, want ErrStopped", context.Cause(ctx))
	}
	drain.end(run)
	if drain.Stop("org_1", "user_1", "run_1") {
		t.Fatal("an ended run was stopped")
	}
	var none *Drain
	if none.Stop("org_1", "user_1", "run_1") {
		t.Fatal("a nil drain stopped a run")
	}
}
