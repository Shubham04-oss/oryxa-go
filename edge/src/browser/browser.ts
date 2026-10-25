// BrowserDO (A2): one Browser Run session per DO, named
// org/{org}/room/{kind}/{id}/browser/{session}. It implements the browser
// API of contract §6 for its room, with the page scripts the AgentCore
// toolset runs (scripts.ts), so snapshots and selectors are the same.
//
// A member can take over (handoff): while they have control, the agent's
// calls are refused with conflict until the member hands back (resume).
// Sessions are recorded by Browser Run; screenshots go to R2 under the run.
//
// Only the agent's calls start a browser. Watching (live) and handing back
// reconnect to the session that is running and never launch one, and every
// close reconnects to the stored session first, because after the DO is
// evicted its driver starts with nothing attached (B2, B3).
import { DurableObject } from "cloudflare:workers";
import {
  ClickRequest,
  FillRequest,
  HandoffRequest,
  LiveRequest,
  NavigateRequest,
  type PageSnapshot,
  ResumeRequest,
  ROOM_RUN,
  runPrefix,
} from "../contracts";
import type { Env } from "../env";
import { EdgeError, base64Encode } from "../http";
import { type ChildContext, type ChildEvent, type ChildReply, childError } from "../room/child";
import { blockedTarget } from "../sandbox/egress";
import { type BrowserDriver, PuppeteerDriver, VIEWPORT } from "./driver";
import {
  MAX_ELEMENT_TEXT_BYTES,
  MAX_FILL_BYTES,
  MAX_SELECTOR_BYTES,
  MAX_SNAPSHOT_ELEMENTS,
  MAX_SNAPSHOT_TEXT_BYTES,
  MAX_URL_BYTES,
  SNAPSHOT_EXPRESSION,
  clickExpression,
  fillExpression,
} from "./scripts";

export const BROWSER_IDLE_MS = 10 * 60 * 1000;
const LIVE_VIEW_MS = 15 * 60 * 1000;
// A link that lets a member type into the page is short-lived; the pane
// fetches a new one before it expires (B13).
const CONTROL_VIEW_MS = 5 * 60 * 1000;
// The DO's own alarm closes a session the room's sweep left open, this long
// after the sweep should have closed it, and retries a close that failed.
const BACKSTOP_GRACE_MS = 2 * 60 * 1000;
const CLOSE_RETRY_MS = 60 * 1000;
const MAX_CLOSE_RETRIES = 10;
const DEFAULT_HANDOFF_MS = 10 * 60 * 1000;
const MAX_SCREENSHOT_BYTES = 10 << 20;

export type BrowserOp = "navigate" | "snapshot" | "click" | "fill" | "screenshot" | "reset" | "live" | "handoff" | "handoff/state" | "resume";

export type DriverFactory = (env: Env, name: string) => BrowserDriver;

let driverFactory: DriverFactory | null = null;

export function setDriverFactory(factory: DriverFactory | null): void {
  driverFactory = factory;
}

interface Handoff {
  handoffId: string;
  reason: string;
  startedAt: number;
  member: string;
  // Older handoffs have no timeout stored: they get the default.
  timeoutMs?: number;
}

// A stopped browser, as the reply to watching or handing it back.
const STOPPED = "this browser has stopped";

export class BrowserDO extends DurableObject<Env> {
  private driverInstance: BrowserDriver | null = null;
  private queue: Promise<unknown> = Promise.resolve();

  async op(context: ChildContext, op: BrowserOp, body: unknown): Promise<ChildReply> {
    const events: ChildEvent[] = [];
    try {
      await this.bind(context);
      const result = await this.serialize(() => this.run(context, op, body, events));
      return { status: 200, body: result, events };
    } catch (error) {
      return childError(error, events);
    }
  }

  async stop(reason: string): Promise<ChildReply> {
    const events: ChildEvent[] = [];
    try {
      await this.serialize(async () => {
        const sessionId = await this.closeSession();
        if (sessionId) events.push({ type: "browser.stopped", summary: `Browser closed (${reason})`, data: { reason, browserSession: sessionId } });
      });
      return { status: 200, body: { stopped: true }, events };
    } catch (error) {
      return childError(error, events);
    }
  }

