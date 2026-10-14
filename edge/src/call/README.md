# Calls and the agent's voice (Baseline B)

This directory is integration-2's, together with `edge/src/voice/`. The plan is in [03-baseline-realtimekit.md](../../../docs/design/live-layer/03-baseline-realtimekit.md), and the decisions in discussion #217.

**How it works:**
- **Calls:** a room's call is one RealtimeKit meeting. Each member who joins gets their own RealtimeKit participant, which is deleted when they leave and when the call ends, revoking its 100-day token.
- **Transcript:** each member's client forwards its own speaker's live transcript to the room.
- **The agent:** the agent (VoiceDO) is a virtual participant. It listens through the transcript, and speaks through `voice.*` frames on `/voice/live`.
- **Real work:** goes to Go at `POST /v1/edge/dispatch`. The answer comes back at `/voice/result`.

## API (members, through WebEdge)

| Route | Body | Answer |
|---|---|---|
| `GET …/call` | | `{call: CallView \| null}` |
| `POST …/call/join` | `{name?}` | `{callId, meetingId, participantId, token, preset}`: the RealtimeKit token the client joins with |
| `POST …/call/leave` | | `{left}`; the last to leave ends the call |
| `POST …/call/end` | | `{ended}` |
| `POST …/call/transcript` | `{callId, segments: [{id, speaker, text, partial, at}]}` | `202 {accepted}`; `speaker` must be the sending member |
| `POST …/call/recording` | `{on}` | `{recording: {on, by, since}}`: the consent banner shows who and since when |
| `POST …/call/agent` | `{present}` | `{call}`: asks the agent in or dismisses it |
| `GET …/voice/live` (WebSocket) | | `voice.state`, `voice.speaking`, `voice.audio` (MP3, base64, a sentence each, in `seq` order), `voice.stop` |

The shapes are in `contracts.ts`.

**Go (edge token):** `POST /v1/rooms/{kind}/{id}/voice/result` takes a `DispatchResult` and needs scope `call:speak`. The result must be for the token's member.

**RealtimeKit:** `POST /v1/webhooks/realtimekit` takes webhooks signed with `rtk-signature`. It handles `meeting.ended`, `recording.statusUpdate` (copies the file to R2) and `meeting.transcript` (copies it to R2).

**R2:** `calls/{org}/{kind}/{room}/{callId}/` holds:
- `transcript.ndjson`: the live transcript, speaker-labelled, including the agent;
- `transcript.realtimekit`: RealtimeKit's post-call Whisper transcript;
- `recording/{id}.mp4`.

**The room log:** the event types are `call.*`, `transcript.segment`, `voice.dispatch` and `voice.result`.

## Setup (B7; macbookpro sets the secrets)

Calls answer 503 until this is done. Everything else in the Worker is unaffected.

**1. A RealtimeKit app.** Create it in the dashboard (Realtime → RealtimeKit), one for staging and one for production. Set the account id and the app id in `wrangler.jsonc` as `REALTIMEKIT_ACCOUNT_ID` and `REALTIMEKIT_APP_ID`. Then create an API token with the **Realtime Admin** permission:

```sh
wrangler secret put REALTIMEKIT_API_TOKEN [--env staging]
```

**2. The member preset.** Guests are out of this release, so there is no guest preset.

```sh
A=https://api.cloudflare.com/client/v4/accounts/$ACCOUNT/realtime/kit/$APP
curl -sX POST $A/presets -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' -d '{
  "name": "oryxa-member",
  "config": {"view_type": "GROUP_CALL", "max_video_streams": {"desktop": 9, "mobile": 4}, "max_screenshare_count": 1,
             "media": {"audio": {"enable_stereo": false}, "video": {"quality": "hd", "frame_rate": 30}, "screenshare": {"quality": "hd", "frame_rate": 5}}},
  "permissions": {
    "media": {"audio": {"can_produce": "ALLOWED"}, "video": {"can_produce": "ALLOWED"}, "screenshare": {"can_produce": "ALLOWED"}},
    "transcription_enabled": true,
    "can_record": false, "recorder_type": "NONE", "kick_participant": false, "hidden_participant": false,
    "chat": {"public": {"can_send": true, "text": true, "files": false}, "private": {"can_send": false, "can_receive": false, "text": false, "files": false}},
    "waiting_room_type": "SKIP", "show_participant_list": true, "can_edit_display_name": false
  },
  "ui": {"design_tokens": {"theme": "dark"}}
}'
```

- **Recording:** `can_record: false` is on purpose. Recording starts only through the edge (`/call/recording`), which logs who turned it on and shows the banner.
- **Ending calls:** `kick_participant: false` means members end a call through `/call/end`.

**3. The webhook.** Fetch RealtimeKit's signing key, and set `data.publicKey` as `REALTIMEKIT_WEBHOOK_PUBLIC_KEY`, a PEM var in `wrangler.jsonc`:

```sh
curl -s https://api.realtime.cloudflare.com/.well-known/webhooks.json
```

Then register the webhook:

```sh
curl -sX POST $A/webhooks -H "authorization: Bearer $TOKEN" -H 'content-type: application/json' -d '{
  "name": "oryxa-edge", "url": "https://edge.oryxa.in/v1/webhooks/realtimekit",
  "events": ["meeting.ended", "meeting.participantLeft", "recording.statusUpdate", "meeting.transcript"]
}'
```

**4. The voice.**
- Workers AI comes through the `AI` binding.
- Set the model key:

  ```sh
  wrangler secret put GEMINI_API_KEY [--env staging]
  ```

  The key needs access to `VOICE_MODEL` (default `gemini-flash-latest`).
- Optional vars: `VOICE_TTS_MODEL` (`@cf/deepgram/aura-1`), `VOICE_TTS_SPEAKER` (`angus`) and `VOICE_AGENT_NAME` (`Krude`).

**5. The edge→agents key** (contract §4). Generate a P-256 key pair once per environment:

```sh
node -e 'const {generateKeyPairSync}=require("crypto");const {publicKey,privateKey}=generateKeyPairSync("ec",{namedCurve:"P-256"});
console.log("EDGE_SIGNING_KEY="+JSON.stringify(privateKey.export({format:"jwk"})));
console.log("AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS="+JSON.stringify([publicKey.export({type:"spki",format:"der"}).toString("base64")]))'
```

- **Edge:** `wrangler secret put EDGE_SIGNING_KEY` takes the JWK. `AGENTS_URL` in `wrangler.jsonc` is where agents listens.
- **Agents:** set `AGENTS_PRIMARY_EDGE_TOKEN_PUBLIC_KEYS` to the array. It needs `AGENTS_PRIMARY_EDGE_URL` and `AGENTS_PRIMARY_SERVICE_KEY_ID` too; see `agents/primary/README.md`.
- **Rotating the key:** add the new public key to the array, switch the secret, then drop the old key.

**6. The WAF.** `POST /v1/edge/dispatch` on api.oryxa.in must be reachable from the edge. It is part of F4's `/v1/edge/*` allowance.

## Not in the baseline (Layer C)

- **Who the agent can see:** the room-audience guard on spoken answers, and sidekicks.
- **Voice quality:** latency calibration, streamed model output, Gemini → Sonnet failover.
- **Consent:** consent per participant beyond the recording banner.
- **Retention:** deleting call records.
- **Guests:** a guest preset and invitations.
