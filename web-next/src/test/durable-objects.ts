// A stand-in for the Agents SDK, for the tests of the direct messages'
// objects (src/direct-message-agent.ts, src/inbox-agent.ts,
// src/realtime-registry-agent.ts) and of the purges that reach a teamspace's
// room (src/teamspace-room-agent.ts), which only run in workerd. Each object
// gets a real SQLite database (Node's built-in, as Durable Object SQLite is
// SQLite) with workerd's `ctx.storage.transactionSync` and `ctx.storage.kv`
// on it, the SDK's synced state, sockets the test opens, speaks through and
// reads, and schedules the test runs; `getAgentByName` reaches the other
// objects over a stand-in RPC that copies what crosses it, as Durable Object
// RPC does, and fails for an object the test says was reset. Use it with
// `vi.mock("agents", async () => (await import("@/test/durable-objects")).agentsModule)`.

type Row = Record<string, unknown>

type Database = {
  prepare: (sql: string) => { all: (...values: Array<unknown>) => Array<Row> }
}

// Loaded without an import, so no Node type declarations are needed.
const { DatabaseSync } = (
  globalThis as unknown as {
    process: { getBuiltinModule: (id: string) => unknown }
  }
).process.getBuiltinModule("node:sqlite") as {
  DatabaseSync: new (path: string) => Database
}

/** A schedule, as the SDK keeps one: `time` in whole seconds, rounded down. */
export type Job = {
  id: string
  callback: string
  time: number
  payload: unknown
}

/** A socket, as the SDK hands an object one; what the object sends it, parsed. */
export class FakeSocket {
  readonly sent: Array<Record<string, unknown>> = []
  state: unknown = null
  open = true
  closed?: { code?: number; reason?: string }

  constructor(readonly id: string) {}

  setState(next: unknown) {
    if (!this.open) throw new Error("closed")
    this.state = next
    return next
  }

  send(data: string) {
    if (!this.open) throw new Error("closed")
    this.sent.push(JSON.parse(data) as Record<string, unknown>)
  }

  close(code?: number, reason?: string) {
    this.open = false
    this.closed = { code, reason }
  }

  /** What the object sent of one type, oldest first. */
  of(type: string): Array<Record<string, unknown>> {
    return this.sent.filter((message) => message.type === type)
  }

  /** The last message of one type. */
  last(type: string): Record<string, unknown> | undefined {
    return this.of(type).at(-1)
  }

  clear() {
    this.sent.length = 0
  }
}

let scheduleIds = 0

/**
 * workerd's synchronous key-value storage (`ctx.storage.kv`), as far as the
 * objects use it, kept in the object's own database, so that a restart keeps
 * it: values as JSON, listed in key order.
 */
class FakeKv {
  constructor(private readonly db: () => Database) {}

  private table(): Database {
    const db = this.db()
    db.prepare(
      "CREATE TABLE IF NOT EXISTS _fake_kv (key TEXT PRIMARY KEY, value TEXT NOT NULL)"
    ).all()
    return db
  }

  get<T>(key: string): T | undefined {
    const row = this.table()
      .prepare("SELECT value FROM _fake_kv WHERE key = ?")
      .all(key)
      .at(0)
    return row === undefined ? undefined : (JSON.parse(String(row.value)) as T)
  }

  put(key: string, value: unknown): void {
    this.table()
      .prepare(
        "INSERT INTO _fake_kv (key, value) VALUES (?, ?) ON CONFLICT (key) DO UPDATE SET value = excluded.value"
      )
      .all(key, JSON.stringify(value))
  }

  delete(key: string): boolean {
    const had = this.get(key) !== undefined
    this.table().prepare("DELETE FROM _fake_kv WHERE key = ?").all(key)
    return had
  }

  list<T>({
    prefix = "",
    reverse = false,
    limit = -1,
  }: { prefix?: string; reverse?: boolean; limit?: number } = {}): Array<
    [string, T]
  > {
    return this.table()
      .prepare(
        `SELECT key, value FROM _fake_kv WHERE substr(key, 1, ?) = ? ORDER BY key ${reverse ? "DESC" : "ASC"} LIMIT ?`
      )
      .all(prefix.length, prefix, limit)
      .map((row) => [String(row.key), JSON.parse(String(row.value)) as T])
  }
}

