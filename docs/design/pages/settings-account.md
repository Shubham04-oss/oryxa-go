# Account settings (`/app/settings/profile`) · Phase 5

**Job:** your name, your organizations, and what your agents may do while you
are away.

## Today

Well written, and carrying the two most consequential switches in the product:

- **Agent access** — "Your agents can no longer act for you while you're away.
  They can still help while you're here, and you can grant this again any
  time."
- **Updates from your apps** — "When your connected apps report something,
  Krude wakes up and briefs you… Turn it off to hear nothing; what arrives
  while it is off is not kept."

Both sentences are exactly right. The problem is that they sit in a flat list
of settings rows, below Profile and Organizations, weighted the same as the
field for your display name.

There is also one leaked internal name: **Krude**, which is the agent's
codename in our code, not a word any person has met.

## Becomes

- **Two groups, in this order:** *What your agents may do* first, then *You* —
  name, email, organizations, sign-in. The consequential things come first.
- Each agent-boundary switch states its current effect in one line beneath,
  in the present tense: "Your agents are acting for you while you are away."
  A switch whose effect you have to infer from the label is a switch people get
  wrong.
- `Krude` becomes "your agent" or the agent's own name.
- Leaving an organization stays behind a confirm dialog and keeps its excellent
  consequence copy.

## Words

| Now | Becomes |
| --- | --- |
| Krude wakes up and briefs you | your agent wakes up and briefs you |
| Background agent access | What your agents may do while you are away |
| Team call emails | Email me when an agent calls my team |
