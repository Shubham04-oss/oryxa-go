# Onboarding: one CLI sets up the whole workspace

Draft, 2026-09-28. Part of [north-star.md](../north-star.md). This is the
package the setup CLI implements: what it does, in what order, the file it
applies, the API it calls, and the rules it keeps. Anything marked **(verify)**
is believed but not yet checked against the provider's current documentation.

## What it is for

Setting a company up by hand is the bottleneck: registering its own Slack app,
linking its directory, adding teams and people, marking channels, telling
Oryxa who owns what. The CLI does all of it in about a minute. It is built to
be run by the company's own coding agent (Claude Code, Cursor, Windsurf) as
well as by a person, and the agent can hand over what it already knows ("we
use Glean, here is its MCP address"), so most questions are never asked.

It is not only for day one. Every later change to the workspace goes through
the same file and the same commands.

## Rules the CLI keeps

1. **Built for agents first.** Every step runs without prompts when given
   flags or a file, prints JSON with `--json`, can be re-run safely, and picks
   up where it stopped. When it needs a person, it says exactly what to do and
   stops with a distinct exit code.
2. **Configuration is a file.** One file, `oryxa.yaml`, describes the
   workspace. `plan` shows what would change; `apply` makes it so; applying
   the same file twice changes nothing.
3. **Secrets never touch files, flags or output.** Tokens and client secrets
   are entered in the browser or at a hidden prompt and go straight into
   Oryxa's encrypted vault. The file names them; it never holds them. The CLI
   refuses secrets passed as flags and never prints one, `--json` included.
4. **Short-lived and minimal.** The CLI holds one short-lived Oryxa session in
   memory, writes no token to disk, asks providers for the least they need,
   and deletes itself when done unless told to stay.
5. **A person where the provider insists.** Some steps need a click by an
   admin (a Slack configuration token, an admin consent screen). The CLI opens
   the exact page, waits, and checks the result.

## Getting the CLI

```bash
curl -fsSL https://get.oryxa.in/setup.sh | sh
```

The script detects the OS and architecture, downloads the CLI for a pinned
version, checks its checksum and signature, runs `oryxa setup`, and leaves
nothing behind unless asked. For teams that do not pipe scripts to a shell:
a package (`brew install oryxa/tap/oryxa`), or a direct download with its
checksum and signature. `ORYXA_VERSION` pins a version.

## Commands

| Command | What it does |
| --- | --- |
| `oryxa setup` | The whole flow below, in order, resuming where it stopped |
| `oryxa login` | Signs the admin in to Oryxa in the browser; creates the organization if it is new |
| `oryxa init` | Writes a starting `oryxa.yaml` from what it can detect: the organization, the linked directory, the Slack workspace |
| `oryxa plan -f oryxa.yaml` | Validates the file and shows every change, the secrets still missing and the steps that need a person |
| `oryxa apply -f oryxa.yaml` | Applies the file, collecting missing secrets and running provider steps |
| `oryxa connect <slack\|google\|microsoft\|glean>` | Runs one provider's linking step on its own |
| `oryxa secret set <name>` | Takes one secret at a hidden prompt or from stdin, straight to the vault |
| `oryxa status` | Checks each piece is working: the Slack bot, its events, the directory, external search |
| `oryxa logs` | Recent setup and provisioning events |
| `oryxa purge` | Deletes the CLI and its temporary state |

Flags on every command: `--json`, `--yes` (no confirmations), `--dry-run`,
`--org`, `--no-browser` (print the URL instead of opening it), `--api`
(default `https://api.oryxa.in`).

**Output for agents.** With `--json`, each step prints one object:

```json
{"step": "slack.install", "status": "needs_human",
 "detail": "A Slack admin must approve the install",
 "next": {"action": "open_url", "url": "https://slack.com/oauth/v2/authorize?...",
          "then": "oryxa apply -f oryxa.yaml"}}
```

`status` is `done`, `skipped`, `needs_human` or `failed`. Exit codes: `0`
done, `2` waiting on a person, `3` the file is invalid, `4` a provider
refused, `1` anything else.

## The flow

v1 is Slack first. Each step is also reachable on its own, and `apply` runs
only the steps the file needs.

**0. Download and verify.** As above.

**1. Sign in to Oryxa.** The CLI starts a listener on `127.0.0.1` and a random
port, opens the browser, and completes an OAuth sign-in with PKCE (RFC 8252).
The admin picks or creates the organization. The session lives in the CLI's
memory for this run only.

**2. Link the directory (optional in v1).** The admin picks Google Workspace
or Microsoft 365 and consents for the organization in the browser. Oryxa keeps
the grant in its vault and reads people, groups, departments and managers as
raw data; how that becomes the org chart is the organization's to define.

- **Microsoft 365:** registering the app and granting admin consent can be
  scripted with the admin's own `az` login. (verify)
- **Google Workspace:** creating an OAuth client through an API is not
  generally available. (verify) One of: a guided step (the CLI opens the exact
  console page and the admin pastes the client ID and secret with
  `oryxa secret set`), or the admin grants Oryxa's service account
  domain-wide delegation by pasting its client ID and scopes into the Admin
  console. Decision below.

**3. Preferences.** Taken from the file or flags, asked only when missing:

- **Search:** use the company's existing search (for example Glean, through its
  MCP server), Oryxa's own knowledge base, or none. Oryxa's knowledge base is on
  hold, so v1 records the choice and uses only external search.
