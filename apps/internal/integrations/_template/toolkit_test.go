package toolkit

import (
	"testing"

	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration"
	"github.com/Shubham04-oss/oryxa-go/apps/internal/integration/integrationtest"
)

// The harness is what a toolkit's own test is one line of, and the cassettes
// it reads are built from the provider's own documented answers rather than
// from a live account, so this test needs no key and reaches nothing.
//
// Conformance fails with a list of everything wrong with the toolkit, so a
// worker reads all of it at once and fixes it once.
func TestConformance(t *testing.T) {
	integrationtest.Conformance(t, New(), "testdata")
}

// The manifest is what the core serves tools, readers and events from, so it
// is checked on its own too: a manifest that does not validate stops the
// service at start rather than a member at connect.
func TestTheManifestValidates(t *testing.T) {
	manifest := New().Manifest()
	if err := manifest.Validate(); err != nil {
		t.Fatalf("the manifest does not validate: %v", err)
	}
	// Every tool and event name begins with the toolkit's own prefix, because
	// an agent's skill reads tools by exactly those names.
	prefix := "ACME_"
	for _, tool := range manifest.Tools {
		if len(tool.Name) <= len(prefix) || tool.Name[:len(prefix)] != prefix {
			t.Errorf("the tool %s does not begin with %s", tool.Name, prefix)
		}
	}
	for _, trigger := range manifest.Triggers {
		if len(trigger.Slug) <= len(prefix) || trigger.Slug[:len(prefix)] != prefix {
			t.Errorf("the event %s does not begin with %s", trigger.Slug, prefix)
		}
	}
	// The three optional interfaces are implemented by the runtime beside this
	// file, and the manifest declares what each of them offers.
	if _, ok := interface{}(New()).(integration.Reader); !ok {
		t.Error("the integration declares readers and is not one")
	}
	if _, ok := interface{}(New()).(integration.Poller); !ok {
		t.Error("the integration declares a polled event and is not a Poller")
	}
	if _, ok := interface{}(New()).(integration.Pusher); !ok {
		t.Error("the integration declares a pushed event and is not a Pusher")
	}
}
