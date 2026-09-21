package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/probe"
	"github.com/Shubham04-oss/oryxa-go/test/internal/chaos/rig"
)

// session is what every probe command needs: the rig, a client, a cast.
type session struct {
	state   rig.State
	client  *probe.Client
	world   *probe.World
	runner  *probe.Runner
	monitor *probe.Monitor
	logAt   int64
	workdir string
	verbose bool
}

func openSession(ctx context.Context, workdir string, verbose bool) (*session, error) {
	state, err := rig.LoadState(workdir)
	if err != nil {
		return nil, err
	}
	client, err := probe.NewClient(state.OryxaURL)
	if err != nil {
		return nil, err
	}
	world, err := probe.Bootstrap(ctx, state, client)
	if err != nil {
		return nil, err
	}
	logf := func(format string, args ...any) { fmt.Printf(format+"\n", args...) }
	s := &session{state: state, client: client, world: world, runner: probe.NewRunner(world, logf), workdir: workdir, verbose: verbose}
	s.runner.Stop = func() string {
		if s.monitor != nil && s.monitor.LowDisk(1024) {
			return "stopping: the Docker disk has under 1 GiB free, and a full disk takes every container down, the development database included"
		}
		return ""
	}
	if info, err := os.Stat(state.LogPath); err == nil {
		s.logAt = info.Size()
	}
	if s.monitor, err = probe.StartMonitor(ctx, state, filepath.Join(workdir, "series.jsonl"), 2*time.Second); err != nil {
		return nil, err
	}
	return s, nil
}

// finish stops the monitor and prints what the run did to the system.
func (s *session) finish(title string) {
	// Close my keep-alive connections and give the server time to let go of its
	// own, so whatever it still holds afterwards is a leak and not my client.
	s.client.CloseIdle()
	time.Sleep(6 * time.Second)
	summary := s.monitor.Stop()
	digest, _, _ := probe.ScanLog(s.state.LogPath, s.logAt)

	fmt.Printf("\n══ %s ══\n\n", title)
	fmt.Println("Findings")
	probe.WriteFindings(os.Stdout, s.runner.Findings.All(), s.verbose)
	fmt.Println("\nWhat the server said about itself")
	probe.WriteLogDigest(os.Stdout, digest, 25)
	fmt.Println("\nThe server's own view of each route")
	probe.WriteAccess(os.Stdout, digest, 40)
	fmt.Println("\nWhat the client saw")
	probe.WriteStatusSpread(os.Stdout, s.client.Stats())
	fmt.Println("\nHow the system moved")
	summary.Render(os.Stdout)
}

func fuzzCommand(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("fuzz", flag.ExitOnError)
	workdir := flags.String("workdir", defaultWorkDir(), "rig scratch directory")
	only := flags.String("only", "", "comma-separated services (default: all in the catalog)")
	workers := flags.Int("workers", 8, "concurrent requests")
	baselineOnly := flags.Bool("baseline-only", false, "run each operation's working request once and stop")
	verbose := flags.Bool("v", false, "show response snippets and reproduction commands")
	skipFuzz := flags.Bool("skip-fuzz", false, "skip the mutation pass")
	skipIsolation := flags.Bool("skip-isolation", false, "skip the cross-tenant pass")
	_ = flags.Parse(args)

	s, err := openSession(ctx, *workdir, *verbose)
	if err != nil {
		return err
	}
	s.runner.Workers = *workers
	services := probe.Select(splitList(*only))
	ops := probe.Flatten(services)
	fmt.Printf("world: org A %s, org B %s; %d operations across %d services\n", s.world.A.ID, s.world.B.ID, len(ops), len(services))

	// 1. Every working request, once. If these fail, fuzzing them proves nothing.
	fmt.Println("\n── baseline: does each operation work when used properly?")
	results := s.world.RunBaselines(ctx, ops, false)
	failed := 0
	for _, result := range results {
		if result.Org == "a" && !result.OK {
			failed++
			fmt.Printf("  ✗ %-28s %3d  %s\n", result.Op.Name, result.Status, truncate(result.Body, 150))
		}
	}
	fmt.Printf("  %d of %d operations answered 2xx for org A\n", countOK(results, "a"), countOrg(results, "a"))
	if *baselineOnly {
		s.world.RunBaselines(ctx, ops, true)
		s.finish("baseline")
		return nil
	}

	// 2. Mutations.
	if !*skipFuzz {
		fmt.Println("\n── mutation: hostile variants of each operation")
		for _, service := range services {
			authProbeDone := false
			for _, op := range service.Ops {
				if op.Skip != "" {
					continue
				}
				authProbe := !authProbeDone && op.Method != "GET"
				if authProbe {
					authProbeDone = true
				}
				started := time.Now()
				sent, err := s.runner.FuzzOp(ctx, op, "a", authProbe)
				fmt.Printf("  %-28s %5d requests  %s\n", op.Name, sent, time.Since(started).Round(time.Millisecond))
				if err != nil {
					s.finish("aborted: " + err.Error())
					return err
				}
				if ctx.Err() != nil {
					break
				}
			}
		}
	}

	// 3. Tenant boundaries and roles.
	if !*skipIsolation {
		fmt.Println("\n── isolation: valid credentials, someone else's objects")
		sent, matrix := s.runner.Isolation(ctx, ops)
		fmt.Printf("  %d requests across %d operations\n", sent, len(matrix))
		if *verbose {
			probe.WriteMatrix(os.Stdout, []string{"operation", "A+B-ids", "dual+B-ids", "B-owner→A", "ghost→A", "ghost→B", "A-owner→B", "no-creds", "garbage"}, matrix)
		}
		fmt.Println("\n── roles: what each role of org A may do to org A's objects")
		header, rows := s.runner.RoleMatrix(ctx, ops)
		probe.WriteMatrix(os.Stdout, header, rows)
	}

	// 4. The operations that use up what they act on, last, on objects nothing
	// else needs any more.
	fmt.Println("\n── consuming operations, last: do the deletes work when used properly?")
	final := s.world.RunBaselines(ctx, ops, true)
	for _, result := range final {
		if result.Org == "a" && !result.OK {
			fmt.Printf("  ✗ %-28s %3d  %s\n", result.Op.Name, result.Status, truncate(result.Body, 150))
		}
	}
	fmt.Printf("  %d of %d answered 2xx for org A\n", countOK(final, "a"), countOrg(final, "a"))

	s.finish("fuzz")
	return nil
}

func splitList(text string) []string {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return strings.Split(text, ",")
}

func truncate(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + "…"
}

func countOK(results []probe.BaselineResult, org string) int {
	n := 0
	for _, result := range results {
		if result.Org == org && result.OK {
			n++
		}
	}
	return n
}

func countOrg(results []probe.BaselineResult, org string) int {
	n := 0
	for _, result := range results {
		if result.Org == org {
			n++
		}
	}
	return n
}