/** The Durable Object's state, as far as the objects use it. */
type FakeState = {
  id: { name: string }
  storage: { transactionSync: <T>(work: () => T) => T; kv: FakeKv }
}

/** The SDK's `Agent`, as far as the objects use it. */
class Agent {
  readonly ctx: FakeState
  readonly env: unknown
  /** Its storage: a restart (`World.restart`) hands it to the new instance. */
  db: Database
  sockets: Array<FakeSocket> = []
  jobs: Array<Job> = []
  /** The synced state before the object first sets one: an object's own, if it has one. */
  initialState: unknown = undefined
  private savepoints = 0

  constructor(ctx: { id: { name: string } }, env: unknown) {
    this.ctx = {
      ...ctx,
      storage: {
        transactionSync: (work) => this.transactionSync(work),
        kv: new FakeKv(() => this.db),
      },
    }
    this.env = env
    this.db = new DatabaseSync(":memory:")
  }

  // As workerd's: a savepoint, released once `work` returns, rolled back
  // when it throws, and nested inside one under way.
  private transactionSync<T>(work: () => T): T {
    const savepoint = `transaction_${this.savepoints}`
    this.savepoints += 1
    this.db.prepare(`SAVEPOINT ${savepoint}`).all()
    try {
      const result = work()
      this.db.prepare(`RELEASE ${savepoint}`).all()
      return result
    } catch (error) {
      this.db.prepare(`ROLLBACK TO ${savepoint}`).all()
      this.db.prepare(`RELEASE ${savepoint}`).all()
      throw error
    } finally {
      this.savepoints -= 1
    }
  }

  get name(): string {
    return this.ctx.id.name
  }

  /** The synced state: the last one set, kept in its database as the SDK keeps it, or the initial one. */
  get state(): unknown {
    const kept = this.ctx.storage.kv.get<{ state: unknown }>("_fake_state")
    return kept === undefined ? this.initialState : kept.state
  }

  // As the SDK's: checked, kept, and sent to every socket open whose
  // protocol is on.
  setState(state: unknown): void {
    const validate = (this as { validateStateChange?: unknown })
      .validateStateChange
    if (typeof validate === "function") validate.call(this, state, "server")
    this.ctx.storage.kv.put("_fake_state", { state })
    for (const socket of this.getConnections()) {
      if (!this.isConnectionProtocolEnabled(socket)) continue
      socket.send(JSON.stringify({ type: "cf_agent_state", state }))
    }
  }

  isConnectionProtocolEnabled(_connection: FakeSocket): boolean {
    return true
  }

  // As the SDK: `?` for each value, run on the object's own database.
  sql<T>(strings: TemplateStringsArray, ...values: Array<unknown>): Array<T> {
    const query = strings.reduce(
      (text, part, index) => text + part + (index < values.length ? "?" : ""),
      ""
    )
    return this.db
      .prepare(query)
      .all(...values)
      .map((row) => ({ ...row }) as T)
  }

  getConnections(): Array<FakeSocket> {
    return this.sockets.filter((socket) => socket.open)
  }

  async schedule(when: Date | number, callback: string, payload?: unknown) {
    scheduleIds += 1
    const time =
      when instanceof Date
        ? Math.floor(when.getTime() / 1000)
        : Math.floor((Date.now() + when * 1000) / 1000)
    const job = { id: `job-${scheduleIds}`, callback, time, payload }
    this.jobs.push(job)
    return job
  }

  async cancelSchedule(id: string) {
    const before = this.jobs.length
    this.jobs = this.jobs.filter((job) => job.id !== id)
    return this.jobs.length < before
  }

  // As the SDK lists them: a schedule running is still listed.
  async listSchedules() {
    return this.jobs.map((job) => ({ ...job, type: "scheduled" as const }))
  }

  async keepAliveWhile<T>(work: () => Promise<T>): Promise<T> {
    return work()
  }
}

type ObjectClass = new (ctx: { id: { name: string } }, env: unknown) => unknown

