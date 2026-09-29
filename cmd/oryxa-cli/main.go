// Command oryxa-cli configures an Oryxa organization from oryxa.yaml.
package main

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"strings"

	"github.com/Shubham04-oss/oryxa-go/internal/workspace/runtime"
	"github.com/Shubham04-oss/oryxa-go/internal/workspace/spec"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, os.Getenv))
}

type options struct {
	api, org, file, output, preset, domains string
	json, dryRun, noInput, yes              bool
	noBrowser                               bool
	agentHelp                               bool
	to                                      int64
}

//go:embed agent-help.md
var agentHelp string

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		usage(stdout)
		return 0
	}
	command := args[0]
	flagArgs := args[1:]
	connectToolkit := ""
	if command == "connect-app" && len(flagArgs) > 0 && !strings.HasPrefix(flagArgs[0], "-") {
		connectToolkit, flagArgs = flagArgs[0], flagArgs[1:]
	}
	var opts options
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&opts.api, "api", envOr(getenv, "ORYXA_API_URL", "https://api.oryxa.in"), "API base URL")
	flags.StringVar(&opts.org, "org", getenv("ORYXA_ORG"), "organization slug")
	flags.StringVar(&opts.file, "file", "oryxa.yaml", "workspace file")
	flags.StringVar(&opts.file, "f", "oryxa.yaml", "workspace file")
	flags.StringVar(&opts.output, "output", "", "write YAML to this file")
	flags.StringVar(&opts.output, "o", "", "write YAML to this file")
	flags.StringVar(&opts.preset, "preset", "team", "startup, team or regulated")
	flags.StringVar(&opts.domains, "domains", "", "comma-separated member email domains for init")
	flags.BoolVar(&opts.json, "json", false, "print machine-readable result")
	flags.BoolVar(&opts.dryRun, "dry-run", false, "show changes without applying")
	flags.BoolVar(&opts.noInput, "no-input", false, "never prompt for confirmation")
	flags.BoolVar(&opts.noBrowser, "no-browser", false, "print sign-in URL without opening a browser")
	flags.BoolVar(&opts.agentHelp, "agent-help", false, "print the coding-agent workspace mapping guide")
	flags.BoolVar(&opts.yes, "yes", false, "confirm setup plan or overwrite an output file")
	flags.Int64Var(&opts.to, "to", 0, "workspace version to restore (rollback)")
	if err := flags.Parse(flagArgs); err != nil {
		return 3
	}
	if command == "connect-app" && connectToolkit == "" && flags.NArg() == 1 {
		connectToolkit = flags.Arg(0)
	} else if flags.NArg() != 0 {
		fmt.Fprintln(stderr, "unexpected argument:", flags.Arg(0))
		return 3
	}
	if command == "connect-app" {
		return connectApp(ctx, connectToolkit, opts, stdout, stderr, getenv)
	}
	if command == "agent-help" || (command == "setup" && opts.agentHelp) {
		if opts.json {
			return report(stdout, true, map[string]string{"instructions": agentHelp})
		}
		_, err := io.WriteString(stdout, agentHelp)
		if err != nil {
			return reportError(err, stderr)
		}
		return 0
	}
	if command == "init" {
		return initCommand(ctx, opts, stdout, stderr, getenv)
	}
	if command == "login" {
		client, err := signedInClient(ctx, opts, stdout, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		var me json.RawMessage
		if err := client.Whoami(ctx, &me); err != nil {
			return reportError(err, stderr)
		}
		if _, err := stdout.Write(append(me, '\n')); err != nil {
			return reportError(err, stderr)
		}
		return 0
	}
	if command == "setup" {
		document, err := setupDocument(opts, stderr)
		if err != nil {
			return reportError(err, stderr)
		}
		if opts.org != "" && opts.org != document.Metadata.Organization {
			return reportError(fmt.Errorf("--org does not match metadata.organization"), stderr)
		}
		client, err := signedInClient(ctx, opts, stdout, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		state, err := client.Snapshot(ctx, document.Metadata.Organization)
		if err != nil {
			return reportError(err, stderr)
		}
		plan, err := workspacePlan(ctx, client, document, state)
		if err != nil {
			return reportError(err, stderr)
		}
		if code := report(stdout, opts.json, plan); code != 0 || opts.dryRun {
			return code
		}
		if hasRemoval(plan) && opts.noInput {
			return reportError(fmt.Errorf("prune requires an interactive confirmation"), stderr)
		}
		if !opts.yes && len(plan.Changes) > 0 {
			if opts.noInput {
				return reportError(fmt.Errorf("apply requires --yes under --no-input"), stderr)
			}
			fmt.Fprint(stderr, "Apply these changes? [y/N] ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				return reportError(fmt.Errorf("apply was cancelled"), stderr)
			}
		}
		result, err := workspaceApply(ctx, client, document, state, plan)
		if err != nil {
			return reportError(err, stderr)
		}
		if report(stdout, opts.json, result) != 0 {
			return 1
		}
		return applyExitCode(result.Status)
	}
	if command == "validate" {
		document, err := readDocument(opts.file)
		if err != nil {
			return reportError(err, stderr)
		}
		return report(stdout, opts.json, map[string]any{"step": "validate", "status": "done", "organization": document.Metadata.Organization})
	}
	if command == "rollback" {
		if opts.org == "" || opts.to <= 0 {
			return reportError(fmt.Errorf("rollback requires --org and a positive --to version"), stderr)
		}
		client, err := signedInClient(ctx, opts, stdout, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		state, err := client.Snapshot(ctx, opts.org)
		if err != nil {
			return reportError(err, stderr)
		}
		if state.Organization == nil {
			return reportError(fmt.Errorf("organization was not found"), stderr)
		}
		plan, err := client.WorkspaceRollbackPlan(ctx, state.Organization.ID, opts.to)
		if err != nil {
			return reportError(err, stderr)
		}
		if code := report(stdout, opts.json, plan); code != 0 || opts.dryRun {
			return code
		}
		if hasRemoval(plan) {
			if opts.noInput {
				return reportError(fmt.Errorf("prune requires an interactive confirmation"), stderr)
			}
			if !opts.yes {
				fmt.Fprint(stderr, "Confirm these removals? [y/N] ")
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(strings.ToLower(answer)) != "y" {
					return reportError(fmt.Errorf("rollback was cancelled"), stderr)
				}
			}
		}
		result, err := client.WorkspaceRollback(ctx, state.Organization.ID, opts.to, plan)
		if err != nil {
			return reportError(err, stderr)
		}
		if code := report(stdout, opts.json, result); code != 0 {
			return code
		}
		return applyExitCode(result.Status)
	}
	if command == "plan" || command == "apply" || command == "diff" {
		document, err := readDocument(opts.file)
		if err != nil {
			return reportError(err, stderr)
		}
		if opts.org != "" && opts.org != document.Metadata.Organization {
			return reportError(fmt.Errorf("--org does not match metadata.organization"), stderr)
		}
		client, err := signedInClient(ctx, opts, stdout, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		if command == "apply" && !opts.dryRun {
			state, err := client.Snapshot(ctx, document.Metadata.Organization)
			if err != nil {
				return reportError(err, stderr)
			}
			approved, err := workspacePlan(ctx, client, document, state)
			if err != nil {
				return reportError(err, stderr)
			}
			if hasRemoval(approved) {
				if opts.noInput {
					return reportError(fmt.Errorf("prune requires an interactive confirmation"), stderr)
				}
				if code := report(stdout, opts.json, approved); code != 0 {
					return code
				}
				if !opts.yes {
					fmt.Fprint(stderr, "Confirm these removals? [y/N] ")
					answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					if strings.TrimSpace(strings.ToLower(answer)) != "y" {
						return reportError(fmt.Errorf("apply was cancelled"), stderr)
					}
				}
			}
			result, err := workspaceApply(ctx, client, document, state, approved)
			if err != nil {
				return reportError(err, stderr)
			}
			if code := report(stdout, opts.json, result); code != 0 {
				return code
			}
			return applyExitCode(result.Status)
		}
		state, err := client.Snapshot(ctx, document.Metadata.Organization)
		if err != nil {
			return reportError(err, stderr)
		}
		plan, err := workspacePlan(ctx, client, document, state)
		if err != nil {
			return reportError(err, stderr)
		}
		return report(stdout, opts.json, plan)
	}
	if command == "pull" || command == "status" || command == "doctor" || command == "whoami" {
		client, err := signedInClient(ctx, opts, stdout, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		if command == "whoami" {
			var me json.RawMessage
			if err := client.Whoami(ctx, &me); err != nil {
				return reportError(err, stderr)
			}
			_, err := stdout.Write(append(me, '\n'))
			if err != nil {
				return reportError(err, stderr)
			}
			return 0
		}
		if opts.org == "" {
			return reportError(fmt.Errorf("--org or ORYXA_ORG is required"), stderr)
		}
		state, err := client.Snapshot(ctx, opts.org)
		if err != nil {
			return reportError(err, stderr)
		}
		if state.Organization == nil {
			return reportError(fmt.Errorf("organization was not found"), stderr)
		}
		if command == "status" || command == "doctor" {
			status, err := client.WorkspaceStatus(ctx, state.Organization.ID)
			if err != nil {
				return reportError(err, stderr)
			}
			if command == "doctor" {
				var detail map[string]any
				if err := json.Unmarshal(status, &detail); err != nil {
					return reportError(err, stderr)
				}
				detail["step"] = "doctor"
				switch detail["status"] {
				case "not_configured":
					detail["fix"] = "run oryxa-cli setup with the workspace file"
				case "drift":
					detail["fix"] = "review the plan and rerun apply with the workspace file"
				case "needs_human":
					detail["fix"] = "complete the listed human steps, then rerun apply with the workspace file"
				}
				return report(stdout, opts.json, detail)
			}
			return report(stdout, opts.json, status)
		}
		document, err := client.WorkspacePull(ctx, state.Organization.ID)
		if err != nil {
			return reportError(err, stderr)
		}
		return writeDocument(document, opts.output, opts.yes, stdout, stderr)
	}
	fmt.Fprintln(stderr, "unknown command:", command)
	usage(stderr)
	return 3
}

func workspacePlan(ctx context.Context, client *runtime.Client, document spec.Document, state runtime.Snapshot) (runtime.Plan, error) {
	if state.Organization == nil {
		servicePlan, err := runtime.BuildPlan(document, state)
		if err != nil {
			return runtime.Plan{}, err
		}
		_, contextPlan, err := runtime.MergeContext(spec.Document{}, document, state, false)
		if err != nil {
			return runtime.Plan{}, err
		}
		_, policyPlan, err := runtime.MergePolicies(spec.Document{}, document, false)
		if err != nil {
			return runtime.Plan{}, err
		}
		return runtime.CombinePlans(servicePlan, contextPlan, policyPlan), nil
	}
	return client.WorkspacePlan(ctx, state.Organization.ID, document)
}

func workspaceApply(ctx context.Context, client *runtime.Client, document spec.Document, state runtime.Snapshot, approved runtime.Plan) (runtime.WorkspaceApplyResult, error) {
	if state.Organization == nil {
		if _, err := runtime.ApplyApproved(ctx, client, document, approved); err != nil {
			return runtime.WorkspaceApplyResult{}, err
		}
		var err error
		state, err = client.Snapshot(ctx, document.Metadata.Organization)
		if err != nil {
			return runtime.WorkspaceApplyResult{}, err
		}
		if state.Organization == nil {
			return runtime.WorkspaceApplyResult{}, fmt.Errorf("created organization has not appeared in the API; retry apply")
		}
	}
	return client.WorkspaceApply(ctx, state.Organization.ID, document, approved)
}

func applyExitCode(status string) int {
	switch status {
	case "done":
		return 0
	case "needs_human":
		return 2
	default:
		return 1
	}
}

func setupDocument(opts options, stderr io.Writer) (spec.Document, error) {
	document, err := readDocument(opts.file)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return document, err
	}
	if opts.org == "" {
		if opts.noInput {
			return spec.Document{}, fmt.Errorf("--org is required to create a starter workspace under --no-input")
		}
		fmt.Fprint(stderr, "Organization slug: ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		opts.org = strings.TrimSpace(answer)
	}
	document = spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: opts.org}, Preset: opts.preset}
	if opts.domains != "" {
		for _, domain := range strings.Split(opts.domains, ",") {
			document.Metadata.Domains = append(document.Metadata.Domains, strings.TrimSpace(domain))
		}
	}
	if err := document.Validate(); err != nil {
		return spec.Document{}, err
	}
	data, err := document.YAML()
	if err != nil {
		return spec.Document{}, err
	}
	if err := os.WriteFile(opts.file, data, 0600); err != nil {
		return spec.Document{}, err
	}
	return document, nil
}

func hasRemoval(plan runtime.Plan) bool {
	for _, change := range plan.Changes {
		if change.Op == "delete" {
			return true
		}
	}
	return false
}

func signedInClient(ctx context.Context, opts options, stdout io.Writer, getenv func(string) string) (*runtime.Client, error) {
	token := getenv("ORYXA_ACCESS_TOKEN")
	if token == "" {
		if opts.noInput {
			return nil, fmt.Errorf("browser sign-in is required under --no-input; run without --no-input or provide a short-lived ORYXA_ACCESS_TOKEN")
		}
		var err error
		token, err = nativeLogin(ctx, opts.api, opts.noBrowser, opts.json, stdout)
		if err != nil {
			return nil, err
		}
	}
	return runtime.NewClient(opts.api, token)
}

func initCommand(ctx context.Context, opts options, stdout, stderr io.Writer, getenv func(string) string) int {
	if opts.org == "" {
		return reportError(fmt.Errorf("--org or ORYXA_ORG is required"), stderr)
	}
	document := spec.Document{APIVersion: spec.APIVersion, Kind: "Workspace", Metadata: spec.Metadata{Organization: opts.org}, Preset: opts.preset}
	domains := []string{}
	if opts.domains != "" {
		for _, domain := range strings.Split(opts.domains, ",") {
			domains = append(domains, strings.TrimSpace(domain))
		}
	}
	document.Metadata.Domains = domains
	if token := getenv("ORYXA_ACCESS_TOKEN"); token != "" {
		client, err := runtime.NewClient(opts.api, token)
		if err != nil {
			return reportError(err, stderr)
		}
		state, err := client.Snapshot(ctx, opts.org)
		if err != nil {
			return reportError(err, stderr)
		}
		if state.Organization != nil {
			document, err = client.WorkspacePull(ctx, state.Organization.ID)
			if err != nil {
				return reportError(err, stderr)
			}
			document.Preset = opts.preset
			if opts.domains != "" {
				document.Metadata.Domains = domains
			}
		}
	}
	return writeDocument(document, opts.output, opts.yes, stdout, stderr)
}

func readDocument(path string) (spec.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return spec.Document{}, err
	}
	return spec.Parse(data)
}

func writeDocument(document spec.Document, path string, overwrite bool, stdout, stderr io.Writer) int {
	data, err := document.YAML()
	if err != nil {
		return reportError(err, stderr)
	}
	if path == "" {
		if _, err := stdout.Write(data); err != nil {
			return reportError(err, stderr)
		}
		return 0
	}
	mode := os.O_WRONLY | os.O_CREATE | os.O_EXCL
	if overwrite {
		mode = os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	}
	file, err := os.OpenFile(path, mode, 0600)
	if err != nil {
		return reportError(err, stderr)
	}
	_, writeErr := file.Write(data)
	closeErr := file.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return reportError(err, stderr)
	}
	return 0
}

