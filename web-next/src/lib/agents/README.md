# Agents and requests: the data layer

Typed calls, query keys, query options and cache helpers for the agents
service's member-facing API, with no UI. It covers `lib/agents`, plus the
room client in `lib/rooms` that the Worker's room object uses. Every call goes
through `@/lib/api/client`, which sends the cookie and refreshes an expired
session once. The handlers are in
`agents/primary/internal/httpserver/server.go`, and each function's JSDoc
names its handler.

**Archived 2026-09-30.** The clients for multi-agent teams, workflows,
specialists' lifecycle (create, delete), teamspaces and the page's room
helpers were removed with their screens. They are in the git tag
`archive/pre-simplify-2026-09-30`; the agents service still serves those
routes, and the Worker still hosts the specialist, room and side-conversation
objects (REALTIME.md).

## What is here

Each domain lists its calls, then its query options.

- **Your agents**, `managed-agents.ts`:
  - `listAgents`, `getAgent`, `updateAgent`;
  - `agentsQueryOptions`, `agentQueryOptions`.
  - `listAgents` also returns specialists the member made earlier; the page
    only reads the primary agent out of it (`primaryAgent` in `policy.ts`).
    The primary agent cannot be deleted, and `updateAgent` resumes a paused
    one.
- **Agent requests**, `agent-requests.ts`:
  - `listAgentRequests(box, { origin, askId })`, `getAgentRequest`,
    `decideAgentRequest`, `respondToAgentRequest`, `reviewAgentRequest`;
  - the Slack consent loop's calls: `consentAgentRequest` (the owner's
    choice, `summary`, `access` or `deny`; a choice that lost the race to a
    click in Slack throws `AgentRequestAlreadyDecided`, carrying the winner),
    `replyToAgentRequest` (the owner answers in their own words, for a
    pending request or a held answer; it loses the same race the same way),
    `revokeAgentLease`, `listAgentActivity`, and a credential's three calls,
    `shareAgentRequestSecret`, `openAgentRequestSecret` (once; a second open
    is `isSecretGone`) and `revokeAgentRequestSecret`;
  - `agentRequestsQueryOptions`, `agentRequestQueryOptions`.
- **Organization agent**, `organization-agent.ts`:
  - `getOrganizationAgent`, `updateOrganizationAgent`,
    `getOrganizationContext`, `listOrganizationFollowUps`;
  - `organizationAgentQueryOptions`, `organizationContextQueryOptions`,
    `organizationFollowUpsQueryOptions`. No screen uses these today.
- **Rooms**, `../rooms/client.ts`: `roomApi(request)` (`read`, `say`,
  `catchUp`, `readAfter`), which the teamspace room's Durable Object makes as
  each member (`src/teamspace-room-agent.ts`). No page calls it.

Across them:

- **Keys:** `agentKeys` (in `queries.ts`). Every key starts with
  `["agents", organizationId]`. A collection key such as
  `agentKeys.requests(orgId)` is the prefix of everything beneath it. A key
  with a page size (`requestBox`, `organizationFollowUps`) is a prefix of
  every size when called without `limit`.
- **Pure helpers:** `policy.ts` (`primaryAgent`, who may decide or answer a
  request, `isUnavailableHere`). They mirror the server's checks; the server
  stays the authority.

## Polling

| Query                       | Re-reads                                  |
| --------------------------- | ----------------------------------------- |
| `agentRequestsQueryOptions` | every 10 s                                |
| everything else             | no polling (app default `staleTime` 30 s) |

## After a mutation

Call the matching helper from the mutation's `onSuccess`. The helpers are in
`invalidation.ts`.

| Mutation | Helper | What it does |
| --- | --- | --- |
| `updateAgent` | `afterUpdateAgent` | caches the agent; agent list |
| `decideAgentRequest` / `respondToAgentRequest` / `replyToAgentRequest` | `afterDecideAgentRequest` / `afterRespondToAgentRequest` / `afterReplyToAgentRequest` | caches the request; both boxes |
| `updateOrganizationAgent` | `afterUpdateOrganizationAgent` | caches it |

## What a page must handle

- **Errors.** Every error here is `{error: {code, message}}`, so
  `ApiError.code` is always set. `isUnavailableHere(error)` is true for the
  503 codes that mean "not set up on this server" (`agents_unavailable`,
  `interactions_unavailable`, …); plain `unavailable` (auth down) is
  transient.
- **409 conflict codes:** `name_conflict` (agent names are unique across the
  organization), `invalid_state` and `request_expired` (requests).
- **Tools and the answer guard.** A request's `requested_tools` and its
  approval's `allowed_tools` (a subset; none unless named) hold toolbox
  names, `AGENT_REQUEST_TOOLBOXES`: `apps` and `knowledge`. An answer the
  answer guard holds leaves the request in `review` until its target
  member reviews it (`POST …/agent-requests/{id}/review`, from their own
  session), then `completed` (or `failed`, for a held failure) or
  `withheld`, which Oryxa also does when it expires in review. The target
  member's reads carry the held answer while it is in review (`held`); the
  requester reads no `result` or `guard` in `review` or `withheld`.
- **Replying yourself, and who answers.** `replyToAgentRequest` (`POST
  …/agent-requests/{id}/reply {text}`, the target member in their own
  session; an agent's token is refused) answers a pending request, or
  replaces a held answer, with the owner's own words, 1 to
  `REPLY_MAX_CHARACTERS` characters. Their agent never runs: the answer's
  `result.written_by_owner` is true for the owner and the asker, and the
  request records `decision.consent: "reply"` (answered while pending) or
  `review.action: "reply"` (replaced a held answer). `isOwnerWritten` reads
  any of the three. A request's `answer_by` is `owner` when Jev judged, at
  creation, that answering needs the owner's own judgment, so nothing runs
  their agent until they choose to let it (`needsOwnerReply`), and absent or
  `agent` otherwise.
- **Strict bodies.** Every body is decoded strictly, nested objects too: an
  unknown field is a 400. The calls copy only the fields each handler reads,
  so passing a whole agent is safe. Calling `apiRequest` directly bypasses
  that. Every update needs at least one field.
- **Page sizes.** Server defaults and caps: requests 20 (max 100); follow-ups
  100 (max 500). The query options use the old app's sizes: requests 50,
  follow-ups 20.
- **Empty states.** The organization context before any synthesis is
  revision 0 with `agent_id: ""`.
