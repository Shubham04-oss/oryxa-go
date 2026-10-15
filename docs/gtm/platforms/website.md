# The website: oryxa.in

## Why here

Every link ends here. Slack's and Google's reviewers read it. A champion
sends it to their boss. Search starts here. Today it sells the second act
with a demo gate ([positioning.md](../positioning.md#the-gap-with-the-landing-page))
and has no changelog, docs, blog or pricing page.

The site is the private `oryxa-website` repository (Next.js), not this
one; this page is what to change, and the routine that keeps it true.

## Who is there

Everyone: a buyer from LinkedIn, a developer from HN, a reviewer from Slack,
a partner's security person.

## The thing

**The hero says the ask, the first button is the sign-up, and the changelog
is updated every Friday.**

## The pages

| Page | What | When |
|---|---|---|
| `/` | The hero and buttons from [positioning.md](../positioning.md#the-gap-with-the-landing-page); the sixty-second video above the fold; "Where the team works" as section two (a teamspace, a DM, a side conversation, a parley, and "Slack stays"); "And your own agent" as section three; "How it stays yours" with four proof points; the logo strip corrected to the day-one apps; the GitHub link in the footer | Week 2 |
| `/partners` | [design-partners.md](../design-partners.md#the-partner-page) | Week 2 |
| `/changelog` | One entry per GitHub release, same words, with the Friday recording's still | Week 3, then weekly |
| `/docs` | The three `docs/beta` pages exist as MDX (getting started, first agent, data flow); add "Asking in Slack" and "Background access" | Week 4 |
| `/security` | The real answers: regions (AWS us-west-2, Cloudflare, SES in ap-south-1), models (Vertex AI, Bedrock) and their data terms, the 7-day purge, grants and revoke, the guard, how to report a vulnerability. The same words as [content-bank.md](../content-bank.md#objections-and-answers) | Week 4 |
| `/support` | The docs, the email, "same business day"; Slack's review wants it | Week 7 |
| `/blog` | The technical series, one a month, canonical here and cross-posted | Week 4 |
| `/legal/privacy`, `/legal/terms` | Checked to cover the Slack bot and Google data (a launch blocker in STATUS.md) before the Google verification is filed | Week 8 |
| `/pricing` | Not until Dodo is live; then the team plan, the seats plan, and the founding rate for partners | Week 13 |

Also: the OG image (the approve card on deep sea), the title tag per page
("Oryxa · An agent for everyone on your team"), privacy-respecting analytics
with no cookie banner (Cloudflare Web Analytics or Plausible), and one
optional field on sign-up, in web-next: "Where did you hear about Oryxa?"

## The routine

| When | What | Who |
|---|---|---|
| Friday | The changelog entry from the release: the agents produce the markdown, the owner merges it in the website repo | Agents, founder |
| Monthly | One blog post from the technical series | Founder, drafted by agents |
| Quarterly | Every page read against [positioning.md](../positioning.md): nothing on the site that isn't live, nothing live that the site doesn't say | Founder |
| After each dry-run | The sign-up path from the site's first button, as a stranger, with analytics confirming the referrer | macbookpro |

## Rules

- The site says only what is live. The roadmap lives on `/partners` under
  "what we don't have yet".
- No pricing page until money can be taken. No "Enterprise: contact us".
- No cookie banner: no cookies that need one.
- The demo video is the real product, re-recorded when the product changes.

## The first ten moves

1. The hero copy and the buttons.
2. The video above the fold.
3. `/partners`.
4. `/changelog`, fed by releases.
5. `/docs` from `docs/beta`.
6. `/security`.
7. The OG image and title tags.
8. Analytics and the sign-up source field.
9. `/support` and the legal check.
10. `/blog` with post #1.

## What we count

Visits by referrer; sign-up conversion from the first button; demo plays;
changelog visits (partners read it).

## Founder or agent

Agents write the changelog, docs and drafts. The owner merges in the
website repo and signs off copy.