  // alarm closes what the room's sweep didn't: a session idle past the
  // limit (the room's own stop failed or never came), or one whose close
  // failed. It never runs while a handoff is still within its time.
  async alarm(): Promise<void> {
    await this.serialize(async () => {
      const sessionId = await this.ctx.storage.get<string>("session");
      if (!sessionId) return;
      const due = await this.closeDue();
      if (due > Date.now()) {
        await this.ctx.storage.setAlarm(due);
        return;
      }
      try {
        await this.closeSession();
      } catch (error) {
        console.error(JSON.stringify({ msg: "closing idle browser failed", error: String(error) }));
      }
    });
  }

  // closeSession ends the stored session (reconnecting to it when the driver
  // has nothing attached) and only then forgets it. When the close fails, the
  // id stays and the alarm tries again, a bounded number of times.
  private async closeSession(): Promise<string | undefined> {
    const sessionId = await this.ctx.storage.get<string>("session");
    try {
      await this.driver().close(sessionId);
    } catch (error) {
      const retries = ((await this.ctx.storage.get<number>("closeRetries")) ?? 0) + 1;
      if (retries <= MAX_CLOSE_RETRIES) {
        await this.ctx.storage.put("closeRetries", retries);
        await this.ctx.storage.setAlarm(Date.now() + CLOSE_RETRY_MS);
      } else {
        // Browser Run's keep-alive ends it in the end; stop retrying.
        await this.ctx.storage.delete(["session", "handoff", "closeRetries", "lastUsedAt"]);
      }
      throw new EdgeError("unavailable", `the browser could not be closed: ${String((error as Error).message ?? error)}`, true);
    }
    await this.ctx.storage.delete(["session", "handoff", "closeRetries", "lastUsedAt"]);
    return sessionId;
  }

  // closeDue is when an unused session should be closed: the idle limit
  // after the agent last used it, or the end of an open handoff if later.
  private async closeDue(): Promise<number> {
    if ((await this.ctx.storage.get<number>("closeRetries")) !== undefined) return 0;
    const lastUsed = (await this.ctx.storage.get<number>("lastUsedAt")) ?? 0;
    const handoff = await this.ctx.storage.get<Handoff>("handoff");
    const handoffEnd = handoff ? handoff.startedAt + (handoff.timeoutMs ?? DEFAULT_HANDOFF_MS) : 0;
    return Math.max(lastUsed + BROWSER_IDLE_MS, handoffEnd) + BACKSTOP_GRACE_MS;
  }

  private async used(): Promise<void> {
    await this.ctx.storage.put("lastUsedAt", Date.now());
    await this.ctx.storage.setAlarm(await this.closeDue());
  }

  private serialize<T>(work: () => Promise<T>): Promise<T> {
    const next = this.queue.then(work, work);
    this.queue = next.catch(() => undefined);
    return next;
  }

  private driver(): BrowserDriver {
    this.driverInstance ??= driverFactory ? driverFactory(this.env, this.ctx.id.name ?? "") : new PuppeteerDriver(this.env.BROWSER, BROWSER_IDLE_MS + 60_000);
    return this.driverInstance;
  }

  private async bind(context: ChildContext): Promise<void> {
    const stored = await this.ctx.storage.get<{ org: string; kind: string; id: string; name: string }>("identity");
    if (!stored) {
      await this.ctx.storage.put("identity", { org: context.org, kind: context.room.kind, id: context.room.id, name: context.name });
      return;
    }
    if (stored.org !== context.org || stored.kind !== context.room.kind || stored.id !== context.room.id || stored.name !== context.name) {
      throw new EdgeError("forbidden", "this browser belongs to another room");
    }
  }

  // session opens the browser, reconnecting to the stored session when it is
  // still alive, and reports whether a new one had to start.
  private async session(events: ChildEvent[]): Promise<boolean> {
    const stored = await this.ctx.storage.get<string>("session");
    let opened: { sessionId: string; created: boolean };
    try {
      opened = await this.driver().open(stored);
    } catch (error) {
      const reason = String((error as Error).message ?? error).slice(0, 300);
      events.push({ type: "browser.failed", summary: "The browser didn't start", data: { reason } });
      throw new EdgeError("unavailable", `the browser did not start: ${reason}`, true);
    }
    if (opened.sessionId !== stored) await this.ctx.storage.put("session", opened.sessionId);
    await this.used();
    if (opened.created) {
      await this.ctx.storage.delete("handoff");
      events.push({ type: "browser.started", summary: "Browser started", data: { browserSession: opened.sessionId, recording: true } });
    }
    return opened.created;
  }

