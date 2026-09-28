import { Agent, getAgentByName } from "agents"
import type { Connection } from "agents"
import type { ConversationEnv } from "./conversation-env"
import { inboxName, isNameId } from "./lib/dms/names"
import { teamspaceRoomAgentName } from "./lib/rooms/room-agent"

/** How long a purge's event id is remembered: the same event again within it does nothing. */
export const purgeMemoryMs = 24 * 60 * 60 * 1000

/** How many members, or rooms, an organization's purge empties at once. */
const purgeConcurrency = 8

/** The longest event id a purge takes. */
export const maxPurgeEventId = 200

/** Whether `value` is an event id a purge takes: 1 to `maxPurgeEventId` characters. */
export function isPurgeEventId(value: unknown): value is string {
  return (
    typeof value === "string" && value !== "" && value.length <= maxPurgeEventId
  )
}

/** What a purge did: the work, or nothing because its event was purged already. */
export type PurgeOutcome = "purged" | "repeated"

/**
 * What a purge empties: every member of the organization, or one member who
 * left it at `removedAt` (a `PurgeInstant`), whose conversations keep what
 * is newer than that.
 */
export type PurgeTarget =
  | { scope: "organization" }
  | { scope: "member"; userId: string; removedAt: string }

// An instant as the purge hands it on: ISO 8601 in UTC to the millisecond,
// as `toISOString` writes it and as messages record theirs, so that two of
// them compare as strings.
const purgeInstant = /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$/

/** Whether `value` is an instant in the form the purge hands on. */
export function isPurgeInstant(value: unknown): value is string {
  return (
    typeof value === "string" &&
    purgeInstant.test(value) &&
    Number.isFinite(Date.parse(value))
  )
}

/**
 * Runs `work` for each item (a member, or a room's teamspace),
 * `purgeConcurrency` at a time; resolves how many failed, each logged as
 * `what` could not be purged.
 */
async function eachOf(
  items: readonly string[],
  what: string,
  work: (item: string) => Promise<void>
): Promise<number> {
  let next = 0
  let failed = 0
  const worker = async () => {
    while (next < items.length) {
      const item = items[next]
      next += 1
      try {
        await work(item)
      } catch (error) {
        failed += 1
        console.error(`realtime registry: could not purge ${what}`, error)
      }
    }
  }
  await Promise.all(
    Array.from({ length: Math.min(purgeConcurrency, items.length) }, worker)
  )
  return failed
}

/**
 * An organization's registry of the inboxes its members have, and of its
 * teamspaces' rooms, named after the organization (`registryName`). An inbox
 * registers itself here with its first conversation, so that an
 * organization's purge, or a teamspace's, can find every inbox, and through
 * them every conversation; a room (`TeamspaceRoomAgent`) registers itself as
 * it lets its first socket in, so that the organization's purges, and a
 * member's, reach every room somebody has open. It also remembers the
 * purges it has carried out, by event id, so the same event again (Go
 * retrying one it did not hear the end of, or a replayed request) does
 * nothing.
 *
 * No page reaches it: the Worker has no route to it, and it refuses sockets
 * and requests.
 */
export class RealtimeRegistryAgent extends Agent<ConversationEnv> {
  private schemaReady = false
  /**
   * How many times each member's inbox has registered with this instance: a
   * purge that emptied an inbox compares it with what it was before, to tell
   * an inbox that registered again meanwhile. Memory is enough, as an
   * instance that is gone runs no more of its purges.
   */
  private readonly registrations = new Map<string, number>()

  override shouldSendProtocolMessages(): boolean {
    return false
  }

  override onConnect(connection: Connection): void {
    connection.close(1008, "Not a conversation")
  }

  override onRequest(): Response {
    return new Response(null, { status: 404 })
  }

  /** An inbox with conversations exists for `userId`. */
  async register(userId: string): Promise<void> {
    this.schema()
    if (!isNameId(userId)) throw new TypeError("not a user id")
    this.sql`INSERT INTO registry_members (user_id, registered_at)
      VALUES (${userId}, ${new Date().toISOString()})
      ON CONFLICT (user_id) DO NOTHING`
    this.registrations.set(userId, (this.registrations.get(userId) ?? 0) + 1)
  }

  /** The members with an inbox, in id order. */
  async members(): Promise<string[]> {
    this.schema()
    return this.memberIds()
  }

  /** The room of the teamspace `teamspaceId` has let a socket in. */
  async registerRoom(teamspaceId: string): Promise<void> {
    this.schema()
    if (!isNameId(teamspaceId)) throw new TypeError("not a teamspace id")
    this.sql`INSERT INTO registry_rooms (teamspace_id, registered_at)
      VALUES (${teamspaceId}, ${new Date().toISOString()})
      ON CONFLICT (teamspace_id) DO NOTHING`
  }

