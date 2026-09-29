# Prepare an Oryxa workspace file

You are the customer's coding agent. Gather facts from the company sources you
can access, prepare a reviewable `oryxa.yaml`, and let an organization admin
review the plan before applying it. Slack installation remains a manual step.

## Map source facts

1. Start with `oryxa init --org SLUG --domains example.com -o oryxa.yaml`.
   Use `oryxa pull --org SLUG -o current.yaml` for an existing organization.
2. Put canonical lowercase work emails in `people.members`. Map administrator
   and owner roles only when the source or admin confirms them. Use `guest:
   true` for an outside-domain person. Do not invent an email from a name.
3. Map departments and working groups to `teams`. Put each member's email in
   `members`; put confirmed team admins in `leads`. A lead is added to the
   team automatically. A title by itself does not prove a team-admin role.
4. Map explicit ownership statements to `ownership`: `owner` plus at least
   one of `what`, `system`, or `channel`. Use `tags` for alternate search
   terms. One selector set has one owner; raise conflicting claims for review.
5. Map confirmed reporting or authority edges to `orgChart.relationships`.
   Each edge has `from`, `relation`, and `to`. `from` is the person above or
   otherwise related to `to`. List only relation names that confer authority
   in `orgChart.authority`. Put confirmed top-level executives in
   `orgChart.executives`. Do not infer authority from team membership alone.
6. Leave uncertain facts out and ask the admin about them. Keep source evidence
   and unresolved questions in your review message, not in unsupported YAML
   fields. Never put passwords, tokens, or secrets in the file or your chat.
7. If the admin provides a request expiry or review rule, map it to
   `rules.consent.expireAfter` and `safety.guard.holdForReview`. Map the
   organization default for app update wake-ups to
   `agents.personal.proactive: on` or `off`. Do not infer these policies.
8. If the admin chooses Oryxa's own knowledge search, set `search.mode:
   native`; if they explicitly turn agent knowledge tools off, use
   `search.mode: none`. Leave it out when no search decision was made.

Example:

```yaml
apiVersion: oryxa.in/v1
kind: Workspace
metadata:
  organization: acme
  domains: [acme.com]
people:
  invite: quiet
  members:
    - {email: maya@acme.com, role: admin}
    - {email: sam@acme.com, role: member}
teams:
  - name: Engineering
    members: [maya@acme.com, sam@acme.com]
    leads: [maya@acme.com]
ownership:
  - {owner: sam@acme.com, system: billing, tags: [payments]}
orgChart:
  relationships:
    - {from: maya@acme.com, relation: manages, to: sam@acme.com}
  authority: [manages]
  executives: [maya@acme.com]
tools:
  provider: composio
  offer: [github, jira]
  autoSubscribe: false
search:
  mode: native
```

## Review and apply

Run `oryxa validate -f oryxa.yaml` for schema errors, then `oryxa plan -f
oryxa.yaml --json` for live identity checks and every proposed change. Show
the admin the file, source evidence, unresolved questions, and plan. Apply
with `oryxa apply -f oryxa.yaml` after review. If an invite is pending, wait
for the person to join and run apply again. Run `oryxa status --org SLUG
--json` to check the result.

Omission preserves existing people, teams, ownership hints, and chart facts.
Explicit `prune: true` under the relevant section plans removals and requires
review. `tools.offer` is an organization allowlist: omission allows every
Composio toolkit, while `offer: []` allows none. Research actual toolkit slugs
before listing them; a disallowed app's existing connection cannot act or
deliver events or refresh knowledge sources until it is allowed again.
Previously indexed documents remain. `tools.autoSubscribe` is the default for
new connections. Unsupported sections fail validation before any write.
The CLI does not currently configure Slack, external directory providers,
external search providers, the other governance and safety controls, retention, or the
other tool settings.
