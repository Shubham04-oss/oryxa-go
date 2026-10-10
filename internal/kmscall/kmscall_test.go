package kmscall

import (
	"context"
	"testing"
	"time"
)

func TestContextBoundsACallWithNoDeadline(t *testing.T) {
	ctx, cancel := Context(context.Background())
	defer cancel()
	deadline, ok := ctx.Deadline()
	if !ok {
		t.Fatal("a context with no deadline was left unbounded")
	}
	if remaining := time.Until(deadline); remaining <= 0 || remaining > Timeout {
		t.Fatalf("deadline is %v away, want within %v", remaining, Timeout)
	}
}

func TestContextKeepsTheCallersDeadline(t *testing.T) {
	for name, wait := range map[string]time.Duration{"shorter": time.Second, "longer": time.Minute} {
		t.Run(name, func(t *testing.T) {
			parent, cancelParent := context.WithTimeout(context.Background(), wait)
			defer cancelParent()
			want, _ := parent.Deadline()
			ctx, cancel := Context(parent)
			defer cancel()
			if got, _ := ctx.Deadline(); !got.Equal(want) {
				t.Fatalf("deadline %v, want the caller's %v", got, want)
			}
		})
	}
}
