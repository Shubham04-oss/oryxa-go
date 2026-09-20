# Redesign plan

The product works. Using it does not feel like it works.

This folder is the plan for fixing that: what the app should become, screen by
screen, before a line of it is rebuilt. It is written to be read in an hour and
argued with, not admired.

## Read in this order

| Document | What it settles |
| --- | --- |
| [philosophy.md](philosophy.md) | The two references decoded, the one rule — pass as little as possible on to the user — and the ten things this app stops asking |
| [direction.md](direction.md) | Why the app feels the way it does, the six rules that fix it, and the navigation it collapses to |
| [foundations.md](foundations.md) | Black, eleven greys, white — the tokens every page draws from |
| [dots.md](dots.md) | The four orbs, the only thing in the product that moves |
| [icons.md](icons.md) | What may carry an icon, what carries a monogram instead, and what is banned |
| [components.md](components.md) | Cards, rows, buttons, badges, and the four states every surface owes a person |
| [pages/](pages) | One file per screen: what it is for, what is wrong, what it becomes |

The specimen — the room, the three colour candidates, type in place, and the
avatars — is a canvas beside this plan rather than a file in it; the room is
the thing to judge first.

## The pages

| Page | Route | Phase |
| --- | --- | --- |
| [Home](pages/home.md) | `/app` | 3 |
| [Chats](pages/chats.md) | `/app/threads`, `/app/threads/$id` | 3 |
| [Rooms](pages/rooms.md) | `/app/teamspaces` | 3 |
| [Agents](pages/agents.md) | `/app/agents` | 4 |
| [Library](pages/library.md) | `/app/knowledge`, `/app/files` | 4 |
| [Apps](pages/apps.md) | `/app/connections` | 4 |
| [People and teams](pages/people-and-teams.md) | `/app/teams` — deleted, absorbed | 2 |
| [Organization settings](pages/settings-organization.md) | `/app/settings/organization/*` | 5 |
| [Account settings](pages/settings-account.md) | `/app/settings/profile` | 5 |
| [Signing in and joining](pages/auth-and-onboarding.md) | `/auth/*`, `/onboarding`, `/invitations/*` | 5 |
| [The shell](pages/shell.md) | sidebar, header, search | 2 |

## Order of work

Each phase is shippable on its own and leaves the app better than it found it.

1. **Foundations.** Tokens, icon rules, component contract. Nothing moves on
   screen except colour and type. One PR, no behaviour change.
2. **The shell.** Eight nav items to five, the mode labels go, People and teams
   is absorbed. One PR.
3. **The two screens that matter.** Home and Rooms, end to end. One PR each.
4. **The rest of the work surfaces.** Agents, Library, Apps.
5. **Settings, auth, and dark mode.**

Phases 1 and 2 are prerequisites for everything after them. Within phases 3–5
the order is free.

## What this plan does not do

It does not change what the product does, what any endpoint returns, or how
agents behave. Every change here is naming, arrangement, colour, and what gets
deleted. Where a change needs a new field from the API, the page's document
says so under **Needs from the backend** — there are four of them in total.
