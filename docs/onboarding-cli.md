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

After filling in members and teams, run the one-process setup flow. It opens a
local browser page with **Sign in** and **Create account**. Sign in uses the
Cognito hosted page for email/password or Google, then returns an authorization
code to the CLI on `127.0.0.1`. Create account opens the Oryxa web sign-up page
in a new tab; after verifying email, return to the original tab and sign in.
No password is entered in the CLI. The authorization code exchange uses PKCE,
state, and nonce. The CLI saves only the resulting access token in a private
local file scoped to the API URL, reports its expiry, and reuses it for later
commands until shortly before the token expires. It never saves the refresh
or ID token. The provisioned CLI app client issues a 15-minute access token.
The browser sign-in window closes after 20
minutes. Setup creates a missing starter file only after sign-in, then shows
the plan and asks before applying. The auth server must expose
`COGNITO_CLI_CLIENT_ID`, its fixed callback URL, and the web sign-up URL in
`/v1/auth/config`. `--no-browser` prints the local start URL for a coding agent
to open; `--json` prints machine-readable progress.

```sh
./bin/oryxa setup -f oryxa.yaml
```

If the file is absent, `setup --org acme` writes a starter file after sign-in
and before planning. Without `--org`, it asks for the slug. It also accepts
`--preset` and `--domains` for that first file.

Separate remote commands reuse the cached session while it is valid, then open
browser sign-in again. They can also read a short-lived member access token
from `ORYXA_ACCESS_TOKEN`; an environment-provided token is never cached. No
token is accepted in the workspace file or as a flag. `oryxa login` shows the
current account using the cached session, and `oryxa login --force-login`
starts a new browser flow. `oryxa logout` removes the cached access token for
the selected `--api` without changing the browser's Cognito session. The cache
is stored in the user's config directory under `oryxa/cli-sessions` with
private file permissions; set `ORYXA_SESSION_DIR` to choose another private
directory.

