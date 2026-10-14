// BrowserDriver is what BrowserDO needs from a browser session.
// PuppeteerDriver is the real one, on Cloudflare Browser Run through
// @cloudflare/puppeteer; tests use a fake.
import puppeteer, { type Browser, type Page } from "@cloudflare/puppeteer";

export const VIEWPORT = { width: 1440, height: 900 };
const NAVIGATION_TIMEOUT_MS = 30_000;
const READY_TIMEOUT_MS = 15_000;

export interface HandoffInfo {
  active: boolean;
  handoffId?: string;
  instructions?: string;
  durationMs?: number;
}

export interface BrowserDriver {
  // open connects to sessionId when it's still alive, or launches a new
  // session; it reports the session it is on and whether it is new.
  open(sessionId: string | undefined): Promise<{ sessionId: string; created: boolean }>;
  // evaluate runs a page script and returns its value.
  evaluate<T>(expression: string): Promise<T>;
  navigate(url: string): Promise<void>;
  waitUntilReady(): Promise<void>;
  screenshot(): Promise<Uint8Array>;
  liveView(mode: "tab" | "full", expiresInMs: number): Promise<string>;
  handoff(instructions: string, timeoutMs: number): Promise<string>;
  handoffState(): Promise<HandoffInfo>;
  handoffComplete(success: boolean, reason: string | undefined): Promise<void>;
  close(): Promise<void>;
}

export class PuppeteerDriver implements BrowserDriver {
  private browser: Browser | null = null;
  private page: Page | null = null;

  constructor(
    private readonly binding: Fetcher,
    private readonly keepAliveMs: number,
  ) {}

  async open(sessionId: string | undefined): Promise<{ sessionId: string; created: boolean }> {
    if (this.browser?.connected && this.page) return { sessionId: this.browser.sessionId(), created: false };
    if (sessionId) {
      try {
        this.browser = await puppeteer.connect(this.binding as never, sessionId);
        const pages = await this.browser.pages();
        this.page = pages[0] ?? (await this.browser.newPage());
        await this.page.setViewport(VIEWPORT);
        return { sessionId, created: false };
      } catch {
        // The session ended (its keep-alive ran out): start a new one.
      }
    }
    this.browser = await puppeteer.launch(this.binding as never, { keep_alive: this.keepAliveMs, recording: true });
    this.page = (await this.browser.pages())[0] ?? (await this.browser.newPage());
    await this.page.setViewport(VIEWPORT);
    return { sessionId: this.browser.sessionId(), created: true };
  }

  private current(): Page {
    if (!this.page) throw new Error("browser session is not open");
    return this.page;
  }

  async evaluate<T>(expression: string): Promise<T> {
    return (await this.current().evaluate(expression)) as T;
  }

  async navigate(url: string): Promise<void> {
    const response = await this.current().goto(url, { waitUntil: "domcontentloaded", timeout: NAVIGATION_TIMEOUT_MS });
    if (response === null && this.current().url() === "about:blank") throw new Error("navigation failed");
  }

  async waitUntilReady(): Promise<void> {
    const deadline = Date.now() + READY_TIMEOUT_MS;
    for (;;) {
      try {
        const state = await this.current().evaluate("document.readyState");
        if (state === "interactive" || state === "complete") return;
      } catch (error) {
        // A navigation can briefly destroy the execution context; retrying
        // is safe because this check never changes the page.
        if (Date.now() > deadline) throw error;
      }
      if (Date.now() > deadline) throw new Error("the page did not finish loading");
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
  }

  async screenshot(): Promise<Uint8Array> {
    const image = await this.current().screenshot({ type: "png", captureBeyondViewport: false });
    return image instanceof Uint8Array ? image : new Uint8Array(image as unknown as ArrayBuffer);
  }

  private async cdp<T>(method: string, params: Record<string, unknown>): Promise<T> {
    const session = await this.current().createCDPSession();
    try {
      return (await session.send(method as never, params as never)) as T;
    } finally {
      await session.detach().catch(() => undefined);
    }
  }

  async liveView(mode: "tab" | "full", expiresInMs: number): Promise<string> {
    const view = await this.cdp<{ devtoolsFrontendUrl: string }>("Cloudflare.getLiveView", { mode, expiresInMs });
    return view.devtoolsFrontendUrl;
  }

  async handoff(instructions: string, timeoutMs: number): Promise<string> {
    const started = await this.cdp<{ handoffId: string }>("Cloudflare.handoff", { instructions, timeout: timeoutMs });
    return started.handoffId;
  }

  async handoffState(): Promise<HandoffInfo> {
    return this.cdp<HandoffInfo>("Cloudflare.getHandoffState", {});
  }

  async handoffComplete(success: boolean, reason: string | undefined): Promise<void> {
    await this.cdp("Cloudflare.handoffComplete", { success, ...(reason ? { reason } : {}) });
  }

  async close(): Promise<void> {
    const browser = this.browser;
    this.browser = null;
    this.page = null;
    if (browser) await browser.close().catch(() => undefined);
  }
}