  /** The teamspaces whose rooms are registered, in id order. */
  async rooms(): Promise<string[]> {
    this.schema()
    return this.roomIds()
  }

  /**
   * Carries out a purge for Go (src/realtime-purge.ts). First every
   * registered room: for an organization, each lets go of everything and
   * leaves the registry (`TeamspaceRoomAgent.purgeRoom`); for a member who
   * left, each closes their sockets and lets go of what it kept of them, and
   * stays (`TeamspaceRoomAgent.purgeMember`). Then every registered member's
   * conversations and inbox, entirely, for an organization; for a member who
   * left, what in theirs is not newer than when they left
   * (`InboxAgent.purge`). A member leaves the registry only once their inbox
   * is emptied, and the event is remembered only once every room and every
   * member is done. A purge that failed part-way throws, and one cut off
   * part-way (a deploy, an eviction) says nothing; either way, the same event
   * again carries on with the members not yet done, and goes through the
   * rooms again, which a purge done already finds nothing in.
   */
  async purge(eventId: string, target: PurgeTarget): Promise<PurgeOutcome> {
    this.schema()
    const { scope } = target as { scope?: unknown }
    if (
      !isPurgeEventId(eventId) ||
      (scope !== "organization" &&
        (scope !== "member" ||
          !isNameId((target as { userId?: unknown }).userId) ||
          !isPurgeInstant((target as { removedAt?: unknown }).removedAt)))
    ) {
      throw new TypeError("not a purge")
    }
    if (this.remembered(eventId)) return "repeated"

    // The rooms first: a socket open in one hears the room until it closes.
    const rooms = this.roomIds()
    const roomsFailed = await eachOf(rooms, "a room", async (teamspaceId) => {
      const room = await this.room(teamspaceId)
      if (target.scope === "organization") {
        await room.purgeRoom()
        this.sql`DELETE FROM registry_rooms WHERE teamspace_id = ${teamspaceId}`
      } else {
        await room.purgeMember(target.userId, target.removedAt)
      }
    })
    const users =
      target.scope === "organization" ? this.memberIds() : [target.userId]
    const removedAt =
      target.scope === "organization" ? undefined : target.removedAt
    const failed = await eachOf(users, "a member", (user) =>
      this.purgeMember(user, removedAt)
    )
    if (failed > 0) {
      throw new Error(`${failed} of ${users.length} members were not purged`)
    }
    if (roomsFailed > 0) {
      throw new Error(`${roomsFailed} of ${rooms.length} rooms were not purged`)
    }
    this.remember(eventId)
    return "purged"
  }

  /**
   * Carries out a teamspace's deletion for Go (src/realtime-purge.ts): its
   * room, which closes every socket and lets go of everything it held
   * (`TeamspaceRoomAgent.purgeRoom`) and leaves the registry, registered or
   * not; then every side conversation in `teamspaceId`, entirely, and what
   * the inboxes list of them, through every registered member's inbox
   * (`InboxAgent.purgeTeamspace`), 8 at a time.
   *
   * Nobody leaves the registry: everybody is still in the organization.
   * Instead, a member is recorded done for the event
   * (`registry_purge_progress`) once their inbox is, and the event is
   * remembered, and its progress let go, only once the room and every member
   * are done. A purge that failed part-way throws, and one cut off part-way
   * (a deploy, an eviction) says nothing; either way, the same event again
   * purges the room again, which finds nothing, and carries on with the
   * members not yet done. What a purge under way had done is forgotten after
   * `purgeMemoryMs`, and the members then done again.
   */
  async purgeTeamspace(
    eventId: string,
    teamspaceId: string
  ): Promise<PurgeOutcome> {
    this.schema()
    if (!isPurgeEventId(eventId) || !isNameId(teamspaceId)) {
      throw new TypeError("not a teamspace's purge")
    }
    if (this.remembered(eventId)) return "repeated"

    const roomFailed = await eachOf([teamspaceId], "a room", async () => {
      await (await this.room(teamspaceId)).purgeRoom()
      this.sql`DELETE FROM registry_rooms WHERE teamspace_id = ${teamspaceId}`
    })
    const users = this.sql<{ user_id: string }>`SELECT user_id
      FROM registry_members
      WHERE user_id NOT IN (
        SELECT user_id FROM registry_purge_progress WHERE event_id = ${eventId}
      )
      ORDER BY user_id`.map((row) => row.user_id)
    const failed = await eachOf(users, "a member", async (user) => {
      const inbox = await getAgentByName(
        this.env.InboxAgent,
        inboxName(this.name, user)
      )
      await inbox.purgeTeamspace(teamspaceId)
      this.sql`INSERT INTO registry_purge_progress (event_id, user_id, done_at)
        VALUES (${eventId}, ${user}, ${Date.now()})
        ON CONFLICT (event_id, user_id) DO NOTHING`
    })
    if (failed > 0) {
      throw new Error(
        `${failed} of ${users.length} members' side conversations in the teamspace were not purged`
      )
    }
    if (roomFailed > 0) throw new Error("the teamspace's room was not purged")
    this.ctx.storage.transactionSync(() => {
      this.remember(eventId)
      this.sql`DELETE FROM registry_purge_progress WHERE event_id = ${eventId}`
    })
    return "purged"
  }

