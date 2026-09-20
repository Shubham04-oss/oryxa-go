# People and teams (`/app/teams`) — deleted · Phase 2

**Job today:** "People organized around the work, so assistants reach the right
ones."

**Verdict:** the page should not exist.

## Why

There are two team pages in this product: `/app/teams` (People & teams) and
`/app/settings/organization/teams` (Teams). They manage the same objects. The
first is in the main navigation, one click from everything, and does nothing a
person does during work — it lists teams and, until you click one, shows a
large empty box reading "Choose a team to see its people."

It also breaks the pattern the app sets elsewhere: Teamspaces auto-selects the
first item in the same master–detail arrangement. Two pages, two behaviours,
same shape.

## Where it goes

| What it did | Where it lives now |
| --- | --- |
| Which teams exist, creating and renaming them | **Settings → Organization → Teams** |
| Who is on a team, adding and removing people | **Settings → Organization → Teams → a team** |
| Who I am working with right now | The **room's rail** — "In this room" |
| Finding a colleague | ⌘K search, which already indexes people |

## What must be true before deleting it

1. The settings Teams page gains what this one had: a member list per team with
   add and remove, and the create-team action. It is the weaker of the two
   today.
2. The rooms list makes a team's room reachable in one click, since "open my
   team's room" is the only journey this page really served.
3. `/app/teams` redirects to `/app/settings/organization/teams` rather than
   404ing — people will have it bookmarked.
