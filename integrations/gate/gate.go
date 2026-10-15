package gate

import (
	"context"
	"time"

	"github.com/Shubham04-oss/oryxa-go/integrations/host"
)

// Failure is one check a connector did not pass.
type Failure struct {
	Check, Detail string
}

func (f Failure) String() string { return f.Check + ": " + f.Detail }

// Options say which connector to check.
type Options struct {
	// App is the manifest id.
	App string
	// Binary is the built connector.
	Binary string
	// Dir holds the connector's testdata/.
	Dir string
}

// Run starts the connector under the real host and runs every check against
// it. It returns what failed; the error is for what stopped the gate itself.
func Run(ctx context.Context, opts Options) ([]Failure, error) {
	h, err := host.New(host.Config{
		StartTimeout: 10 * time.Second, SyncIdle: 10 * time.Second, SyncLimit: 30 * time.Second, InvokeTimeout: 10 * time.Second,
	})
	if err != nil {
		return nil, err
	}
	defer h.Close()
	if err := h.Start(ctx, host.Spec{ID: opts.App, Path: opts.Binary}); err != nil {
		// A manifest the validator refuses fails here, which is the first check.
		return []Failure{{"manifest", err.Error()}}, nil
	}
	m, _ := h.Manifest(opts.App)

	out := Lint(m)
	sc, err := loadScenario(opts.Dir)
	if err != nil {
		return append(out, Failure{"replay", err.Error()}), nil
	}
	r := &replayer{h: h, m: m, dir: opts.Dir, checkpoints: map[string][]byte{}}
	out = append(out, r.run(ctx, sc)...)
	return out, nil
}
