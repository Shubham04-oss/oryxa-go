# 04: Layer C: the nuances (after both baselines are wired)

These start **only when Baselines A and B are wired end to end on staging**. Each one changes how the baseline *behaves*, so building them earlier means building them twice. Like the baselines, each must be wired end to end, from the member or agent, through the edge and Go, into the UI and the room log, with tests.

## C1. Rooms of shared and private work

- **The shared room sandbox** (`sandbox/shared`) for work everyone sees. **Private workspaces** (`sandbox/private-{member}`) for a sidekick's quiet work, visible only to that member. Browser sessions are per task, all listed by the RoomDO, and private ones are marked.
- The room log's `private` events are shown only to their member, and are excluded from what other agents read.
- Lanes: EDGE (RoomDO rules), GO (which workspace a run uses), UI (private panes).

## C2. Org agent and sidekicks

- **Org agent:** a room participant for everyone. It answers general and org questions from **org-scoped knowledge only**, speaks when addressed, and routes anything personal through that person's consent, live. It builds on the existing `teamspaces.EnsureOrganizationAgent`.
- **Sidekick:** each member's own agent hears the call (the shared transcript), replies **privately** in that member's side panel, with optional local speech on their device, handles quick tasks, and makes rare, opt-in nudges. It speaks to the room only when the member taps **"say this in the room"**.
- Only the org agent publishes audio. Sidekicks never speak into the call.
- Lanes: GO (roles, scopes, routing), EDGE (VoiceDO roles, the private channel), UI (sidekick panel, "say this in the room").

## C3. The room-audience guard

- Anything said or posted to the room contains only what **everyone present** may know (the intersection of their access).
- Anything more goes to the member privately, or asks the owner's consent in the call.
- It extends the answer guard (Jev plus Bedrock Guardrails) with a room-audience mode. **It fails closed.**
- Lanes: GO.

## C4. Voice behaviour and latency calibration

- **Target:** speech end → first audio, about 0.5–1 s at the median; at the 95th percentile, under 1.5 s.
- **Turn detection** by meaning, not just silence, and **barge-in** (stop the moment a person speaks).
- **Instant acknowledgements:** pre-recorded clips play while work starts.
- **Address detection**, so the agent knows it was spoken to, with measured false-positive and false-negative rates.
- **Warm context:** prefetch the doc in focus and the people mentioned.
- Speaker labels for overlapping speech, and echo cancellation.
- **Never call AWS on the voice fast path.** Real work goes through dispatch (B5).
- Measured with the harness in [05](05-testing-calibration.md), under real network conditions (India ↔ US, jitter, loss).
- Lanes: EDGE (VoiceDO), TEST (harness).

## C5. Model routing and failover

- **Gemini Flash** by default (Flash-Lite for the cheapest checks); **Claude Opus** via Bedrock for tough jobs; **Claude Sonnet** via Bedrock as the fallback and middle tier.
- On Gemini 429 or 5xx: a short retry, then **fail over to Sonnet** behind a circuit breaker.
- The model is logged per run; the **price table** is complete, so costs are right.
- A 15–20-task eval sets the Flash vs Opus line.
- Lanes: GO (`modelrouter`, `modelcache`, the price table), TEST (eval).

## C6. Budgets, limits and cost

- Per org, per agent and per run: a warning at 80% and a stop at 100%.
- Per org: concurrency caps for sandboxes, browsers and calls; CPU and call minutes.
- The cost of each task is shown. Unit economics are measured: cost per active member per month, and per room-hour of calls.
- Agents don't run the model while only listening: a cheap check wakes them; rolling summaries are used, not full transcripts.
- Lanes: GO + EDGE + UI.

## C7. Isolation hardening

- **Sandbox network rules:** no cloud metadata, no private ranges; an egress allowlist per org where set. Only scoped, short-lived tokens inside sandboxes, never our secrets.
- No shared browser profiles; cookies per session; no reuse across orgs.
- R2 access checks on every read.
- **Jurisdiction:** EU-only DOs and R2 for orgs that need it (per the open decision).
- Lanes: EDGE + INFRA, attack tests in TEST.

## C8. Consent, retention and deletion

- **Recording and transcription consent:** a banner; per-org policy; regions that need explicit consent.
- **Retention per org** for runs, recordings and transcripts in R2.
- **Deletion:** the existing purge relay (`realtime` → `POST /internal/realtime/purge`) extends to the edge. An org deleted or a member leaving wipes their rooms' logs, sandboxes, recordings and R2 objects.
- Lanes: GO (realtime purge), EDGE (purge handler), INFRA (lifecycle rules).

## C9. Observability

- Dashboards from the room logs and R2:
  - voice latency, median and 95th percentile;
  - tool latency;
  - failure rates;
  - cost per org and per model;
  - concurrency;
  - failover events.
- Alerts on latency above target, failover storms, budget stops and isolation-check refusals.
- Lanes: INFRA + TEST.

## Order inside Layer C

C3 and C1 first, because privacy comes before features. Then C2, then C4 and C5 together, then C6. C7, C8 and C9 run throughout and must be green before release.
