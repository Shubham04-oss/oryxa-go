# Dots

An agent should never sit behind a spinner. A spinner says "wait"; a dotted
orb says what kind of work is being done, and in a product where half the
participants are agents, that difference is most of the reassurance.

## The four

| Shape | State | When |
| --- | --- | --- |
| **Sphere** — Fibonacci points, dense at the rim, brighter at the front, turning slowly | Thinking | an agent is taking its turn in a room |
| **Globe** — latitude rings, turning | Searching | reading knowledge, files, or an app |
| **Scatter** — a cloud with a few outliers drifting past the edge | Working | a tool is running |
| **Ring** — a circle of dots that breathes rather than spins | Waiting on you | an approval is sitting with somebody |

Four, and no more. A fifth would mean inventing a distinction the product does
not actually make.

## Rules

1. **Dots mean something is happening.** Not a texture, not a background, not
   a decorative grid. When nothing is running, the screen has no dots on it.
2. **The shape carries the kind.** Never two orbs for the same state, never
   one orb for two states.
3. **Always beside words.** `reading the April invoice`, not a lone orb. The
   sentence is what carries it for anyone who cannot see motion, and it is
   also what makes the wait tolerable.
4. **One per line.** 20px inline beside a name, 46px when the orb is the only
   thing on the screen. It takes the avatar's place; it never covers it.
5. **Motion is optional.** Under reduced motion the dots hold still. Nothing
   is lost, because the shape and the sentence were carrying it anyway.

## Where they appear

- **In a room**, in the place the answer will land: the agent's avatar, its
  name, and the orb with what it is doing. Today a room simply goes quiet for
  twenty seconds and nobody can tell working from broken.
- **In your agent's rail**, on an offer that is waiting on you.
- **On a run**, in the agents list, with the step it has reached.
- **Never** on page load, on a list fetch, or anywhere a skeleton would do.

## Drawing them

Generated, not drawn: positions come from geometry (a Fibonacci sphere, evenly
spaced latitudes, a seeded scatter), each dot 0.4–1px with opacity from depth,
white on black. That keeps them crisp at any size and means a new size is a
parameter rather than a new asset.
