# 03: Baseline B: RealtimeKit (voice, video and agents in calls)

Goal: every room can hold a **call** (voice, video, screen share) through Cloudflare RealtimeKit, with a **shared transcript** in the room log, and an **agent that can join, listen and speak**, handing heavy work to the Go work agent on AWS.

This is the baseline. The behaviour rules (sidekicks, the room-audience guard, latency tuning, routing) are Layer C, and come after this is wired.

It must be **wired end to end**:

member clicks "Start call" → RoomDO creates or joins the RealtimeKit meeting → participant tokens → call UI → transcription → room log + R2 → VoiceDO joins as the agent → voice in, voice out → dispatch to Go for real work → result back into the call and the room → covered by tests

Depends on: the foundation (F1–F6). It can run **in parallel with Baseline A**.

## Work packages

| ID | What | Lane | Owns | Depends on |
|---|---|---|---|---|
| B1 | **Call lifecycle in RoomDO**: create the RealtimeKit meeting for the room on first start; issue participant tokens after checking room membership, with presets per role (`member`, `agent`, later `guest`); track who is in the call; end it; log `call.*` events. | EDGE | `edge/src/call/**`, the call part of `edge/src/room/**` | F2 |
| B2 | **Transcription into the room log**: RealtimeKit transcription segments with speaker labels become `transcript.segment` events in the RoomDO, then R2 `calls/…/transcript.ndjson`. One transcript per room call, shared by every agent. | EDGE | `edge/src/call/transcript*` | B1 |
| B3 | **Recording**: on or off per the decision in the README, with the consent banner state stored in RoomDO; files to R2 `calls/…/recording/`. | EDGE + UI | `edge/src/call/recording*` | B1 |
| B4 | **VoiceDO: the voice agent pipeline**: joins the meeting as the `agent` participant; speech in → speech-to-text → the fast voice model (decision in the README) → text-to-speech → speech out; streams both ways; basic turn-taking, plus stopping when a person speaks. Baseline only answers when addressed. | EDGE | `edge/src/voice/**` | B1 |
| B5 | **Dispatch to the work agent**: VoiceDO sends real work (knowledge, tools, the browser, consent) to Go at `POST /v1/edge/dispatch {room, member, request, transcriptContext}` and gets an immediate acknowledgement. When Go finishes, it calls `POST edge …/voice/result`, and VoiceDO speaks it at a pause or posts it in the room. | GO + EDGE | `agents/primary/internal/edgedispatch/**`, the route under `agents/primary/internal/httpserver` (new file only), `edge/src/voice/dispatch*` | F3, B4 |
| B6 | **Call UI**: start, join and leave a call; mic, camera and screen share; device picker; grid and speaker views; mobile layout; in-call chat tied to the room; the agent shown as a participant with a speaking indicator; the live transcript pane; the consent banner. | UI | `web-next/src/components/call-*`, `web-next/src/lib/call/**` | F5, B1 |
| B7 | **Infra**: the RealtimeKit app, organization and presets set up through wrangler/tofu or a scripted setup; secrets; speech-to-text and text-to-speech provider keys, if not on Workers AI; staging. | INFRA | `deploy/**`, `edge/wrangler.jsonc` | F4 |
| B8 | **Tests**: call lifecycle; tokens refused for non-members; transcript in the log and R2; the agent joins and answers when addressed; dispatch round trip; reconnect after a network drop; a deploy during a call. Plus the latency harness skeleton (calibration itself is Layer C). | TEST + each lane | `test/live/call/**` | all |

## Parallel plan

- **EDGE** (B1, B2, B4) and **UI** (B6) work at the same time against the call part of the contracts. B6 uses RealtimeKit's own UI kit or core SDK, and starts with two people in a call before the agent exists.
- **GO** (B5) builds the dispatch endpoint against fixtures while VoiceDO is being built.
- **INFRA** (B7) runs first, because B1 needs a working RealtimeKit app on staging.

## Done means (production, our org first)

1. Members in a room start a call with voice, video and screen share, on desktop and phone.
2. The room log holds a live, speaker-labelled transcript, and R2 holds it after the call.
3. The agent joins, is addressed by name, and answers by voice.
4. A request that needs real work is acknowledged at once, done by the Go agent, and its result is spoken or posted.
5. Recording follows the decided default, with the banner.
6. Non-members can't join; tokens expire; isolation tests pass.
