# PR #125 review tasks

Review supplied by the user against `1f25057`. Latest `origin/main` was merged
into the published PR branch before fixes, preserving the branch's history.
Fixes are grouped into commits named with their task IDs and regression tests.
Fakes must reproduce real services' normalization and authorization rules.
Unchecked tasks remain open; a checked task links to its implementation evidence
in the commit history. This is a condensed tracking copy of the supplied review.

## P0

- [x] **T01 — Normalize before planning.** Agents trims description/instructions
  and collapses name whitespace; auth trims organization names. Raw block-scalar
  trailing newlines currently prevent convergence and any version/policy save.
  Normalize in spec and validate nonblank org names <=120 characters. Prove one
  apply saves version 1 for `|`, `>` and double spaces with realistic fakes.
- [x] **T02 — Restrict CLI sessions.** Mark CLI-client tokens as a distinct token
  class, introspect `cli:true`, and allow only setup/workspace routes plus whoami
  across auth, agents, apps, files and other services. No owner-wide ordinary
  member access (decisions/reviews, threads, files, org deletion). Eliminate
  plaintext session tokens: memory or encrypted short-lived storage, purge after
  setup/apply. CLI must validate the nonce it retained against a verified ID
  token; server binds the nonce to a challenge it issued and consumes it once.
  Scope each setup session to one organization. Tests for route denials and mismatched nonce.
- [x] **T03 — Bind approvals to server plans.** Plan returns plan_id, plan_hash,
  base_version; persist normalized document hash. Apply presents plan_id, server
  replans and rejects changed document, base version or sensitive changes as
  409 stale_plan. Sensitive changes include removals, role changes/elevations,
  >25 invitations and loosening holdForReview/consent expiry/tool offer. Missing
  or fabricated approval cannot authorize deletes or policy loosening. Bind
  deletion approvals to resource IDs, not just reusable names/emails.
- [x] **T04 — Omitted member role preserves existing role.** Only new invitees
  default to member. Listing owner/admin without role plans nothing. Role errors
  name the person; role changes count as sensitive.
- [x] **T05 — Drop retained stale facts.** Retained ownership/chart/directory
  facts for departed users or deleted teams must not block unrelated plan/apply.
  Drop and show a plan note, keeping explicit invalid submissions errors.
- [x] **T06 — Plan owner-only checks.** Admin cannot update organization, change
  member roles or invite admins. Fail at plan with named reason before side
  effects, using ActorRole and auth's actual rules.
- [x] **T07 — New team with prune converges.** Team creation automatically seats
  creator as admin; later prune must not wedge on that seat or produce unapproved
  team_admin deletion. Handle expected creator seat or a no-creator API option.
  First apply converges; second is noop, including new lead variant.
- [x] **T08 — Mirror team naming.** Trim names, reject controls/format characters,
  count runes <=120, match/dedupe by lower(name). Engineering vs engineering must
  not create duplicate; leading space converges; Sales/sales fails validation;
  41 CJK characters accepted, including directory group.team and pull.
- [x] **T09 — Record partial apply.** Push knowable failures into plan (T06/08/
  16/17/24); record progress or a partial version for unpredictable failures,
  report completed work, and allow resume. Partial state must not activate
  unapplied policies. Inject failure midway and prove next apply finishes.

## P1

- [x] **T10 — Small tolerant policy projection.** Save normalized effective
  settings as small jsonb beside version. Read tolerantly, ignoring unknown keys,
  missing means defaults. Version-keyed memo/shared reader; never reparse up to
  1MiB strict manifest on each operation. New unknown manifest key must not break
  runtime reads after rollback. Also consider strict previous() recovery.
- [x] **T11 — Preserve answers on policy error.** Respond currently errors after
  approved work ran, leading to repeated work and eventual failed request. On
  policy read error hold answer for review rather than losing it. Regression.
- [x] **T12 — Exit codes and JSON errors.** 1 general, 2 needs_human, 3 only input/
  validation, 5 only apply lock. Decode structured API code, don't equate every
  409 with lock. JSON failures always stdout {step,status:failed,error}. Table
  test 401/403/500/network/taken slug/apply lock/stale_plan.
- [x] **T13 — Keep stdout clean.** Sign-in prompts/chatter to stderr; pull/init
  stdout YAML, whoami stdout JSON. Cached pull output must parse as manifest.
