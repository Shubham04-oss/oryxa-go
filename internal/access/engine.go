package access

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// The engines a service can decide with (docs/design/permissions, step 5).
const (
	EngineCode    = "code"
	EngineSpiceDB = "spicedb"
)

// Engine is the engine ACCESS_ENGINE_<SERVICE> names for service: the code
// unless it says spicedb. Anything else is a configuration mistake, refused
// rather than read as either.
func Engine(getenv func(string) string, service string) (string, error) {
	name := "ACCESS_ENGINE_" + strings.ToUpper(service)
	switch value := strings.TrimSpace(getenv(name)); value {
	case "", EngineCode:
		return EngineCode, nil
	case EngineSpiceDB:
		return EngineSpiceDB, nil
	default:
		return "", fmt.Errorf("%s=%q: want %q or %q", name, value, EngineCode, EngineSpiceDB)
	}
}

// ErrNoAnswer is a decision SpiceDB doesn't model, such as the org agent's;
// the service decides it in code as before.
var ErrNoAnswer = errors.New("spicedb has no answer for this decision")

// Decider decides from SpiceDB for a service switched to it
// (*shadow.Shadow). codeAllowed is what the code would have answered; a
// difference is recorded as a disagreement. While services phase in, a caller
// lets SpiceDB only narrow: it allows when both do. Any error other than
// ErrNoAnswer means no decision: the caller refuses.
type Decider interface {
	Decide(ctx context.Context, ask Ask, codeAllowed bool) (allowed bool, err error)
	// DecideMany decides a list's asks, of one organization, in one round
	// trip, and answers what the caller gets for each: allowed only when both
	// the code and SpiceDB allow, the code's answer where SpiceDB has none.
	// Differences are recorded as Decide records them. An error is no
	// decision for any of them: the caller refuses the list.
	DecideMany(ctx context.Context, asks []Ask, codeAllowed []bool) (allowed []bool, err error)
}
