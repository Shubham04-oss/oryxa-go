# Lanes: parallel work

Updated 2026-09-29. Read this before starting work beside other workers. A lane is a unit of work one worker can finish alone, with files that no other open lane touches. [REMAINING-WORK.md](REMAINING-WORK.md) stays the index of everything unfinished. This file covers what is being built now, and by whom.

## How lanes work

- **One lane, one branch, one worker.** The table gives each lane's branch.
- **Claim a lane before writing code.** Push its branch and open a draft PR titled `[<lane id>] <what it does>`. Run `gh pr list` first: a lane with an open PR is taken. The tables below say who planned to take a lane, but the open PRs are the truth.
- **Stay inside your lane's files.** If you need a file that another open lane owns, stop and say so in your PR; don't edit it.
- **Hot files** are the handful every lane wants. Each has a rule below.
- **Commit after each item**, not only at the end. Don't spawn sub-agents.
- **Before marking a PR ready:**
  - rebase onto main (the Slack lanes rebase onto `slack/base`);
  - Go: run `go build ./... && go vet ./...` and `go test` on your packages;
  - web-next: run `npm run typecheck && npm test && npm run build` from `web-next/`.
- **Don't edit this file in a lane PR.** The orchestrator updates it after merges, so lane PRs never conflict on it.
- **Everything merged to main deploys to production.** If a lane needs infrastructure (a queue, a secret, an environment variable), its PR says so, and the infrastructure goes in before the merge, never after. A service that consumes events without its queue refuses to start. That stopped a deploy on 2026-09-29.

### Hot files

| File | Rule |
|---|---|
| `web-next/src/components/workspace-shell.tsx` | A lane adds only an import, a nav entry and a render branch for its section. The screen lives in the lane's own component files. Rebases here are expected and small. |
| `web-next/src/components/agents-mode.tsx` | S-H adds the Requests section. W-WORKFLOWS replaces the read-only workflows block with its component. Nothing else. |
| `web-next/src/components/workspace-agent-panel.tsx` | W-PANEL only. |
| `web-next/src/server.ts`, `web-next/src/*-agent.ts`, `web-next/REALTIME.md` | S-D only, until it merges. |
| `web-next/PARITY.md` | Edit your area's row only. Leave the summary paragraph at the top alone; the orchestrator refreshes it after merges. |
| `agents/primary/service.go`, `agents/primary/README.md` | S-F only. Any other Go lane that needs wiring puts the exact lines in its PR description. |
| `deploy/production/**`, `deploy/scripts/**` | S-I only, except `answer_guard.tf`, which is S-G's. |
| `go.mod`, `go.sum` | Avoid new modules. If a lane needs one, its PR says why. |
| Migrations | One lane per migration directory. Take the lock numbers the table gives; `git grep` a lock before using a new one. |

## Slack consent loop: the orchestrator's workers

The contracts and work packages are in [design/slack-implementation.md](design/slack-implementation.md), and the design is in [design/slack-consent-loop.md](design/slack-consent-loop.md).

Slack lanes branch from `slack/base` (main plus S-A, S-B and S-E) and merge back into it. The stack goes to main once S-F's end-to-end test passes against the fake Slack. Everything stays off in production until the Slack app's secrets are stored and `APPS_SLACK_SURFACE` is set.

