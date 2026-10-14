# How the integrations loop runs

The integrations service ([README.md](README.md)) is built a piece a day by an agent, and reviewed and merged by another, both as **Claude Code cloud routines**. They run in Anthropic's cloud, each in a fresh VM, so nothing runs or accumulates on a1, this MacBook or the Mac Mini.

## The two routines

| Routine | When | What it does | Rules |
|---|---|---|---|
| **integrations-loop** (builder) | Every 2 hours, day and night: :30 past even UTC hours (00:30, 02:30 … 22:30 UTC; 06:00, 08:00 … IST) | Takes the next ready issue from the queue (or fixes review comments first), builds it on a `loop/<issue>-<slug>` branch, tests it, pushes, opens one PR, and waits for CI | [loop-agent.md](loop-agent.md) |
| **integrations-review** (macbookpro) | Every 2 hours, an hour after the builder: :30 past odd UTC hours (01:30, 03:30 … 23:30 UTC) | Reviews every open `loop` PR: merges the good ones, sends the rest back with numbered fixes, and reports anything that needs the owner | The prompt in the routine ([§ The reviewer's prompt](#the-reviewers-prompt)) |

Both check out `Shubham04-oss/oryxa-go` and `Shubham04-oss/oryxa-platform`, and both act through the **Claude GitHub App** with the owner's GitHub identity.

```
 GitHub issues (label loop)          ┌───────────── Anthropic cloud ─────────────┐
   P0 … P6, "Depends on #N"   ──────▶│ integrations-loop (every 2 h)             │
                                     │   build → test → push loop/* → open PR    │
 PR (label loop) ◀───────────────────│                                           │
   CI (GitHub, a1 runners)           │ integrations-review (every 2 h, offset 1h)│
   ◀──── merge / request changes ────│   review → merge or numbered fixes        │
                                     └───────────────────────────────────────────┘
 main ──▶ CD deploys (the service ships switched off: INTEGRATIONS_ENABLED)
```

A cycle in the loop, every two hours, day and night:
1. **:30 past an even UTC hour:** the builder runs.
   - It first resumes a run paused for usage, or fixes review comments and failing CI.
   - Otherwise it takes the lowest-numbered ready issue.
   - It opens or updates one PR.
2. **The PR's CI** runs on GitHub, using a1's two existing CI runners.
3. **An hour later:** the reviewer merges it, or comments numbered fixes.
4. **An hour after that:** the builder applies the fixes, or moves on.

At most one issue per builder run, and **backpressure** (3 open loop PRs means no new work) keeps the reviewer from falling behind. Runs can overlap: the builder skips an issue another run is on (`loop:in-progress` with activity in the last 3 hours).

## The queue

- **Issues labelled `loop`.**
  - `loop:ready`: can be taken.
  - `loop:in-progress`: being built.
  - `loop:blocked-owner`: needs the owner or macbookpro.
  - `phase:P0`–`phase:P6`: the phases in the design, §18.
- **Order:** the builder takes the lowest-numbered ready issue whose `Depends on #N` issues are all closed. P0 and P1 are #283–#301.
- **Adding work:** macbookpro opens the next phase's issues when the current one is nearly done. The reviewer flags this when fewer than 3 ready issues remain. Anyone can add an issue in the same format: a title, the scope, `**Acceptance:**`, `Depends on #N`, and the labels.

## Usage limits

Both routines run on the owner's plan, so both keep **at least 5% of the 5-hour usage window free**:
- They check usage before starting and before each big step.
- At 5% or less left, or on a usage-limit error, they stop cleanly.
  - **The builder** pushes its work (a draft PR if it has none) and leaves a `Paused for usage` handoff note, so its next run resumes there ([loop-agent.md](loop-agent.md), § Usage limits).
  - **The reviewer** finishes or skips the PR it's on and reports what it didn't reach.
- Neither waits inside a run for the window to reset. **The two-hourly schedule is the retry.**
- **If paused runs keep showing up,** usage is too tight for this cadence. Make the builder's schedule sparser (for example `30 */4 * * *`) with `RemoteTrigger update`, after changing it here first.

## Safety

The builder acts with the owner's GitHub identity, so its limits are rules plus review, not permissions:
- **Instructions** ([loop-agent.md](loop-agent.md)):
  - never merge, approve, push to `main` or force-push;
  - never touch `.github/`, deploy, create credentials or copy code under a restrictive licence;
  - stay inside `integrations/**`.
- **Review:** every change goes through the reviewer, which blocks on security, scope, licence and missing tests.
- **The cloud sandbox:** a fresh VM per run, no access to our machines or AWS. Network is limited to the default allowlist (package registries, GitHub, Docker Hub). The GitHub proxy blocks branch deletions and tag pushes.
- **Production:** merges deploy, but the service ships switched off (`INTEGRATIONS_ENABLED`). Turning it on, its ECS service, secrets and OAuth apps are macbookpro's and the owner's steps, never the loop's.
- **Signing:** the builder signs posts `[agent:integrations-loop]` and the reviewer `[agent:macbookpro]`, so you can tell them apart even though GitHub shows the same account.

**Known limit:** nothing on GitHub *technically* stops a routine from merging, because it acts as the owner. If that ever matters more, the fix is a dedicated GitHub machine account for the loop, with write access but excluded from a branch ruleset on `main`.

## What the owner does

- **Once:**
  - install the Claude GitHub App on `oryxa-go` and `oryxa-platform`;
  - create a cloud environment with **Trusted** network access at claude.ai/code;
  - give its id to whoever sets up the routines (§ Setting up the routines).
- **Weekly-ish:** answer `loop:blocked-owner` issues, and do the live smoke test when an app's phase ends (P2–P5 exit criteria).
- **To pause:** switch a routine off at <https://claude.ai/code/routines>. **To run now:** "Run now" on the same page.

## Setting up the routines (a guide for the agent doing it)

Nothing here is set up yet. Whoever sets it up, the owner or an agent in a Claude Code session with the owner's go-ahead, follows these steps exactly.

### 0. Before you start

- **The Claude GitHub App** is installed on `Shubham04-oss/oryxa-go` and `Shubham04-oss/oryxa-platform` (claude.ai/code → GitHub).
- **A cloud environment** exists with **Trusted** network access (claude.ai/code → environments), and you have its id. The owner gives it to you; don't guess one.
- **Nothing already runs:** with the `RemoteTrigger` tool (load it with `ToolSearch select:RemoteTrigger`), call `{action: "list"}` and check no routine named `integrations-loop` or `integrations-review` exists. If one does, **update** it instead of creating a second.

### 1. The builder routine

`RemoteTrigger` `{action: "create", body: …}` with this body. Replace `ENVIRONMENT_ID`, and generate a fresh lowercase v4 UUID for `uuid`.

```json
{
  "name": "integrations-loop",
  "cron_expression": "30 */2 * * *",
  "enabled": true,
  "job_config": {
    "ccr": {
      "environment_id": "ENVIRONMENT_ID",
      "session_context": {
        "model": "claude-opus-5-5",
        "sources": [
          {"git_repository": {"url": "https://github.com/Shubham04-oss/oryxa-go"}},
          {"git_repository": {"url": "https://github.com/Shubham04-oss/oryxa-platform"}}
        ],
        "allowed_tools": ["Bash", "Read", "Write", "Edit", "Glob", "Grep", "WebFetch", "TodoWrite"]
      },
      "events": [
        {"data": {
          "uuid": "FRESH-UUID",
          "session_id": "",
          "type": "user",
          "parent_tool_use_id": null,
          "message": {"role": "user", "content": "You are the integrations loop agent for Shubham04-oss/oryxa-go. Read docs/design/integrations/loop-agent.md in the oryxa-go checkout and follow it exactly for today's run, starting at step 1. The design is docs/design/integrations/README.md; how the loop runs is docs/design/integrations/operations.md. oryxa-platform is checked out next to oryxa-go."}
        }}
      ]
    }
  }
}
```

### 2. The reviewer routine

The same shape, with these differences:
- `"name": "integrations-review"`;
- `"cron_expression": "30 1-23/2 * * *"`;
- `"model": "claude-opus-5-5"`;
- **only** the `oryxa-go` source;
- `allowed_tools` without `WebFetch`;
- `message.content` set to the reviewer's prompt below, verbatim.

### 3. Check

- `RemoteTrigger` `{action: "get", trigger_id}` for each: enabled, with the next run time shown in UTC.
- Optionally `{action: "run", trigger_id}` on the builder once, then `list_runs` and `get_run_log` to watch it.
- Tell the owner both routine links: `https://claude.ai/code/routines/<id>`.

### 4. Changing them later

- **Edit the prompts here first, then update the routine,** so this file stays the source of truth.
- **Schedules are UTC:** the builder runs at :30 past even hours, the reviewer at :30 past odd hours; IST is UTC+5:30. The minimum interval a routine allows is one hour.
- **Deleting** a routine is only possible at <https://claude.ai/code/routines>.

## Debugging a run

- **Runs and logs:** <https://claude.ai/code/routines> lists each run with its session. In Claude Code, the `RemoteTrigger` tool's `list_runs` and `get_run_log` show the same.
- **A fire that never started** (routine paused, a limit, GitHub disconnected) leaves no run. Check that the routine is enabled and its next run time. If GitHub stays disconnected for 72 hours, routines switch off.
- **Limits:** at least an hour between scheduled runs; 30 "run now" or API fires per hour per routine; a 4 vCPU, 16 GB VM.

## The reviewer's prompt

The reviewer routine's prompt is kept here so it can be recreated:

```text
You are macbookpro, the lead agent for Shubham04-oss/oryxa-go. The owner has authorized you to review and merge the integrations loop agent's pull requests on this schedule. Start every GitHub post with [agent:macbookpro]. Never print tokens or secrets.

Usage: the owner's plan has a 5-hour usage window; keep at least 5% of it free. Check usage (the /usage command, or the usage the session shows) before starting and before each PR review. At 5% or less left, or on any usage-limit or rate-limit error: stop starting new reviews, finish or abandon the current one without merging half-checked work, and end with a report naming the PRs you didn't reach. They wait for your next run in two hours. Never wait inside the run for the window to reset.

The rules the loop follows are in docs/design/integrations/loop-agent.md; the design it builds is docs/design/integrations/README.md; how the loop runs is docs/design/integrations/operations.md. Read all three first.

1. List open loop PRs: gh pr list --label loop --state open --json number,title,headRefName,mergeable,statusCheckRollup,reviewDecision,updatedAt. If none, report "no loop PRs" and go to step 5.
2. Skip draft PRs: they are builder runs paused for usage, not ready for review. Review each other PR, oldest first: gh pr view, gh pr diff, and the linked issue. Look only for what decides the outcome, not polish:
   - correctness against the issue's acceptance criteria and the design;
   - security (block on any): weakened egress handles, verification, SSRF checks, grants or the effect gate; secrets in code, logs or testdata; cross-org data access; code copied from Nango, Airbyte, Pipedream or n8n;
   - scope: only integrations/**, docs/design/integrations/**, or apps/ files the issue names; nothing under .github/;
   - tests for new behaviour, including one failure path; the connector gate where relevant;
   - CI green.
   For a diff over about 800 lines, use a review subagent for security and correctness and verify its findings yourself.
3. Act:
   - Good and CI green: gh pr merge <n> --merge. If mergeability is UNKNOWN, wait 10 s and retry. If CONFLICTING, comment asking the loop to merge main in. Then make sure the linked issue is closed, and remove loop:in-progress from it.
   - Needs changes: one comment with numbered, concrete fixes (file:line, why, the fix). Don't merge.
   - CI failing: don't merge. If it's clearly a flaky test unrelated to the PR, rerun the failed job once (gh run rerun <id> --failed).
   - Needs the owner (a design question, a security trade-off, production, infrastructure or secrets): don't merge; comment what's needed, and list it in the report.
4. After merging, check the Deploy workflow for the merge commit succeeded (gh run list --branch main --limit 3). The integrations service ships switched off, so a deploy changes nothing live; still report a failure prominently.
5. Queue health: count open loop:ready issues and list loop:blocked-owner ones. If fewer than 3 ready issues remain in the current phase and the next phase has none, say the queue needs the next phase's issues (design §18).

Report in under 15 lines: merged (PR numbers and titles), sent back (PR and a one-line reason), needs the owner, the queue's state, any deploy failure.
```
