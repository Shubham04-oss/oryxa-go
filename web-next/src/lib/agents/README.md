# Agents, teamspaces and rooms: the data layer

Typed calls, query keys, query options and cache helpers for the agents
service's member-facing API, with no UI. It covers `lib/agents`,
`lib/teamspaces` and `lib/rooms`. Every call goes through
`@/lib/api/client`, which sends the cookie and refreshes an expired session
once. The handlers are in `agents/primary/internal/httpserver/server.go`, and
each function's JSDoc names its handler. Specialist runs and specialist
threads (SSE) live in `lib/specialists`, not here.

## What is here

Each domain lists its calls, then its query options.

- **Your agents**, `managed-agents.ts`:
  - `listAgents`, `getAgent`, `createAgent`, `updateAgent`, `deleteAgent`;
  - `agentsQueryOptions`, `agentQueryOptions`.
- **Agent teams and their conversation**, `agent-teams.ts`:
  - `listAgentTeams`, `getAgentTeam`, `createAgentTeam`, `updateAgentTeam`,
    `deleteAgentTeam`, `listAgentTeamMessages`, `postAgentTeamMessage`,
    `getAgentTeamThreads`;
  - `agentTeamsQueryOptions`, `agentTeamQueryOptions`,
    `agentTeamMessagesQueryOptions`, `agentTeamThreadsQueryOptions`.
- **Workflows and executions**, `agent-workflows.ts`:
  - `listAgentWorkflows`, `getAgentWorkflow`, `createAgentWorkflow`,
    `updateAgentWorkflow`, `deleteAgentWorkflow`, `startAgentWorkflow`,
    `listWorkflowExecutions`, `getWorkflowExecution`;
  - `agentWorkflowsQueryOptions`, `agentWorkflowQueryOptions`,
    `workflowExecutionsQueryOptions`, `workflowExecutionQueryOptions`.
- **Agent requests**, `agent-requests.ts`:
  - `listAgentRequests(box, { origin, askId })`, `getAgentRequest`,
    `decideAgentRequest`, `respondToAgentRequest`, `reviewAgentRequest`;
  - the Slack consent loop's calls: `consentAgentRequest` (the owner's
    choice, `summary`, `access` or `deny`; a choice that lost the race to a
    click in Slack throws `AgentRequestAlreadyDecided`, carrying the winner),
    `revokeAgentLease`, `listAgentActivity`, and a credential's three calls,
    `shareAgentRequestSecret`, `openAgentRequestSecret` (once; a second open
    is `isSecretGone`) and `revokeAgentRequestSecret`;
  - `agentRequestsQueryOptions`, `agentRequestQueryOptions`.
- **Organization agent**, `organization-agent.ts`:
  - `getOrganizationAgent`, `updateOrganizationAgent`,
    `getOrganizationContext`, `listOrganizationFollowUps`;
  - `organizationAgentQueryOptions`, `organizationContextQueryOptions`,
    `organizationFollowUpsQueryOptions`.
- **Teamspaces**, `../teamspaces/client.ts`:
  - `listTeamspaces`, `getTeamspace`, `createTeamspace`, `updateTeamspace`,
    `deleteTeamspace`, `addTeamspaceContribution`,
    `updateTeamspaceContribution`, `deleteTeamspaceContribution`,
    `listTeamspaceEntries`, `publishTeamspaceEntry`;
  - `teamspacesQueryOptions`, `teamspaceQueryOptions`,
    `teamspaceEntriesQueryOptions`.
- **Rooms**, `../rooms/client.ts`:
  - `roomApi(request)`: `read`, `say`, `catchUp` and `readAfter`, made
    through `request`. The room's Durable Object makes them as each member
    (`src/teamspace-room-agent.ts`); a page reads and posts through
    `useTeamspaceRoom` and never calls them itself;
  - `catchUpOnRoom` and `roomCatchUpQueryOptions`, for the catch-up brief;
  - `checkTeamspaceRoom`, the Worker's check behind the room's socket.

Across all three:

- **Keys:** `agentKeys` (in `queries.ts`), `teamspaceKeys` and `roomKeys`.
  Every key starts with `["agents" | "teamspaces", organizationId]`. A collection key
  such as `agentKeys.teams(orgId)` is the prefix of everything beneath it.
  A key with a page size (`workflowExecutions`, `requestBox`,
  `organizationFollowUps`, `entries`) is a prefix of every size when called
  without `limit`.
