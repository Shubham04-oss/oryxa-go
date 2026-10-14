# How the integrations loop runs

The integrations service ([README.md](README.md)) is built a piece a day by an agent, and reviewed and merged by another, both as **Claude Code cloud routines**. They run in Anthropic's cloud, each in a fresh VM, so nothing runs or accumulates on a1, this MacBook or the Mac Mini.

## The two routines

| Routine | When | What it does | Rules |
|---|---|---|---|
| **integrations-loop** (builder) | Daily, 20:30 UTC (02:00 IST) | Takes the next ready issue from the queue (or fixes review comments first), builds it on a `loop/<issue>-<slug>` branch, tests it, pushes, opens one PR, and waits for CI | [loop-agent.md](loop-agent.md) |
| **integrations-review** (macbookpro) | Twice daily, 04:00 and 16:00 UTC (09:30 and 21:30 IST) | Reviews every open `loop` PR: merges the good ones, sends the rest back with numbered fixes, and reports anything that needs the owner | The prompt in the routine ([§ The reviewer's prompt](#the-reviewers-prompt)) |

Both check out `Shubham04-oss/oryxa-go` and `Shubham04-oss/oryxa-platform`, and both act through the **Claude GitHub App** with the owner's GitHub identity.

```
 GitHub issues (label loop)          ┌───────────── Anthropic cloud ─────────────┐
   P0 … P6, "Depends on #N"   ──────▶│ integrations-loop (daily)                 │
                                     │   build → test → push loop/* → open PR    │
 PR (label loop) ◀───────────────────│                                           │
   CI (GitHub, a1 runners)           │ integrations-review (twice daily)         │
   ◀──── merge / request changes ────│   review → merge or numbered fixes        │
                                     └───────────────────────────────────────────┘
 main ──▶ CD deploys (the service ships switched off: INTEGRATIONS_ENABLED)
```

A day in the loop:
1. **02:00 IST:** the builder runs. If any loop PR has review comments or failing CI, it fixes that first. Otherwise it takes the lowest-numbered ready issue.
2. **The PR's CI** runs on GitHub, using a1's two existing CI runners.
3. **09:30 IST:** the reviewer merges it, or comments numbered fixes.
4. **Next night:** the builder applies the fixes, or moves on.
5. **21:30 IST:** a second review pass catches anything late.

## The queue

- **Issues labelled `loop`.**
  - `loop:ready`: can be taken.
  - `loop:in-progress`: being built.
  - `loop:blocked-owner`: needs the owner or macbookpro.
  - `phase:P0`–`phase:P6`: the phases in the design, §18.
- **Order:** the builder takes the lowest-numbered ready issue whose `Depends on #N` issues are all closed. P0 and P1 are #283–#301.
- **Adding work:** macbookpro opens the next phase's issues when the current one is nearly done. The reviewer flags this when fewer than 3 ready issues remain. Anyone can add an issue in the same format: a title, the scope, `**Acceptance:**`, `Depends on #N`, and the labels.

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
  - tell macbookpro its id.
- **Weekly-ish:** answer `loop:blocked-owner` issues, and do the live smoke test when an app's phase ends (P2–P5 exit criteria).
- **To pause:** switch a routine off at <https://claude.ai/code/routines>. **To run now:** "Run now" on the same page.

## Debugging a run

- **Runs and logs:** <https://claude.ai/code/routines> lists each run with its session. In Claude Code, the `RemoteTrigger` tool's `list_runs` and `get_run_log` show the same.
- **A fire that never started** (routine paused, a limit, GitHub disconnected) leaves no run. Check that the routine is enabled and its next run time. If GitHub stays disconnected for 72 hours, routines switch off.
- **Limits:** at least an hour between scheduled runs; 30 "run now" or API fires per hour per routine; a 4 vCPU, 16 GB VM.

## The reviewer's prompt

The reviewer routine's prompt is kept here so it can be recreated:

```text
You are macbookpro, the lead agent for Shubham04-oss/oryxa-go. The owner has authorized you to review and merge the integrations loop agent's pull requests on this schedule. Start every GitHub post with [agent:macbookpro]. Never print tokens or secrets.

The rules the loop follows are in docs/design/integrations/loop-agent.md; the design it builds is docs/design/integrations/README.md; how the loop runs is docs/design/integrations/operations.md. Read all three first.

1. List open loop PRs: gh pr list --label loop --state open --json number,title,headRefName,mergeable,statusCheckRollup,reviewDecision,updatedAt. If none, report "no loop PRs" and go to step 5.
2. Review each, oldest first: gh pr view, gh pr diff, and the linked issue. Look only for what decides the outcome, not polish:
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
