// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest"
import { apiFetch, resumeSessionAfterSignIn, sessionHasEnded } from "./client"
import { signIn } from "@/lib/auth/client"
import { getOAuthClient } from "@/lib/apps/client"
import { consentAgentRequest } from "@/lib/agents/agent-requests"
import {
  fetchArtifactLink,
  retryUnlessRefused,
  streamRunRequest,
} from "@/lib/threads/client"

const credentials = {
  email: "member@example.com",
  password: "correct-password",
}
const json = (body: unknown, status = 200) => Response.json(body, { status })

function pendingJson(response: Response) {
  let answer!: (body: unknown) => void
  const read = vi.spyOn(response, "json").mockImplementation(
    () =>
      new Promise((resolve) => {
        answer = resolve
      })
  )
  return { read, answer: (body: unknown) => answer(body) }
}

function protectedResponse(response: Response) {
  vi.stubGlobal(
    "fetch",
    vi.fn(async (url: string) =>
      url === "/v1/auth/password/login"
        ? new Response(null, { status: 204 })
        : response
    )
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
  resumeSessionAfterSignIn()
  vi.restoreAllMocks()
})

describe("custom protected response consumers", () => {
  it("keeps caller abort silent during delayed stream refusal decoding", async () => {
    const response = json({}, 403)
    const body = pendingJson(response)
    protectedResponse(response)
    const controller = new AbortController()
    const onItem = vi.fn()
    const request = streamRunRequest(
      "/v1/organizations/org_sim/threads/th_old/runs",
      { message: "question" },
      { onItem, signal: controller.signal }
    )
    await vi.waitFor(() => expect(body.read).toHaveBeenCalledOnce())
    controller.abort()
    body.answer({ error: "forbidden" })
    await expect(request).resolves.toEqual({ outcome: "aborted" })
    expect(onItem).not.toHaveBeenCalled()
    expect(sessionHasEnded()).toBe(false)
  })

  it.each([200, 400])(
    "does not release a delayed app %s body or field error after sign-in",
    async (status) => {
      const response = json(
        { error: { code: "invalid_credentials", field: "token" } },
        status
      )
      const bodyResponse = status === 400 ? response.clone() : response
      if (status === 400)
        vi.spyOn(response, "clone").mockReturnValue(bodyResponse)
      const body = pendingJson(bodyResponse)
      protectedResponse(response)
      const request = getOAuthClient("org_sim", "github")
      const cancelled = expect(request).rejects.toMatchObject({
        name: "AbortError",
      })
      await vi.waitFor(() => expect(body.read).toHaveBeenCalledOnce())
      await signIn(credentials)
      body.answer(
        status === 200
          ? { client_id: "old private client" }
          : { error: { code: "invalid_credentials", field: "token" } }
      )
      await cancelled
      expect(sessionHasEnded()).toBe(false)
    }
  )

  it.each([200, 409])(
    "does not release a delayed consent %s result or winner after sign-in",
    async (status) => {
      const response = json({}, status)
      const bodyResponse = status === 409 ? response.clone() : response
      if (status === 409)
        vi.spyOn(response, "clone").mockReturnValue(bodyResponse)
      const body = pendingJson(bodyResponse)
      protectedResponse(response)
      const request = consentAgentRequest("org_sim", "irq_old", "summary")
      const cancelled = expect(request).rejects.toMatchObject({
        name: "AbortError",
      })
      await vi.waitFor(() => expect(body.read).toHaveBeenCalledOnce())
      await signIn(credentials)
      body.answer(
        status === 200
          ? { status: "approved", private: "old consent" }
          : {
              error: { code: "already_decided" },
              winner: {
                request_id: "irq_old",
                status: "approved",
                revision: 2,
              },
            }
      )
      await cancelled
    }
  )

  it("does not release an old artifact URL after its delayed body cancellation", async () => {
    let answerCancel!: () => void
    const cancel = vi.fn(
      () =>
        new Promise<void>((resolve) => {
          answerCancel = resolve
        })
    )
    const response = new Response(new ReadableStream({ cancel }), {
      headers: { "Content-Type": "text/html", "Content-Disposition": "inline" },
    })
    protectedResponse(response)
    const request = fetchArtifactLink("org_sim", "th_old", {
      name: "report.html",
    })
    const cancelled = expect(request).rejects.toMatchObject({
      name: "AbortError",
    })
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledOnce())
    await signIn(credentials)
    answerCancel()
    await cancelled
  })

  it.each(["sign-in", "expiry"])(
    "cancels an accepted stream before old items can arrive after %s",
    async (change) => {
      const cancel = vi.fn()
      const response = new Response(
        new ReadableStream<Uint8Array>({ cancel }),
        { headers: { "Content-Type": "text/event-stream" } }
      )
      vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string) => {
          if (url === "/v1/auth/password/login")
            return new Response(null, { status: 204 })
          if (url.endsWith("/runs")) return response
          return new Response(null, { status: 401 })
        })
      )
      const onResponse = vi.fn()
      const onItem = vi.fn()
      const request = streamRunRequest(
        "/v1/organizations/org_sim/threads/th_old/runs",
        { message: "question" },
        { onItem, onResponse }
      )
      const cancelled = expect(request).rejects.toMatchObject(
        change === "sign-in"
          ? { name: "AbortError" }
          : { status: 401, code: "session_ended" }
      )
      await vi.waitFor(() => expect(onResponse).toHaveBeenCalledOnce())
      if (change === "sign-in") await signIn(credentials)
      else await apiFetch("/v1/expiry")
      await cancelled
      expect(cancel).toHaveBeenCalledOnce()
      expect(onItem).not.toHaveBeenCalled()
    }
  )

  it("never retries obsolete-generation or terminal private reads", async () => {
    expect(
      retryUnlessRefused(0, new DOMException("old request", "AbortError"))
    ).toBe(false)
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response(null, { status: 401 }))
    )
    await apiFetch("/v1/expiry")
    expect(retryUnlessRefused(0, new TypeError("offline"))).toBe(false)
  })

  it.each(["sign-in", "expiry"])(
    "aborts protected HTTP in flight on %s while public auth remains usable",
    async (change) => {
      let protectedSignal: AbortSignal | null | undefined
      vi.stubGlobal(
        "fetch",
        vi.fn(async (url: string, init?: RequestInit) => {
          if (url === "/v1/auth/password/login")
            return new Response(null, { status: 204 })
          if (url === "/v1/slow") {
            protectedSignal = init?.signal
            return new Promise<Response>((_, reject) =>
              protectedSignal?.addEventListener("abort", () =>
                reject(protectedSignal?.reason)
              )
            )
          }
          return new Response(null, { status: 401 })
        })
      )
      const pending = apiFetch("/v1/slow")
      const cancelled = expect(pending).rejects.toMatchObject(
        change === "sign-in"
          ? { name: "AbortError" }
          : { status: 401, code: "session_ended" }
      )
      if (change === "sign-in") await signIn(credentials)
      else await apiFetch("/v1/expiry")
      await cancelled
      expect(protectedSignal?.aborted).toBe(true)
      await signIn(credentials)
      expect(sessionHasEnded()).toBe(false)
    }
  )
})