- **Pure helpers:** `policy.ts` (primary vs specialists, team `@handles`,
  executions in flight, who may decide or answer a request,
  `isUnavailableHere`) and `../teamspaces/policy.ts` (`publicationAuthors`,
  `canPublishSharedWork`, `canContribute`, `contributionCandidates`,
  `canChangeContribution`, `canRemoveContribution`, `canUseRoom`,
  `safeArtifactHref`). They mirror the server's checks; the server stays the
  authority.

## Creating a specialist

A member can call `POST …/agents` directly with `{name, description?,
instructions}`. `createAgent` runs the same `managedagents.Service.Create`
that Krude's `agent_system_create_agent` tool runs, so it applies the same
validation and emits the same event. The old app only created specialists by
talking to Krude, which needs `lib/threads`.

- **Plain form:** use `createAgent` when the member writes the definition.
- **Guided:** use the Krude conversation when Krude should draft the
  instructions.

Call `afterCreateAgent` either way.

- The name is 1–60 characters and unique across the whole organization,
  case-insensitively. Another member's agent can cause `409 name_conflict`.
- Instructions are required, up to 20,000 characters.
- `listAgents` also returns the caller's primary agent, first; take
  specialists with `specialistAgents`.
- The primary agent cannot be deleted (400). A specialist on a team or in a
  teamspace cannot be deleted either: `409 agent_in_use`.

## Polling and presence

| Query                                | Re-reads                                    |
| ------------------------------------ | ------------------------------------------- |
| `agentTeamMessagesQueryOptions`      | every 5 s (the latest 200 messages)         |
| `agentRequestsQueryOptions`          | every 10 s                                  |
| `workflowExecution(s)QueryOptions`   | every 2 s while one is queued or running, else never |
| everything else                      | no polling (app default `staleTime` 30 s)   |

**Reading a room is presence.** Every read, catch-up and post marks the
member present. After 90 s without one they count as away, and their own
agent may answer for them. A page does not read the room itself: the room
is a Durable Object (`TeamspaceRoomAgent`, REALTIME.md) that reads Go as
each member with the room open in a visible tab, and never as a member
whose tabs are all hidden. `useTeamspaceRoom` holds its socket; open it
only when `canUseRoom(space)` holds.

- A paused room answers 400.
- Someone neither on the team nor managing it gets 404.
- A manager who is not on the team can read but gets 403 on a post; the
  Worker keeps them out of the room's socket.
- Only people with a seat may post: team members whose primary agent is
  active.

**A room is read incrementally** (`readAfter`). A read without `after`
starts at the room's *first* message, so the old app's `?limit=200` poll
froze on messages 1–200 once a room grew past that.

- With nothing held, it calls `catchup` to find where the room ends, then
  reads its latest page.
- Later reads start after `readThrough`, the last seq a read returned, page
  by page (at most ten a time).
- A post's answer is merged by seq without moving `readThrough`, because
  another member's message can sit between the seqs a post returns.

Team thread pointers (`agent-threads`) are not polled. Only posting to the
team changes them, and `afterPostAgentTeamMessage` refreshes them.

## After a mutation

Call the matching helper from the mutation's `onSuccess`. The helpers are in
`invalidation.ts` and `../teamspaces/queries.ts`.