| Lane | What | Owns | Needs | State |
|---|---|---|---|---|
| S-A | Apps gateway: Slack ingress, install, templates, the internal API, `slackfake` | `apps/internal/{slackbot,hooks,httpserver,serviceconfig,postgres}`, `contracts/events/apps/v1/slack.go`, `internal/slackfake` | — | built, in review (`feat/slack-apps-gateway`) |
| S-B | Consent core: asks, the three choices, tickets, leases, bypass, one-time secret routes | `agents/primary/internal/{interactions,interactiondelivery,httpserver}`, `agents/primary/internal/agents/interactions.go`, `contracts/events/agents/v1` | — | built, in review (`feat/slack-consent-core`) |
| S-E | Router: who is asked, identities, refusals, consent policy | `agents/primary/internal/slackrouter` | — | built, in review (`feat/slack-router`) |
| S-C | Surfaces: owner notices, buttons, thread answers; the apps client; askers' notes | `agents/primary/internal/{surfaces,appsclient}`, `agents/primary/internal/threads/{notes,items}.go` | S-A, S-B | building |
| S-D | Realtime relay: request events reach the Worker; DM and inbox objects note them | `realtime/**`, `web-next/src/{realtime-requests.ts,server.ts,direct-message-agent.ts,inbox-agent.ts}`, `web-next/src/lib/dms/**`, `web-next/REALTIME.md` | S-B | building |
| S-G | Consent guard: the summary rule, Slack credential stripping, grounding | `agents/primary/internal/consentguard`, `deploy/production/answer_guard.tf` | S-B | building |
| S-H | Web: the request card, Requests (Inbox, Sent, Activity), Slack settings | new `web-next/src/components/{request-card,requests-view,requests-activity,slack-settings}*`, `web-next/src/lib/agents/agent-requests.ts`, new `web-next/src/lib/apps/slack.ts` | S-A, S-B contracts | building |
| S-I | One-time secret shares, plus the deploy tofu for Slack | `agents/primary/internal/secretshares`, `deploy/{production,scripts,README.md}` except `answer_guard.tf` | S-B | building |
| S-K | Screen: one Jev read of the ask, plus a department check | `agents/primary/internal/slackrouter` | S-E | after S-E's review |
| S-F | Wiring and the end-to-end test | `agents/primary/{service.go,README.md}`, `agents/primary/internal/{inbound,serviceconfig}`, `test/slack` | all above | last |
| S-L, S-M | Slice 4: DMs with the bot, organization answers | see the plan | S-F | later |

## Web: fewer empty pages (open, suggested for the owner's worker)

Each lane touches web-next only. The Go routes and web-next's API clients already exist, so this is screen work.

**Read first:** `web-next/AGENTS.md`, `DESIGN.md` in full, and the area's row in `PARITY.md`.
- Show no example data. Give each block its own loading, empty and error states.
- Treat a 404 from an optional service as "not available here".
- Signed out, show only the dev-only layout preview, as the other sections do.

Test with component tests over mocked `fetch`, as the existing `*.test.tsx` files do.

