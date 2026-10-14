// Package call runs Baseline B's calls end to end against a running edge
// (B8, docs/design/live-layer/03-baseline-realtimekit.md): two members join
// a room's call the way the call screen does, through web-next's binding to
// the edge's WebEdge entrypoint, forward their own transcript, hear the
// agent on /voice/live, and read what the room logged.
//
// It checks:
//
//   - the call's lifecycle: join, rejoin, leave, the last one out ending it;
//   - who is refused: a non-member, no session, someone not in the call;
//   - the transcript: each member's own words in the log, another speaker's
//     refused, the transcript saved to R2 when the call ends;
//   - the agent: silent until addressed, answering by voice to everyone in
//     the call, stopped when talked over, dismissed and recalled;
//   - a dispatch round trip to agents and back, private to its member;
//   - a dropped socket and a rejoin, and a deploy during the call;
//   - and, as a skeleton for Layer C's calibration, the voice latency.
//
// It skips unless CALL_LIVE_ROOM_URL is set. Everything it needs comes from
// the environment:
//
//	CALL_LIVE_ROOM_URL   where a room's edge API is behind the door, as a
//	                     template: {org}, {kind} and {id} are filled in, e.g.
//	                     https://app-staging.oryxa.in/edge/v1/organizations/{org}/rooms/{kind}/{id}
//	CALL_LIVE_DOOR       "web" (default): members are web-next sessions, sent
//	                     as Cookie with the room URL's origin as Origin.
//	                     "local": test/live/call/door in front of `wrangler
//	                     dev`, which takes the member from x-live-identity.
//	CALL_LIVE_ORG        the organization.
//	CALL_LIVE_ROOM       kind/id of a room both members are in, e.g.
//	                     teamspace/tsp_123. Optional when the test seeds its
//	                     own rooms (below); then each test gets a fresh one.
//	CALL_LIVE_A_ID, CALL_LIVE_A_COOKIE, CALL_LIVE_B_ID, CALL_LIVE_B_COOKIE
//	                     the two members: their user ids (the speaker on
//	                     their transcript) and, for the web door, a session
//	                     cookie header value.
//	CALL_LIVE_OUTSIDER_ID, CALL_LIVE_OUTSIDER_COOKIE
//	                     someone in the organization but not the room. The
//	                     local door makes one up; the web door skips that
//	                     check without one.
//	CALL_LIVE_AGENT_NAME the voice agent's name (the edge's VOICE_AGENT_NAME,
//	                     default Krude).
//
// Seeding: with EDGE_URL and EDGE_SIGNING_KEY_PEM (the private half of a key
// the edge trusts, as edgelive uses), the test makes a fresh room for each
// test and makes both members its members, by reading its log with an agent
// token for each. CALL_LIVE_TEAM is the team claim it seeds with. Without
// them, CALL_LIVE_ROOM must already exist on the edge with both members in it
// (A18 pushes membership from Go), and the tests share it, one at a time.
//
// Optional, because they cost money or time or need more access:
//
//	CALL_LIVE_DISPATCH=1           a real dispatch to agents (agents-staging)
//	CALL_LIVE_DEPLOY_WAIT=5m       hold a call open while someone deploys the
//	                               edge, then check it survived
//	CALL_LIVE_LATENCY_SAMPLES=10   ask that many questions and report latency
//	CALL_LIVE_REPORT=path.json     where the latency report goes
//	CALL_LIVE_R2_ENDPOINT, CALL_LIVE_R2_BUCKET, CALL_LIVE_R2_ACCESS_KEY_ID,
//	CALL_LIVE_R2_SECRET_ACCESS_KEY read the saved transcript back from R2
//
// On staging, once the call screen's binding is deployed:
//
//	CALL_LIVE_ROOM_URL='https://app-staging.oryxa.in/edge/v1/organizations/{org}/rooms/{kind}/{id}' \
//	CALL_LIVE_ORG=org_… CALL_LIVE_ROOM=teamspace/tsp_… \
//	CALL_LIVE_A_ID=usr_… CALL_LIVE_A_COOKIE='…' CALL_LIVE_B_ID=usr_… CALL_LIVE_B_COOKIE='…' \
//	go test -count=1 -v ./test/live/call
//
// Locally, before staging's binding exists: test/live/call/door stands in for
// web-next, test/live/call/fakertk for RealtimeKit, and a key of your own for
// agents'. wrangler dev needs a copy of edge/wrangler.jsonc without its
// remote bindings (ai, browser, containers), and a .dev.vars beside it with
// AGENTS_TOKEN_PUBLIC_KEYS=["<base64 DER SPKI of the key>"],
// REALTIMEKIT_ACCOUNT_ID, REALTIMEKIT_APP_ID, REALTIMEKIT_API_TOKEN (any
// values) and REALTIMEKIT_API_URL=http://127.0.0.1:8790. Then:
//
//	go run ./test/live/call/fakertk &
//	(cd edge && pnpm exec wrangler dev --local --enable-containers=false \
//	  -c ../test/live/call/door/wrangler.jsonc -c /path/to/edge.local.json --port 8788) &
//	CALL_LIVE_ROOM_URL='http://127.0.0.1:8788/edge/v1/organizations/{org}/rooms/{kind}/{id}' CALL_LIVE_DOOR=local \
//	CALL_LIVE_ORG=org_livecall CALL_LIVE_A_ID=usr_a CALL_LIVE_B_ID=usr_b \
//	EDGE_URL=http://127.0.0.1:8788 EDGE_SIGNING_KEY_PEM="$(cat key.pem)" \
//	go test -count=1 -v ./test/live/call
//
// The lifecycle, refusals and transcript pass there; anything that needs the
// agent to speak stops at "voice model not configured". Touching an edge
// source file mid-call restarts it, which stands in for a deploy.
//
// The agent never joins the RealtimeKit meeting (the virtual participant,
// discussion #217), so nothing here needs WebRTC: a member's client is its
// HTTP calls and its /voice/live socket. Whether the RealtimeKit token really
// joins the meeting, and audio and video between people, are the call
// screen's own check (B6).
package call
