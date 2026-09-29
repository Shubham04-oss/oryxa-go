import { describe, expect, it } from "vitest"
import { securityHeaders, withSecurityHeaders } from "./security-headers"

const page = "https://app.oryxa.in/"

/** A policy's directives by name: `{ "default-src": ["'self'"], … }`. */
function directives(policy: string): Record<string, string[]> {
  return Object.fromEntries(
    policy.split(";").map((directive) => {
      const [name, ...sources] = directive.trim().split(/\s+/)
      return [name, sources]
    })
  )
}

describe("securityHeaders", () => {
  const headers = securityHeaders(new URL(page))

  it("sets HSTS, nosniff, a referrer policy and frame protection", () => {
    expect(headers["Strict-Transport-Security"]).toBe(
      "max-age=31536000; includeSubDomains"
    )
    expect(headers["X-Content-Type-Options"]).toBe("nosniff")
    expect(headers["Referrer-Policy"]).toBe("strict-origin-when-cross-origin")
    expect(headers["X-Frame-Options"]).toBe("DENY")
  })

  it("enforces only what cannot break the app's scripts, sockets or API calls", () => {
    expect(headers["Content-Security-Policy"]).toBe(
      "frame-ancestors 'none'; object-src 'none'; base-uri 'self'"
    )
    // Nothing that limits what the page may load or connect to.
    expect(Object.keys(directives(headers["Content-Security-Policy"]))).toEqual(
      ["frame-ancestors", "object-src", "base-uri"]
    )
  })

  it("holds a fuller policy to report-only", () => {
    const fuller = directives(headers["Content-Security-Policy-Report-Only"])

    expect(fuller["default-src"]).toEqual(["'self'"])
    // TanStack Start and next-themes write inline scripts; eval stays refused.
    expect(fuller["script-src"]).toEqual(["'self'", "'unsafe-inline'"])
    expect(fuller["style-src"]).toEqual(["'self'", "'unsafe-inline'"])
    expect(fuller["img-src"]).toEqual(["'self'", "data:", "https:"])
    expect(fuller["connect-src"]).toEqual(["'self'", "wss://app.oryxa.in"])
    expect(fuller["frame-src"]).toEqual(["'none'"])
    expect(fuller["form-action"]).toEqual(["'self'"])
    expect(headers["Content-Security-Policy-Report-Only"]).not.toContain(
      "unsafe-eval"
    )
    // What is enforced is in it too, so it can replace the enforced policy.
    expect(fuller["object-src"]).toEqual(["'none'"])
    expect(fuller["base-uri"]).toEqual(["'self'"])
  })

  it("names the page's own socket origin, whatever host and scheme serve it", () => {
    const socketOf = (url: string) =>
      directives(
        securityHeaders(new URL(url))["Content-Security-Policy-Report-Only"]
      )["connect-src"]

    expect(socketOf("https://app.oryxa.in:8443/x")).toContain(
      "wss://app.oryxa.in:8443"
    )
    expect(socketOf("http://localhost:3001/")).toContain("ws://localhost:3001")
  })

  it("sends HSTS only over https, where a browser reads it", () => {
    const plain = securityHeaders(new URL("http://localhost:3001/"))

    expect(plain["Strict-Transport-Security"]).toBeUndefined()
    expect(plain["X-Frame-Options"]).toBe("DENY")
    expect(plain["Content-Security-Policy"]).toBeDefined()
  })
})

