# Status: handoff, 2026-09-30

The one page to read before picking up work, whatever agent you are. [LANES.md](LANES.md) says who owns which files, and [REMAINING-WORK.md](REMAINING-WORK.md) is the full backlog. This page says what is true **now**, and what to do next, in order.

## Rules that are not negotiable

- **Main auto-deploys.** Every merge to main runs CD: the Go image goes to ECS, then the web-next Worker to Cloudflare. A PR that adds an event consumer, a queue, a secret or an env var needs its infrastructure applied first, and widened EventBridge rules come **after** the release (`deploy/scripts/provision-app.sh`). Otherwise the old consumer dead-letters the new types.
- **Plan before build.** The owner's "today we'll do X" is planning. Start builders only on an explicit go.
- **The owner's steps are the owner's:** AWS and gcloud logins, `tofu apply`, secrets (typed into hidden prompts, never pasted in chat), the Slack app console, merging their own PRs (#137), and live tests that post as people.
- **Never:**
  - merge the Cedar branches (#109, `feat/rag-authz`, `feat/kb-policies-web`);
  - edit the owner's untracked root `CLAUDE.md`;
  - discard the "Linear integration work" git stash.
- **Workers:** one lane, one worktree, own files only. Commit after each item. No sub-agents.

## Production now

- **Live release** `611233f541a3`, from `deploy/production/release.auto.tfvars.json`: main with the onboarding CLI's workspace service (#125), #134 and #135.
  - **#125:** the CLI itself is **off**, since `COGNITO_CLI_CLIENT_ID` is empty.
  - **#134:** the Worker refuses CLI tokens for members' conversations.
  - **#135:** the relevance judge runs in **shadow**: it logs and holds nothing.
  - Health on the last check: 0 errors. The only WARN is Dodo's missing key (a free beta, on purpose).
- **Hardening is applied:**
  - IAM narrowed;
  - alarms in us-west-2 and ap-south-1 (SES);
  - `/v1/internal` blocked at the edge;
  - the sign-in rate limit.
- The **database** upgrade to `db.t4g.small` is pending for the maintenance window on **Sun 2026-10-04, 11:00 UTC**.
- **Not deployed:** Slack, the phase-2 integrations, the agent workspace (#137).

## Open PRs, in merge order

| # | Branch | State | Next step |
|---|---|---|---|
| **#137** | `codex/agent-presentation` (the owner's) | Review fixed (12 findings), CI green, mergeable | **The owner decides.** Merge it first: it and #140 both touch `web-next/src/components/workspace-shell.tsx`. |
| **#140** | `slack/base` → main | The Slack consent loop, slices 1–3, **switched off**. CI green. A production-safety review of "merging while off" was running when the session hit its limit: rerun it if its result isn't in the PR. | After #137: merge main into `slack/base` and fix the shell conflict (H's `openRequestsSection` must also call #137's `closePrimaryWork()`). Rerun CI, merge, then follow the Slack runbook below. |
| **#141** | `lane/i-2a` → `slack/base` (draft) | The integrations runtime (phase 2A), reviewed, one read-only bug fixed | Once #140 is merged, retarget to main, rerun CI, and merge. |
| **#139** | `lane/i-2b` → main (draft) | Knowledge's apps extractor (2B), reviewed | Merge after #141, whose pages route it calls. |
| #109 | Cedar (draft) | Parked: 3 high-severity bypasses | Do not merge. |

**The merge sequence, as commands.** Run each only after its CI is green. Each merge deploys.

1. #137 is the owner's call: `gh pr merge 137 --merge`, then wait for the Deploy run: `gh run list --branch main`.
2. Update #140:
   - `git fetch origin && git switch slack/base && git merge origin/main`;
   - resolve `workspace-shell.tsx` so `openRequestsSection` also calls `closePrimaryWork()`;
   - run `go build ./... && go vet ./...`, and `npm run typecheck && npm test && npm run build` in `web-next/`;
   - `git push origin slack/base`, then wait for `gh pr checks 140`.
3. Run the production-safety check below on #140, and fix anything it finds.
4. Apply the Slack runbook's step 1 (tofu, switch off), then `gh pr merge 140 --merge`. After the rollout: `deploy/scripts/provision-app.sh`.
5. `gh pr edit 141 --base main`; update the branch (`git merge origin/main`), wait for CI, `gh pr ready 141`, then `gh pr merge 141 --merge`.
6. `gh pr ready 139 && gh pr merge 139 --merge`.

**The production-safety check for #140.** It must be clean before merging. Answer each for production with the loop OFF: `AGENTS_PRIMARY_SLACK_CONSENT` unset, `APPS_SLACK_SURFACE=false`, no Slack secrets, rules not yet widened.
1. **Migrations on live data:**
   - apps `008_slack_surface`;
   - interactions `013`–`015`, which widen `agent_interactions` checks `NOT VALID` then validate them against existing rows;
   - slackrouter lock 719, surfaces 720, secretshares 721.

   Do they run at start? Could any fail on existing rows, or clash on a lock id?
2. **Startup** with production's env (`deploy/production/compute.tf`): does every service start? Does any new variable validation refuse? Is the event-queue guard (`ORYXA_REQUIRE_EVENT_QUEUES`) still satisfied?
3. **agents' inbound consumer:** with the loop off, is every event that succeeded before still handled, with no new errors?
4. **The now-wired `GuardPolicy`:** could an agent-request answer now be held or fail closed where it wasn't? Relevance is capped at shadow.
5. **realtime:** the purge relay and its signature are unchanged, and nothing new arrives until the rules widen.
6. **web-next:** Requests, Settings → Slack and the DM request line render and fail soft against today's Go.
7. **Tofu** with `slack_app_ready=false`: no reference to a missing secret, nothing that stops tasks starting, and the IAM policies stay valid.

Behaviour change when #140 merges, even switched off: `interactions.Config.GuardPolicy` is now wired (main never wired it). Members' "What your agents share" levels and the org floor now apply to agent-request answers, and relevance runs on them in shadow.

## The Slack runbook, after #140 merges

Details are in `deploy/README.md` → "The Slack bot" and `docs/design/slack-implementation.md` §5.

1. **Apply tofu with the switch off.** It needs the owner's AWS session (`aws login`) and their OK. Run `cd deploy/production && tofu init && tofu plan -out=slack-off.tfplan`, review the plan, then `tofu apply slack-off.tfplan`. `slack_app_ready` stays false. The apply adds the consent guardrail and its version, the `agents-secrets` KMS key, and empty secret containers. If `tofu` complains about Google credentials, the owner runs `gcloud auth application-default login`.
2. **Merge #140.** CD releases Go, then the Worker. Wait for the rollout to finish.
3. **Run `deploy/scripts/provision-app.sh`.** It widens the agents and realtime rules to the new exact event types.
4. **The owner:**
   - api.slack.com → Create from manifest `apps/internal/slackbot/manifest.yaml`;
   - `deploy/scripts/put-secrets.sh` (a hidden prompt for the client id, client secret and signing secret; the ticket key is generated);
   - commit `deploy/production/slack.auto.tfvars.json` with `{"slack_app_ready": true}`, then apply. This turns on `AGENTS_PRIMARY_SLACK_CONSENT` and `APPS_SLACK_SURFACE` together;
   - verify both request URLs in the Slack console;
   - install through **Oryxa → Settings → Slack → Add to Slack**, never Slack's own Install button.
5. **Live test in the owner's workspace** (§5, slice 1). Both people need the standing agent grant, or the screen refuses the ask with `no_grant`. Tune the screen thresholds in `slackrouter/screen.go` and the grounding minimums in `consentguard` on real asks.

## The road to a design-partner launch (about 1–2 days)

1. Ship #140 and run the Slack runbook above.
2. **Dry-run as an outsider:** sign up → create a workspace → invite → connect Drive and Gmail (Composio) → grant → Add to Slack → ask in a channel. It has never been run end to end.
3. Turn on public distribution in the Slack console, then invite partners.

**Blockers for a public launch:**
- **Cognito email:** sign-ups and password resets use Cognito's default sender, capped at **50 emails a day**. Move it to SES.
- **Slack Marketplace review:** it needs 10+ workspaces first.
- **Legal pages:** check that `oryxa.in/legal/privacy` and `/legal/terms` cover the Slack bot and Google data.
- **Polish:** the empty web pages.

## Lanes ready to hand out

- **Integration apps**, after #141 merges: one agent per app, owning `apps/internal/integrations/<app>/**` only, following the "Work-package template" in `docs/design/integrations-plan.md`. The order:

  | Lane | SDK |
  |---|---|
  | **I-GITHUB** | `go-github` (GitHub's own `octokit/go-sdk` is still alpha) |
  | **I-SLACK** | `slack-go` (Slack has no official Go SDK; this is members' own Slack, not the Oryxa bot) |
  | **I-GMAIL, I-GDRIVE, I-GCAL** | Google's official `google.golang.org/api`, already in `go.mod`, sharing phase 1's `google` sign-in |
  | **I-NOTION** | Notion's REST API through `Session.HTTP` (there is no official Go SDK) |

  Knowledge has no calendar extractor, so I-GCAL decides that. The gates: G1 CI plus `cmd/check`; G2 review; G3 the owner's live smoke test; G4 enabling it (the registry line plus `APPS_ORYXA_TOOLKITS`), which is the orchestrator's step.
- **Web**, after #137 merges: W-HOME ("Today" in place of the empty overview and the fake saved views), W-HISTORY, W-FILES (check against #137's files panel first), W-PANEL, W-WORKFLOWS, W-CMDK. Details are in LANES.md.
- **Guard:**
  - **G-SURFACES:** side conversations send no `sources` to the check route (`web-next/src/lib/side/asking.ts` `guardCheckBody`), so the judge never runs on them.
  - **G-ORG:** confirm the org guard settings come from the workspace policy.
- **The owner's Codex, for the CLI (#125 follow-ups):**
  1. Organization domain and guest changes should count as sensitive.
  2. The secret scanner misses `gho_…` and `rk_live_…` keys.
  3. `--yes` approves the caller's own change.
  4. Migration 009 marks old invitations quiet.

  Also CLI-ON: set `cli_client_id` in `deploy/production/app.auto.tfvars.json` to the Cognito CLI client that `provision-app.sh` created, then apply.

## Prompts to hand an agent

**An integration app**, after #141 merges; swap in the app:
```text
Read docs/STATUS.md, docs/LANES.md and the "Work-package template" in docs/design/integrations-plan.md. Take lane I-GITHUB: create branch lane/i-github from origin/main in a new worktree, and open a draft PR titled "[I-GITHUB] GitHub integration". Own only apps/internal/integrations/github/**, follow the template's steps exactly, and use the SDK STATUS.md names for this app. Build end to end: MCP tools (each read or write, with its scopes), readers into knowledge, and triggers (poll, or push through hooks). Record cassettes from the provider's documented responses. Pass go test ./apps/internal/integrations/github/... and go run ./apps/internal/integration/cmd/check github. List every tool, reader and trigger in the PR. Do not enable the toolkit (gate G4 is the orchestrator's).
```
**A web lane**, after #137 merges:
```text
Read docs/STATUS.md and docs/LANES.md, then web-next/AGENTS.md and web-next/DESIGN.md in full. Take lane W-HOME: create branch lane/w-home from origin/main in a new worktree, and open a draft PR titled "[W-HOME] Home overview". Edit only the files the lane owns, show no example data, give every block its own loading, empty and error states, and run npm run typecheck, npm test and npm run build in web-next before marking it ready.
```

## Design documents

- **Slack:** `docs/design/slack-consent-loop.md` is the design. `docs/design/slack-implementation.md` has the contracts, the lanes and the §5 live checklist.
- **Integrations:** `docs/design/integrations-plan.md`.
- **The owner's direction:** `docs/north-star.md`.
- **web-next:** `web-next/DESIGN.md`, `PARITY.md` and `REALTIME.md`.

## Follow-ups noticed, not yet lanes

- **CI coverage:** CI never sets `KNOWLEDGE_`, `FILES_`, `REALTIME_` or `OUTBOX_TEST_DATABASE_URL`, so those DB tests have always skipped there. `SLACK_` was added in #140.
- **Slack:**
  - Refused Slack asks don't count toward the 5-open cap. Add a per-asker hourly cap.
  - Requests has no navigation badge.
  - The asker's `type:"request"` thread items aren't rendered in the agent timeline yet. Do it after #137, which edits the timeline.
  - The Slack identity is a confirmed Slack email, so a Slack workspace admin could pose as a member. The fix is explicit account linking.
- **Docs:** the `knowledge/README.md` provider table needs the apps reader. REMAINING-WORK.md:562 says the Cloudflare zone is Free, but it is **Enterprise**.
- **The owner's local Postgres:** an empty leftover database `apps_test` in `/opt/homebrew/var/postgresql@18`, made by an agent's test run.

## How to work here, fast

- **Go:** `go build ./... && go vet ./...`, then `go test -race -count=1 ./<pkg>/...`. DB tests read `<SERVICE>_TEST_DATABASE_URL`. Use a **throwaway** cluster (`initdb` plus `pg_ctl` on a free port under a temp dir), never the owner's Homebrew Postgres. The Slack end-to-end test is `SLACK_TEST_DATABASE_URL=… go test -timeout 15m ./test/slack`, about 90 s.
- **web-next:** `npm run typecheck && npm test && npm run build`. Install only with `npx pnpm@12.4.2 install --frozen-lockfile`: four TanStack dependencies track "latest", and a plain install bumps them.
- **Deploy checks:**
  - `gh run list --branch main`. A CI run cancelled by a newer push triggers a Deploy run that skips itself; that is normal.
  - `aws ecs describe-services --cluster oryxa-production --services oryxa --region us-west-2`.
  - Logs are in CloudWatch Logs Insights, group `/oryxa-production/app`, us-west-2, e.g. `fields @timestamp, level, msg, error | filter service_version like /^<sha12>/ and level != "INFO"`.
  - Health: `https://api.oryxa.in/healthz` (204) and `https://app.oryxa.in/` (200).
- **OpenCode (the owner's free-model side agents):**
  - Run: `OPENCODE_CONFIG=/Users/shubham/Projects/oryxa-go-worktrees/opencode-lanes.json opencode run --auto -m opencode/space-bunny-free --variant max --dir <worktree> --title "[LANE] …" "<self-contained brief>"`.
  - The config denies push, gh, AWS, tofu, docker, curl, installs, and the owner's Postgres. The orchestrator reviews, pushes and opens PRs.
  - Use only **Space Bunny** or **LongCat 2.5 Preview**, the only zero-retention free models. Space Bunny's free week may end about now; then use `opencode/longcat-2.5-preview-free`.
- **Worktrees** live in `/Users/shubham/Projects/oryxa-go-worktrees/`:
  - `slack-*`: remove them after #140 merges;
  - `oc-i2a` and `oc-i2b`;
  - `agent-presentation`, for #137;
  - `authz-core`, `rag-authz` and `kb-policies-web`: Cedar, parked;
  - `docs-*`.
