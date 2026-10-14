package edgedispatch

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/edgeclient"
	edgev1 "github.com/Shubham04-oss/oryxa-go/contracts/edge/v1"
)

// resultTimeout bounds posting one result, every attempt together.
const resultTimeout = 30 * time.Second

// ResultSuffix is where, under the room's path, its voice agent takes a
// result: POST /v1/rooms/{kind}/{id}/voice/result.
const ResultSuffix = "/voice/result"

// Caller sends one call to the edge (*edgeclient.Client).
type Caller interface {
	Do(ctx context.Context, call edgeclient.Call, out any) error
}

// EdgeResults posts results to the room's voice agent, with an edge token
// for the member the run acted for, scoped to speak in the call.
type EdgeResults struct {
	Edge Caller
}

// Deliver posts result. It is idempotent: the voice agent takes a result once
// per dispatch id, so an attempt that may have landed is safe to repeat.
func (results EdgeResults) Deliver(ctx context.Context, result Result) error {
	path, err := edgev1.RoomPath(result.Room)
	if err != nil {
		return fmt.Errorf("edgedispatch: %w", err)
	}
	return results.Edge.Do(ctx, edgeclient.Call{
		Claims: edgev1.TokenClaims{
			Org: result.Organization, Team: result.Team, Room: result.Room, Member: result.Member, Run: result.RunID,
			Scopes: []edgev1.Scope{edgev1.ScopeCallSpeak},
		},
		Method:     http.MethodPost,
		Path:       path + ResultSuffix,
		Body:       result,
		Timeout:    resultTimeout,
		Idempotent: true,
	}, nil)
}