| Lane | What | Owns | Uses |
|---|---|---|---|
| W-HOME | **Home → Overview**, today an empty canvas under three fake "Saved views". Make it a *Today* page: what waits on the member, their agents, their teamspaces, tools needing attention, knowledge bases indexing or failing. Each card shows a count and the top three, and links to its section. Remove the `savedViews` labels. The layout is the owner's call; this is the default. | new `components/home-overview.{tsx,css,test.tsx}`; the overview branch and `savedViews` in `workspace-shell.tsx` | `agentRequestsQueryOptions(org, "inbox")`, `threadListQueryOptions`, the teamspaces list, apps connections, rag corpora |
| W-FILES | **Files**, a new Home section: the member's, their teams' and the organization's files. Upload (8 MB at most, retention 7/30/90 days), download, delete where `canDeleteFile` allows, and where a file came from. | new `components/files-view.{tsx,css,test.tsx}`; a nav entry. You may *add to* `lib/files/client.ts`, but don't change what exists: rooms and knowledge bases use it. | `lib/files/client.ts`; Go `GET/POST …/files`, `…/files/{id}/download-url`, `DELETE …/files/{id}` |
| W-HISTORY | **Conversation history.** The primary agent's past conversations in the agent panel: list, open, delete. Delete clears the chat object first, then deletes in Go, as specialists do. Also a specialist's past conversations in its focused view. | new `components/conversation-history.{tsx,css,test.tsx}`; edits to `agent-chat.tsx`, `specialist-chat.tsx`, `specialist-focus.tsx` | `listThreads`, `deleteThreadChat`, `deleteThread`, `listSpecialistThreads`, `getSpecialistThread` |
| W-WORKFLOWS | **Workflows**, today read-only on the agent overview. Create, edit and delete; Run now; executions with their statuses (an execution whose assignment didn't complete is `failed`), and each execution's detail. | new `components/workflow-*.{tsx,css}` plus tests; the workflows block in `agents-mode.tsx` | `lib/agents/agent-workflows.ts` (the full CRUD plus executions) |
| W-CMDK | **⌘K / Ctrl+K palette.** Jump to a person (opens the DM), a teamspace, an agent, a specialist or team, a knowledge base, a tool, or a settings page. Recent items first; keyboard only is fine. | new `components/command-palette.{tsx,css,test.tsx}`, `hooks/use-command-palette.ts`; one mount in `workspace-shell.tsx` | the queries the shell already loads; `searchMembers` for people |
| W-PANEL | **The right panel's Teams tab** ("Team rooms will appear here"): the member's teamspace rooms and multi-agent team rooms, each opening its room. Also a teammate in a room's participants gets a Message action that opens the side conversation. | `workspace-agent-panel.tsx`, new `components/panel-teams-tab.{tsx,css,test.tsx}`, the participant list in `teamspace-room.tsx` | teamspaces and agent-teams queries; `SideConversationView` |

## Guard and backend (open)

| Lane | What | Owns | Notes |
|---|---|---|---|
| G-ORG | The guard reads the organization's settings from the workspace policy (#125). Replace `guardpolicy`'s `NoOrgPolicies` with an adapter over the policy settings #125 projects, and check that its guard fields are present. | `agents/primary/internal/guardpolicy/**` | The wiring lines go to S-F. |
| G-SURFACES | Every surface the guard design names runs the relevance stage. Teamspace ledger posts call `CheckWith`. Side-conversation answers send the tools their run called as `sources`, plus context, to the check route. Today `lib/side/asking.ts` sends only the text, origin and request, so the judge never runs. Rooms stay excluded by design. | the call sites in `agents/primary/internal/teamspaces`, and web-next's side-conversation check call | Coordinate with S-D if you touch the Worker. |

## The owner's (CLI and workspace)

| Lane | What |
|---|---|
| CLI-1 | Organization updates that change domains or guest access aren't treated as sensitive changes. They should need approval like the others. |
| CLI-2 | The secret scanner misses keys such as `gho_…` and `rk_live_…`. |
| CLI-3 | `--yes` approves the caller's own sensitive change. It shouldn't. |
| CLI-4 | Migration 009 marks existing invitations quiet, which silences their emails. Check that this is intended. |
| CLI-ON | Switch the CLI on in production. Set `cli_client_id` in `deploy/production/app.auto.tfvars.json` to the Cognito CLI client that `provision-app.sh` created, then `tofu apply`. Today `COGNITO_CLI_CLIENT_ID` is empty, so the CLI is off. |

## Waiting on the owner

1. **Create the Slack app** from `apps/internal/slackbot/manifest.yaml`. Keep the Client ID, Client Secret and Signing Secret in a password manager, not in chat. Don't click Slack's own "Install to Workspace": install through Oryxa, so it receives and seals the bot token.
2. **Store those values** once S-I's tofu is applied, with `deploy/scripts/put-secrets.sh` and its hidden prompt. Then verify both request URLs in Slack's settings.
3. **Home's design**, if the *Today* default in W-HOME isn't what you want.
4. **The relevance guard's rollout.** Production runs it in shadow: it judges, logs and holds nothing. After a few days of logs, hold for Oryxa's own organization (`AGENTS_PRIMARY_GUARD_HOLD_ORGS`), then raise `AGENTS_PRIMARY_GUARD_RELEVANCE_CAP` once false holds stay at or under 1%.
5. **CLI-ON**, when you want the CLI live.

## Known gaps, not lanes yet

- **Cognito mail:** it uses AWS's default sender, which is capped at 50 emails a day. Abuse of "forgot password" could block real resets. The edge now rate-limits `/v1/auth/password/*`. Moving the mail to SES needs a region check, since SES in us-west-2 was denied.
- **Slack identity** is a confirmed Slack email, so a Slack workspace admin could pose as a member. The fix is explicit account linking.
- **Cedar:** the branches (#109, rag-authz, kb-policies-web) stay parked. Three high-severity bypasses need fixing before any merge. Knowledge-base policies are postponed.
- **#125:** about 30 of its 47 review tasks were not re-verified after the fixes.
- **Payments:** Dodo's live keys are unset, so checkout is off during the free beta.
- **Cleanup:**
  - `speaker.go` confirmation items;
  - `threads.Run` shutdown recorded as failed;
  - side-conversation edge cases;
  - agent-system docs;
  - a REMAINING-WORK refresh.