- [x] **T14 — Apply timeout and token headroom.** Client 20s currently cancels
  server's 2-minute apply; use long apply/rollback timeout. Refresh before long
  operation when remaining token TTL cannot cover budget. Test slow apply and
  almost-expired cached token.
- [x] **T15 — Admin-safe status.** Stored prune document must not make status
  502 for admins. Convert planning errors to needs_human, compute safe drift;
  joiners not executable deletion plans for nonowners.
- [x] **T16 — Deleted mapping target recovery.** Stale external group resource_id
  causes Save failure after team recreated. Detect before mutations; explicit
  sensitive rebinding or deletion-event cleanup. Reapply same file converges.
- [x] **T17 — Static duplicate mapping references.** Reject duplicate (source,
  team)/(source,member) before IDs exist and before any mutation, named reason.
- [x] **T18 — Preserve meaningful apply errors.** Downstream 400/403/409 must not
  become bare 502. Preserve bounded sanitized structured reasons for each status;
  removal-plan-changed stays 409 inside apply too. Never expose credential bodies.
- [x] **T19 — secret_in_manifest.** Validate all free text before persistence or
  model context: reject Slack xox tokens, ghp_, sk_live_, AKIA, JWTs and long
  high-entropy credentials, naming field without printing secret. Handle normal
  opaque external IDs safely. Pattern regressions.
- [x] **T20 — Quiet revoke stays quiet.** Persist quiet on invitation, carry in
  revoke event, skip withdrawn email. Refuse invitation emails with empty accept
  link; review rolling compatibility. Quiet prune sends no mail.
- [x] **T21 — Explicit empty domains.** Preserve explicit emptiness so domains: []
  survives CLI serialization and clears org domain restriction.
- [x] **T22 — Null offer must fail.** offer:, offer: ~ and all-commented list
  must fail with use [] for none or omit key for all. Nil must not fail open.
- [x] **T23 — Exact org rollback.** Rollback to omitted domains/timezone/guests
  clears later restrictions or explicitly says settings retained; preferably
  exact reset to defaults. v2 domains acme.com/guests false -> v1 defaults.
- [x] **T24 — Preflight live invite domains.** Validate new invitees against live
  effective domains/guest policy when omitted in manifest; fail before mutation.
- [x] **T25 — No silent WORKSPACE_URL policy bypass.** Loader reads database, not
  URL. Split deployments without URL must enforce existing workspace schema or
  at minimum WARN startup if enforcement disabled.
- [x] **T26 — Preserve policy error causes.** Apps/knowledge should wrap/log
  cause alongside unavailable classification so diagnostics expose invalid
  stored policy rather than only unavailable. Do not leak secrets to clients.
- [x] **T27 — Policy skips don't exhaust autosubscribe.** Claim increments attempts;
  refund or check before claim on policy block/error. Off then on must resume.
- [x] **T28 — Policy read outside batching transaction.** Avoid second pool
  connection while transaction/advisory lock held. Pool=2 concurrent records
  test must finish.
- [x] **T29 — ReplaceCredential respects offer.** Check allowToolkit before live
  credential proof/provider call or persistence; unoffered toolkit returns 403.
- [x] **T30 — Above rejects unknown target.** Executive is not above arbitrary,
  empty or typo addresses. Target must be known chart/joined member.
- [x] **T31 — Forget current departed identities.** Consume member.removed and
  team-deleted events; delete mappings/bindings so next apply has no stale facts.
  Document explicit historical version retention/redaction decision.
- [x] **T32 — Held answer review window and scope.** Give held request answers a
  full independent review window (e.g.24h), even if work finished 1m before
  consent expiry. Docs must specify agent-request ledger only; room posts,
  teamspace publications and side conversations have no review state.

## P2

- [x] **T33 — Pending connect-app needs_human.** Rerun pending connection and
  polling timeout reissue URL and exit2, not3.
- [x] **T34 — Concurrent CLI sign-ins.** Fixed8765 blocks concurrent/abandoned
  runs; random port if provider supports it or registered fallback ports. Release
  promptly. Explain device flow support honestly; two simultaneous signins.
- [x] **T35 — Validate server API URL at startup.** Bad nonloopback HTTP
  WORKSPACE_API_URL/AUTH_URL fails boot clearly, or distinct internal client
  permits intentional internal HTTP. Don't defer failure to every request.