| Mutation | Helper | What it does |
| --- | --- | --- |
| `createAgent` | `afterCreateAgent` | agent list |
| `updateAgent` | `afterUpdateAgent` | caches the agent; agent list, **all agent teams** (members and coordinator shown by live name and status), **all teamspaces** (whisperers, contributions; a room's seats follow on its Durable Object's next read) |
| `deleteAgent` | `afterDeleteAgent` | drops the agent; agent list. Its threads belong to `lib/specialists`. |
| `createAgentTeam` / `updateAgentTeam` | `afterCreateAgentTeam` / `afterUpdateAgentTeam` | caches the team; team list |
| `deleteAgentTeam` | `afterDeleteAgentTeam` | drops the team, its conversation and thread pointers; team list |
| `postAgentTeamMessage` | `afterPostAgentTeamMessage` | merges the lines into a cached conversation; re-reads it and the thread pointers |
| `createAgentWorkflow` / `updateAgentWorkflow` | `afterCreateAgentWorkflow` / `afterUpdateAgentWorkflow` | caches the workflow; workflow list |
| `deleteAgentWorkflow` | `afterDeleteAgentWorkflow` | drops it and its execution lists; workflow list |
| `startAgentWorkflow` | `afterStartAgentWorkflow` | caches the execution; that workflow's executions (their 2 s polling starts) |
| `decideAgentRequest` / `respondToAgentRequest` | `afterDecideAgentRequest` / `afterRespondToAgentRequest` | caches the request; both boxes |
| `updateOrganizationAgent` | `afterUpdateOrganizationAgent` | caches it; **all teamspaces**, which embed it (its status gates publishing) |
| `createTeamspace` / `updateTeamspace` | `afterCreateTeamspace` / `afterUpdateTeamspace` | caches the space; teamspace list |
| `deleteTeamspace` | `afterDeleteTeamspace` | drops the space, its ledger and room; teamspace list |
| any contribution change | `afterTeamspaceContributionChange` | caches the returned space; list; its room's catch-up (the room's seats follow on its Durable Object's next read, within 12 s) |
| `publishTeamspaceEntry` | `afterPublishTeamspaceEntry` | that ledger. The organization context updates later, from an event. |
| a room post (`useTeamspaceRoom().send`) | in the hook | merges the answer; re-reads the ledger (the room publishes what it concludes) |

## What a page must handle

- **Errors.** Every error here is `{error: {code, message}}`, so
  `ApiError.code` is always set. `isUnavailableHere(error)` is true for the
  503 codes that mean "not set up on this server" (`agents_unavailable`,
  `teamspaces_unavailable`, `rooms_unavailable`, …); plain `unavailable`
  (auth down) is transient.
- **409 conflict codes:**
  - `name_conflict` (agents, teams, workflows);
  - `agent_in_use`, `team_in_use`;
  - `team_disabled`, `workflow_disabled`;
  - `teamspace_conflict` (one teamspace per team, names unique in the
    organization, an agent already contributed);
  - `teamspace_disabled` (a paused space, or a disabled organization agent,
    blocks contributing and publishing);
  - `invalid_state` and `request_expired` (requests).
- **Tools and the answer guard.** A request's `requested_tools` and its
  approval's `allowed_tools` (a subset; none unless named) hold toolbox
  names, `AGENT_REQUEST_TOOLBOXES`: `apps` and `knowledge`. An answer the
  answer guard holds leaves the request in `review` until its target
  member reviews it (`POST …/agent-requests/{id}/review`, from their own
  session), then `completed` (or `failed`, for a held failure) or
  `withheld`, which Oryxa also does when it expires in review. The target
  member's reads carry the held answer while it is in review (`held`); the
  requester reads no `result` or `guard` in `review` or `withheld`. A side
  conversation's cards do this through their object (REALTIME.md).
- **Strict bodies.** Every body is decoded strictly, nested objects too: an
  unknown field is a 400. The calls copy only the fields each handler reads,
  so passing `team.members` or a whole workflow is safe. Calling
  `apiRequest` directly bypasses that. Every update needs at least one field.
  `schedule` and `clear_schedule` cannot go together.
- **Slow posts.** Both answer only after the agents have replied, so don't
  show them optimistically:
  - `postAgentTeamMessage` can take up to about 90 s (up to 4 agent turns);
  - a room post (`roomApi().say`) spends up to 20 s on agent turns (checked
    only between turns: one real-model post took 96 s), plus a brief
    rewrite. The room's Durable Object reads the room while it waits, so
    each message reaches the page as Go records it.
- **Workflows need AWS.** Starting one needs an orchestrator, and a
  workflow with a schedule needs EventBridge Scheduler. Otherwise the server
  answers `503 agent_workflows_unavailable`; locally, set
  `AGENTS_WORKFLOW_LOCAL=true`.
- **Page sizes.** Server defaults and caps: executions 50 (a `limit` over 100
  also becomes 50); requests 20 (max 100); entries and follow-ups 100 (max
  500); rooms 200 max. The query options use the old app's sizes: executions
  20, requests 50, entries 100, follow-ups 20.
- **Empty states.**
  - The organization context before any synthesis is revision 0 with
    `agent_id: ""`.
  - A room brief before its first rewrite is revision 0 with `updated_at`
    `0001-01-01T00:00:00Z`.
  - Agent seats in a room always have `present: false`; presence is for
    people.
- **Links and handles.** Render an entry's artifact links only through
  `safeArtifactHref`. Agent-team `@handles` are not in the API; compute them
  with `agentTeamSeats`, which matches the server's routing.
