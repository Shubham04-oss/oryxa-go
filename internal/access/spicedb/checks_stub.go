package spicedb

// TEMPORARY: a stand-in for checks.go, which the replay-harness PR adds with
// exactly this API. Whichever of that PR and the shadow PR (step 4d) merges
// second deletes this file. Until then Checks skips everything, so the shadow
// asks SpiceDB nothing.

import "github.com/Shubham04-oss/oryxa-go/internal/access"

// Object is a SpiceDB object: a definition and an id.
type Object struct{ Type, ID string }

// Check is one permission check.
type Check struct {
	Resource   Object
	Permission string
	Subject    Object
}

// Checks turns a decision into the checks that must all hold for SpiceDB to
// allow it. ok=false means there is nothing to compare.
func Checks(actor access.Actor, userID, agentID, action string, resource Object, limited bool) (checks []Check, ok bool) {
	return nil, false
}
