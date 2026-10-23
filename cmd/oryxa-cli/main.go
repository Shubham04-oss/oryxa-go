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
	"os"
	"slices"
	"strings"
	"time"

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
	device                                  bool
	forceLogin                              bool
	agentHelp                               bool
	to                                      int64
	minimumSessionTTL                       time.Duration
}

//go:embed agent-help.md
var agentHelp string

func run(ctx context.Context, args []string, stdout, stderr io.Writer, getenv func(string) string) int {
	if len(args) == 0 || slices.Contains([]string{"help", "-h", "--help"}, args[0]) {
		usage(stdout)
		return 0
	}
	command := args[0]
	stderr = &commandErrorOutput{Writer: stderr, stdout: stdout, step: command, json: slices.Contains(args, "--json") || slices.Contains(args, "--json=true")}
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
	flags.BoolVar(&opts.device, "device", false, "sign in with a code approved in any browser (for SSH or a machine without a browser)")
	flags.BoolVar(&opts.forceLogin, "force-login", false, "sign in again instead of using the cached session")
	flags.BoolVar(&opts.agentHelp, "agent-help", false, "print the coding-agent workspace mapping guide")
	flags.BoolVar(&opts.yes, "yes", false, "confirm setup plan or overwrite an output file")
	flags.Int64Var(&opts.to, "to", 0, "workspace version to restore (rollback)")
	if err := flags.Parse(flagArgs); err != nil {
		return reportInputError(err, stderr)
	}
	if command == "connect-app" && connectToolkit == "" && flags.NArg() == 1 {
		connectToolkit = flags.Arg(0)
	} else if flags.NArg() != 0 {
		return reportInputError(fmt.Errorf("unexpected argument: %s", flags.Arg(0)), stderr)
	}
	if (command == "setup" || command == "apply" || command == "rollback") && !opts.dryRun && !opts.agentHelp {
		opts.minimumSessionTTL = runtime.ApplyTimeout + 30*time.Second
		defer func() {
			if err := clearSession(opts.api, getenv); err != nil {
				fmt.Fprintln(stderr, "Could not clear CLI session:", err)
			}
		}()
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
		client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
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
	if command == "logout" {
		if err := clearSession(opts.api, getenv); err != nil {
			return reportError(err, stderr)
		}
		return report(stdout, opts.json, map[string]string{"step": "logout", "status": "done"})
	}
	if command == "setup" {
		client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		document, err := setupDocument(opts, stderr)
		if err != nil {
			return reportError(err, stderr)
		}
		if opts.org != "" && opts.org != document.Metadata.Organization {
			return reportInputError(fmt.Errorf("--org does not match metadata.organization"), stderr)
		}
		state, err := planningState(ctx, client, document.Metadata.Organization)
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
		if hasSensitiveChanges(plan) && opts.noInput {
			return reportHumanError(fmt.Errorf("sensitive changes require an interactive confirmation"), stderr)
		}
		if !opts.yes && len(plan.Changes) > 0 {
			if opts.noInput {
				return reportHumanError(fmt.Errorf("apply requires --yes under --no-input"), stderr)
			}
			fmt.Fprint(stderr, "Apply these changes? [y/N] ")
			answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
			if strings.TrimSpace(strings.ToLower(answer)) != "y" {
				return reportInputError(fmt.Errorf("apply was cancelled"), stderr)
			}
		}
		result, err := workspaceApply(ctx, client, document, state, plan, opts, stdout, stderr)
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
			return reportInputError(fmt.Errorf("rollback requires --org and a positive --to version"), stderr)
		}
		client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		organization, err := client.OrganizationBySlug(ctx, opts.org)
		if err != nil {
			return reportError(err, stderr)
		}
		plan, err := client.WorkspaceRollbackPlan(ctx, organization.ID, opts.to)
		if err != nil {
			return reportError(err, stderr)
		}
		if code := report(stdout, opts.json, plan); code != 0 || opts.dryRun {
			return code
		}
		if hasSensitiveChanges(plan) {
			if opts.noInput {
				return reportHumanError(fmt.Errorf("sensitive changes require an interactive confirmation"), stderr)
			}
			if !opts.yes {
				fmt.Fprint(stderr, "Confirm these sensitive changes? [y/N] ")
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(strings.ToLower(answer)) != "y" {
					return reportInputError(fmt.Errorf("rollback was cancelled"), stderr)
				}
			}
		}
		result, err := client.WorkspaceRollback(ctx, organization.ID, opts.to, plan)
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
			return reportInputError(fmt.Errorf("--org does not match metadata.organization"), stderr)
		}
		client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
		if err != nil {
			return reportError(err, stderr)
		}
		if command == "apply" && !opts.dryRun {
			state, err := planningState(ctx, client, document.Metadata.Organization)
			if err != nil {
				return reportError(err, stderr)
			}
			approved, err := workspacePlan(ctx, client, document, state)
			if err != nil {
				return reportError(err, stderr)
			}
			if hasSensitiveChanges(approved) {
				if opts.noInput {
					return reportHumanError(fmt.Errorf("sensitive changes require an interactive confirmation"), stderr)
				}
				if code := report(stdout, opts.json, approved); code != 0 {
					return code
				}
				if !opts.yes {
					fmt.Fprint(stderr, "Confirm these sensitive changes? [y/N] ")
					answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
					if strings.TrimSpace(strings.ToLower(answer)) != "y" {
						return reportInputError(fmt.Errorf("apply was cancelled"), stderr)
					}
				}
			}
			result, err := workspaceApply(ctx, client, document, state, approved, opts, stdout, stderr)
			if err != nil {
				return reportError(err, stderr)
			}
			if code := report(stdout, opts.json, result); code != 0 {
				return code
			}
			return applyExitCode(result.Status)
		}
		state, err := planningState(ctx, client, document.Metadata.Organization)
		if err != nil {
			return reportError(err, stderr)
		}
		plan, err := workspacePlan(ctx, client, document, state)
		if err != nil {
			return reportError(err, stderr)
		}
		return report(stdout, opts.json, plan)
	}
	if command == "pull" || command == "status" || command == "doctor" || command == "mappings" || command == "whoami" {
		client, err := signedInClient(ctx, opts, stdout, stderr, getenv)
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
			return reportInputError(fmt.Errorf("--org or ORYXA_ORG is required"), stderr)
		}
		if command == "mappings" {
			organization, err := client.OrganizationBySlug(ctx, opts.org)
			if err != nil {
				return reportError(err, stderr)
			}
			mappings, err := client.WorkspaceMappings(ctx, organization.ID)
			if err != nil {
				return reportError(err, stderr)
			}
			return report(stdout, opts.json, map[string]any{"mappings": mappings})
		}
		organization, err := client.OrganizationBySlug(ctx, opts.org)
		if err != nil {
			return reportError(err, stderr)
		}
		if command == "status" || command == "doctor" {
			status, err := client.WorkspaceStatus(ctx, organization.ID)
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
		document, err := client.WorkspacePull(ctx, organization.ID)
		if err != nil {
			return reportError(err, stderr)
		}
		return writeDocument(document, opts.output, opts.yes, stdout, stderr)
	}
	return reportInputError(fmt.Errorf("unknown command: %s", command), stderr)
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
		_, directoryPlan, err := runtime.MergeDirectory(spec.Document{}, document, state, nil, false)
		if err != nil {
			return runtime.Plan{}, err
		}
		_, policyPlan, err := runtime.MergePolicies(spec.Document{}, document, false)
		if err != nil {
			return runtime.Plan{}, err
		}
		return runtime.CombinePlans(servicePlan, contextPlan, directoryPlan, policyPlan), nil
	}
	return client.WorkspacePlan(ctx, state.Organization.ID, document)
}