describe("withSecurityHeaders", () => {
  const request = new Request(page)
  const names = Object.keys(securityHeaders(new URL(page)))

  it("adds them to a page and keeps everything else about it", async () => {
    const original = new Response("<!doctype html><p>hi</p>", {
      status: 200,
      statusText: "Fine",
      headers: {
        "Content-Type": "text/html; charset=utf-8",
        "Cache-Control": "no-store",
      },
    })

    const response = withSecurityHeaders(original, request)

    for (const name of names) {
      expect(response.headers.get(name)).toBe(
        securityHeaders(new URL(page))[name]
      )
    }
    expect(response.status).toBe(200)
    expect(response.statusText).toBe("Fine")
    expect(response.headers.get("Content-Type")).toBe(
      "text/html; charset=utf-8"
    )
    expect(response.headers.get("Cache-Control")).toBe("no-store")
    expect(await response.text()).toBe("<!doctype html><p>hi</p>")
  })

  it("adds them to an API answer, and to one with no body", async () => {
    const refusal = withSecurityHeaders(
      Response.json({ error: { code: "unauthorized" } }, { status: 401 }),
      request
    )
    const empty = withSecurityHeaders(
      new Response(null, { status: 204 }),
      request
    )

    for (const response of [refusal, empty]) {
      expect(response.headers.get("X-Content-Type-Options")).toBe("nosniff")
      expect(response.headers.get("Content-Security-Policy")).toContain(
        "frame-ancestors 'none'"
      )
    }
    expect(refusal.status).toBe(401)
    expect(await refusal.json()).toEqual({ error: { code: "unauthorized" } })
    expect(empty.status).toBe(204)
    expect(empty.body).toBeNull()
  })

  it("sets its own over any the response came with", () => {
    const response = withSecurityHeaders(
      new Response("x", {
        headers: {
          "X-Frame-Options": "SAMEORIGIN",
          "Content-Security-Policy": "frame-ancestors *",
          "Referrer-Policy": "unsafe-url",
        },
      }),
      request
    )

    expect(response.headers.get("X-Frame-Options")).toBe("DENY")
    expect(response.headers.get("Content-Security-Policy")).toBe(
      "frame-ancestors 'none'; object-src 'none'; base-uri 'self'"
    )
    expect(response.headers.get("Referrer-Policy")).toBe(
      "strict-origin-when-cross-origin"
    )
  })

  it("keeps every Set-Cookie", () => {
    const original = new Response("x")
    original.headers.append("Set-Cookie", "a=1; Path=/; HttpOnly")
    original.headers.append("Set-Cookie", "b=2; Path=/; Secure")

    const response = withSecurityHeaders(original, request)

    expect(response.headers.getSetCookie()).toEqual([
      "a=1; Path=/; HttpOnly",
      "b=2; Path=/; Secure",
    ])
  })

  it("copes with a response whose headers cannot be changed", () => {
    // What `fetch` and `Response.redirect` give: an immutable header list.
    const redirect = Response.redirect("https://app.oryxa.in/auth/sign-in", 307)
    expect(() => redirect.headers.set("X-Test", "1")).toThrow(TypeError)

    const response = withSecurityHeaders(redirect, request)

    expect(response.status).toBe(307)
    expect(response.headers.get("Location")).toBe(
      "https://app.oryxa.in/auth/sign-in"
    )
    expect(response.headers.get("X-Frame-Options")).toBe("DENY")
  })

  it("streams the body through without reading it", async () => {
    let pulled = 0
    const body = new ReadableStream<Uint8Array>({
      pull(controller) {
        pulled += 1
        controller.enqueue(new TextEncoder().encode(`chunk ${pulled};`))
        if (pulled === 3) controller.close()
      },
    })

    const response = withSecurityHeaders(new Response(body), request)

    // Wrapping the response has not started reading its stream.
    expect(pulled).toBeLessThanOrEqual(1)
    expect(await response.text()).toBe("chunk 1;chunk 2;chunk 3;")
  })

  it("returns a WebSocket upgrade exactly as it is", () => {
    // Node cannot build a 101 Response; the Worker's is this shape.
    const socket = {}
    const upgrade = {
      status: 101,
      headers: new Headers({ Upgrade: "websocket" }),
      body: null,
      webSocket: socket,
    } as unknown as Response

    expect(withSecurityHeaders(upgrade, request)).toBe(upgrade)
    expect(upgrade.headers.has("Content-Security-Policy")).toBe(false)
    expect(upgrade.headers.has("X-Frame-Options")).toBe(false)
    expect(upgrade.webSocket).toBe(socket)
  })
})
