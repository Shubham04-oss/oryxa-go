// Command fakeapp is a connector with no app behind it: one read tool and one
// stream, for the connector host's tests and the gate.
package main

import (
	_ "embed"
	"log"

	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
)

//go:embed manifest.json
var manifestJSON string

func main() {
	if err := gosdk.Main(server{}, "fakeapp"); err != nil {
		log.Fatal(err)
	}
}
