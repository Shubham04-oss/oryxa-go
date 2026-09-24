import { Agent, getAgentByName } from "agents"
import type { Connection } from "agents"
import type { ConversationEnv } from "./conversation-env"
import { inboxName, isNameId } from "./lib/dms/names"

/** How long a purge's event id is remembered: the same event again within it does nothing. */
export const purgeMemoryMs = 24 * 60 * 60 * 1000

/** How many members an organization's purge empties at once. */
const purgeConcurrency = 8

/** The longest event id a purge takes. */
export const maxPurgeEventId = 200

/** What a purge did: the work, or nothing because its event was purged already. */
export type PurgeOutcome = "purged" | "repeated"

/**
 * An organization's registry of the inboxes its members have, named after
 * the organization (`registryName`). An inbox registers itself here with its
 * first conversation, so that an organization's purge can find every
 * inbox, and through them every conversation. It also remembers the purges
 * it has carried out, by event id, so the same event again (Go retrying one
 * it did not hear the end of, or a replayed request) does nothing.
 *
 * No page reaches it: the Worker has no route to it, and it refuses sockets
 * and requests.
 */
export class RealtimeRegistryAgent extends Agent<ConversationEnv> {
  private schemaReady = false

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
  }

  /** The members with an inbox, in id order. */
  async members(): Promise<string[]> {
    this.schema()
    return this.memberIds()
  }

  /**
   * Carries out a purge for Go (src/realtime-purge.ts): one member's
   * conversations and inbox when `userId` is given, else every registered
   * member's. Each member's inbox empties their conversations, takes them
   * off the other people's inboxes and empties itself; then the member
   * leaves the registry. The event is remembered only once all of it is
   * done: a purge that failed part-way throws, and the same event again
   * carries on from where it stopped.
   */
  async purge(eventId: string, userId?: string): Promise<PurgeOutcome> {
    this.schema()
    if (
      typeof eventId !== "string" ||
      eventId === "" ||
      eventId.length > maxPurgeEventId ||
      (userId !== undefined && !isNameId(userId))
    ) {
      throw new TypeError("not a purge")
    }
    const now = Date.now()
    this.sql`DELETE FROM registry_purges WHERE done_at < ${now - purgeMemoryMs}`
    const done = this.sql<{
      event_id: string
    }>`SELECT event_id FROM registry_purges WHERE event_id = ${eventId}`.at(0)
    if (done) return "repeated"

    const users = userId === undefined ? this.memberIds() : [userId]
    let next = 0
    let failed = 0
    const worker = async () => {
      while (next < users.length) {
        const user = users[next]
        next += 1
        try {
          await this.purgeMember(user)
        } catch (error) {
          failed += 1
          console.error("realtime registry: could not purge a member", error)
        }
      }
    }
    await Promise.all(
      Array.from({ length: Math.min(purgeConcurrency, users.length) }, worker)
    )
    if (failed > 0) {
      throw new Error(`${failed} of ${users.length} members were not purged`)
    }
    this.sql`INSERT INTO registry_purges (event_id, done_at)
      VALUES (${eventId}, ${Date.now()})
      ON CONFLICT (event_id) DO NOTHING`
    return "purged"
  }

  private async purgeMember(userId: string): Promise<void> {
    const inbox = await getAgentByName(
      this.env.InboxAgent,
      inboxName(this.name, userId)
    )
    await inbox.purge()
    this.sql`DELETE FROM registry_members WHERE user_id = ${userId}`
  }

  private memberIds(): string[] {
    return this.sql<{
      user_id: string
    }>`SELECT user_id FROM registry_members ORDER BY user_id`.map(
      (row) => row.user_id
    )
  }

  private schema(): void {
    if (this.schemaReady) return
    this.sql`CREATE TABLE IF NOT EXISTS registry_members (
      user_id TEXT PRIMARY KEY,
      registered_at TEXT NOT NULL
    )`
    this.sql`CREATE TABLE IF NOT EXISTS registry_purges (
      event_id TEXT PRIMARY KEY,
      done_at INTEGER NOT NULL
    )`
    this.schemaReady = true
  }
}