- **Chat:** Slack, Microsoft Teams or both. Teams comes after Slack.
- **Personal accounts:** whether employees may connect their personal Jira and
  GitHub in chat.

**4. Slack.** The CLI creates the organization's own internal Slack app from
Oryxa's app template, installs it, and wires it to Oryxa:

- It creates the app from the template through Slack's App Manifest API,
  which needs an app configuration token that a workspace admin generates
  once at api.slack.com. (verify) The CLI opens that page, and the admin
  enters the token with `oryxa secret set slack-config-token`.
- The template sets the bot's scopes (`app_mentions:read`, `chat:write`,
  `im:write`, `im:history`, `users:read`, `users:read.email`, `channels:read`,
  `groups:read`), its events (`app_mention`, `message.im`) and its
  interactivity. Events and button clicks both point at Oryxa's webhook
  ingress, which answers Slack's URL check itself.
- The app's client ID, client secret and signing secret go straight to the
  vault. (verify: which of these the create call returns)
- The bot is installed through the browser, and its token goes to the vault.
- An internal app skips Slack's Marketplace review and is exempt from its
  2026 limits on apps distributed outside the Marketplace.

**5. External search (optional).** For Glean or any search with an MCP server:
the server's address, how to sign in (OAuth in the browser, or a token entered
with `oryxa secret set`), and whether searches run as the asking member. The
CLI checks it with one test search as the admin.

**6. Teams and members.** Creates teams and adds people, from the linked
directory (by group or unit) or from explicit lists. Whether people receive
an invitation email or are added quietly is a setting.

**7. Mappings.**

- **Identities:** each member's Slack, Teams and GitHub accounts, matched by
  normalized work email, with overrides where emails differ.
- **Departments.**
- **The org chart:** relationships in whatever shape the organization
  defines. Oryxa only asks one question of it: is this person above that one?
- **High-priority channels:** private channels whose questions skip the
  owner's approval.
- **Ownership hints:** "Raya owns the dev keys", "#eng-secrets is Maya's".
  The organization agent uses these to find owners while connectors and
  knowledge bases are on hold.

**8. Rules.**

- Whether crossing departments needs the owner's consent.
- How long consent is remembered per channel.
- How long timed access lasts.
- When approval is skipped: high-priority channels, or someone higher up.
  The owner is always told afterwards.
- Secrets are never posted to Slack. They are shared as a one-time view in
  Oryxa that expires.
- Takeover when an owner is away (later).

**9. Check and finish.** `oryxa status` checks every piece, and the CLI
prints a summary with links into the web app. It then deletes itself (the
default) or, with `--keep`, stays as `oryxa` for `status` and `logs` only.

## The file: `oryxa.yaml`

```yaml
apiVersion: oryxa.in/v1
kind: Workspace
metadata:
  organization: acme              # created if it does not exist
  domain: acme.com

directory:
  provider: google_workspace      # google_workspace | microsoft_365 | none
  import:
    groups: [eng@acme.com, sales@acme.com]
    departmentFrom: department    # the directory attribute to read
    managerFrom: manager

chat:
  slack:
    enabled: true
    workspace: acme.slack.com
    app: {name: Oryxa, botName: oryxa}
  teams:
    enabled: false

search:
  mode: external                  # external | native | none
  external:
    provider: glean
    mcp:
      url: https://acme-be.glean.com/mcp/default
      auth: {type: oauth}         # or {type: token, secret: glean-mcp-token}
      identity: as_member         # searches run as the asking member

accounts:
  personal: {github: true, jira: false}

teams:
  - name: Engineering
    members: {group: eng@acme.com}
  - name: Sales
    members: {emails: [bob@acme.com, carol@acme.com]}
    invite: quiet                 # email | quiet

people:
  identities:                     # only where emails do not line up
    - {member: raya@acme.com, slack: U024BE7LH, github: raya-dev}
  orgChart:                       # the organization's own shape
    relationships:
      - {from: raya@acme.com, relation: reports_to, to: sam@acme.com}
    executives: [ceo@acme.com]
  highPriorityChannels: ["#incident-war-room", "#sec-oncall"]

ownership:
  - {owner: raya@acme.com, what: dev keys, tags: [secrets, engineering]}
  - {owner: maya@acme.com, channel: "#eng-secrets"}

rules:
  consent:
    crossDepartment: required
    rememberPerChannel: 30d
    timedAccess: 1h
  skipApproval:
    highPriorityChannels: true
    higherUp: true
    tellOwner: always
  secrets:
    postToSlack: never
    shareAs: one_time_view
    viewExpires: 15m

secrets:                          # names only; values go to the vault
  - slack-config-token
  - glean-mcp-token               # only with token auth
```

