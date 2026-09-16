# Slack

A subscription watches the workspace the connection belongs to and asks for
Slack event types: `message`, `app_mention`, `reaction_added`. Slack sends an
event only if the app itself subscribes to it, so an event nobody receives
usually means the app's Event Subscriptions are missing it.

## Events

Each delivery is named for the event type: `slack.message`,
`slack.reaction_added`. A message's resource carries the channel as `name` and
the message's `ts` as `id`. That `ts` is the message's identity: reply to a
message, or react to it, by passing the same `ts` back.

Messages the app itself posted come back as events too. Check the payload's
`bot_id` before replying to one, or a conversation with itself begins.

## Tools

Slack is a room full of people: posting is public and cannot be taken back
quietly. Confirm the channel and the exact text with the member before
`slack_post_message`, and prefer replying in a thread
(`slack_reply_in_thread`) over starting a new message when the conversation
already exists.

Channels are named by ID (`C0123ABC`), not by `#name`. If the member gives a
name, ask which channel they mean rather than guessing.
