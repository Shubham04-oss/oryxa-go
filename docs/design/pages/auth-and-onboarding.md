# Signing in and joining (`/auth/*`, `/onboarding`, `/invitations/*`) · Phase 5

**Job:** get a person in, and make the first five minutes make sense.

## Today

Sign-in, sign-up, forgot password, reset password, verify email, onboarding,
and invitation accept. They work. They are also the only part of the product a
person meets before they know anything, and they currently hand straight over
to a home screen that opens with "What do you want to move forward?" and a
taxonomy of work modes.

## Becomes

The screens themselves change very little. What changes is what the first
minute after them says.

- **One column, centred, the product's name in real type**, the form, and
  nothing else. No feature list, no illustration.
- **Every error is about the person's attempt**, not the system's state: "That
  email and password do not match" rather than a code.
- **Invitations say who invited you and to what**: "Priya invited you to
  Simulated Org" above the accept button, with the organization's name in the
  page title.

## The first five minutes

This is the part worth designing, and it does not exist yet.

After the first sign-in, one screen, three questions, each skippable:

1. **What should your agent be called?** — with a suggested name. Naming the
   agent is what makes it yours, and it is currently generated for you
   (`Maya-XFZP`, with a random suffix a person would never choose).
2. **What do you do?** — one line, in their words. This is what routing reads
   when somebody asks the room a question nobody was named for, and today
   nothing ever asks for it.
3. **Connect one app** — or skip.

Then land them in a chat with their agent, with its first message already
written, referring to what they just said. Not on a dashboard.

## Words

| Now | Becomes |
| --- | --- |
| Maya-XFZP | a name the person chose, or "Maya" |
| Primary agent | your agent |

## Open question

The random suffix on agent names (`-XFZP`, `-RGMI`) makes handles unique in a
room. If people name their own agents, two "Sam"s in one room need
disambiguating — the room's handle generator already does this and should be
the only place it happens.