/** A call over the stand-in RPC. */
export type RpcCall = { binding: string; name: string; method: string }

/** A Durable Object namespace: its objects by name, each made on first use. */
export class FakeNamespace {
  readonly objects = new Map<string, unknown>()
  private readonly starting = new Map<string, Promise<unknown>>()

  constructor(
    readonly world: World,
    readonly binding: string,
    private readonly make: ObjectClass
  ) {}

  /** The object named `name`, started as the SDK starts one (`onStart`) before its first call. */
  get(name: string): Promise<unknown> {
    let started = this.starting.get(name)
    if (!started) {
      const object = new this.make({ id: { name } }, this.world.env)
      this.objects.set(name, object)
      started = this.start(name, object)
    }
    return started
  }

  /**
   * The object named `name` as a restart leaves it (a deploy, an eviction):
   * a new instance on the same storage (its database, its schedules and its
   * hibernated sockets), started again, which every later call reaches. What
   * the old instance kept in memory is gone, and the test sees to it that
   * what it had not finished never resumes.
   */
  restart(name: string): Promise<unknown> {
    const old = this.objects.get(name) as Agent | undefined
    if (!old) throw new Error(`no ${this.binding} named ${name} to restart`)
    const object = new this.make({ id: { name } }, this.world.env) as Agent
    object.db = old.db
    object.jobs = [...old.jobs]
    object.sockets = old.sockets
    this.objects.set(name, object)
    return this.start(name, object)
  }

  private start(name: string, object: unknown): Promise<unknown> {
    const onStart = (object as { onStart?: () => Promise<void> }).onStart
    const started = Promise.resolve(onStart?.call(object)).then(() => object)
    this.starting.set(name, started)
    return started
  }

  /** What `getAgentByName` hands back: a stub whose calls cross the stand-in RPC. */
  stub(name: string): unknown {
    return new Proxy(
      {},
      {
        get: (_target, method) => {
          // Not a thenable: awaiting the stub is awaiting the stub.
          if (typeof method !== "string" || method === "then") return undefined
          return async (...args: Array<unknown>) => {
            this.world.calls.push({ binding: this.binding, name, method })
            if (this.world.failing.has(name)) {
              throw new Error(`Durable Object reset: ${name}`)
            }
            await this.world.gate(name)
            const object = (await this.get(name)) as Record<
              string,
              (...params: Array<unknown>) => unknown
            >
            const result = await object[method].call(
              object,
              ...structuredClone(args)
            )
            return structuredClone(result)
          }
        },
      }
    )
  }
}

/**
 * The objects a test works with: a namespace per binding, the environment
 * they share, the RPC calls made, and the objects whose calls fail.
 */
export class World {
  readonly env: Record<string, unknown>
  readonly calls: Array<RpcCall> = []
  /** Objects whose calls fail, by name, as one that was reset mid-call. */
  readonly failing = new Set<string>()
  private readonly namespaces = new Map<string, FakeNamespace>()
  private readonly gates = new Map<string, Promise<void>>()

  constructor(
    classes: Record<string, ObjectClass>,
    vars: Record<string, unknown> = {}
  ) {
    this.env = { ...vars }
    for (const [binding, make] of Object.entries(classes)) {
      const namespace = new FakeNamespace(this, binding, make)
      this.namespaces.set(binding, namespace)
      this.env[binding] = namespace
    }
  }

  /** The object `name` of `binding`, started. */
  async object<T>(binding: string, name: string): Promise<T> {
    const namespace = this.namespaces.get(binding)
    if (!namespace) throw new Error(`no binding ${binding}`)
    return (await namespace.get(name)) as T
  }

  /** The object `name` of `binding` restarted on its storage (`FakeNamespace.restart`). */
  async restart<T>(binding: string, name: string): Promise<T> {
    const namespace = this.namespaces.get(binding)
    if (!namespace) throw new Error(`no binding ${binding}`)
    return (await namespace.restart(name)) as T
  }

  /** Makes calls to the object `name` wait until the returned release is called. */
  hold(name: string): () => void {
    let release!: () => void
    this.gates.set(
      name,
      new Promise<void>((resolve) => {
        release = resolve
      })
    )
    return () => {
      this.gates.delete(name)
      release()
    }
  }

