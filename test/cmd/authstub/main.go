// Command authstub serves the simulated auth stub standalone, so a
// developer can drive another service's API as one of the seeded
// identities (test/identities) without a real Cognito login. Not for
// deployment; see test/internal/authstub.
package main

import (
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
	log.Printf("authstub listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, authstub.Handler()))
}
