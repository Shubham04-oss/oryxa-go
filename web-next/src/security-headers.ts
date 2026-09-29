// The security headers on what the Worker (src/server.ts) sends to
// app.oryxa.in: the app's pages, and the Worker's own answers about a
// conversation or a purge. Static files under /assets are served by the
// platform's asset layer without running the Worker, and /v1/* goes to Go
// through the tunnel (deploy/production/cloudflare.tf), so neither passes
// here.

/**
 * What no page of the app may do, whatever else it loads. Enforced. It
 * asks nothing of TanStack Start's scripts, the sockets to the Durable
 * Objects or the API calls, so it cannot break them:
 *
 * - `frame-ancestors 'none'`: no other site can frame the app (clickjacking).
 *   `X-Frame-Options` says the same to a browser that predates it.
 * - `object-src 'none'`: no plugins.
 * - `base-uri 'self'`: injected markup cannot re-point relative URLs.
 */
const enforcedPolicy = [
  "frame-ancestors 'none'",
  "object-src 'none'",
  "base-uri 'self'",
]

/**
 * The policy the app would be held to if `default-src` were enforced:
 * reported, never enforced, so a mistake here costs a console message and
 * nothing else. What each source is for:
 *
 * - `script-src 'unsafe-inline'`: TanStack Start's server rendering puts the
 *   state it hydrates from in an inline script, and next-themes sets the
 *   theme in one before first paint. A nonce for both is the way to drop it.
 * - `style-src 'unsafe-inline'`: React writes `style` attributes.
 * - `img-src data: https:`: DiceBear avatars are `data:` URIs, and the Tools
 *   catalog's logos are URLs Composio's catalog gives, on any https host.
 * - `connect-src`: the API is the app's own origin (`/v1`, `VITE_API_URL`
 *   unset), and the Durable Objects' sockets are on it too. Its socket
 *   origin is named because a browser may not count `'self'` as `ws:`/`wss:`.
 *
 * The only report the sign-in page gives (the rest of the app has not been
 * browsed under it): Zod probes `Function("")` once at load to choose its
 * fast path, and falls back when it is refused, so the console names
 * `unsafe-eval`. Enforcing this costs Zod that fast path, not correctness.
 * Move a directive into `enforcedPolicy` once a browsing session across the
 * app, with the console open, has shown it no other violation.
 */
function reportOnlyPolicy(socketOrigin: string): string {
  return [
    "default-src 'self'",
    "script-src 'self' 'unsafe-inline'",
    "style-src 'self' 'unsafe-inline'",
    "img-src 'self' data: https:",
    "font-src 'self' data:",
    `connect-src 'self' ${socketOrigin}`,
    "manifest-src 'self'",
    "frame-src 'none'",
    "form-action 'self'",
    "object-src 'none'",
    "base-uri 'self'",
  ].join("; ")
}

/** The headers for a response to a request for `url`. */
export function securityHeaders(url: URL): Record<string, string> {
  const secure = url.protocol === "https:"
  return {
    // A browser ignores this over plain http, and RFC 6797 §7.2 forbids
    // sending it there, as `vite dev` and `vite preview` do.
    ...(secure && {
      "Strict-Transport-Security": "max-age=31536000; includeSubDomains",
    }),
    "X-Content-Type-Options": "nosniff",
    "Referrer-Policy": "strict-origin-when-cross-origin",
    "X-Frame-Options": "DENY",
    "Content-Security-Policy": enforcedPolicy.join("; "),
    "Content-Security-Policy-Report-Only": reportOnlyPolicy(
      `${secure ? "wss" : "ws"}://${url.host}`
    ),
  }
}

/**
 * `response` with the security headers set over any it carries, for pages
 * and answers alike. A WebSocket upgrade is returned as it is: it is no
 * document, and rebuilding the response that carries the socket would risk
 * the socket. The copy shares `response`'s body, which stays a stream.
 */
export function withSecurityHeaders(
  response: Response,
  request: Request
): Response {
  if (response.status === 101 || response.webSocket) return response
  // The Workers idiom for a response whose headers may be immutable, as they
  // are for one that came from `fetch` or a Durable Object: a copy whose
  // headers can be set.
  const copy = new Response(response.body, response)
  for (const [name, value] of Object.entries(
    securityHeaders(new URL(request.url))
  )) {
    copy.headers.set(name, value)
  }
  return copy
}
