package host

import (
	"time"

	"github.com/Shubham04-oss/oryxa-go/internal/eventauth"
)

// TestRegistry is the event signing identities a service test mounts with.
// It is what openPlatform builds — a registry whose services get local
// keys — without the process around it, so a test constructing a Platform
// directly can mount the way a running host does.
func TestRegistry() *eventauth.Registry {
	return eventauth.NewRegistry(nil, time.Now)
}
