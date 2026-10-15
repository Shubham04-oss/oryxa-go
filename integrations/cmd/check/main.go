// Command check runs the connector gate (docs/design/integrations/README.md §8.4):
//
//	go run ./integrations/cmd/check <app>
//
// It builds integrations/connectors/<app>, runs it under the connector host
// against the cassettes in its testdata/ and exits non-zero if any check fails.
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"

	"github.com/Shubham04-oss/oryxa-go/integrations/gate"
)

var appRE = regexp.MustCompile(`^[a-z][a-z0-9_]{1,31}$`)

func main() {
	if len(os.Args) != 2 || !appRE.MatchString(os.Args[1]) {
		fmt.Fprintln(os.Stderr, "usage: go run ./integrations/cmd/check <app>")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	failures, err := check(ctx, os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "check:", err)
		os.Exit(2)
	}
	for _, f := range failures {
		fmt.Println("FAIL", f)
	}
	if len(failures) > 0 {
		os.Exit(1)
	}
	fmt.Println("ok", os.Args[1])
}

func check(ctx context.Context, app string) ([]gate.Failure, error) {
	dir := filepath.Join("integrations", "connectors", app)
	if _, err := os.Stat(dir); err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "oryxa-check")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	bin := filepath.Join(tmp, app)
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./"+filepath.ToSlash(dir))
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return []gate.Failure{{Check: "build", Detail: err.Error()}}, nil
	}
	return gate.Run(ctx, gate.Options{App: app, Binary: bin, Dir: dir})
}