  // attached reconnects to the running session and never starts one. When
  // the session has ended, it forgets it, says so in the log, and refuses
  // with conflict: watching or handing back must not launch a browser.
  private async attached(events: ChildEvent[]): Promise<void> {
    const stored = await this.ctx.storage.get<string>("session");
    if (stored && (await this.driver().attach(stored))) return;
    // A session Browser Run still lists didn't answer this time: keep it.
    if (stored && (await this.driver().alive(stored).catch(() => true))) {
      throw new EdgeError("unavailable", "the browser did not answer; try again", true);
    }
    if (stored) {
      await this.ctx.storage.delete(["session", "handoff", "lastUsedAt"]);
      events.push({ type: "browser.stopped", summary: "Browser closed (it had ended)", data: { reason: "ended", browserSession: stored } });
    }
    throw new EdgeError("conflict", STOPPED);
  }

  // handoffOver reports whether a stored handoff has run past its time.
  private handoffOver(handoff: Handoff): boolean {
    return Date.now() - handoff.startedAt >= (handoff.timeoutMs ?? DEFAULT_HANDOFF_MS);
  }

  private async endHandoff(handoff: Handoff, events: ChildEvent[]): Promise<void> {
    await this.ctx.storage.delete("handoff");
    events.push({
      type: "browser.handoff.timeout",
      summary: "Nobody took over in time; the agent has the browser back",
      data: { handoffId: handoff.handoffId },
      durationMs: Date.now() - handoff.startedAt,
    });
  }

  // agentTurn refuses an agent's action while a member has control.
  private async agentTurn(events: ChildEvent[]): Promise<void> {
    const handoff = await this.ctx.storage.get<Handoff>("handoff");
    if (!handoff) return;
    if (this.handoffOver(handoff)) {
      await this.endHandoff(handoff, events);
      return;
    }
    const state = await this.driver()
      .handoffState()
      .catch(() => ({ active: true }));
    if (state.active) throw new EdgeError("conflict", "a member has control of this browser; wait for them to hand back");
    await this.ctx.storage.delete("handoff");
  }

