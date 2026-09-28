# Workspace CLI: first implementation

This implements the organization, people, teams, and organization-agent slice
of [the onboarding design](design/onboarding.md). Slack installation is a
manual prerequisite. The remaining sections in the design still need runtime
owners and are rejected before an apply makes any change.

Build the standalone command from the repository root:

```sh
mkdir -p bin
go build -o bin/oryxa ./cmd/oryxa-cli
```

Create a starting file and inspect it:

```sh
./bin/oryxa init --org acme --domains acme.com -o oryxa.yaml
./bin/oryxa validate -f oryxa.yaml
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

The parser refuses unknown keys, duplicate members or teams, unrecognized
roles, and unsupported sections. Omitting a person or team never removes it
unless its section sets `prune: true`. The normal compact `teams: [...]` form
is still supported.
Owner role transfers are not yet supported. `preset` is accepted in the file
so the file shape matches the design; its consent, retention, and security
defaults do not take effect yet. Organization timezone is stored but the
scheduler does not yet read it. Directory sync, org chart, governance, safety,
tool allowlists, external search, notifications, retention, security policy,
ownership hints, and personal-agent defaults remain unimplemented. A local
`go test ./...` run exercises the code paths, but PostgreSQL migrations and a
live deployment still need integration verification.
