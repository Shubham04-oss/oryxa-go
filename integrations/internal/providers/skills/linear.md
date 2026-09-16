# Linear

Linear delivers a workspace's changes to a webhook for one team, or for every
public team when a subscription names none. Only a workspace admin's API key,
or an OAuth connection with the `admin` scope, may create one.

## Events

A subscription asks for Linear resource types (`Issue`, `Comment`, `Project`),
and each delivery names the change as `linear.issue.create`,
`linear.comment.update` and so on. `remove` means the resource was deleted or
archived.

An issue has two names: its ID, a UUID, and its identifier, `ENG-42`, which is
what people recognise. The trigger's resource carries the UUID as `id`, the
title as `name`, and the issue's page as `url`. Say `ENG-42` to a person, and
use the UUID when calling Linear.

## Working with what arrives

The payload is Linear's own, including the issue's state, assignee, labels and
priority as they were at that moment. Priority is a number: 0 none, 1 urgent,
4 low. A delivery names the webhook it came from, so an event for a webhook
this subscription no longer has is dropped.

## Tools

Every tool that names an issue takes either its UUID or its identifier, such
as ENG-42 — pass whichever you already have. An identifier costs Linear an
extra lookup before it can act, so once a call gives you the UUID, such as
get_issue's `id`, carry that into the next call rather than the identifier.
Say the identifier to the member; nobody recognises a UUID.

Confirm the team before `linear_create_issue`, the exact wording before
`linear_comment_on_issue`, and which issue before `linear_close_issue`.
Closing moves the issue to its team's first workflow state of type
"completed"; a team with none refuses the close rather than guessing at one.

Only a workspace admin's connection can run any of this: the same API key or
`admin`-scoped OAuth connection a webhook needs. When a call fails with
`needs_reconnect`, the member's Linear connection has to be reconnected
before anything will work — say so rather than trying again.
