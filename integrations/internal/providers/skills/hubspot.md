# HubSpot

A subscription watches a HubSpot account's CRM objects and asks for HubSpot's
own event types, named `object.event`: `contact.creation`,
`contact.propertyChange`, `deal.creation`, `ticket.propertyChange`. A
subscription that asks for none receives every event the HubSpot app
subscribes to.

## Events

Each delivery is named for the event type: `hubspot.contact.creation`,
`hubspot.deal.propertyChange`. The delivery names the portal it happened in, so
it reaches only the subscriptions of the connections belonging to that account.
A `propertyChange` event carries the property's name and its new value.

HubSpot posts up to a hundred events in one request, so a single delivery can
carry several unrelated changes. Treat each as its own event.

An event names an **object ID**, not a person. To say anything useful about it,
read the record with `hubspot_get_contact` or a search.

## Tools

Say what you are about to do before you write anything. When an action fails
with `needs_reconnect`, the member's HubSpot connection has to be reconnected
before anything will work — say so rather than trying again. `scope_required`
means the connection was not granted the scope that action needs.

Reading:

- `hubspot_get_contact` — one contact by ID. Always pass `properties` when you
  want specific fields; the default set is small.
- `hubspot_search_contacts` — free text across the searchable properties, or an
  exact email address. Search is the way to turn an email into an ID.

Writing:

- `hubspot_create_contact` — needs an email address, which HubSpot treats as the
  contact's unique identifier. Creating one that already exists is refused
  rather than merged; search first, then update.
- `hubspot_update_contact` — pass `id_property: "email"` to update by email
  address instead of by ID.
- `hubspot_create_company` — needs a name.
- `hubspot_create_note` — attaches a note to a contact's timeline. A note with
  no contact is invisible in the CRM, so one is required.

Prefer one precise update to several, and do not create a contact to correct a
detail on one that already exists.