  /** What a call to `name` waits for: nothing, unless the object is held. */
  gate(name: string): Promise<void> | undefined {
    return this.gates.get(name)
  }

  /** The object if it was ever made, without making it. */
  existing<T>(binding: string, name: string): T | undefined {
    return this.namespaces.get(binding)?.objects.get(name) as T | undefined
  }

  /** The RPC calls made to the objects of `binding`, by method. */
  callsTo(binding: string, method?: string): Array<RpcCall> {
    return this.calls.filter(
      (call) =>
        call.binding === binding &&
        (method === undefined || call.method === method)
    )
  }
}

async function getAgentByName(namespace: FakeNamespace, name: string) {
  // As the SDK's, which starts the object before handing its stub back.
  if (namespace.world.failing.has(name)) {
    throw new Error(`Durable Object reset: ${name}`)
  }
  await namespace.get(name)
  return namespace.stub(name)
}

export const agentsModule = {
  Agent,
  getAgentByName,
  MessageType: { CF_AGENT_STATE: "cf_agent_state" },
}

/** An object as the tests reach into it: its sockets, schedules and database. */
export type Harness = {
  sockets: Array<FakeSocket>
  jobs: Array<Job>
  sql: <T>(strings: TemplateStringsArray, ...values: Array<unknown>) => Array<T>
  onConnect: (
    socket: FakeSocket,
    context: { request: Request }
  ) => Promise<void>
  onMessage: (socket: FakeSocket, message: string) => Promise<void>
  onClose: (socket: FakeSocket) => Promise<void>
}

let socketIds = 0

/**
 * Opens a socket on an object as the Worker routes one: with the identity
 * headers it sets, which `headers` can change or blank out.
 */
export async function openSocket(
  object: unknown,
  identity: { organizationId: string; userId: string },
  headers: Record<string, string> = {}
): Promise<FakeSocket> {
  socketIds += 1
  const socket = new FakeSocket(`socket-${socketIds}`)
  const harness = object as Harness
  harness.sockets.push(socket)
  await harness.onConnect(socket, {
    request: new Request("http://localhost:3004/agents/x/y", {
      headers: {
        Origin: "http://localhost:3004",
        "X-Oryxa-Organization": identity.organizationId,
        "X-Oryxa-User": identity.userId,
        ...headers,
      },
    }),
  })
  return socket
}

/** The page says something on a socket. */
export async function say(
  object: unknown,
  socket: FakeSocket,
  message: unknown
): Promise<void> {
  await (object as Harness).onMessage(
    socket,
    typeof message === "string" ? message : JSON.stringify(message)
  )
}

/** The page closes a socket. */
export async function closeSocket(
  object: unknown,
  socket: FakeSocket
): Promise<void> {
  socket.open = false
  await (object as Harness).onClose(socket)
}

/** Lets every promise chain that can move, move: a call held by `World.hold` is then waiting. */
export async function drain(): Promise<void> {
  for (let turn = 0; turn < 50; turn += 1) await Promise.resolve()
}

/**
 * Lets all the work under way run as far as it goes, to its end or to a call
 * that never answers: the objects' work is promises only, and every one of
 * them runs before the next timer does.
 */
export function settle(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0))
}

/**
 * Runs every schedule due by now, as the SDK's alarm does, oldest first:
 * each stays listed while its callback runs, and goes once it returns.
 */
export async function runDueJobs(object: unknown): Promise<void> {
  const harness = object as Harness & Record<string, unknown>
  const ran = new Set<Job>()
  for (;;) {
    const due = harness.jobs
      .filter((job) => job.time * 1000 <= Date.now() && !ran.has(job))
      .sort((left, right) => left.time - right.time)
      .at(0)
    if (!due) return
    ran.add(due)
    const callback = harness[due.callback] as (
      payload: unknown,
      schedule: Job
    ) => Promise<void>
    try {
      await callback.call(harness, due.payload, due)
    } finally {
      harness.jobs = harness.jobs.filter((job) => job !== due)
    }
  }
}
