package assembly

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"

	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/agents"
	"github.com/Shubham04-oss/oryxa-go/agents/primary/internal/serviceconfig"
)

// validAWSConfig is enough for agentcorebrowser.New's synchronous validation
// (a region and a non-nil credential provider) without ever making a network
// call: NewStaticCredentialsProvider never contacts AWS.
func validAWSConfig() aws.Config {
	return aws.Config{
		Region:      "us-east-1",
		Credentials: credentials.NewStaticCredentialsProvider("test", "test", ""),
	}
}

// TestBuildRequiresGoogleCloudProject checks the one Build failure this
// package can exercise without real AWS or Google credentials: agents.Gemini
// rejects an empty project synchronously, after the sandbox and browser are
// already open, so it also proves Build's own cleanup path runs and
// succeeds — a real AWS/Google call is never available in this test.
func TestBuildRequiresGoogleCloudProject(t *testing.T) {
	t.Parallel()
	_, err := Build(context.Background(), Options{
		Settings: serviceconfig.Config{
			// GoogleCloudProject is left empty on purpose.
		},
		AWS: validAWSConfig(),
	})
	if err == nil {
		t.Fatal("Build with no Google Cloud project: error = nil, want one naming the missing project")
	}
	if !strings.Contains(err.Error(), "project") {
		t.Fatalf("Build error = %q, want it to mention the missing project", err.Error())
	}
	// Build had already opened the sandbox and browser when Gemini failed.
	// Its cleanup path closes both before returning; had either close
	// itself failed, that failure would be joined into this same error.
	// Neither ever started a remote session (Invoke was never called), so
	// their Close is a local no-op and cannot fail here — this is what
	// proves the partially built state left nothing open.
	if strings.Contains(err.Error(), "stop AgentCore") {
		t.Fatalf("Build error = %q, want no leaked-resource complaint: its own cleanup should have succeeded", err.Error())
	}
}

// TestKrudeCloseNoClosers checks the zero-value-ish case: a Krude that never
// opened anything (or one Build never got around to on the very first
// failure) closes cleanly.
func TestKrudeCloseNoClosers(t *testing.T) {
	t.Parallel()
	krude := &Krude{}
	if err := krude.Close(context.Background()); err != nil {
		t.Fatalf("Close() on a Krude with no closers = %v, want nil", err)
	}
}

// TestKrudeCloseRunsAllAndJoinsErrors checks the two things Close promises
// beyond a plain loop: every closer runs even though an earlier one failed,
// and their errors all reach the caller. It also locks in the shutdown
// order — last opened, first closed — that mirrors the LIFO order devui's
// own defers ran in before this wiring moved here.
func TestKrudeCloseRunsAllAndJoinsErrors(t *testing.T) {
	t.Parallel()
	errBoom := errors.New("boom")
	var ran []int
	krude := &Krude{closers: []func(context.Context) error{
		func(context.Context) error { ran = append(ran, 1); return nil },
		func(context.Context) error { ran = append(ran, 2); return errBoom },
		func(context.Context) error { ran = append(ran, 3); return fmt.Errorf("also broken: %w", errBoom) },
	}}

	err := krude.Close(context.Background())
	if err == nil {
		t.Fatal("Close() with failing closers: error = nil, want one joining their failures")
	}
	if !errors.Is(err, errBoom) {
		t.Fatalf("Close() error = %v, want it to wrap the closers' errors (errors.Is boom)", err)
	}
	if want := ([]int{3, 2, 1}); !slices.Equal(ran, want) {
		t.Fatalf("closers ran in order %v, want %v (reverse of registration, all run despite failures)", ran, want)
	}
}

// TestWithToolboxAddsOnlyTheOneGiven: a delivery's toolboxes are the
// unattended run's, which hold none of the member's requests, with the
// delivering interactions toolbox after them and nothing else changed;
// without one they are as they were, and the unattended list itself never
// changes.
func TestWithToolboxAddsOnlyTheOneGiven(t *testing.T) {
	t.Parallel()
	unattended := []agents.Toolbox{{Name: "apps", Description: "reads"}}
	delivering := agents.Toolbox{Name: "interactions", Description: "delivering"}
	described := func(toolboxes []agents.Toolbox) []string {
		var descriptions []string
		for _, box := range toolboxes {
			descriptions = append(descriptions, box.Name+":"+box.Description)
		}
		return descriptions
	}
	if got := described(withToolbox(unattended, &delivering)); !slices.Equal(got, []string{"apps:reads", "interactions:delivering"}) {
		t.Fatalf("with the delivering toolbox = %v", got)
	}
	if got := described(withToolbox(unattended, nil)); !slices.Equal(got, []string{"apps:reads"}) {
		t.Fatalf("with none = %v", got)
	}
	if got := described(unattended); !slices.Equal(got, []string{"apps:reads"}) {
		t.Fatalf("the unattended list changed: %v", got)
	}
}