```sh
./bin/oryxa plan -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json
./bin/oryxa apply -f oryxa.yaml --json  # resume after invited people join
./bin/oryxa status --org acme --json
./bin/oryxa doctor --org acme --json
./bin/oryxa pull --org acme -o current-oryxa.yaml
./bin/oryxa rollback --org acme --to 1
./bin/oryxa mappings --org acme --json
./bin/oryxa connect-app github --org acme --json
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

`connect-app TOOLKIT --org SLUG` starts the signed-in member's own Composio
connection. It opens the provider's authorization page and waits up to two
minutes for the callback to make the connection active. `--no-browser` or
`--no-input` returns a JSON `needs_human` step with the page URL and connection
ID (exit `2`), so a coding agent can hand that step to the member. Running it
again after the connection is active returns `done` without starting another.
An unfinished pending connection must be completed or removed in app settings
before starting over. This command does not connect another member's account.

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
| `directory.identities[]` | Maps a stable external user ID to a member email with `{source, externalId, member}`; `source` is `provider:tenant`, for example `google_workspace:acme.com` |
| `directory.groups[]` | Maps a stable external group ID to a team with `{source, externalId, team}`; the team must exist or be declared in `teams` |
| `directory.prune: true` | Removes mappings omitted from a reviewed source snapshot; omission keeps prior mappings |
| `teams[].name`, `.members`, `.leads` | Create teams, add joined members, and grant team-admin rights to leads |
| `teams: {items: [...], prune: true}` | Remove omitted teams and memberships; when a kept team lists leads, demote other listed members who were team admins. Every removal or demotion is listed for confirmation |
| `agents.organization.enabled`, `.displayName`, `.description`, `.instructions` | Reconcile the existing organization agent; `plan` reads effective defaults without creating it |
| `ownership[]` | Give agents explicit owner hints for a topic, system, or channel; only joined members appear in the agent's read view |
| `orgChart.relationships`, `.authority`, `.executives` | Supply explicit reporting and authority relationships for agents to query |
| `ownership: {items: [...], prune: true}`, `orgChart.prune: true` | Remove omitted owner hints or chart facts after a reviewed removal plan |
| `rules.consent.expireAfter` | Sets the default and maximum lifetime of cross-member requests (`5m` through `7d`) |
| `safety.guard.holdForReview` | Holds cross-member agent-request answers for the addressed member's review for 24 hours from completion; `regulated` defaults this to `true`. Room posts, teamspace publications, and side-conversation answers use their existing answer guard without this review queue |
| `safety.guard.pii.minimum.owner` | Records the minimum PII masking level for answers the member starts: `secrets` or `strict` |
| `safety.guard.pii.minimum.peer` | Records the minimum PII masking level for answers another member reads: `secrets` or `strict` |
| `safety.guard.relevance` | Records how the relevance check handles answers unrelated to the request: `hold`, `shadow`, or `off` |
| `agents.personal.proactive: on\|off` | Sets the organization default for app update wake-ups; a member's explicit preference takes precedence |
| `tools.provider: composio` | Declares the existing Composio provider; Oryxa's own enabled app integrations use the same organization offer policy |
| `tools.offer` | Enables only the listed toolkit slugs for this organization; omitting it enables all, and `[]` enables none. Existing connections stay recorded but cannot run, read, subscribe, deliver events, or refresh knowledge sources while disallowed. Previously indexed documents remain until their normal removal lifecycle |
| `tools.employeeConnections` | Allows ordinary members to start their own connections (`true` by default); `false` reserves new connections for organization owners and admins. Existing member connections continue to work while their toolkit is offered |
| `tools.autoSubscribe` | Sets the default for new app connections; a member's per-connection choice takes precedence, and deployment-wide `APPS_AUTO_SUBSCRIBE=false` still disables automatic subscriptions |
| `tools.wakeUpWithin` | Sets the maximum cadence used to schedule new digest triggers (`1m` through `90m`); omission uses `BATCHING_MAX_WAIT` (one hour by default). Already scheduled triggers keep their deadlines |
| `search.mode: native\|none` | Enables the existing RAG knowledge toolbox or prevents organization agents from using its search and source tools; omission keeps existing behavior |

The three PII/relevance settings are validated, planned, saved, and projected
for the answer guard. Connecting that projection to the agents service's
`guardpolicy` provider is a separate integration; until then these workspace
overrides do not replace the guard's existing defaults and member preferences.

The parser refuses unknown keys, duplicate members or teams, unrecognized
roles, and unsupported sections. Omitting a person or team never removes it
unless its section sets `prune: true`. The normal compact `teams: [...]` form
is still supported.
Directory snapshots use the same `plan` and `apply` flow as people and teams.
The customer's coding agent exports and maps source data into the file, then
reruns `oryxa apply` for each new snapshot. External IDs are stored in a
normalized mapping table keyed by organization, source, kind, and external ID.
The same file must declare imported people and team memberships in `people`
and `teams`; ID mappings alone do not add them.
Once bound to an Oryxa user or team, a mapping cannot silently move to another
resource. Members who have not joined retain an unbound mapping until a later
apply. Run `oryxa mappings --org SLUG --json` to inspect current bindings.
Provider credentials and direct scheduled Google/Microsoft fetching are not
configured by this snapshot workflow.
Owner role transfers are not yet supported. `preset` is accepted in the file;
its safety hold-for-review default takes effect for `regulated`, while its
other consent, retention, and security defaults do not. Organization timezone
is stored but the scheduler does not yet read it. The agent, apps, knowledge,
and batching services
read applied workspace policies when `WORKSPACE_URL` is set; the all-in-one
host supplies that URL automatically. Split deployments must share the
workspace PostgreSQL schema for these reads. Direct directory provider fetching, the remaining
governance and safety controls, external search providers, notifications, retention,
security policy, other tool settings, and other personal-agent defaults remain
unimplemented. A local integration run against an isolated PostgreSQL database
verified the auth, teams, and workspace migrations and exercised CLI
validate, plan, apply, status, pull, and mappings. It also confirmed an
invitation can be resumed after the member joins and an external ID then binds
to that member. A deployed environment and live provider OAuth still need
verification.
