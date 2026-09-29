# Workspace CLI: first implementation

This implements the organization, people, teams, ownership, org chart,
organization-agent, tool offer, native search mode, and selected operational policy settings from
[the onboarding design](design/onboarding.md).
Slack installation is a manual prerequisite. Unsupported settings in the
design are rejected before an apply makes any change.

Build the standalone command from the repository root:

```sh
mkdir -p bin
go build -o bin/oryxa ./cmd/oryxa-cli
```

Create a starting file and inspect it:

```sh
./bin/oryxa init --org acme --domains acme.com -o oryxa.yaml
./bin/oryxa validate -f oryxa.yaml
./bin/oryxa setup --agent-help        # mapping instructions for your coding agent
```

After filling in members and teams, run the one-process setup flow. It opens
Cognito in a browser, receives the redirect on `127.0.0.1`, shows the plan,
and asks before applying. The access token stays in that process's memory.
The server must have `COGNITO_CLI_CLIENT_ID` and its fixed callback URL from
the Cognito provisioner. `--no-browser` prints the URL for a coding agent to
open; `--json` prints machine-readable progress.

```sh
./bin/oryxa setup -f oryxa.yaml
```

If the file is absent, `setup --org acme` writes a starter file before
planning. It also accepts `--preset` and `--domains` for that first file.

Separate remote commands open browser sign-in when they need a session. They
can also read a short-lived member access token from `ORYXA_ACCESS_TOKEN`.
No token is accepted in the file or as a flag, and the CLI does not save it.
`oryxa login` checks sign-in and shows the current account; its in-memory
session ends with that command. For one sign-in and several setup steps, use
`oryxa setup`. A reusable cross-command session needs a future in-memory
daemon or a different security design.

```sh
./bin/oryxa plan -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json  # resume after invited people join
./bin/oryxa status --org acme --json
./bin/oryxa doctor --org acme --json
./bin/oryxa pull --org acme -o current-oryxa.yaml
./bin/oryxa rollback --org acme --to 1
```

`--api` defaults to `https://api.oryxa.in`; local development may point it
at `http://localhost:8080`. `init` and `pull` write YAML to stdout unless
`-o` is supplied. They refuse to overwrite an existing file without `--yes`.
`plan`, `apply`, and `diff` read `oryxa.yaml` by default. `--dry-run` on apply
prints the plan. JSON output on plan and apply contains `changes` and
`needs_human`; apply exits `2` when a person must accept an invitation before
their team membership can be added. Applied files and actor IDs are versioned
in PostgreSQL by the workspace service. Existing organizations use a
per-organization apply lock; a concurrent apply returns exit code `5`.
`rollback` applies a saved version as a new version after checking its removal
plan. The all-in-one host serves the workspace API. Split deployments must set
`WORKSPACE_API_URL` to the gateway carrying auth, teams, agents, and workspace
routes.

The current file accepts these settings:

| Setting | Effect |
| --- | --- |
| `metadata.organization` | Organization slug; creates the organization if missing |
| `metadata.domains` | Stored in auth and enforced for new invitations and invitation acceptance |
| `organization.name` | Organization display name |
| `organization.timezone` | Stored IANA timezone for the organization |
| `people.members[].email`, `.role`, `.guest` | Invite members and change existing non-owner roles; outside-domain invitees need `guest: true` |
| `people.guests` | Allow or refuse new guest invitations |
| `people.invite: email` | Uses the invitation email flow |
| `people.invite: quiet` | Creates invitations without sending email; members can find and accept them after signing in |
| `people.prune: true` | Removes omitted members and pending invitations after an explicit removal plan and confirmation; the signed-in member and final owner are protected |
| `teams[].name`, `.members`, `.leads` | Create teams, add joined members, and grant team-admin rights to leads |
| `teams: {items: [...], prune: true}` | Remove omitted teams and memberships after listing every removal for confirmation |
| `agents.organization.enabled`, `.displayName`, `.description`, `.instructions` | Reconcile the existing organization agent; `plan` reads effective defaults without creating it |
| `ownership[]` | Give agents explicit owner hints for a topic, system, or channel; only joined members appear in the agent's read view |
| `orgChart.relationships`, `.authority`, `.executives` | Supply explicit reporting and authority relationships for agents to query |
| `ownership: {items: [...], prune: true}`, `orgChart.prune: true` | Remove omitted owner hints or chart facts after a reviewed removal plan |
| `rules.consent.expireAfter` | Sets the default and maximum lifetime of cross-member requests (`5m` through `7d`) |
| `safety.guard.holdForReview` | Holds every cross-member answer for the addressed member to review; `regulated` defaults this to `true` |
| `agents.personal.proactive: on\|off` | Sets the organization default for app update wake-ups; a member's explicit preference takes precedence |
| `tools.provider: composio` | Declares the existing Composio provider |
| `tools.offer` | Allows only the listed toolkit slugs to connect or act in this organization; omitting it allows all, and `[]` allows none. Existing connections stay recorded but cannot run, read, subscribe, deliver events, or refresh knowledge sources while disallowed. Previously indexed documents remain until their normal removal lifecycle |
| `tools.autoSubscribe` | Sets the default for new app connections; a member's per-connection choice takes precedence, and deployment-wide `APPS_AUTO_SUBSCRIBE=false` still disables automatic subscriptions |
| `search.mode: native\|none` | Enables the existing RAG knowledge toolbox or prevents organization agents from using its search and source tools; omission keeps existing behavior |

The parser refuses unknown keys, duplicate members or teams, unrecognized
roles, and unsupported sections. Omitting a person or team never removes it
unless its section sets `prune: true`. The normal compact `teams: [...]` form
is still supported.
Owner role transfers are not yet supported. `preset` is accepted in the file;
its safety hold-for-review default takes effect for `regulated`, while its
other consent, retention, and security defaults do not. Organization timezone
is stored but the scheduler does not yet read it. The agent and apps services
read applied workspace policies when `WORKSPACE_URL` is set; the all-in-one
host supplies that URL automatically. Split deployments must share the
workspace PostgreSQL schema for these reads. Directory sync, the remaining
governance and safety controls, external search providers, notifications, retention,
security policy, other tool settings, and other personal-agent defaults remain
unimplemented. A local
`go test ./...` run exercises the code paths, but PostgreSQL migrations and a
live deployment still need integration verification.
