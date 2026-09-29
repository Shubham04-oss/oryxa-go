package interactions

import (
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/guardpolicy"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/managedagents"
)

// What the service is wired with, checked where it is built rather than where it
// is wired: the registry finds the primary agents an ask files its requests
// between, the answer guard's settings say how strictly an answer is read, and
// the store is the ledger's consent side as well as its repository.
var (
	_ PrimaryAgents = (*managedagents.Service)(nil)
	_ GuardPolicy   = (*guardpolicy.Service)(nil)
	_ Repository    = (*Store)(nil)
	_ Consents      = (*Store)(nil)
)