  private async run(context: ChildContext, op: BrowserOp, body: unknown, events: ChildEvent[]): Promise<unknown> {
    switch (op) {
      case "navigate": {
        const { url } = NavigateRequest.parse(body);
        const target = checkURL(url);
        const recreated = await this.session(events);
        await this.agentTurn(events);
        const started = Date.now();
        try {
          await this.driver().navigate(target.toString());
          await this.driver().waitUntilReady();
        } catch (error) {
          const reason = String((error as Error).message ?? error).slice(0, 300);
          events.push({ type: "browser.failed", summary: `Couldn't open ${target.hostname}`, data: { url: target.toString(), reason } });
          throw new EdgeError("invalid_request", `navigation failed: ${reason}`);
        }
        const page = await this.snapshot();
        events.push({ type: "browser.navigate", summary: `Opened ${hostOf(page.url) || target.hostname}`, data: { url: page.url, title: page.title }, durationMs: Date.now() - started });
        return withRecreated({ page }, recreated);
      }
      case "snapshot": {
        const recreated = await this.session(events);
        const page = await this.snapshot();
        events.push({ type: "browser.snapshot", summary: `Read ${hostOf(page.url) || "the page"}`, data: { url: page.url } });
        return withRecreated({ page }, recreated);
      }
      case "click": {
        const { selector } = ClickRequest.parse(body);
        checkSelector(selector);
        const recreated = await this.session(events);
        await this.agentTurn(events);
        await this.pageScript(clickExpression(selector));
        await this.driver().waitUntilReady();
        const page = await this.snapshot();
        events.push({ type: "browser.click", summary: `Clicked ${selector.slice(0, 120)}`, data: { selector: selector.slice(0, 500), url: page.url } });
        return withRecreated({ page }, recreated);
      }
      case "fill": {
        const { selector, text } = FillRequest.parse(body);
        checkSelector(selector);
        if (new TextEncoder().encode(text).length > MAX_FILL_BYTES) throw new EdgeError("too_large", "text exceeds the 100 KiB fill limit");
        const recreated = await this.session(events);
        await this.agentTurn(events);
        await this.pageScript(fillExpression(selector, text));
        const page = await this.snapshot();
        // The text itself stays out of the log: it may be personal.
        events.push({ type: "browser.fill", summary: `Filled ${selector.slice(0, 120)}`, data: { selector: selector.slice(0, 500), length: text.length, url: page.url } });
        return withRecreated({ page }, recreated);
      }
      case "screenshot": {
        const recreated = await this.session(events);
        const image = await this.driver().screenshot();
        if (image.length === 0) throw new EdgeError("internal", "the browser returned an empty screenshot");
        if (image.length > MAX_SCREENSHOT_BYTES) throw new EdgeError("too_large", "screenshot exceeds 10 MiB");
        const digest = new Uint8Array(await crypto.subtle.digest("SHA-256", image));
        const hex = [...digest.subarray(0, 8)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
        const key = `${runPrefix(context.org, context.room, context.run ?? ROOM_RUN)}media/screenshot-${hex}.png`;
        await this.env.RUNS.put(key, image, { httpMetadata: { contentType: "image/png" }, customMetadata: { org: context.org, member: context.member } });
        events.push({ type: "browser.screenshot", summary: "Took a screenshot", data: { key } });
        return { key, mimeType: "image/png", width: VIEWPORT.width, height: VIEWPORT.height, data: base64Encode(image), ...(recreated ? { sessionRecreated: true } : {}) };
      }
      case "reset": {
        // Reset promises the cookies are gone: report success only once the
        // old session is really closed.
        const sessionId = await this.closeSession();
        events.push({ type: "browser.reset", summary: "Browser reset: cookies and pages cleared", data: { browserSession: sessionId ?? null } });
        return { reset: true };
      }
      case "live": {
        const { mode } = LiveRequest.parse(body);
        await this.attached(events);
        const lifetime = mode === "full" ? CONTROL_VIEW_MS : LIVE_VIEW_MS;
        const url = await this.driver().liveView(mode ?? "tab", lifetime);
        return { url, expiresAt: new Date(Date.now() + lifetime).toISOString() };
      }
      case "handoff": {
        const request = HandoffRequest.parse(body);
        await this.session(events);
        const existing = await this.ctx.storage.get<Handoff>("handoff");
        if (existing) throw new EdgeError("conflict", "a handoff is already open");
        const timeout = request.timeoutMs ?? DEFAULT_HANDOFF_MS;
        const handoffId = await this.driver().handoff(request.reason, timeout);
        await this.ctx.storage.put("handoff", { handoffId, reason: request.reason, startedAt: Date.now(), member: context.member, timeoutMs: timeout } satisfies Handoff);
        await this.used();
        const live = await this.driver().liveView("tab", Math.max(timeout, LIVE_VIEW_MS));
        events.push({ type: "browser.handoff", summary: `Asked a member to take over: ${request.reason.slice(0, 200)}`, data: { handoffId, reason: request.reason.slice(0, 1_000) } });
        return { handoffId, live: { url: live, expiresAt: new Date(Date.now() + Math.max(timeout, LIVE_VIEW_MS)).toISOString() } };
      }
      case "handoff/state": {
        const handoff = await this.ctx.storage.get<Handoff>("handoff");
        if (!handoff) return { active: false };
        if (this.handoffOver(handoff)) {
          await this.endHandoff(handoff, events);
          return { active: false, handoffId: handoff.handoffId };
        }
        // A session that has ended has no handoff open: say so rather than
        // guessing it is still active.
        const sessionId = await this.ctx.storage.get<string>("session");
        const alive = sessionId ? await this.driver().alive(sessionId).catch(() => true) : false;
        if (!alive) {
          await this.ctx.storage.delete("handoff");
          return { active: false, handoffId: handoff.handoffId };
        }
        const state = await this.driver()
          .attach(sessionId)
          .then((attached) => (attached ? this.driver().handoffState() : { active: false }))
          .catch(() => ({ active: true }));
        if (!state.active) {
          await this.ctx.storage.delete("handoff");
          return { active: false, handoffId: handoff.handoffId };
        }
        return { active: true, handoffId: handoff.handoffId, reason: handoff.reason, elapsedMs: Date.now() - handoff.startedAt };
      }
      case "resume": {
        const request = ResumeRequest.parse(body);
        const handoff = await this.ctx.storage.get<Handoff>("handoff");
        if (!handoff) throw new EdgeError("conflict", "no handoff is open");
        await this.attached(events);
        try {
          await this.driver().handoffComplete(request.success, request.note);
        } catch (error) {
          // The handoff already ended on Browser Run's side (it timed out):
          // the agent has the browser back, which is what the member wants.
          const state = await this.driver()
            .handoffState()
            .catch(() => ({ active: true }));
          if (state.active) throw error;
        }
        await this.ctx.storage.delete("handoff");
        await this.used();
        const page = await this.snapshot();
        events.push({
          type: "browser.resume",
          summary: request.success ? "A member handed control back" : "A member handed back without finishing",
          data: { handoffId: handoff.handoffId, success: request.success, url: page.url },
          durationMs: Date.now() - handoff.startedAt,
        });
        return { resumed: true, page };
      }
    }
  }

  private async pageScript(expression: string): Promise<void> {
    try {
      await this.driver().evaluate(expression);
    } catch (error) {
      throw new EdgeError("invalid_request", `page script failed: ${String((error as Error).message ?? error)}`);
    }
  }

  private async snapshot(): Promise<PageSnapshot> {
    let raw: RawSnapshot;
    try {
      raw = await this.driver().evaluate<RawSnapshot>(SNAPSHOT_EXPRESSION);
    } catch (error) {
      throw new EdgeError("internal", `snapshot failed: ${String((error as Error).message ?? error)}`);
    }
    return normalizeSnapshot(raw);
  }
}

interface RawSnapshot {
  title?: string;
  url?: string;
  text?: string;
  elements?: Array<{ selector?: string; tag?: string; role?: string; type?: string; name?: string; text?: string; href?: string; disabled?: boolean }>;
  elements_truncated?: boolean;
}

// normalizeSnapshot bounds a snapshot as agentcorebrowser's normalizeSnapshot
// does, and drops empty optional fields.
export function normalizeSnapshot(raw: RawSnapshot): PageSnapshot {
  const [text, textTruncated] = truncate(raw.text ?? "", MAX_SNAPSHOT_TEXT_BYTES);
  const all = raw.elements ?? [];
  const elements = all.slice(0, MAX_SNAPSHOT_ELEMENTS).map((element) => {
    const out: PageSnapshot["elements"] extends (infer E)[] | undefined ? E : never = {
      selector: truncate(element.selector ?? "", MAX_SELECTOR_BYTES)[0],
      tag: truncate(element.tag ?? "", 64)[0],
    };
    for (const [field, limit] of [["role", 128], ["type", 128], ["name", MAX_ELEMENT_TEXT_BYTES], ["text", MAX_ELEMENT_TEXT_BYTES], ["href", MAX_URL_BYTES]] as const) {
      const value = truncate(element[field] ?? "", limit)[0];
      if (value) out[field] = value;
    }
    if (element.disabled) out.disabled = true;
    return out;
  });
  return {
    title: truncate(raw.title ?? "", MAX_ELEMENT_TEXT_BYTES)[0],
    url: truncate(raw.url ?? "", MAX_URL_BYTES)[0],
    ...(text ? { text } : {}),
    ...(elements.length ? { elements } : {}),
    ...(textTruncated ? { textTruncated } : {}),
    ...(raw.elements_truncated || all.length > MAX_SNAPSHOT_ELEMENTS ? { elementsTruncated: true } : {}),
  };
}

function truncate(value: string, limit: number): [string, boolean] {
  const bytes = new TextEncoder().encode(value);
  if (bytes.length <= limit) return [value, false];
  let end = limit;
  while (end > 0 && (bytes[end]! & 0xc0) === 0x80) end--;
  return [new TextDecoder().decode(bytes.subarray(0, end)), true];
}

// checkURL accepts a public http(s) URL without credentials, as the
// browser_navigate tool's contract says.
export function checkURL(raw: string): URL {
  const value = raw.trim();
  if (new TextEncoder().encode(value).length > MAX_URL_BYTES) throw new EdgeError("invalid_request", "URL is too long");
  let url: URL;
  try {
    url = new URL(value);
  } catch {
    throw new EdgeError("invalid_request", "URL must be absolute");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") throw new EdgeError("invalid_request", "only http and https URLs can be opened");
  if (url.username || url.password) throw new EdgeError("invalid_request", "URLs containing credentials are rejected");
  const blocked = blockedTarget(url);
  if (blocked) throw new EdgeError("forbidden", `the browser can't open ${blocked}es`);
  return url;
}

function checkSelector(selector: string): void {
  if (selector.trim() === "") throw new EdgeError("invalid_request", "CSS selector is required");
  if (new TextEncoder().encode(selector).length > MAX_SELECTOR_BYTES) throw new EdgeError("invalid_request", "CSS selector is too long");
  if (selector.includes("\0")) throw new EdgeError("invalid_request", "CSS selector must not contain null bytes");
}

function hostOf(url: string): string {
  try {
    return new URL(url).hostname;
  } catch {
    return "";
  }
}

function withRecreated<T extends object>(body: T, recreated: boolean): T & { sessionRecreated?: true } {
  return recreated ? { ...body, sessionRecreated: true } : body;
}