func report(writer io.Writer, asJSON bool, value any) int {
	if asJSON {
		if err := json.NewEncoder(writer).Encode(value); err != nil {
			return 1
		}
		return 0
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return 1
	}
	_, err = fmt.Fprintln(writer, string(encoded))
	if err != nil {
		return 1
	}
	return 0
}

func reportError(err error, stderr io.Writer) int {
	fmt.Fprintln(stderr, err)
	if runtime.IsStatus(err, http.StatusConflict) {
		return 5
	}
	return 3
}

func envOr(getenv func(string) string, key, fallback string) string {
	if value := getenv(key); value != "" {
		return value
	}
	return fallback
}

func usage(writer io.Writer) {
	fmt.Fprint(writer, `Usage: oryxa-cli <command> [flags]

Commands:
  setup      browser sign-in, plan and apply in one process
  agent-help print the coding-agent workspace mapping guide
  login      browser sign-in and show the current account (no saved session)
  init       write a starting oryxa.yaml to stdout or -o
  validate   validate a workspace file without contacting Oryxa
  plan       show changes against the current organization
  apply      make the planned changes, then report any pending joins
  diff       alias of plan
  pull       write live organization settings as YAML
  status     check the applied workspace for drift and pending steps
  doctor     show status and the next corrective command
  rollback  restore a saved workspace version with --to VERSION
  whoami     show the authenticated user and organizations
  connect-app TOOLKIT  connect your own Composio app through its browser page

Remote commands sign in through the browser when ORYXA_ACCESS_TOKEN is absent.
Each command keeps its session in memory and never saves the token. Use -f for
the input file, --org for an organization
slug, --api for the API URL, --json for machine-readable output, and --dry-run
to plan an apply. Unsupported sections are rejected before any write.
`)
}