func workspaceApply(ctx context.Context, client *runtime.Client, document spec.Document, state runtime.Snapshot, approved runtime.Plan, opts options, stdout, stderr io.Writer) (runtime.WorkspaceApplyResult, error) {
	if state.Organization == nil {
		name := document.Metadata.Organization
		if document.Organization != nil && document.Organization.Name != "" {
			name = document.Organization.Name
		}
		if err := client.CreateOrganization(ctx, name, document.Metadata.Organization, document); err != nil {
			return runtime.WorkspaceApplyResult{}, err
		}
		organization, err := client.OrganizationBySlug(ctx, document.Metadata.Organization)
		if err != nil {
			return runtime.WorkspaceApplyResult{}, err
		}
		state.Organization = &organization
		approved, err = client.WorkspacePlan(ctx, organization.ID, document)
		if err != nil {
			return runtime.WorkspaceApplyResult{}, err
		}
		if hasSensitiveChanges(approved) {
			if code := report(stdout, opts.json, approved); code != 0 {
				return runtime.WorkspaceApplyResult{}, fmt.Errorf("write bootstrap plan")
			}
			if opts.noInput {
				return runtime.WorkspaceApplyResult{}, fmt.Errorf("sensitive changes require an interactive confirmation; organization created, rerun apply to review the server plan")
			}
			if !opts.yes {
				fmt.Fprint(stderr, "Confirm these sensitive changes? [y/N] ")
				answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				if strings.TrimSpace(strings.ToLower(answer)) != "y" {
					return runtime.WorkspaceApplyResult{}, fmt.Errorf("apply was cancelled; organization created")
				}
			}
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

func hasSensitiveChanges(plan runtime.Plan) bool {
	return len(runtime.SensitiveChanges(plan)) > 0
}

func signedInClient(ctx context.Context, opts options, stdout, stderr io.Writer, getenv func(string) string) (*runtime.Client, error) {
	token := getenv("ORYXA_ACCESS_TOKEN")
	if token == "" {
		if !opts.forceLogin {
			session, ok, err := loadSession(opts.api, getenv)
			if err != nil {
				fmt.Fprintln(stderr, "Could not read cached CLI session:", err)
			} else if ok && session.ExpiresAt.After(time.Now().Add(opts.minimumSessionTTL)) {
				if opts.json {
					_ = json.NewEncoder(stderr).Encode(map[string]string{"step": "login", "status": "done", "source": "cache", "expires_at": session.ExpiresAt.UTC().Format(time.RFC3339)})
				} else {
					fmt.Fprintf(stderr, "Using saved sign-in. Access expires at %s.\n", session.ExpiresAt.Local().Format(time.RFC3339))
				}
				client, err := runtime.NewClient(opts.api, session.AccessToken)
				if err == nil {
					client.TokenExpiresAt = session.ExpiresAt
				}
				return client, err
			}
		}
		if opts.noInput {
			return nil, &humanError{fmt.Errorf("browser sign-in is required under --no-input; run without --no-input or provide a short-lived ORYXA_ACCESS_TOKEN")}
		}
		var err error
		var session cliSession
		if useDeviceLogin(opts, getenv) {
			session, err = deviceLogin(ctx, opts.api, opts.json, stderr)
		} else {
			session, err = nativeLogin(ctx, opts.api, opts.noBrowser, opts.json, stderr)
		}
		if err != nil {
			return nil, err
		}
		token = session.AccessToken
		if err := saveSession(opts.api, session, getenv); err != nil {
			fmt.Fprintln(stderr, "Could not save short-lived CLI session:", err)
		}
	}
	client, err := runtime.NewClient(opts.api, token)
	if err == nil && getenv("ORYXA_ACCESS_TOKEN") == "" {
		if session, ok, _ := loadSession(opts.api, getenv); ok {
			client.TokenExpiresAt = session.ExpiresAt
		}
	}
	return client, err
}

func initCommand(ctx context.Context, opts options, stdout, stderr io.Writer, getenv func(string) string) int {
	if opts.org == "" {
		return reportInputError(fmt.Errorf("--org or ORYXA_ORG is required"), stderr)
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
		return spec.Document{}, &inputError{err}
	}
	document, err := spec.Parse(data)
	if err != nil {
		return spec.Document{}, &inputError{err}
	}
	return document, nil
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

type inputError struct{ error }

func (err *inputError) Unwrap() error { return err.error }

type humanError struct{ error }

func (err *humanError) Unwrap() error { return err.error }

type commandErrorOutput struct {
	io.Writer
	stdout io.Writer
	step   string
	json   bool
}

func reportHumanError(err error, stderr io.Writer) int { return reportError(&humanError{err}, stderr) }

func reportInputError(err error, stderr io.Writer) int { return reportError(&inputError{err}, stderr) }

func reportError(err error, stderr io.Writer) int {
	exitCode, code := 1, "operation_failed"
	var input *inputError
	var human *humanError
	var remote *runtime.StatusError
	switch {
	case errors.Is(err, runtime.ErrSessionExpiring):
		exitCode, code = 2, "needs_human"
	case errors.As(err, &human):
		exitCode, code = 2, "needs_human"
	case errors.As(err, &input):
		exitCode, code = 3, "invalid_input"
	case errors.As(err, &remote):
		if remote.Code != "" {
			code = remote.Code
		}
		if remote.Code == "apply_lock" {
			exitCode = 5
		}
		if remote.Code == "invalid_input" {
			exitCode = 3
		}
	}
	if output, ok := stderr.(*commandErrorOutput); ok && output.json {
		payload := map[string]any{"step": output.step, "status": "failed", "error": err.Error(), "code": code}
		if remote != nil && remote.Partial {
			payload["apply_id"] = remote.ApplyID
			payload["completed"] = remote.Completed
			payload["partial"] = true
			payload["next"] = "review a new plan and rerun apply"
		}
		if report(output.stdout, true, payload) != 0 {
			return 1
		}
	} else {
		fmt.Fprintln(stderr, err)
		if remote != nil && remote.Partial {
			fmt.Fprintf(stderr, "Apply %s completed %d writes before stopping. Review a new plan and rerun apply.\n", remote.ApplyID, len(remote.Completed))
		}
	}

	return exitCode
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
  setup      sign in or create an account, plan and apply in one process
  agent-help print the coding-agent workspace mapping guide
  login      sign in or create an account and show the current account
  logout     remove the saved short-lived CLI access token
  init       write a starting oryxa.yaml to stdout or -o
  validate   validate a workspace file without contacting Oryxa
  plan       show changes against the current organization
  apply      make the planned changes, then report any pending joins
  diff       alias of plan
  pull       write live organization settings as YAML
  mappings  show external directory IDs and their Oryxa bindings
  status     check the applied workspace for drift and pending steps
  doctor     show status and the next corrective command
  rollback  restore a saved workspace version with --to VERSION
  whoami     show the authenticated user and organizations
  connect-app TOOLKIT  connect your own Composio app through its browser page

Remote commands reuse a private, short-lived access token cache, then prompt for
browser sign-in when it expires. Use login --force-login to switch accounts or
logout to remove the cached token. Over SSH or on a machine without a browser,
use login --device: it prints a URL and a short code to approve in any browser
where you are signed in to Oryxa. It is chosen automatically when SSH_CONNECTION
is set or no browser can be opened. Use -f for
the input file, --org for an organization
slug, --api for the API URL, --json for machine-readable output, and --dry-run
to plan an apply. Unsupported sections are rejected before any write.
`)
}

func planningState(ctx context.Context, client *runtime.Client, slug string) (runtime.Snapshot, error) {
	organization, err := client.OrganizationBySlug(ctx, slug)
	if errors.Is(err, runtime.ErrOrganizationNotFound) {
		return client.Snapshot(ctx, slug)
	}
	if err != nil {
		return runtime.Snapshot{}, err
	}
	return runtime.Snapshot{Organization: &organization}, nil
}
