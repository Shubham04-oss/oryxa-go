// Package kmscall bounds a call to AWS KMS.
//
// The SDK puts no deadline on a request, so a KMS endpoint that accepts a
// connection and never answers holds the caller forever: a request that
// opens a sealed secret, a service token being signed or verified, an event
// being signed. Every KMS call in this module runs under Context.
package kmscall

import (
	"context"
	"time"
)

// Timeout bounds a KMS call whose caller set no deadline. KMS answers in tens
// of milliseconds; five seconds leaves room for the SDK's own retries while
// still failing a stuck call quickly.
const Timeout = 5 * time.Second

// Context is ctx bounded by Timeout, unless ctx already has a deadline of its
// own, which then decides. Call the returned cancel when the call returns.
func Context(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, Timeout)
}
