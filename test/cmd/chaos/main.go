// Command chaos throws fake and hostile payloads at a locally running copy of
// the whole Oryxa stack and watches what happens.
//
//	chaos rig up      boot an isolated stack (own database, queues, fakes) and hold it
//	chaos rig status  show the running rig
//	chaos rig clean   remove a dead rig's database and emulator resources
//
// See test/README.md. Nothing here is a product service and none of it may be
// deployed.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/rig"
)

func main() {
	// Whatever else happens, no AWS profile on this machine may be read: the
	// rig talks to emulators with static test credentials only.
	for name, value := range map[string]string{
		"AWS_CONFIG_FILE": "/dev/null", "AWS_SHARED_CREDENTIALS_FILE": "/dev/null",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_PROFILE": "",
	} {
		_ = os.Setenv(name, value)
	}
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "rig":
		err = rigCommand(ctx, os.Args[2:])
	case "fuzz":
		err = fuzzCommand(ctx, os.Args[2:])
	case "attack":
		err = attackCommand(ctx, os.Args[2:])
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "chaos:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: chaos rig (up|status|clean) [flags]
       chaos fuzz [flags]
       chaos attack [flags] <name|all>...   (no name lists them)`)
}

func defaultWorkDir() string { return filepath.Join(os.TempDir(), "oryxa-chaos") }

func rigCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		usage()
		return fmt.Errorf("rig needs a subcommand")
	}
	flags := flag.NewFlagSet("rig "+args[0], flag.ExitOnError)
	workdir := flags.String("workdir", defaultWorkDir(), "scratch directory for binaries, logs and state")
	id := flags.String("id", time.Now().Format("0102150405"), "run id: resource prefix (lower-case letters and digits)")
	services := flags.String("services", "", "ORYXA_SERVICES (default: every service but auth, which the stub replaces)")
	keep := flags.Bool("keep", false, "leave the database and emulator resources behind on exit")
	minFree := flags.Int("min-free-gb", 3, "refuse to start with less free space than this on the Docker disk")
	_ = flags.Parse(args[1:])

	root, err := repoRoot()
	if err != nil {
		return err
	}
	opt := rig.Options{Root: root, WorkDir: *workdir, RunID: *id, Services: *services, Keep: *keep, MinFreeBytes: int64(*minFree) << 30}

	switch args[0] {
	case "up":
		instance, err := rig.Up(ctx, opt)
		if err != nil {
			return err
		}
		defer func() {
			shutdown, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			instance.Down(shutdown)
		}()
		fmt.Println("rig is up; press Ctrl-C to tear it down")
		return instance.Wait(ctx)
	case "status":
		state, err := rig.LoadState(*workdir)
		if err != nil {
			return err
		}
		encoded, _ := json.MarshalIndent(state, "", "  ")
		fmt.Println(string(encoded))
		return nil
	case "clean":
		return rig.CleanupRun(ctx, opt)
	}
	return fmt.Errorf("unknown rig subcommand %q", strings.Join(args[:1], ""))
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found: run chaos from inside the repository")
		}
		dir = parent
	}
}
