# 05: Testing and calibration (from day one, every stage)

Testing is built **with** each work package, not after it. A package is done only when its tests and its end-to-end check pass on staging, and then in production for our org.

## Layers of testing

| Layer | What | Where | Owner |
|---|---|---|---|
| **Unit** | Each DO class, each Go package | `edge/**` (Workers test environment with vitest), Go `_test.go` | each lane |
| **Contract** | Go and edge agree on every request, response and error | shared fixtures in `contracts/edge/v1/testdata`, run by both sides in CI | GO + EDGE |
| **End to end on staging** | The scenarios below, scripted, run on every merge to the live layer | `test/live/**`, the staging edge plus the a1 stack | TEST |
| **Isolation attacks** | Must-fail cases (below) | `test/live/isolation/**` | TEST |
| **Calibration** | Voice latency and behaviour; the model eval | `test/live/voice/**`, `test/live/models/**` | TEST + EDGE + GO |
| **Load and cost** | Many concurrent sandboxes, browsers and calls; cost per minute; budgets enforced | `test/live/load/**` | TEST |
| **Chaos** | Things breaking mid-flight | `test/live/chaos/**` | TEST |

## End-to-end scenarios (the release gate)

1. An agent runs Python in the room sandbox, writes files, starts a preview. The room sees the preview and the terminal. The log and R2 hold every step.
2. A sidekick runs a private task. Only its member sees it, and other agents can't read it.
3. An agent browses, hits a login, asks for handoff. A member takes over and hands back, the agent finishes, and the recording is in R2.
4. A call with voice, video and screen share between two members on desktop and phone. A live transcript with speaker labels.
5. The org agent is addressed and answers by voice. A real-work request is acknowledged, dispatched to Go, and the result is spoken or posted.
6. A member asks their sidekick something private during the call. The answer appears only in their panel. "Say this in the room" passes the room-audience guard.
7. The room-audience guard holds back a fact one participant may not know, and offers the owner's consent instead.
8. Gemini returns 429s. The run fails over to Sonnet, and the log shows it.
9. An org hits its budget. New runs stop with a clear message.
10. An org is deleted. Its R2 objects, logs, sandboxes and recordings are gone.

## Isolation attacks (each must be refused, and logged)

- A token for org A used on a room of org B.
- A token for room X used on room Y in the same org.
- An expired token; a replayed `jti`; a missing scope.
- A guessed DO name without a token.
- A private sandbox reached by another member's token.
- A sandbox reaching `169.254.169.254`, private IPs, or the internal API.
- A browser session's cookies or storage visible to another session.
- An R2 object read across orgs.
- A non-member requesting a call token.

## Voice calibration

- **Harness:** recorded speech clips (single speaker, overlapping speakers, accents, noise) played into a staging call. It measures:
  - speech end → first agent audio, median and 95th percentile;
  - turn-detection delay;
  - the model's time to first token;
  - text-to-speech time to first audio;
  - barge-in reaction time;
  - address detection precision and recall.
- **Network profiles:** a clean line; India ↔ us-west-2; 5% loss; 100 ms jitter.
- **Pass:** median at or under 1 s, 95th percentile at or under 1.5 s on the clean and India profiles; barge-in at or under 300 ms; address-detection false positives at or under 2%. These targets are tuned after the first measurements.

## Model calibration

- 15–20 real delegated tasks (brief, plan, research, sandbox fix, browse and extract), run on Gemini Flash, Sonnet and Opus.
- Recorded per run: success, cost, time.
- That sets the default and escalation rules in `modelrouter`.
- **Failover drill:** inject 429s and 5xx and check the switch, the circuit breaker and the logging.

## Chaos

- A container crash mid-exec.
- A SandboxDO, BrowserDO or RoomDO evicted and restarted mid-run.
- A Browser Run session dropping.
- A RealtimeKit reconnect.
- **A deploy of `edge/`, web-next or agents during a call and during a run** (#196's drain on the Go side; DO state survives on the edge).
- R2 write failures, retried without losing the log.

## Release gate

All ten scenarios, all isolation attacks refused, voice calibration passing, the failover drill passing, and load and cost numbers recorded. Done on staging, then in production for our org, then for everyone.
