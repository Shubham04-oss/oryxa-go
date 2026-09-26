// Command authstub serves the simulated auth stub standalone, so a
// developer can drive another service's API as one of the seeded
// identities (test/identities) without a real Cognito login, and so a run
// can mint and use agent tokens against it the way it does against real
// auth. Not for deployment; see test/internal/authstub.
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/Shubham04-oss/oryxa-go/test/internal/authstub"
)

func main() {
	addr := os.Getenv("AUTHSTUB_ADDRESS")
	if addr == "" {
		addr = "127.0.0.1:18080"
	}

	config, err := authstub.ConfigFromEnv(context.Background())
	if err != nil {
		log.Printf("authstub: %v; POST /v1/internal/agent-tokens will refuse every caller", err)
	} else if config.Services == nil {
		log.Print("authstub: AGENTS_PRIMARY_SERVICE_KEY_ID is not set: POST /v1/internal/agent-tokens will refuse every caller")
	}

	log.Printf("authstub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, authstub.NewHandler(config)))
}