- [x] **T36 — Embed timezone database.** CLI time/tzdata, reject Local; validate
  America/New_York without host zoneinfo.
- [x] **T37 — Large directory context.** context() 2MiB cap fails >3500 members.
  Use /members or at least 8MiB matching directoryclient. 10k-member test.
- [x] **T38 — Preview/unknown offer catalog.** Unknown offer should show all
  offered catalog rather than empty list while API is unavailable or mismatch;
  preview grid with organization set regression.
- [x] **T39 — Safe YAML roundtrip.** Multiline leading tabs can serialize invalid
  YAML and fail after mutations. Quote unsafe strings or reparse output early;
  parse->YAML->parse equivalence over 20k random strings.
- [x] **T40 — Hermetic CLI tests.** No real cache or production API: temp session
  dirs and test servers on every path. Tests work with populated real cache/no
  outside network.

## P3

- [x] **T41 — Server owns first apply.** CLI creates only organization, then all
  plan/apply use API. Remove duplicate local runtime apply for bootstrap.
- [x] **T42 — Workspace service boundaries.** Save resolved mapping IDs from
  runtime, no auth/teams table reads, no cross-service foreign keys.
- [x] **T43 — Settings catalog and explicit unset.** Define setting metadata once
  for validate/merge/plan/pull/projection; new fields must not silently vanish.
  Add explicit unset semantics for offer, expireAfter, wakeUpWithin, etc.
- [x] **T44 — Shared policy Provider.** Export interface/noop; handle typed nil
  loader safely, central shared cache/provider across service instances.
- [x] **T45 — Shared HTTP validation/tracing.** Shared ValidateAPIURL, runtime
  client methods/transport, observability.Transport, cleantext.Check in workspace
  decoders. Prevent control text passing plan then failing downstream.
- [x] **T46 — Reduce repeated calls.** Pass snapshots where available, use slug
  lookup for IDs, concurrent bounded team reads, set-based mapping sync rather
  than 3 queries/identity, context cache by org/version while respecting live
  authorization and membership changes.
- [x] **T47 — Small API cleanup.** cliClientID plain string rather than ignored
  variadic extra arguments; avoid repeated Above graph build where safe. The only production caller
  evaluates one freshly membership-filtered chart per lookup; cross-request
  graph caching was not added because that would retain authorization context.

## Guard fields requested