  /**
   * Whether the event was carried out in the last `purgeMemoryMs`. Older
   * events are forgotten first, and so is what purges under way did as
   * long ago.
   */
  private remembered(eventId: string): boolean {
    const since = Date.now() - purgeMemoryMs
    this.sql`DELETE FROM registry_purges WHERE done_at < ${since}`
    this.sql`DELETE FROM registry_purge_progress WHERE done_at < ${since}`
    return (
      this.sql<{
        event_id: string
      }>`SELECT event_id FROM registry_purges WHERE event_id = ${eventId}`
        .length > 0
    )
  }

  /** Records the event as carried out. */
  private remember(eventId: string): void {
    this.sql`INSERT INTO registry_purges (event_id, done_at)
      VALUES (${eventId}, ${Date.now()})
      ON CONFLICT (event_id) DO NOTHING`
  }

  /**
   * Purges a member's inbox and conversations: entirely, or, with
   * `removedAt`, what is not newer than it. The member stays registered
   * until their inbox says it is empty, so a purge that fails or is cut off
   * before then leaves them for the next, and stays registered after it if
   * their inbox registered again meanwhile (a conversation noted once it was
   * emptied). When something of theirs stays, they are kept registered, or
   * registered if they were not.
   */
  private async purgeMember(userId: string, removedAt?: string): Promise<void> {
    const registrations = this.registrations.get(userId) ?? 0
    const inbox = await getAgentByName(
      this.env.InboxAgent,
      inboxName(this.name, userId)
    )
    const { emptied } = await inbox.purge(
      removedAt === undefined ? undefined : { removedAt }
    )
    if (!emptied) {
      this.sql`INSERT INTO registry_members (user_id, registered_at)
        VALUES (${userId}, ${new Date().toISOString()})
        ON CONFLICT (user_id) DO NOTHING`
    } else if ((this.registrations.get(userId) ?? 0) === registrations) {
      this.sql`DELETE FROM registry_members WHERE user_id = ${userId}`
    }
  }

  private memberIds(): string[] {
    return this.sql<{
      user_id: string
    }>`SELECT user_id FROM registry_members ORDER BY user_id`.map(
      (row) => row.user_id
    )
  }

  private roomIds(): string[] {
    return this.sql<{
      teamspace_id: string
    }>`SELECT teamspace_id FROM registry_rooms ORDER BY teamspace_id`.map(
      (row) => row.teamspace_id
    )
  }

  /** The room of the teamspace `teamspaceId` in this organization. */
  private room(teamspaceId: string) {
    return getAgentByName(
      this.env.TeamspaceRoomAgent,
      teamspaceRoomAgentName(this.name, teamspaceId)
    )
  }

  private schema(): void {
    if (this.schemaReady) return
    this.sql`CREATE TABLE IF NOT EXISTS registry_members (
      user_id TEXT PRIMARY KEY,
      registered_at TEXT NOT NULL
    )`
    // The teamspaces whose rooms have let a socket in.
    this.sql`CREATE TABLE IF NOT EXISTS registry_rooms (
      teamspace_id TEXT PRIMARY KEY,
      registered_at TEXT NOT NULL
    )`
    this.sql`CREATE TABLE IF NOT EXISTS registry_purges (
      event_id TEXT PRIMARY KEY,
      done_at INTEGER NOT NULL
    )`
    // The members a teamspace's purge under way is done with, by event.
    this.sql`CREATE TABLE IF NOT EXISTS registry_purge_progress (
      event_id TEXT NOT NULL,
      user_id TEXT NOT NULL,
      done_at INTEGER NOT NULL,
      PRIMARY KEY (event_id, user_id)
    )`
    this.schemaReady = true
  }
}
