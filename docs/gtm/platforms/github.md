# GitHub

## Why here

The repository is already public, and it is the most honest thing we have:
[STATUS.md](../../STATUS.md), [north-star.md](../../north-star.md), the
design documents, the board and the discussions where three agents
coordinate in the open. Developers judge a company by its repo. Slack's and
Google's reviewers look for a real company behind an app. Cloudflare's and
Google's developer teams look for showcases. All of that is one URL:
[github.com/Shubham04-oss/oryxa-go](https://github.com/Shubham04-oss/oryxa-go).

Today it has no license, no description, no topics, no releases, one star,
and a README that opens with "an AWS-backed Go monorepo". Nothing links it
from the website.

## Who is there

Go developers, people building agent infrastructure, engineers evaluating
Oryxa for their team, and anyone who followed a post. The three agents are
already there, which is itself a story ([content-bank.md](../content-bank.md), #29).

## The thing

**A weekly release with notes a member can read**, a product README on top
of the engineering one, and public discussions where outsiders get an
answer within a day.

## The routine

| When | What | Who |
|---|---|---|
| Daily, 15 min | Answer every new issue or discussion from outside within 24 hours, even with "not now, and here is why". Label it `from:outside` | Founder |
| Thursday | Draft the release notes from PRs merged since the last release: three headings, **For members**, **For admins**, **Under the hood**, each item one sentence with its PR link; anything internal cut; "see it" links to Friday's recording | Agents |
| Friday | Edit the words, tag main's Friday commit `v2026.11.06` (the date), publish the release. The website's changelog pulls from it | Founder |
| Monthly, first Monday | A "State of the build" post in Announcements: what's live, what's next, what we're unsure about, from STATUS.md | Founder, drafted by agents |
| Quarterly | Refresh topics, description, the README's five lines; check the license still says what we mean | Founder |

Main deploys on every merge, so a release is a weekly roll-up, not a
deployment. Say so in the first release's notes.

## Rules

- **No license, no promotion.** Decide it in week 1 ([README.md](../README.md#decisions-for-the-owner)).
  An unlicensed public repo is "all rights reserved" by default, and the
  first HN comment will ask.
- Never ask for stars. Never add "Star us" to a post.
- Partner names never appear in the repo, the board or the discussions. The
  design-partner pipeline is a private project.
- The agents keep posting with `[agent:name]`. It stays as it is; it is the
  content.
- Issues from outsiders are triaged within a day; a bug a stranger found is
  fixed before a feature a partner asked for, when it is in the path.
- No secrets in issues, and no screenshots of production data, ever.

## The first ten moves

1. The license decision and the `LICENSE` file.
2. Five lines above the README: the bio line, the sixty-second demo, "Try
   it: app.oryxa.in", "How it's built: docs/north-star.md, docs/STATUS.md",
   the license.
3. Repository description ("An agent for everyone on your team. Ask in
   Slack; the person who knows approves with one click."), homepage
   `https://oryxa.in`, topics: `go`, `ai-agents`, `slack-app`, `adk-go`,
   `cloudflare-workers`, `durable-objects`, `eventbridge`,
   `team-collaboration`.
4. `SECURITY.md`: how to report a vulnerability, the address, what to expect.
5. `CONTRIBUTING.md`, honest: "We don't take code contributions yet; issues
   and discussions are welcome, and answered."
6. Labels: `from:outside`, `partner`, `good-question`, `docs`.
7. A pinned "Start here" in Announcements for people arriving from a post:
   what Oryxa is, what to read, how to try it, how to ask.
8. The first release, `v2026.11.06`, with notes in the format above.
9. The Q&A category opened to outsiders; the agents' internal categories
   stay as they are.
10. A decision, not a move: whether to move the repo from the user account to
    an `oryxa` organization. It reads better, and it breaks every
    `--owner Shubham04-oss` in `.claude/CLAUDE.md` and the agents' commands.
    If yes, do it in a holiday week with every agent paused.

## What we count

Weekly, from Insights → Traffic: unique visitors, clones, referrers. Stars,
because ecosystems look. Issues and discussions opened by outsiders, and
the median time to our first reply. Sign-ups that name GitHub as where they
heard of us.

## Founder or agent

Agents draft notes, label, digest. The founder publishes and answers anyone
from outside.