**Validation.** Emails must be in the organization's domain unless listed as
guests. Every name referenced (teams, members, channels) must resolve, and
unknown keys are errors, never ignored. A secret's value anywhere in the file
is refused outright. Errors name the path (`teams[1].members.emails[0]`), never
a value.

**Precedence.** Flags override the file for the run; the file is what gets
applied and stored. The applied file is versioned in Oryxa, and the web app
shows the current one and its history.

## The API the CLI calls

Additions to Oryxa's API. All are organization-scoped, need an admin session,
and every change is audited and published as an event.

| Call | Purpose |
| --- | --- |
| `POST …/workspace:validate` | Check a file: errors by path |
| `POST …/workspace:plan` | The changes a file would make, the secrets still missing and the steps waiting on a person |
| `PUT …/workspace` | Apply a file, idempotently. Slow steps such as a directory import run as operations the CLI follows |
| `GET …/workspace` | The applied file and its versions |
| `GET …/workspace/status` | Health of each piece |
| `PUT …/secrets/{name}` | Write-only. Takes a secret into the vault; nothing ever reads one back out through the API |
| `POST …/providers/slack/app` | Create the Slack app from the template with the configuration token |
| `GET /v1/apps/oauth/callback` | Existing: completes provider sign-ins |
| `POST …/providers/{google\|microsoft}/link` | Link the directory |
| `POST …/search/verify` | Test the external search connection |

The CLI's own sign-in uses a public OAuth client for native apps with PKCE
and a loopback redirect.

## Built on what exists

- **auth:** organizations, members, invites and admin roles, with Cognito for
  sign-in.
- **teams:** teams and membership.
- **apps:** the sign-in vault with KMS-sealed secrets, each organization's own
  OAuth clients, the webhook ingress that already verifies Slack's signatures,
  and the Slack integration's declaration.
- **The event bus and outbox**, for audit events.

**New:**
- the workspace service (validate, plan, apply, status);
- the CLI and its release pipeline (signed builds, the install script);
- Slack app creation through the manifest API;
- directory import for Google and Microsoft;
- the external search registry and its MCP client;
- storage for the org chart, ownership hints and rules.

## Security

- **The install script:** HTTPS only, a pinned version, a verified checksum and
  signature, no `sudo`, and a temporary directory.
- **Sign-in:** loopback OAuth on `127.0.0.1` with PKCE and state. The session
  stays in memory only and ends when the run does.
- **Secrets:** never in flags, files, logs or JSON output; sealed in the vault.
  Agents are told never to paste a secret into their chat, and to use the
  hidden prompt or the browser.
- **Least privilege:** only the scopes each step needs, and admin-only
  commands.
- **Audit:** every change records who made it and when.
- **Purge:** it removes the binary and all temporary state. Kept, it can only
  report status and logs.

## Phases

- **v1, Slack first:**
  - sign-in;
  - the Slack app, created and installed;
  - teams and members from lists;
  - identities by email;
  - high-priority channels, ownership hints and rules;
  - status and purge.
- **v1.1:**
  - directory import (Microsoft, then Google, per the decision below);
  - Glean and other MCP search;
  - the org chart.
- **Later:**
  - Microsoft Teams;
  - Oryxa's own knowledge base as a search mode;
  - applying the file from CI on every change.

## Decisions

| Decision | Recommended default |
| --- | --- |
| Download domain and signing | `get.oryxa.in`, with signed releases and checksums |
| Google directory route | A guided client step for v1.1, keeping the customer in control, over domain-wide delegation |
| Where the workspace service lives | A new `setup` package in the one binary, which calls auth, teams and apps rather than duplicating them |
| Invitations | Quiet for people imported from the directory, email for people added by list |
| The org chart | Yours to define. The API carries relationships and one question: is this person above that one? |
