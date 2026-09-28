# Onboarding: the setup CLI and the workspace file

*Draft, 2026-09-28. The owner's priority: onboarding first, then agents. This
document is the complete package the setup CLI implements: what it does, the
one file it applies, every setting from the smallest preference to the
largest structural choice, the API behind it, and the rules it keeps.*

*Settled: Slack goes through one Oryxa app in the Slack Marketplace
([slack-consent-loop.md](slack-consent-loop.md)). **Composio stays exactly as
it is**: tools and app connections keep going through the apps service and
Composio as today, and Oryxa's own sign-in (integrations phase 1) stays
switched off. The org chart exists, in a shape the owner defines.*

*Anything marked **(verify)** is believed but not yet checked against the
provider's current documentation. Every setting's status is marked **Today**
(exists), **New** (to build) or **Later** (planned, not in v1).*

---

## 1. Why this comes first

A company adopts Oryxa only if it can set it up without a week of admin work.
Today that work is manual and scattered: installing the Slack app, adding
people and teams, telling Oryxa who owns what, choosing rules, connecting
search. The setup CLI does all of it in minutes, and it is built so that the
company's own coding agent (Claude Code, Cursor, Windsurf) can run it. The
agent already knows a lot about the company and hands it over ("we use Glean,
here is its MCP address; engineering is these people; incidents live in
#inc"), so the admin answers only what nobody could infer.

Onboarding is not a one-off. The same file and the same commands carry every
later change, from renaming an agent to restructuring teams.

## 2. Principles

1. **Agents first, people welcome.** Every step runs non-interactively from
   flags or the file, prints machine-readable results with `--json`, can be
   re-run safely and resumed. When a person is needed, the CLI says exactly
   what they must do and stops with a distinct exit code.
2. **One file describes the workspace.** `oryxa.yaml`. `plan` shows every
   change it would make; `apply` makes them; applying the same file twice
   changes nothing; `pull` writes the current workspace back into a file.
3. **Small to large, with defaults all the way down.** A one-line file (a
   preset) produces a working workspace. Every setting has a default; the
   file only states what differs.
4. **Nothing is deleted by omission.** Leaving something out of the file never
   removes it. Removal needs `prune: true` on that section, and `plan` shows
   every removal before it happens.
5. **Secrets never touch files, flags, logs or output.** They go in through the
   browser or a hidden prompt, straight into Oryxa's encrypted vault. The file
   refers to them by name. The CLI refuses a secret passed as a flag and never
   prints one, `--json` included.
6. **Short-lived and least-privileged.** The CLI holds one short-lived session
   in memory, writes no token to disk, asks every provider for the least it
   needs, and deletes itself when done unless told to stay.
7. **People only where providers insist.** Approving the Slack install and
   granting directory consent need an admin's click. The CLI opens the exact
   page, waits, and verifies the result.

## 3. Who runs it

| Mode | How | What it is like |
| --- | --- | --- |
| **A coding agent** | The admin tells their agent "set Oryxa up for us". The agent reads the instructions page (§8), gathers facts, writes `oryxa.yaml`, runs `plan`, shows the admin, then `apply` | The fastest path. The admin answers a handful of questions and clicks twice |
| **A person at a terminal** | `oryxa setup` asks for anything missing, one question at a time, with a default ready | About a minute on a preset |
| **CI** *(Later)* | `oryxa plan` on pull requests and `oryxa apply` on merge, with a service token | Configuration as code for companies that want review on every change |

## 4. A first run, end to end

The admin, in their editor: *"Set up Oryxa for Acme. We use Slack and Glean.
Engineering is Raya, Maya and Sam; sales is Bob and Carol. #incident-war-room
is our incident channel. Raya owns the dev keys."*

```text
agent ▶ curl -fsSL https://get.oryxa.in/setup.sh | sh     # verify + install to a temp dir
agent ▶ oryxa login --json                                # browser opens → admin signs in
        {"step":"login","status":"done","organization":"acme","role":"owner"}
agent ▶ oryxa init --preset team --json > oryxa.yaml      # starting file from what Oryxa knows
agent ✎ edits oryxa.yaml: teams, people, #incident-war-room, ownership, Glean
agent ▶ oryxa plan -f oryxa.yaml --json
        {"changes":[{"op":"create","kind":"team","name":"Engineering"}, …],
         "needs_human":[{"step":"slack.install","why":"approve Add to Slack"},
                        {"step":"search.glean.auth","why":"sign in to Glean"}],
         "missing_secrets":[]}
agent ▷ shows the plan to the admin → "apply?" → yes
agent ▶ oryxa apply -f oryxa.yaml --json
        {"step":"slack.install","status":"needs_human","next":{"action":"open_url","url":"https://slack.com/oauth/v2/authorize?…"}}
admin   clicks Allow in Slack
agent ▶ oryxa apply -f oryxa.yaml --json                 # resumes where it stopped
        … "search.glean.auth" needs_human → admin signs in to Glean → resume …
        {"step":"done","status":"done","summary":"https://app.oryxa.in/acme/settings/setup"}
agent ▶ oryxa status --json                              # every check green
agent ▶ oryxa purge                                      # the CLI deletes itself
```

Two clicks and one question for the admin.

## 5. The CLI

### Getting it

```bash
curl -fsSL https://get.oryxa.in/setup.sh | sh
```

The script detects the OS and architecture, downloads the static binary for a
pinned version, verifies its SHA-256 checksum and signature, and runs it from
a temporary directory, with no `sudo`. Alternatives: `brew install
oryxa/tap/oryxa`, or a direct download with its published checksum and
signature. `ORYXA_VERSION=v1.4.2` pins a version with any of them.

### Commands

| Command | What it does |
| --- | --- |
| `oryxa setup` | The whole flow (login, init, plan, apply, status), resuming where a previous run stopped |
| `oryxa login` | Browser sign-in to Oryxa; picks or creates the organization. `whoami` shows who is signed in |
| `oryxa init [--preset P]` | Writes a starting `oryxa.yaml` from the preset and from what Oryxa already knows about the organization |
| `oryxa plan -f F` | Validates the file and lists every create, update and removal, the secrets still missing, and the steps waiting on a person |
| `oryxa apply -f F` | Applies the file. Asks for missing secrets through the browser or a hidden prompt, and runs provider steps |
| `oryxa pull [-o F]` | Writes the workspace's current settings to a file, including changes made in the web app |
| `oryxa diff -f F` | `plan` without validating provider steps: a quick drift check |
| `oryxa connect <provider>` | One provider step on its own: `slack`, `google`, `microsoft`, `glean`, `mcp` (any MCP search) |
| `oryxa secret set <name>` | One secret at a hidden prompt or from stdin, straight into the vault |
| `oryxa status` | Checks every piece is working (§13) |
| `oryxa doctor` | `status` plus the fix for each failure |
| `oryxa logs [--since 1h]` | Recent setup, provisioning and audit events |
| `oryxa rollback --to V` | Applies a previous version of the workspace file |
| `oryxa purge` | Deletes the CLI and its temporary state |

Flags on every command: `--json`, `--no-input` (never prompt; fail with
`needs_input` instead), `--yes` (no confirmations), `--dry-run`, `--org`,
`--no-browser` (print URLs instead of opening them), `--api` (default
`https://api.oryxa.in`), `--verbose`.

### The output contract

With `--json`, every step prints one line:

```json
{"step": "people.import", "status": "done|skipped|needs_human|needs_input|failed",
 "detail": "Added 14 members, 2 already present",
 "changes": 14,
 "next": {"action": "open_url|run|answer", "url": "…", "command": "…",
          "question": {"key": "rules.consent.timedAccess", "choices": ["30m","1h","4h"], "default": "1h"}}}
```

Exit codes: `0` done · `2` waiting on a person (`needs_human`) · `3` the file
is invalid, or input is missing under `--no-input` · `4` a provider refused ·
`5` someone else is applying to this organization · `1` anything else.

### State, resume and safety

- Every step is idempotent and has a key; re-running skips steps already done
  with the same input.
- State lives on the server, per organization, so a run can resume from
  another machine or another agent session.
- An apply takes a per-organization lock; a second apply waits or exits `5`.
- An apply that removes anything (`prune`) asks for confirmation unless
  `--yes`, and `--no-input` refuses it.
- The CLI never deletes members' conversations, agent memories, knowledge
  content or audit history; those have their own controls in the web app.

## 6. The workspace file

### Shape

```yaml
apiVersion: oryxa.in/v1
kind: Workspace
metadata:  {organization, domains}
preset:    startup | team | regulated        # defaults for everything below
organization: {…}        # §7.A basics
people:       {…}        # §7.B members, roles, invitations
teams:        [...]      # §7.C teams and departments
directory:    {…}        # §7.D linking Google Workspace or Microsoft 365
orgChart:     {…}        # §7.E the organization's own shape
chat:         {…}        # §7.F Slack (Teams later)
agents:       {…}        # §7.G the organization agent and personal agents
rules:        {…}        # §7.H consent and governance
safety:       {…}        # §7.I screen and answer guard
tools:        {…}        # §7.J Composio, as today
search:       {…}        # §7.K external search and knowledge
notifications: {…}       # §7.L
data:         {…}        # §7.M retention and privacy
security:     {…}        # §7.N
ownership:    [...]      # §7.O who owns what
secrets:      [...]      # names only
```

### Presets

| Preset | For | What it sets |
| --- | --- | --- |
| `startup` | Up to ~30 people, one team | Consent across departments off; skipping approval for owners' managers on; quiet invitations; short retention |
| `team` *(default)* | Several teams, one company | Consent across departments on; one hour of timed access; consent remembered 30 days per channel; the owner always told |
| `regulated` | Finance, health, anything audited | Consent everywhere; no skipped approvals except marked channels; answers held until the owner reviews them; long audit retention; MFA required |

### Three sizes of the same file

**The smallest**, a preset only:

```yaml
apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: acme, domains: [acme.com]}
preset: team
chat: {slack: {install: add_to_slack}}
```

**A typical one**, the file the agent wrote in §4:

```yaml
apiVersion: oryxa.in/v1
kind: Workspace
metadata: {organization: acme, domains: [acme.com]}
preset: team
people:
  invite: quiet
  members:
    - {email: raya@acme.com, role: admin}
    - {email: maya@acme.com}
    - {email: sam@acme.com}
    - {email: bob@acme.com}
    - {email: carol@acme.com}
teams:
  - {name: Engineering, members: [raya@acme.com, maya@acme.com, sam@acme.com]}
  - {name: Sales, members: [bob@acme.com, carol@acme.com]}
chat:
  slack:
    install: add_to_slack
    highPriorityChannels: ["#incident-war-room"]
search:
  mode: external
  external: {provider: glean, mcp: {url: https://acme-be.glean.com/mcp/default, auth: oauth, identity: as_member}}
ownership:
  - {owner: raya@acme.com, what: dev keys, tags: [secrets, engineering]}
```

**The full reference** is §7, section by section, and `oryxa pull`'s output,
which always writes every setting with its current value.

### Rules of the file

- Unknown keys are errors, never ignored.
- References must resolve: teams, members and channels. Emails must be in the
  organization's domains unless the member is marked `guest: true`.
- A secret value anywhere is refused; secrets are named in `secrets:` and
  referenced by name.
- Removals need `prune: true` on the section. Lists without it are "ensure
  these exist".
- Flags override the file for that run only. The applied file is stored and
  versioned; the web app shows the current version, its history and who
  applied it.
- The web app edits the same settings. `plan` marks such drift; `pull` brings
  it back into the file.

## 7. The settings catalog

Every setting the CLI can set, grouped, from small to large. *Owner* is the
service that stores it.

### A. Organization basics

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `organization.name` | Display name | text | Today | auth |
| `metadata.organization` | The slug, used in URLs | text | Today | auth |
| `metadata.domains` | Email domains that count as members | list | New | auth |
| `organization.timezone` | Default for schedules, digests and quiet hours | IANA zone (from the admin) | New | auth |
| `organization.language` | Default language of agents' replies | BCP 47 (`en`) | New | agents |
| `organization.workingHours` | Default working days and hours | `Mon-Fri 09:00-18:00` | New | agents |
| `organization.logo` | Shown in the web app and Slack messages | a file path, uploaded | New | files |

### B. People, roles and invitations

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `people.members[]` | Members by email | `{email, name?, role?, guest?}` | Today | auth |
| `people.members[].role` | What they may administer | `owner`, `admin`, `member` (`member`) | Today | auth |
| `people.invite` | How new members are told | `email`, `quiet` (`email`; `quiet` for directory imports) | New for `quiet` | auth |
| `people.guests` | Whether people outside the domains may join | `false` | New | auth |
| `people.prune` | Remove members missing from the file | `false` | New | auth |

### C. Teams and departments

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `teams[].name` | A team | text | Today | teams |
| `teams[].members` | Its members | emails, or `{fromGroup: …}` | Today (emails); New (groups) | teams |
| `teams[].leads` | Who leads it; a hint for approvals and escalation | emails | New | teams |
| `teams[].department` | The department it belongs to, used by consent rules | text (the team's name) | New | teams |
| `teams.prune` | Remove teams missing from the file | `false` | New | teams |

### D. Directory and identity

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `directory.provider` | Links the company directory | `google_workspace`, `microsoft_365`, `none` (`none`) | New | auth |
| `directory.import.groups` | Which groups or units become members and teams | list (none) | New | auth |
| `directory.import.departmentFrom`, `managerFrom` | Which directory fields to read | attribute names | New | auth |
| `directory.sync` | Keep members in step with the directory | `off`, `daily` (`daily` once linked) | New | auth |
| `people.identities[]` | Link members' Slack, Teams and GitHub accounts where emails differ | `{member, slack?, github?, teams?}` | New | agents (`slackrouter`) |

Identities are matched by normalized work email automatically; overrides are
only for exceptions.

### E. Org chart and authority

The organization defines its own chart; it is not limited to reporting lines.
Oryxa stores the relationships as given and asks one question of them: *is
this person above that one?*

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `orgChart.relationships[]` | Any relationships the organization uses | `{from, relation, to}` | New | auth |
| `orgChart.authority` | Which relations count as "above", for skipping approval | the owner's definition | New | agents (`Authority`) |
| `orgChart.executives` | People who rank above everyone | emails | New | auth |

### F. Chat surfaces

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `chat.slack.install` | Installs Oryxa's Slack app in the workspace | `add_to_slack` | New | apps |
| `chat.slack.highPriorityChannels` | Private channels whose questions skip the owner's approval | channel names | New | apps |
| `chat.slack.answerIn` | Where public answers appear | `thread` | New | agents |
| `chat.slack.acknowledge` | How the asker learns their question was taken | `ephemeral`, `reaction` (`ephemeral`) | New | agents |
| `chat.slack.nonMembers` | When someone who isn't a member asks | `explain`, `ignore` (`explain`) | New | agents |
| `chat.slack.maxOpenAsksPerPerson` | Limits floods | number (5) | New | agents |
| `chat.teams.*` | Microsoft Teams | the same shape | Later | apps |

### G. Agents

**The organization agent** (the one that answers in channels)

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `agents.organization.enabled` | Whether it answers in channels | `true` | New | agents |
| `agents.organization.displayName` | How it signs its messages | text (`Oryxa`) | New | agents |
| `agents.organization.answerFromOrgMaterial` | Answer from organization-wide material without asking anyone | `true` | New | agents |

**Personal agents** (each member's own)

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `agents.personal.naming` | How each member's agent gets its name | `generated`, `member_chooses`, `fixed` (`member_chooses`, with a generated suggestion) | New | agents |
| `agents.personal.style` | Default reply style | `concise`, `detailed` (`concise`) | New | agents |
| `agents.personal.language` | Default language | the organization's | New | agents |
| `agents.personal.proactive` | Whether it tells the member about things arriving from their apps | `on`, `digest`, `off` (`on`) | Today (on) | agents |
| `agents.personal.quietHours` | No pings outside these; things queue for the morning | the working hours | New | agents |
| `agents.personal.digest` | A summary of what happened while quiet | `off`, `morning`, `evening` (`morning`) | New | agents |
| `agents.personal.thinking` | Speed against depth of reasoning | `low`, `medium`, `high` (`low`) | Today (deployment-wide) | agents |

**Delegation and standing permissions**

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `agents.delegation.specialistsAutomatic` | Whether specialists may accept work without their owner | `false` | Today (per specialist) | agents |
| `agents.standingPermissions.allow` | Actions members may pre-approve for their agent | list of tool kinds (reads only) | New | agents |
| `agents.standingPermissions.maxDuration` | Longest a standing permission may last | duration (`30d`) | New | agents |
| `agents.backgroundAccess` | Whether members are asked to let their agent work while they are away | `ask`, `off` (`ask`) | Today | auth |

### H. Consent and governance

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `rules.consent.buttons` | The owner's three choices | fixed: summary, timed access, deny | New | agents |
| `rules.consent.timedAccess` | How long "Give access" lasts | `30m`, `1h`, `4h` (`1h`) | New | agents |
| `rules.consent.rememberPerChannel` | How long an approval covers the same question in the same channel | duration (`30d`) | New | agents |
| `rules.consent.crossDepartment` | Crossing departments needs the owner's consent | `required`, `off` (`required`) | New | agents |
| `rules.consent.reminderAfter` | Remind an owner who hasn't answered | duration (`15m`) | New | agents |
| `rules.consent.expireAfter` | Give up and tell the asker | duration (`24h`) | New | agents |
| `rules.skipApproval.highPriorityChannels` | Marked channels skip approval | `true` | New | agents |
| `rules.skipApproval.higherUp` | People above the owner skip approval | `true` | New | agents |
| `rules.skipApproval.tellOwner` | The owner is told afterwards | `always` | New | agents |
| `rules.secrets.postToSlack` | Secrets in Slack | `never` (fixed) | New | agents |
| `rules.secrets.viewExpires` | How long a one-time secret view lasts | `15m` | New | agents |
| `rules.takeover` | Escalating to a manager when an owner is away | `off` | Later | agents |
| `rules.signOff` | Approval by N of M people | `off` | Later | agents |

### I. Safety

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `safety.screen.strictness` | How hard the request screen checks need and plausibility before anyone is pinged | `standard`, `strict` (`standard`) | New | agents |
| `safety.guard.bannedTopics` | Topics never let out, whoever approves | list of short descriptions | New | agents (Bedrock Guardrails) |
| `safety.guard.pii` | Personal data in answers | `mask` | Today (Guardrails) | agents |
| `safety.guard.grounding` | Answers must match their approved source | `hold` (for the owner's review), `block` (`hold`) | New | agents |
| `safety.guard.holdForReview` | Hold every cross-member answer for the owner | `false` (`true` in `regulated`) | Today (for held answers) | agents |

### J. Tools, through Composio as today

Tools and app connections keep going through the apps service and Composio,
unchanged. Onboarding only records the organization's choices and nudges
members to connect.

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `tools.provider` | Where tools come from | `composio` (fixed for now) | Today | apps |
| `tools.offer` | Which apps the organization offers members | toolkit slugs (all) | New (an organization allowlist) | apps |
| `tools.recommend` | Apps suggested per team | `{team: [toolkits]}` | New | apps |
| `tools.autoSubscribe` | New connections receive every event they can deliver | `true` | Today (deployment-wide) | apps |
| `tools.wakeUpWithin` | How soon agents react to app events | duration (`1h`) | Today (deployment-wide) | batching |
| `tools.personal` | Whether members may connect personal Jira and GitHub in chat | `{github: true, jira: true}` | New | apps |
| `tools.nudgeMembers` | Invite members to connect their apps after onboarding | `true` | New | apps |

### K. Search and knowledge

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `search.mode` | Where agents search | `external`, `native`, `none` (`none`) | New | agents |
| `search.external.provider` | Which search | `glean`, `mcp` (any MCP search server) | New | agents |
| `search.external.mcp.url` | The search's MCP server | URL | New | agents |
| `search.external.mcp.auth` | How to sign in to it | `oauth`, or `{token: secret-name}` | New | apps (vault) |
| `search.external.identity` | Search as the asking member, or as a service | `as_member` | New | agents |
| `search.native` | Oryxa's own knowledge bases | exist; new work is on hold | Today | rag, knowledge |

### L. Notifications

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `notifications.approvals` | Where owners get consent requests | `slack`, `web`, `both` (`both`) | New | agents |
| `notifications.results` | Where askers hear back | `slack_thread`, `agent_chat` (both) | New | agents |
| `notifications.email` | Email for invitations and summaries | `on` | Today (invitations) | email |

### M. Data and privacy

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `data.region` | Where data lives | `us-west-2` (fixed for now) | Today | all |
| `data.slackText` | How long Slack message text is kept | `7d` | New | apps |
| `data.auditRetention` | How long audit history is kept | `365d` (`7y` in `regulated`) | New | all |
| `data.export` | Organization data export | on request | Later | all |

### N. Security

| Key | What it does | Values (default) | Status | Owner |
| --- | --- | --- | --- | --- |
| `security.mfa` | Multi-factor sign-in | `optional`, `required` (`optional`; `required` in `regulated`) | New (off today) | auth |
| `security.allowedDomains` | Sign-ups only from these domains | `metadata.domains` | New | auth |
| `security.sessionLength` | How long a sign-in lasts | duration (`12h`) | New | auth |
| `security.sso` | Signing in through the company's identity provider | `none` | Later | auth |

### O. Ownership hints

These tell the organization agent who owns what, while connectors and
knowledge bases are on hold.

| Key | What it does | Values | Status | Owner |
| --- | --- | --- | --- | --- |
| `ownership[]` | An owner for a topic, a channel or a system | `{owner, what?, channel?, system?, tags?}` | New | agents |

## 8. Instructions for coding agents

Published at `get.oryxa.in/agents.md` and printed by `oryxa setup
--agent-help`. A coding agent follows it.

```markdown
# Setting up Oryxa for your organization

You are setting up Oryxa, an operator that answers questions across the
company by asking the people who own the answers, and that works for each
member. You will produce one file, `oryxa.yaml`, and apply it with the
`oryxa` CLI.

1. Install and sign in: `curl -fsSL https://get.oryxa.in/setup.sh | sh`,
   then `oryxa login --json`. The human signs in in the browser.
2. Start from what Oryxa knows: `oryxa init --preset team --json > oryxa.yaml`.
3. Fill in what you know about the company from the repository, docs and the
   human. Prefer facts you can see over guesses: people and teams (emails),
   the chat app (Slack) and its high-priority private channels, the search
   they already use (for example Glean, and its MCP address), and who owns
   what (systems, topics, channels).
4. Ask the human only what you cannot infer, one question at a time, with the
   default ready.
5. `oryxa plan -f oryxa.yaml --json`. Show the human every change, especially
   removals, and wait for a yes.
6. `oryxa apply -f oryxa.yaml --json`. On `needs_human`, tell the human exactly
   what to do (open the URL, click Allow), then run apply again.
7. `oryxa status --json`, report the result, then `oryxa purge`.

Never:
- put a secret, token or password in the file, a flag, or your chat. Use
  `oryxa secret set <name>` or the browser and let the human type it;
- use `--yes` with `prune`;
- change settings the human did not ask for.
```

## 9. Provider steps

**Slack (v1).** One Oryxa app, distributed through the Slack Marketplace.
`oryxa connect slack` opens "Add to Slack"; a workspace admin approves; the
bot token goes to the vault, linked to the organization through the
workspace's `team_id`. Scopes: `app_mentions:read`, `chat:write`,
`im:write`, `users:read`, `users:read.email`, `channels:read`, `groups:read`;
events: `app_mention`, `app_uninstalled`, `tokens_revoked`. Every workspace
sends its events and clicks to one Oryxa endpoint. Until the Marketplace
listing is live, installs go through Oryxa's install link as a labelled beta;
the listing needs 10 active workspaces and a review of up to about 12 weeks.

**Google Workspace (v1.1).** The admin consents in the browser to read-only
access to the directory (users, groups, and their departments and managers).
Creating an OAuth client through an API is not generally available (verify),
so v1.1 uses a guided step: the CLI opens the exact console page and the admin
pastes the client ID and secret with `oryxa secret set`.

**Microsoft 365 (v1.1).** Registering the app and granting admin consent for
reading the directory can be scripted with the admin's own `az` login.
(verify)

**Glean, or any MCP search (v1.1).** `oryxa connect glean` registers the MCP
address; the admin signs in with OAuth in the browser or gives a token with
`oryxa secret set`; the CLI runs one test search as the admin. Searches then
run as the asking member where the search supports it.

**Composio (as today).** Nothing to install per organization. Members connect
their apps in Tools, as now; onboarding only sends the nudge and records which
apps are offered.

## 10. The API

Additions to Oryxa's API. Every call is organization-scoped and needs an
admin's session; every change is audited and published as an event.

| Call | Purpose |
| --- | --- |
| `POST …/workspace:validate` | Check a file; errors by path, never by value |
| `POST …/workspace:plan` | Changes, removals, drift, missing secrets and steps waiting on a person |
| `PUT …/workspace` | Apply a file, idempotently; slow steps run as operations |
| `GET …/workspace?format=yaml` | The current settings as a file (`pull`) |
| `GET …/workspace/versions`, `POST …/workspace:rollback` | History and rollback |
| `GET …/operations/{id}` | Progress of a slow step |
| `GET …/workspace/status` | Health checks (`doctor` adds fixes) |
| `PUT …/secrets/{name}` | Write-only; nothing ever reads a secret back out |
| `GET …/providers/slack/install` | The "Add to Slack" link with a single-use state |
| `POST …/providers/{google\|microsoft}/link` | Link the directory |
| `POST …/search/verify` | Test the external search |

The CLI signs in through a public OAuth client for native apps: PKCE with a
loopback redirect on `127.0.0.1`.

## 11. Security

- The install script: HTTPS only, a pinned version, a verified checksum and
  signature, no `sudo`, a temporary directory.
- Sign-in: loopback OAuth with PKCE and state; the session lives in memory
  only.
- Secrets: never in flags, files, logs or JSON output; sealed in the vault;
  write-only through the API.
- Least privilege: only the scopes each step needs; admin-only commands.
- Audit: who applied what, when, from which version to which.
- Purge removes the binary and all temporary state. Kept, the CLI can only
  report status and logs.

## 12. Failure, recovery and drift

- **A step fails:** the apply stops at that step and reports why. Everything
  before it stays applied; a re-run continues from there.
- **A provider is slow:** the step becomes an operation; the CLI follows it,
  and a re-run attaches to it instead of starting again.
- **Two applies at once:** the second waits or exits `5`.
- **A change goes wrong:** `oryxa rollback --to <version>` applies an earlier
  file.
- **Someone changes a setting in the web app:** the next `plan` shows it as
  drift; `pull` takes it into the file, or applying the file overwrites it.

## 13. What `oryxa status` checks

- Signed in, with the organization and role.
- The Slack app is installed: `auth.test` succeeds and a signed test event
  arrives.
- Members' Slack identities are linked (how many, and who isn't).
- The directory is linked and synced, and when it last synced.
- External search answers a test query.
- Composio: which members have connected which apps (a count, as today).
- The rules applied, and the file's current version.

## 14. Building it

**What exists:** auth (organizations, members, roles, invitations), teams,
apps (Composio connections and tools; the encrypted vault and webhook ingress
from integrations phase 1, switched off; Slack's signature checks), agents
(personal agents, specialists with delegation modes, the answer guard,
background access), and the event bus and outbox for audit.

**What's new:** the workspace service (validate, plan, apply, pull, versions,
status), which sits in the one binary and calls auth, teams, apps and agents
rather than duplicating them; the CLI and its release pipeline; the Slack
install and the settings the consent loop needs; directory import; external
search through MCP; and storage for the new settings (org chart, ownership
hints, rules, notifications, data retention).

**Work packages** (no files shared between them):

| WP | Delivers | Owns |
| --- | --- | --- |
| O1 | The workspace file: types, validation, presets, the file format | `internal/workspace/spec/**` (new) |
| O2 | The workspace service: plan, apply, pull, versions, operations, lock | `internal/workspace/service/**` (new), `…/postgres/**`, its migrations |
| O3 | Appliers: calls into auth, teams, apps and agents to make each section so | `internal/workspace/apply/**` (new) |
| O4 | The HTTP API and write-only secrets | `internal/workspace/httpserver/**` (new), the mount in `cmd/oryxa` |
| O5 | The CLI: commands, output, loopback sign-in, resume, purge | `cmd/oryxa-cli/**` (new) |
| O6 | Release: signed builds, checksums, `setup.sh`, the Homebrew tap, `agents.md` | `.github/workflows/cli-release.yml`, `deploy/cli/**` (new) |
| O7 | The Slack install ("Add to Slack", the manifest) | `apps/internal/slackbot/**` (new; shared with the Slack loop's S1, built once) |
| O8 | The web app's Setup page: current file, history, status | `web-next/src/components/workspace-setup-settings.tsx` (new), one entry in `workspace-settings.tsx` |

**Phases.**
- **v1:** the file and presets; login, plan, apply, pull, status, purge;
  people and teams from lists; the Slack install; identities by email;
  high-priority channels; ownership hints; rules and safety defaults; small
  preferences (organization basics, agent naming and style, notifications).
- **v1.1:** directory import (Microsoft, then Google); Glean and other MCP
  search; the org chart; rollback.
- **Later:** CI mode, Microsoft Teams, SSO, takeover and sign-off by N of M,
  data export.

## 15. Decisions

| Decision | Recommended default |
| --- | --- |
| Download domain and signing | `get.oryxa.in`, with signed releases and published checksums |
| The default preset | `team` |
| How members are added from lists | Quiet, with a welcome in Slack from the organization agent; email only for directory imports that aren't in Slack |
| Where the workspace service lives | `internal/workspace`, in the one binary |
| Google directory route (v1.1) | The guided client step, keeping the customer in control, over domain-wide delegation |
| The org chart | Yours to define: the API carries relationships and one question |
| Agent naming at onboarding | Members choose, with a generated suggestion ready |