Commit `4212dbb` from `patch/guard-workspace-fields` was cherry-picked as
`bfa3272`. It adds safety.guard.pii.minimum.owner/peer and safety.guard.relevance
to spec, runtime policy projection and merge. `88192ac` documents all three keys
in onboarding docs/design/agent help. Subsequent catalog and projection changes
preserve these fields and test them.
The guardpolicy adapter in agents/primary/service.go remains a follow-up
after #125 merges, as requested in the user-supplied review. Latest main
(`a591ee4`, including #131 relevance judging) was merged as `56bdc50`.
`a3da21d` classifies the member-visible workspace directory tools for its new
source coverage check; mixed private reads still require judging.

## Additional audit findings

- [x] H01 OAuth configuration/token redirects and raw provider-error disclosure.
- [x] H02 Local plaintext token cache removed; OS credential store or process memory only, with anchored legacy-cache removal.
- [x] H03 Slack mutating GET RPC must not bypass proxy write protection.

- [x] H04 Omitted regulated preset silently disabled its runtime projection; preserve omission and review explicit preset changes (`397209a`).
- [x] H05 Auth and spec disagreed about multibyte organization-name length; count Unicode characters consistently (`1154b60`).
- [x] H06 An offer refetch failure retained connection actions from cached permission data; withdraw actions while preserving the catalog.

- [x] H07 Bound encoded pending OAuth URLs before invoking OS credential storage (`1e425a1`).

## Verification

- Frontend: formatting, lint, typecheck, all 1,383 tests, and production build
  pass. Desktop and
  mobile Tools preview inspected locally.
- PostgreSQL: isolated `oryxa_security_20260929` database, with all nine service
  test database variables enabled. Targeted projection, guarded rebinding,
  lifecycle cleanup, partial-progress, review-window and small-pool batching
  regressions pass. Final `go test -count=1 -json ./...` passes: 183
  packages and 5,223 tests/subtests; 86 tests are skipped and
  51 packages have no tests. This includes the latest main merge.
- Go build and vet pass. Race checks for CLI/spec/runtime/projection and focused
  HTTP normalization pass; auth/session and workspace store race tests also
  pass against PostgreSQL.
- No production deployment or provider configuration has been performed in this
  review pass. Cognito fallback callback URLs and the workspace event queue must
  be provisioned before releasing the corresponding service configuration.

## Implementation commits

| Task | Commits |
| --- | --- |
| T01 | [c961370](https://github.com/Shubham04-oss/oryxa-go/commit/c961370), [25b5b10](https://github.com/Shubham04-oss/oryxa-go/commit/25b5b10), [70210a3](https://github.com/Shubham04-oss/oryxa-go/commit/70210a3), [43bb111](https://github.com/Shubham04-oss/oryxa-go/commit/43bb111) |
| T02 | [b11988f](https://github.com/Shubham04-oss/oryxa-go/commit/b11988f), [d9e982b](https://github.com/Shubham04-oss/oryxa-go/commit/d9e982b), [ce1a950](https://github.com/Shubham04-oss/oryxa-go/commit/ce1a950) |
| T03 | [a5bb781](https://github.com/Shubham04-oss/oryxa-go/commit/a5bb781), [1ed73c3](https://github.com/Shubham04-oss/oryxa-go/commit/1ed73c3), [51df9c2](https://github.com/Shubham04-oss/oryxa-go/commit/51df9c2), [c6b185e](https://github.com/Shubham04-oss/oryxa-go/commit/c6b185e) |
| T04 | [be22155](https://github.com/Shubham04-oss/oryxa-go/commit/be22155), [d143863](https://github.com/Shubham04-oss/oryxa-go/commit/d143863) |
| T05 | [14d1245](https://github.com/Shubham04-oss/oryxa-go/commit/14d1245) |
| T06 | [9170896](https://github.com/Shubham04-oss/oryxa-go/commit/9170896) |
| T07 | [cd430e0](https://github.com/Shubham04-oss/oryxa-go/commit/cd430e0) |
| T08 | [282373b](https://github.com/Shubham04-oss/oryxa-go/commit/282373b), [bbcae7b](https://github.com/Shubham04-oss/oryxa-go/commit/bbcae7b) |
| T09 | [bb14742](https://github.com/Shubham04-oss/oryxa-go/commit/bb14742), [a354937](https://github.com/Shubham04-oss/oryxa-go/commit/a354937), [31870a4](https://github.com/Shubham04-oss/oryxa-go/commit/31870a4) |
| T10 | [94db29d](https://github.com/Shubham04-oss/oryxa-go/commit/94db29d), [e818fe2](https://github.com/Shubham04-oss/oryxa-go/commit/e818fe2), [0f0bcdc](https://github.com/Shubham04-oss/oryxa-go/commit/0f0bcdc), [48fb7dc](https://github.com/Shubham04-oss/oryxa-go/commit/48fb7dc), [ee0af8b](https://github.com/Shubham04-oss/oryxa-go/commit/ee0af8b) |
| T11 | [084faa9](https://github.com/Shubham04-oss/oryxa-go/commit/084faa9) |
| T12 | [82dd757](https://github.com/Shubham04-oss/oryxa-go/commit/82dd757) |
| T13 | [eff5d9c](https://github.com/Shubham04-oss/oryxa-go/commit/eff5d9c) |
| T14 | [9168cc1](https://github.com/Shubham04-oss/oryxa-go/commit/9168cc1) |
| T15 | [af31b39](https://github.com/Shubham04-oss/oryxa-go/commit/af31b39), [9364076](https://github.com/Shubham04-oss/oryxa-go/commit/9364076) |
| T16 | [b786bc9](https://github.com/Shubham04-oss/oryxa-go/commit/b786bc9), [700dcbd](https://github.com/Shubham04-oss/oryxa-go/commit/700dcbd) |
| T17 | [940743e](https://github.com/Shubham04-oss/oryxa-go/commit/940743e) |
| T18 | [b9dbb0a](https://github.com/Shubham04-oss/oryxa-go/commit/b9dbb0a) |
| T19 | [5dedfb9](https://github.com/Shubham04-oss/oryxa-go/commit/5dedfb9), [6db4bb7](https://github.com/Shubham04-oss/oryxa-go/commit/6db4bb7) |
| T20 | [6d80c83](https://github.com/Shubham04-oss/oryxa-go/commit/6d80c83) |
| T21 | [e83f419](https://github.com/Shubham04-oss/oryxa-go/commit/e83f419) |
| T22 | [3a3fe4b](https://github.com/Shubham04-oss/oryxa-go/commit/3a3fe4b) |
| T23 | [c977030](https://github.com/Shubham04-oss/oryxa-go/commit/c977030) |
| T24 | [e8883e8](https://github.com/Shubham04-oss/oryxa-go/commit/e8883e8) |
| T25 | [cee3600](https://github.com/Shubham04-oss/oryxa-go/commit/cee3600), [42e9fe9](https://github.com/Shubham04-oss/oryxa-go/commit/42e9fe9), [5da4e54](https://github.com/Shubham04-oss/oryxa-go/commit/5da4e54) |
| T26 | [798f5ac](https://github.com/Shubham04-oss/oryxa-go/commit/798f5ac) |
| T27 | [bddda06](https://github.com/Shubham04-oss/oryxa-go/commit/bddda06) |
| T28 | [0c347cd](https://github.com/Shubham04-oss/oryxa-go/commit/0c347cd) |
| T29 | [96677f9](https://github.com/Shubham04-oss/oryxa-go/commit/96677f9) |
| T30 | [29488b5](https://github.com/Shubham04-oss/oryxa-go/commit/29488b5) |
| T31 | [1cd4f25](https://github.com/Shubham04-oss/oryxa-go/commit/1cd4f25), [f61f6ce](https://github.com/Shubham04-oss/oryxa-go/commit/f61f6ce) |
| T32 | [0a252f6](https://github.com/Shubham04-oss/oryxa-go/commit/0a252f6) |
| T33 | [ae65e9c](https://github.com/Shubham04-oss/oryxa-go/commit/ae65e9c) |
| T34 | [94593bf](https://github.com/Shubham04-oss/oryxa-go/commit/94593bf), [5ca7310](https://github.com/Shubham04-oss/oryxa-go/commit/5ca7310) |
| T35 | [a814d1e](https://github.com/Shubham04-oss/oryxa-go/commit/a814d1e) |
| T36 | [e722168](https://github.com/Shubham04-oss/oryxa-go/commit/e722168) |
| T37 | [1191d65](https://github.com/Shubham04-oss/oryxa-go/commit/1191d65), [e016899](https://github.com/Shubham04-oss/oryxa-go/commit/e016899) |
| T38 | [5210e2b](https://github.com/Shubham04-oss/oryxa-go/commit/5210e2b) |
| T39 | [03d658c](https://github.com/Shubham04-oss/oryxa-go/commit/03d658c) |
| T40 | [5f92224](https://github.com/Shubham04-oss/oryxa-go/commit/5f92224) |
| T41 | [4c6069b](https://github.com/Shubham04-oss/oryxa-go/commit/4c6069b) |
| T42 | [db67227](https://github.com/Shubham04-oss/oryxa-go/commit/db67227) |
| T43 | [734462e](https://github.com/Shubham04-oss/oryxa-go/commit/734462e) |
| T44 | [7174d5c](https://github.com/Shubham04-oss/oryxa-go/commit/7174d5c) |
| T45 | [910051c](https://github.com/Shubham04-oss/oryxa-go/commit/910051c), [7cd1e75](https://github.com/Shubham04-oss/oryxa-go/commit/7cd1e75), [ab02e8a](https://github.com/Shubham04-oss/oryxa-go/commit/ab02e8a) |
| T46 | [a139cde](https://github.com/Shubham04-oss/oryxa-go/commit/a139cde), [dc260b0](https://github.com/Shubham04-oss/oryxa-go/commit/dc260b0), [89883d8](https://github.com/Shubham04-oss/oryxa-go/commit/89883d8), [875269c](https://github.com/Shubham04-oss/oryxa-go/commit/875269c), [0d6ebcc](https://github.com/Shubham04-oss/oryxa-go/commit/0d6ebcc) |
| T47 | [b684a04](https://github.com/Shubham04-oss/oryxa-go/commit/b684a04) |
