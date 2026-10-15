// Command fakeapp is a connector for a made-up app: two read-and-write tools
// and one stream, for the connector host's tests and the gate.
package main

import (
	"log"

	"github.com/Shubham04-oss/oryxa-go/integrations/connectors/fakeapp/app"
	"github.com/Shubham04-oss/oryxa-go/integrations/gosdk"
)

func main() {
	if err := gosdk.Main(app.New(), "fakeapp"); err != nil {
		log.Fatal(err)
	}
}
