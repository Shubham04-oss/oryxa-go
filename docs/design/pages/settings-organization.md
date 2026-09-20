# Organization settings (`/app/settings/organization/*`) · Phase 5

**Job:** run the organization — who is in it, how it is structured, what it
pays for.

## Today

Six routes: Organization, Members, Teams, Tool access, Email, Billing.

**This is the best-written part of the product.** "Invitations work for 7 days.
Revoking one stops its link from working." "Some email always goes out, because
accounts depend on it." "Members connect their own apps; there is nothing here
to manage." That is the voice the rest of the app should have been using all
along, and the glossary in [direction.md](direction.md) is mostly an exercise
in making the work surfaces sound like these pages already do.

So the work here is small.

## Becomes

- **Teams absorbs `/app/teams`.** It gains the member list per team, add and
  remove, and create — see [people-and-teams.md](people-and-teams.md). It is
  the thinnest page in settings today and the one that has to get thicker.
- **One settings shell** with its own left sub-navigation, reached from a
  single sidebar link rather than six routes hanging off the main nav's memory.
- **Billing** repeats "Current plan" with the same description twice; the
  second is the receipts section and should say so.
- **Tool access** is an admin's read-only view of what members connected. It
  should say, once, why it cannot be managed here — it nearly does already —
  and link to the member's own Apps page.
- Roles get a colour: owner and admin badges in neutral, never in Ember, since
  nothing here is the primary action.

## Deleted

Nothing. This section is kept almost as written.

## States

- **Members, empty of invitations:** the pending section is not rendered at
  all, rather than an empty table.
- **Billing without a payment method:** one line about what happens at the end
  of the trial, with the action.
