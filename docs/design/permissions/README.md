# Permissions: relationships, one engine, and tiers

Status: design for #415, 2026-10-23. Docs only; nothing here is built. It asks
the owner to decide four things, listed under [Decisions](#decisions).

Oryxa's rule is that every member has their own agent, and when someone needs
what another person owns, their agent asks that person's agent and the owner
consents. This page proposes one relationship model built from the org tree,
one place that answers "may this actor do this to that", how the consent flow
uses it, and how bigger plans buy more expressive rules without weakening the
base plan.

Paths are from the repository root.

## 1. Today

### Where the checks live

Every Go service asks auth who the caller is (`*/internal/authclient`), gets
back a role (owner, admin, member) and whether the caller is an agent, and
then decides on its own.

| Service | Who the caller is | What it decides, and where |
|---|---|---|
| auth | Issues sessions and agent tokens; stores org roles and the member's standing agent grant | `auth/internal/agentgrant/manager.go`, `auth/internal/postgres/agentgrants.go` |
| teams | `teams/internal/authclient/client.go` | `CanSeeTeam`, `CanManageTeam` in `teams/internal/domain/control.go:15-26`: an org admin's own session or a team admin. Team tree is an `ltree` path (`teams/internal/postgres/migrations/004_user_member_ltree.sql`) |
| rag | `rag/internal/authclient/client.go` (also reads a specialist's `grant.corpora`) | `mayRead`/`mayWrite`/`mayDelete`/`mayCreate` in `rag/internal/application/service.go:741-835`; `MayReadCorpus` and `ActsAsAdmin` in `rag/internal/domain/domain.go:130-152`; `Document.VisibleTo` in `rag/internal/domain/document.go:228`. Team corpora ask teams over HTTP on every check (`teamAccess`) |
| files | `files/internal/authclient/client.go` | `mayCreate`/`canSee`/`canDelete` in `files/internal/application/service.go:365-430`, same shape as rag, also asking teams |
| apps | `apps/internal/authclient/client.go` (reads the specialist grant) | `MayReach`, `MayRun` in `apps/internal/domain/domain.go:117-158`: a specialist reaches only granted toolkits and tools, read-only unless a write is granted |
| knowledge | `knowledge/internal/authclient/client.go` | `knowledge/internal/application/sources.go`: agents can't register or remove sources (`:55`, `:284`); a member indexes only through their own connection (`:227`); creator or org admin reads a source (`:241`, `:256`) |
| billing | `billing/internal/authclient/client.go` | Only the owner manages billing (`billing/internal/application/service.go:111`). Entitlements are a plan key and a member limit (`billing/internal/catalog/catalog.go:180`) |
| agents/primary | Member session or a per-member agent token (`agents/primary/internal/memberauth`) | What a run may do: `runprofile.table`, role × mode → tools, knowledge reach, memory, guard, may-decide (`agents/primary/internal/runprofile/runprofile.go`). Who may read and decide a request: `canRead`, `answersFor` (`agents/primary/internal/interactions/service.go:880-902`). Teamspace admin: a string compare of the org role (`agents/primary/internal/teamspaces/service.go:475, 506`) |
| agents/primary, consent | Slack asker and owner, mapped to members | `consentpolicy.Decide` (`agents/primary/internal/slackrouter/consentpolicy/policy.go`): refuse, ask, or bypass. Bypasses are a marked private channel and the asker outranking the owner in the org chart (`slackrouter/authority.go`, `internal/workspace/runtime/context.go:310` `Above`). Consent choices summary, access, deny and the one-hour lease (`interactions/consent.go:41`, `interactions/store_asks.go:151-250`) |
| workspace | Admin applying `oryxa.yaml` | Holds `ownership` and `orgChart` (`internal/workspace/spec/spec.go:168-223`). Sensitive plan changes need a reviewed, bound plan (`internal/workspace/runtime/plan.go:43`); only a human owner changes org settings |

One rule runs through all of them and is the most important one to keep:
**an agent never acts with an admin's reach.** `ActsAsAdmin` is true only for
an owner or admin's own session (`rag/internal/domain/domain.go:150`). An
agent acting for an admin gets a plain member's reach, and a specialist gets
less: the intersection of its member's reach and its grant
(`runprofile.Grant`).

The relations already in the product, and what they are used for:

| Relation | Stored in | Used for permissions? |
|---|---|---|
| Org role (owner/admin/member) | auth | Yes, everywhere, copied six times |
| Team member, team admin (`leads`) | teams | Yes: team corpora, team files, teamspaces |
| Team parent (tree) | teams (`ltree` path, `parent_id`) | No. A parent team's admin does not manage its subteams; deleting a team deletes its subtree |
| `orgChart.relationships` + `authority` + `executives` | workspace | Once: the org-chart consent bypass (`Above`) |
| `ownership` (owner of a system, channel or topic) | workspace | No. Routing hints only |
| Specialist grant | agents/primary, carried in the agent token | Yes, in apps and rag |
| Lease (owner → asker, one Slack thread, one hour) | agents/primary `interaction_leases` | Yes, approves follow-ups with no click |

### Known gaps

| # | Gap | Where |
|---|---|---|
| G1 | **Six copies of the role check.** `OrgRole`, `IsOrgAdmin` and `ActsAsAdmin` are defined separately in teams, rag, files, apps and knowledge, and agents/primary compares strings. They agree today only because each was copied carefully | the `domain.go` files above; `teamspaces/service.go:475` |
| G2 | **knowledge uses `IsOrgAdmin` where the others use `ActsAsAdmin`.** An agent acting for an admin can list and read every member's sources' metadata (`query.Admin` is honoured for the agent) | `knowledge/internal/application/sources.go:241, 256` |
| G3 | **Single reads and lists are separate code.** A list route that filters differently from the single read is the bypass class #109's review found ("list routes that aren't gated") | rag `VisibleTo` vs `mayRead`; files `canSee` vs listings |
| G4 | **Team checks are an HTTP call per check,** and each caller must undo teams' answer for an agent acting for an admin (teams shows the admin every team) | `rag/internal/application/service.go:836-850`, files' `teamAccess` |
| G5 | **The org tree grants nothing** beyond the one bypass. A parent team lead can't see or approve for subteams; `ownership` is never consulted | teams, workspace |
| G6 | **Specialist grants hold in apps and rag only;** files and knowledge don't enforce them, nor the browser and sandbox | `docs/design/live-layer/06-agent-isolation.md`, "Still open" |
| G7 | **No single record of why something was allowed.** The consent ledger records consent and bypass decisions; every other allow is silent | `interactions` events only |
| G8 | **Billing can't gate features.** Entitlements are plan key and member limit | `billing/internal/catalog/catalog.go:176-205` |
| G9 | **Leases exist only for Slack asks,** keyed by channel and thread | `interactions/asks.go:161` |
| G10 | **The Cedar layer is parked** with three high-severity bypasses: an agent escaping a member's `forbid`, a `forbid` that errors being ignored, and list routes that aren't gated (#109, `feat/authz-cedar-core`, `feat/rag-authz`, `feat/kb-policies-web`) | `docs/STATUS.md` |

The first two bypasses in G10 come from the model, not the code: Cedar's
`Agent in [Member]` with the same id makes "this member" and "an agent acting
for this member" easy to confuse in a policy, and stored policies that can
fail to evaluate are a second source of truth. The model below avoids both:
agents are their own subjects, and nothing an admin writes is code.

## 2. The relation model

### Entities

| Entity | Id | What it is |
|---|---|---|
| `org` | organization id | The tenant. Everything hangs off one |
| `member` | user id | A person in one org |
| `team` | team id | A node of the team tree |
| `agent` | `agt_…`, `oag_…` | A member's primary agent, a specialist, or the org agent |
| `connection` | apps connection id | One member's sign-in to one app (Gmail, GitHub, …) |
| `corpus` | `cor_…` | A knowledge base: member, team or org scope |
| `file` | files object id | A stored file: member, team or org scope |
| `source` | knowledge source id | Something a connection indexes into a corpus |
| `system` | `org/slug` | Something a person owns per `ownership` (billing, a channel, a topic). Routing only |
| `request` | interaction id | One agent-to-agent ask |

### Relations

| Relation | On → subject | Comes from | Grants |
|---|---|---|---|
| `owner`, `admin`, `member` | org → member | auth roles | `admin`: administer the org, but only in a person's own session |
| `member-of` | team → member | teams membership | Team resources |
| `lead-of` | team → member | teams `leads` (team admin) | Manage the team; on Team plans and up, approve asks for team resources |
| `parent` | team → team | teams tree | Nothing by default; on Business, an org-chart rule can make a lead's reach cover the subtree |
| `admin-of` | org → member | auth | As `admin` above |
| `manager-of` | member → member | `orgChart.relationships` whose relation is in `orgChart.authority`; `executives` | The org-chart consent rules (§4) |
| `related` | member → member | chart edges whose relation is not in `authority` | Nothing. Kept so the chart round-trips |
| `owns-system` | system → member | `ownership` | Nothing. Says whom to ask |
| `agent-of` | agent → member | agents/primary | The agent may act for the member, within its grant and run mode |
| `granted` | corpus, connection → agent | specialist grant | A specialist's reach; `granted_write` for writes |
| `delegates-to` | member → member | the member, on Business and up | Answer for the delegator while it lasts (§4) |
| `lease` | member → member, with thread and expiry | the consent flow | Follow-ups in one thread for up to an hour |
| `requester`, `target` | request → member or agent | interactions | Read the request; the target decides |

### Schema

Written in SpiceDB's schema language because it is the most compact way to
show it, and because §3 recommends SpiceDB. It reproduces today's rules
exactly; the commented lines are the tier rules from §5.

```zed
use expiration

definition member {
    relation org: org
    // set only for people the org chart names, so a person the chart leaves
    // out is above nobody and below nobody, as Above does today
    relation chart: org
    relation manager: member
    // set only on executives, so nobody outranks them
    relation exec_of: org
    // consent shortcuts (§4)
    relation standing_summary: member
    relation delegate: member with during_window              // Business
    relation lease: member with in_thread and expiration

    permission outranked_by = (manager + manager->outranked_by + chart->executive) - exec_of->executive
    permission summary_without_asking = outranked_by + standing_summary + delegate + lease
    permission access_without_asking = lease                     // + delegate_access on Business
}

definition org {
    relation owner: member
    relation admin: member
    relation member: member
    relation executive: member
    permission administer = owner + admin
    permission participate = administer + member
}

definition team {
    relation org: org
    relation parent: team
    relation lead: member
    relation member: member
    // today: an org admin (own session) or the team's own lead
    permission manage = lead + org->administer
    permission manage_member_reach = lead
    permission see_member_reach = member + lead
    permission see = see_member_reach + org->administer
    // Business org-chart rule: a lead's reach covers the subtree
    // permission see_member_reach = member + lead + parent->see_member_reach
}

definition agent {
    relation of: member
    relation org: org
}

definition corpus {
    relation org: org
    relation owner: member        // member scope
    relation team: team           // team scope
    relation everyone: org        // org scope
    relation granted: agent
    relation granted_write: agent
    relation created_by: member

    // what an agent reaches for its member: no admin arrows (ActsAsAdmin)
    permission read_member_reach = owner + team->see_member_reach + everyone->participate
    permission read = read_member_reach + org->administer
    permission write_member_reach = owner + team->see_member_reach
    permission write = write_member_reach + org->administer
    permission delete_member_reach = owner + team->manage_member_reach + created_by
    permission delete = delete_member_reach + org->administer
}

definition file {
    relation org: org
    relation owner: member
    relation team: team
    relation everyone: org        // access "members"
    relation admins_only: org     // access "admins"
    relation created_by: member
    permission read_member_reach = owner + team->see_member_reach + everyone->participate
    permission read = read_member_reach + org->administer + admins_only->administer
    permission delete_member_reach = owner + created_by + team->manage_member_reach
    permission delete = delete_member_reach + org->administer
}

definition connection {
    relation owner: member
    relation granted: agent
    relation granted_write: agent
    permission use = owner
}

definition source {
    relation org: org
    relation connection: connection
    relation corpus: corpus
    relation created_by: member
    permission manage_member_reach = created_by
    permission manage = manage_member_reach + org->administer
}

definition system {
    relation org: org
    relation owner: member
}

definition request {
    relation org: org
    relation requester: member | agent
    relation target: member
    relation target_agent: agent
    permission read = requester + target + target_agent
    permission decide = target + target_agent
}

caveat in_thread(channel string, thread string, ask_channel string, ask_thread string) {
    channel == ask_channel && thread == ask_thread
}

caveat during_window(starts timestamp, ends timestamp, now timestamp) {
    now >= starts && now < ends
}
```

### Example tuples

An org, Acme, with Maya (admin, executive) managing Sam, Engineering with a
Payments subteam, and Sam's specialist.

```text
org:acme#admin@member:maya
org:acme#member@member:sam
org:acme#member@member:lee
org:acme#executive@member:maya

member:maya#chart@org:acme
member:maya#exec_of@org:acme
member:sam#chart@org:acme
member:sam#manager@member:maya                  # orgChart: maya manages sam; "manages" is in authority

team:eng#org@org:acme
team:eng#lead@member:maya
team:eng#member@member:sam
team:payments#parent@team:eng
team:payments#member@member:lee

corpus:cor_eng#team@team:eng
corpus:cor_sam#owner@member:sam
corpus:cor_handbook#everyone@org:acme
corpus:cor_sam#granted@agent:agt_sam_research  # Sam's specialist may read Sam's corpus

agent:agt_sam#of@member:sam
agent:agt_sam_research#of@member:sam
connection:con_sam_gmail#owner@member:sam
connection:con_sam_gmail#granted@agent:agt_sam_research     # read only: no granted_write

system:acme/billing#owner@member:sam            # ownership: {owner: sam, system: billing}

member:sam#lease@member:lee[in_thread:{"channel":"C1","thread":"171.2"}][expiration:2026-10-23T11:00:00Z]
member:sam#delegate@member:lee[during_window:{"starts":"2026-10-24T00:00:00Z","ends":"2026-10-31T00:00:00Z"}]   # Business

request:int_1#requester@agent:agt_lee
request:int_1#target@member:sam
request:int_1#target_agent@agent:agt_sam
```

### How a check is asked

Services never call the engine directly. One Go package, `internal/access`,
takes an actor and turns it into the right checks. This is where the
agent rule lives, once:

| Actor | Check |
|---|---|
| A member's own session | `check(member:u, P, R)` |
| A member's primary agent | `check(member:u, P_member_reach, R)`: never the admin arrows |
| A specialist | `check(member:u, P_member_reach, R)` **and** `check(agent:a, granted[_write], R)`, or the grant is "all" |
| The org agent | Only `org`-scoped resources, by its own rules (`06-agent-isolation.md` §6) |

Lists use the same permission through `LookupResources`, or `CheckBulk` over
a page of candidates, never a second hand-written filter. That closes G3 by
construction.

The run profile stays in code. `runprofile.table` decides what a run may do
by role and mode (watched, wake-up, delivery, room); relations decide what
the actor may reach. A run's reach is the smaller of the two.

## 3. Engine options

The four options, on what matters for Oryxa: one Go binary on ECS, one
Postgres 17 instance on RDS (`deploy/production/database.tf`), a small team,
and orgs of tens to a few thousand members.

| | SpiceDB | OpenFGA | Cedar (cedar-go) | Checks in code, centralized |
|---|---|---|---|---|
| **Model** | Zanzibar: schema + relationships, caveats (CEL), expiring relationships | Zanzibar: model + tuples, conditions (CEL) | Policies over an entity graph you pass in per request | Go functions over each service's own tables |
| **Fit for Go** | Written in Go; `authzed-go` gRPC client is small | Written in Go; can run as a server or embedded as a library (large dependency tree in `go.mod`) | Native Go library, in-process | Native |
| **Self-host on AWS/Postgres** | ECS service, Postgres datastore (a new database on the existing RDS instance). Needs `track_commit_timestamp=on` (a parameter-group change and one reboot) | ECS service or embedded; Postgres datastore | Nothing to host; #109 stores policies in Postgres | Nothing new |
| **Consistency (new enemy)** | ZedTokens: a write returns a token, and a later check can demand "at least as fresh as this". Remove-then-add is safe even with caching on | No consistency tokens; `HIGHER_CONSISTENCY` per request skips the cache. Safe with the cache off, at a latency cost | Whatever data the caller loads; consistent if loaded from the source in the same request | Consistent: reads the source of truth |
| **Latency** | ~1-5 ms per check in-VPC, cached sub-ms; `CheckBulk` and `LookupResources` for lists | Similar; `ListObjects` slower on deep graphs | Microseconds to evaluate, plus loading the entities, which is the real cost | Today: an HTTP call to teams per team check |
| **Ops cost** | One more service, its migrations and GC, relationship sync from each service | Same, or less if embedded | Low; policy store and cache only | Lowest, but every service keeps its own copy of the rules (G1) |
| **Lock-in** | Low: Apache-2.0, open schema, relationships are plain rows. AuthZed's managed service is optional | Low: Apache-2.0, CNCF | Low for the library; Amazon Verified Permissions is the managed path and is AWS-only | None |
| **Time-bound access (leases)** | Native expiring relationships | Conditions with a timestamp; expired tuples stay until deleted | A `context.now` condition in policy | Hand-written, as today |
| **Lists** | `LookupResources` | `ListObjects` | None: filter after loading, which is how list bypasses happen | Hand-written per route, as today |
| **Fits the org tree?** | Yes: recursive arrows for the chart and team tree | Yes | Awkward: the graph must be loaded and passed in each time | Yes, by hand |

### Recommendation

**Put every check behind one Go facade, `internal/access`, now; put SpiceDB
behind it once the facade is in place; keep Cedar out of the base model.**

Why SpiceDB:

- **The product is a graph.** Teams, the team tree, the org chart, agents of
  members, grants and leases are relations. A Zanzibar engine answers "can
  Lee's agent read this" and "which corpora can it read" from one schema, so
  single reads and lists can't drift apart (G3, #109's third bypass).
- **New-enemy safety without giving up caching.** Removing Lee from Payments
  and then adding a payroll document must never let Lee see it. ZedTokens make
  that hold by construction; OpenFGA gets there only by turning the cache off.
- **Leases and delegations are expiring relations,** natively. No sweeper
  decides when access ends.
- **Postgres and Go,** on what we already run, with no managed dependency.
- **Over OpenFGA:** the consistency story and native expiration. Embedding
  OpenFGA would save a service but brings a large module tree into `go.mod`,
  which `docs/LANES.md` asks us to avoid. OpenFGA is the fallback if running
  another service is a blocker.
- **Over Cedar:** Cedar is a policy language, not a relationship store; every
  request would load the graph itself, which is the work SpiceDB does. Its
  stored, admin-written policies are also where #109's bypasses came from.
  The useful part of #109, forbid-only narrowing by an admin, returns on
  Enterprise as a relation (`blocked`), not as code (§5).
- **Over code only:** centralizing is step 1 regardless and fixes G1 and G2.
  But the rules that tiers sell (subtree reach, delegation, approval chains,
  custom roles) are graph rules; written by hand per service they become six
  copies again.

What it costs: one ECS service (two tasks), one RDS parameter change and
reboot, and keeping relationships in sync with their sources (see §6, risks).
If that cost is refused, the facade still stands on its own, and the
fallback is the centralized code with an `authz.relations` table in Postgres
built to the same schema, so moving to SpiceDB later is a projection, not a
rewrite.

## 4. The consent flow

The flow doesn't change shape. `consentpolicy.Decide` still refuses first,
then looks for a reason to skip the owner, then asks. What changes is where
the reasons come from: relation checks through `internal/access` instead of
the one `Outranks` fact.

**Relations never get round a refusal.** The eight refusals in `Decide`
(not a member, group DM, shared channel, no owner or too many, too many open
asks, owner not a member, the screen not clearing the words, the wrong
department) all run before any relation is looked at, on every plan, as they
do now.

### What can skip the owner

| The asker is… | Relation | Skips consent for | Answer goes to | Plan |
|---|---|---|---|---|
| Above the owner in the chart | `chart_person:<org>/<owner>#outranked_by` (per org, #442) | A summary only | Asker's DM (`AnswerFor`) | Team (today: every plan, see §5) |
| Someone the owner said may always get summaries | `standing_summary` | A summary only | Asker's DM | Free |
| In a thread with a live lease | `lease`, caveat `in_thread` + expiry | Summary and access, follow-ups in that thread | The thread, as today | Free |
| The owner's delegate this week | `delegate`, caveat `during_window` | Summary; access if the delegation says so | Asker's DM | Business |
| Asking in a marked private channel | Not a relation: a channel fact | A summary | The thread | Free (today) |
| On the same team, for a team resource | `team->see_member_reach` | Not an ask at all: they can already read it | n/a | Free |

### What always needs consent

| Ask | Why |
|---|---|
| Anything from the owner's own scope: member corpora, files, memory, connected apps | Personal data. No relation reaches it but the owner's own consent |
| Anything that would write or act in the owner's apps | Consent is per request; a summary bypass never grants a tool run |
| A secret | Always the owner, always a one-time view in Oryxa, never in Slack (`secretshares`) |
| Anything an admin asks | `admin-of` is not a bypass. An admin's agent has member reach, as today |
| Quotes (`access`) on the strength of rank | Rank gives a summary in a DM, never quotes in a channel |
| Asks routed by `owns-system` | Ownership picks whom to ask, not whether to ask |

On Team plans and up, `lead-of` adds a second approver, not a bypass: an ask
for a team resource can be approved by the owner or any lead of the team
that holds it. On Business, an approval chain can require two approvals (the
owner, then their manager, or a lead) for asks the org marks sensitive. A
chain only ever adds approvers.

### Leases become relations

Today a lease is a row in `agents_primary.interaction_leases`, found by
`liveLease` inside the transaction that files the follow-up.

| Today | Relation |
|---|---|
| `access` approved → `grantLease` inserts a row, expires in `LeaseTTL` (1 h, or the org's shorter `rules.consent.expireAfter`) | Also write `member:owner#lease@member:asker[in_thread:{channel,thread}][expiration:…]` |
| Renewing keeps the lease's id | Touch the same relationship with a new expiry |
| `RevokeLease` sets `revoked_at` | Delete the relationship **first**, then mark the row |
| `liveLease` reads `FOR SHARE` so a revoke waits | The check carries the revoke's ZedToken, so it can't see a stale lease |

The ledger row stays: it is the audit record and what the owner's Activity
page lists. The relation is what's enforced. A lease can never be longer than
an hour, on any plan.

## 5. Tiers

**Safe defaults hold on every plan. A bigger plan buys expressiveness, never
safety.** Concretely, on every plan:

- an agent never has more reach than its member, and never an admin's;
- a specialist is read-only until its owner grants a write;
- personal data, writes and secrets always need the owner;
- the Slack refusals and the screen run before any shortcut;
- leases last an hour at most, and the owner can revoke any of them;
- every allow is checked in one place and recorded (`internal/access` logs the
  decision and the relation that granted it);
- an evaluation error, a missing relation or a service that doesn't answer
  denies.

Today `billing/plans.example.json` has `unpaid` (3 members), `team`
(25 members) and `seats` (5 or more). The proposal adds `business` and
`enterprise` and a `features` list to the entitlements.

| | Free | Team | Business | Enterprise |
|---|---|---|---|---|
| Roles: owner, admin, member; teams | ✓ | ✓ | ✓ | ✓ |
| Consent: summary, access, deny; one-hour leases; revoke | ✓ | ✓ | ✓ | ✓ |
| Specialist grants (apps, tools, corpora; writes explicit) | ✓ | ✓ | ✓ | ✓ |
| Standing "always share summaries with X" | ✓ | ✓ | ✓ | ✓ |
| Activity log in the app (own requests, leases, bypasses) | 30 days | 90 days | 1 year | Custom |
| Marked private channel bypass | ✓ | ✓ | ✓ | ✓ |
| **Team-lead approvals** (a lead approves asks for team resources) | | ✓ | ✓ | ✓ |
| **Org chart: managers get summaries** (today's `Above` rule) | | ✓ | ✓ | ✓ |
| **Org-chart rules:** subtree reach for leads, which relations confer what, per resource type | | | ✓ | ✓ |
| **Delegation:** time-boxed `delegates-to` (cover while away) | | | ✓ | ✓ |
| **Approval chains:** two approvers for sensitive asks | | | ✓ | ✓ |
| **Audit export** (CSV, S3) | | | ✓ | ✓ |
| **SSO** (SAML/OIDC through the Cognito user pool; enforce SSO) | | | ✓ | ✓ |
| **SCIM** (provisioning and, more important, deprovisioning) | | | | ✓ |
| **Custom roles:** named bundles of the fixed permissions, e.g. "HR reviewer" | | | | ✓ |
| **Policy API:** read and write relations, dry-run "why can X see Y", block (narrow) access by rule | | | | ✓ |
| Audit streaming to a SIEM | | | | ✓ |

Rules for the expressive features, so none of them can weaken a default:

- **Custom roles are made of existing permissions** and can't include
  anything an admin's own session couldn't do. Holding one never gives an
  agent admin reach.
- **The policy API can narrow, not widen,** beyond what the plan's relations
  allow: it writes relations and `blocked` relations, which the schema
  subtracts. It never stores code, so nothing it writes can fail to
  evaluate.
- **Delegation and chains add people,** with an end date; they never remove
  the owner.
- **Downgrading fails toward asking.** The plan is checked when a relation is
  written, and the tier rules carry the plan as a condition at check time.
  An org that leaves Business keeps its delegations on record, but they stop
  applying, so asks go back to the owner.

Two calls for the owner:

- **The org-chart bypass is live on every plan today.** Moving it to Team
  would take it from free orgs that set up `orgChart`. Proposal: keep it for
  existing orgs and make it Team for new ones.
- **SSO is placed on Business,** not Enterprise. Being able to cut off a
  leaver is safety; until SCIM, removing a member in Oryxa does that on every
  plan.

## 6. Migration

Each step ships alone, changes no behavior unless it says so, and leaves the
system working if the next step never happens.

| Step | What | Changes behavior? | Owns |
|---|---|---|---|
| 0 | **Close #109** and its two branches. Keep its truth tables and fail-closed tests as fixtures for step 1 | No | — |
| 1 | **`internal/access`, centralized in code.** One `Actor` (member session, primary, specialist, org agent), one `ActsAsAdmin`, one `TeamAccess`. Replace the copies in teams, rag, files, apps, knowledge and the teamspaces string compare. A truth-table test per service pins today's answers | Fixes G2 only (knowledge then refuses an agent admin reach) | `internal/access`, the `domain.go` role helpers |
| 2 | **One path for reads and lists.** Every list route filters with the same `access` call as the single read; a test per route compares the two | No; closes G3 | rag, files, knowledge list handlers |
| 3 | **Decision log.** `access` records each allow with the relation or rule that granted it; the Activity page reads it | No | `internal/access`, a log table |
| 4 | **SpiceDB in shadow.** Deploy it (ECS service, a database on the RDS instance, `track_commit_timestamp`). Project relationships from the sources: auth roles, teams membership and tree, workspace chart and ownership, grants. Write on change, and backfill and reconcile from the existing events every night. `access` asks both and logs every disagreement; code decides | No | `deploy/production`, a projector, `internal/access` |
| 5 | **Flip one service at a time,** simplest first: files, rag, knowledge, apps, teams. Flip when a week of shadow shows no disagreement; remove the old code a week later | Same answers, from the engine | each service's checks |
| 6 | **Consent on relations.** `Outranks` becomes `check(outranked_by)`; leases are written as expiring relationships alongside the ledger; `standing_summary` ships | Adds standing summaries | `slackrouter`, `interactions` |
| 7 | **Entitlements carry features.** Billing adds `features`; `access` reads them when relations are written and as a condition on tier rules | No, until a feature ships | `billing/internal/catalog`, `internal/access` |
| 8 | **Team and Business features,** each behind its entitlement: lead approvals, org-chart rules, delegation, approval chains, audit export, SSO | Yes, opt-in per org | per feature |
| 9 | **Enterprise:** custom roles, SCIM, policy API, SIEM streaming | Yes, opt-in | per feature |

### Risks

| Risk | Mitigation |
|---|---|
| **Relationships drift from their source** (a write to teams succeeds, the write to SpiceDB fails) | Revoke first, grant last: removals delete the relationship before changing the source, so a failure leaves access *missing*, not left over. A nightly reconcile from the source repairs the rest and alerts on any fix it makes |
| **New-enemy** (a check races a revoke) | Writes return a ZedToken; `access` keeps the latest per org and passes it with checks that follow a change |
| **Latency on hot paths** (rag search filters many hits) | `CheckBulk` per page; `LookupResources` for "which corpora can I search"; cache with ZedTokens |
| **SpiceDB down** | Deny, as the defaults require. Two tasks behind a load balancer; an alarm. The consent flow already falls back to asking the owner when the chart can't be read (`slackrouter/route.go:417-429`) |
| **The RDS parameter change needs a reboot** | Do it in the step 4 maintenance window; it's the only one |
| **Agent vs member confusion** (#109's first bypass) | Agents are their own subjects with `agent-of`; the agent rule is in one function with a truth table |
| **Tier rules weaken a default by mistake** | A test asserts every default holds with every feature on; downgrades are tested to fall back to asking |
| **`go.mod` change** | One client module, `authzed-go`. Noted in the PR as `docs/LANES.md` asks |

## Decisions

1. **Engine:** SpiceDB behind `internal/access` (recommended), OpenFGA, or the
   centralized code alone.
2. **#109:** close it and keep its tests (recommended), or fix the three
   bypasses and keep Cedar for narrowing policies.
3. **Org-chart bypass:** keep it on every plan for existing orgs and make it
   Team for new ones (recommended), or leave it on every plan.
4. **SSO:** Business (recommended) or Enterprise.
