import { afterEach, describe, expect, it, vi } from "vitest"
import { ApiError } from "@/lib/api/client"
import {
  AnswerGuardResponseError,
  answerGuardKeys,
  applyAnswerGuardChange,
  effectiveLevel,
  getAnswerGuardPreferences,
  isBelowFloor,
  setAnswerGuardPreferences,
} from "./client"
import type { AnswerGuardChange, AnswerGuardPreferences } from "./client"

const path = "/v1/organizations/org-1/answer-guard/preferences"

function json(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  })
}

function stubFetch(respond: (url: string, init: RequestInit) => Response) {
  const fetchMock = vi.fn(async (url: string, init: RequestInit) =>
    respond(url, init)
  )
  vi.stubGlobal("fetch", fetchMock)
  return fetchMock
}

function sent(fetchMock: ReturnType<typeof stubFetch>) {
  return fetchMock.mock.calls.map(([url, init]) => [
    init.method,
    url,
    init.body,
  ])
}

const preferences: AnswerGuardPreferences = {
  owner: {
    chosen: null,
    default: "secrets",
    floor: "secrets",
    effective: "secrets",
  },
  peer: {
    chosen: "secrets",
    default: "strict",
    floor: "secrets",
    effective: "secrets",
  },
  relevance: "hold",
  regulated: false,
}

function apiError(code: string, message: string, status: number) {
  return json({ error: { code, message } }, status)
}

afterEach(() => vi.unstubAllGlobals())

describe("reading the settings", () => {
  it("reads the member's own settings as the session, with no body", async () => {
    const fetchMock = stubFetch(() => json(preferences))

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: true,
      preferences,
    })

    expect(sent(fetchMock)).toEqual([["GET", path, undefined]])
    expect(fetchMock.mock.calls[0][1]).toMatchObject({ credentials: "include" })
  })

  it("keeps a choice of null and every level as Go said them", async () => {
    stubFetch(() =>
      json({
        owner: {
          chosen: "strict",
          default: "secrets",
          floor: "strict",
          effective: "strict",
        },
        peer: {
          chosen: null,
          default: "strict",
          floor: "strict",
          effective: "strict",
        },
        relevance: "off",
        regulated: true,
      })
    )

    const result = await getAnswerGuardPreferences("org-1")

    expect(result).toMatchObject({
      available: true,
      preferences: {
        owner: { chosen: "strict", floor: "strict" },
        peer: { chosen: null, default: "strict" },
        relevance: "off",
        regulated: true,
      },
    })
  })

  it("encodes the organization in the path", async () => {
    const fetchMock = stubFetch(() => json(preferences))

    await getAnswerGuardPreferences("org / one")

    expect(fetchMock.mock.calls[0][0]).toBe(
      "/v1/organizations/org%20%2F%20one/answer-guard/preferences"
    )
  })

  it("passes the caller's abort signal on", async () => {
    const fetchMock = stubFetch(() => json(preferences))
    const controller = new AbortController()

    await getAnswerGuardPreferences("org-1", { signal: controller.signal })

    expect(fetchMock.mock.calls[0][1].signal).toBe(controller.signal)
  })

  it("ignores fields it does not know, but not values", async () => {
    stubFetch(() =>
      json({
        ...preferences,
        owner: { ...preferences.owner, note: "later" },
        another: 1,
      })
    )

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: true,
      preferences,
    })
  })
})

describe("changing the settings", () => {
  it("sends only the key that changed", async () => {
    const fetchMock = stubFetch(() => json(preferences))

    await setAnswerGuardPreferences("org-1", { owner: "strict" })
    await setAnswerGuardPreferences("org-1", { peer: "secrets" })

    expect(sent(fetchMock)).toEqual([
      ["PUT", path, '{"owner":"strict"}'],
      ["PUT", path, '{"peer":"secrets"}'],
    ])
    expect(fetchMock.mock.calls[0][1]).toMatchObject({
      credentials: "include",
      headers: { "Content-Type": "application/json" },
    })
  })

  it("clears a choice with null, and leaves out a key that is undefined", async () => {
    const fetchMock = stubFetch(() => json(preferences))

    await setAnswerGuardPreferences("org-1", { owner: null })
    await setAnswerGuardPreferences("org-1", { owner: undefined, peer: null })
    await setAnswerGuardPreferences("org-1", { owner: "strict", peer: null })

    expect(sent(fetchMock)).toEqual([
      ["PUT", path, '{"owner":null}'],
      ["PUT", path, '{"peer":null}'],
      ["PUT", path, '{"owner":"strict","peer":null}'],
    ])
  })

  it("sends nothing Go does not read, whatever the caller hands over", async () => {
    const fetchMock = stubFetch(() => json(preferences))

    await setAnswerGuardPreferences("org-1", {
      owner: "strict",
      relevance: "off",
      regulated: true,
    } as AnswerGuardChange)

    expect(sent(fetchMock)).toEqual([["PUT", path, '{"owner":"strict"}']])
  })

  it("answers the settings as they now are", async () => {
    stubFetch(() =>
      json({
        ...preferences,
        owner: {
          chosen: "strict",
          default: "secrets",
          floor: "secrets",
          effective: "strict",
        },
      })
    )

    const result = await setAnswerGuardPreferences("org-1", {
      owner: "strict",
    })

    expect(result).toMatchObject({
      available: true,
      preferences: { owner: { chosen: "strict", effective: "strict" } },
    })
  })
})

describe("errors", () => {
  it("throws what Go says for invalid input, keeping its status and code", async () => {
    stubFetch(() =>
      apiError(
        "invalid_request",
        'owner must be "secrets", "strict" or null',
        400
      )
    )

    const failure = await setAnswerGuardPreferences("org-1", {
      owner: "loose" as never,
    }).catch((error: unknown) => error)

    expect(failure).toBeInstanceOf(ApiError)
    expect(failure).toMatchObject({
      status: 400,
      code: "invalid_request",
      message: 'owner must be "secrets", "strict" or null',
    })
  })

  it("throws a refusal, such as an agent's token, as a 403", async () => {
    stubFetch(() => apiError("forbidden", "agents cannot change this", 403))

    await expect(getAnswerGuardPreferences("org-1")).rejects.toMatchObject({
      status: 403,
      code: "forbidden",
    })
    await expect(
      setAnswerGuardPreferences("org-1", { owner: null })
    ).rejects.toMatchObject({ status: 403, code: "forbidden" })
  })

  it("refreshes an ended session once, as every other request does, and then retries", async () => {
    const answers = [
      new Response(null, { status: 401 }),
      new Response(null, { status: 204 }),
      json(preferences),
    ]
    const fetchMock = stubFetch(() => answers.shift() ?? json({}, 500))

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: true,
      preferences,
    })

    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      path,
      "/v1/auth/refresh",
      path,
    ])
  })

  it("throws a 401 once the refresh has failed too", async () => {
    const fetchMock = stubFetch((url) =>
      url === "/v1/auth/refresh"
        ? new Response(null, { status: 401 })
        : apiError("unauthorized", "session expired", 401)
    )

    await expect(
      setAnswerGuardPreferences("org-1", { peer: "strict" })
    ).rejects.toMatchObject({ status: 401, message: "session expired" })

    // The refresh is tried once, and the change is not sent again.
    expect(fetchMock.mock.calls.map(([url]) => url)).toEqual([
      path,
      "/v1/auth/refresh",
    ])
  })

  it("throws a failure of Go's, and one of a proxy's that has no JSON", async () => {
    const answers = [
      apiError("internal", "internal server error", 500),
      new Response("<html>bad gateway</html>", {
        status: 502,
        headers: { "Content-Type": "text/html" },
      }),
    ]
    stubFetch(() => answers.shift() ?? json({}))

    await expect(getAnswerGuardPreferences("org-1")).rejects.toMatchObject({
      status: 500,
      code: "internal",
    })
    await expect(getAnswerGuardPreferences("org-1")).rejects.toMatchObject({
      status: 502,
      message: "Request failed",
    })
  })

  it("lets a failure to connect through as it is", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        throw new TypeError("Failed to fetch")
      })
    )

    await expect(getAnswerGuardPreferences("org-1")).rejects.toBeInstanceOf(
      TypeError
    )
  })
})

describe("a server that does not serve the settings", () => {
  it("answers a 404 as not available, for a read and for a change", async () => {
    stubFetch(() =>
      json({ error: { code: "not_found", message: "resource not found" } }, 404)
    )

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: false,
    })
    expect(
      await setAnswerGuardPreferences("org-1", { owner: "strict" })
    ).toEqual({ available: false })
  })

  it("answers a 404 that has no JSON at all as not available too", async () => {
    stubFetch(
      () =>
        new Response("404 page not found", {
          status: 404,
          headers: { "Content-Type": "text/plain" },
        })
    )

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: false,
    })
  })

  it("answers a 503 that says the settings are not kept here as not available", async () => {
    stubFetch(() =>
      apiError(
        "answer_guard_unavailable",
        "The answer guard's settings are not kept on this server.",
        503
      )
    )

    expect(await getAnswerGuardPreferences("org-1")).toEqual({
      available: false,
    })
  })

  it("still throws any other 503", async () => {
    stubFetch(() => apiError("unavailable", "auth is down", 503))

    await expect(getAnswerGuardPreferences("org-1")).rejects.toMatchObject({
      status: 503,
      code: "unavailable",
    })
  })
})

describe("an answer this page cannot read", () => {
  it("is an error when the body is not JSON", async () => {
    stubFetch(
      () =>
        new Response("{not json", {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
    )

    const failure = await getAnswerGuardPreferences("org-1").catch(
      (error: unknown) => error
    )

    expect(failure).toBeInstanceOf(AnswerGuardResponseError)
    expect((failure as Error).cause).toBeInstanceOf(SyntaxError)
  })

  it("is an error when a change comes back as malformed JSON", async () => {
    stubFetch(
      () =>
        new Response('{"owner":', {
          status: 200,
          headers: { "Content-Type": "application/json" },
        })
    )

    await expect(
      setAnswerGuardPreferences("org-1", { owner: "strict" })
    ).rejects.toBeInstanceOf(AnswerGuardResponseError)
  })

  it("is an error when a 200 has no JSON content type", async () => {
    stubFetch(
      () =>
        new Response("<html>hello</html>", {
          status: 200,
          headers: { "Content-Type": "text/html" },
        })
    )

    await expect(getAnswerGuardPreferences("org-1")).rejects.toBeInstanceOf(
      AnswerGuardResponseError
    )
  })

  it.each([
    ["an unknown relevance", { ...preferences, relevance: "maybe" }],
    [
      "an unknown level chosen",
      { ...preferences, peer: { ...preferences.peer, chosen: "loose" } },
    ],
    [
      "an unknown effective level",
      { ...preferences, owner: { ...preferences.owner, effective: "lenient" } },
    ],
    [
      "a level in the wrong case",
      { ...preferences, owner: { ...preferences.owner, floor: "Strict" } },
    ],
    [
      "a missing choice, which is null when there is none",
      {
        ...preferences,
        owner: {
          default: "secrets",
          floor: "secrets",
          effective: "secrets",
        },
      },
    ],
    [
      "a missing default",
      {
        ...preferences,
        peer: { chosen: null, floor: "secrets", effective: "secrets" },
      },
    ],
    ["regulated that is not a boolean", { ...preferences, regulated: "yes" }],
    [
      "a missing regulated",
      { owner: preferences.owner, peer: preferences.peer, relevance: "hold" },
    ],
    ["a missing origin", { ...preferences, peer: undefined }],
    ["an empty object", {}],
    ["null", null],
    ["a list", [preferences]],
    ["a string", "settings"],
  ])("is an error for %s", async (_name, body) => {
    stubFetch(() => json(body))

    await expect(getAnswerGuardPreferences("org-1")).rejects.toBeInstanceOf(
      AnswerGuardResponseError
    )
    await expect(
      setAnswerGuardPreferences("org-1", { owner: null })
    ).rejects.toBeInstanceOf(AnswerGuardResponseError)
  })
})

describe("levels", () => {
  it("tells a level that hides less than the floor requires", () => {
    expect(isBelowFloor("secrets", "strict")).toBe(true)
    expect(isBelowFloor("strict", "strict")).toBe(false)
    expect(isBelowFloor("secrets", "secrets")).toBe(false)
    expect(isBelowFloor("strict", "secrets")).toBe(false)
  })

  it.each([
    [null, "secrets", "secrets", "secrets"],
    [null, "strict", "secrets", "strict"],
    ["secrets", "strict", "secrets", "secrets"],
    ["strict", "secrets", "secrets", "strict"],
    ["secrets", "strict", "strict", "strict"],
    [null, "secrets", "strict", "strict"],
    ["strict", "secrets", "strict", "strict"],
  ] as const)(
    "applies the stricter of the floor and the choice, or the default: chosen %s, default %s, floor %s is %s",
    (chosen, standIn, floor, expected) => {
      expect(effectiveLevel({ chosen, default: standIn, floor })).toBe(expected)
    }
  )

  it("shows a change as Go will answer it, floor and default included", () => {
    const next = applyAnswerGuardChange(preferences, { owner: "strict" })

    expect(next.owner).toEqual({
      chosen: "strict",
      default: "secrets",
      floor: "secrets",
      effective: "strict",
    })
    expect(next.peer).toBe(preferences.peer)
    expect(next.relevance).toBe("hold")
  })

  it("puts a cleared choice back to the default, and keeps the floor above it", () => {
    const strictFloor: AnswerGuardPreferences = {
      ...preferences,
      peer: {
        chosen: "strict",
        default: "secrets",
        floor: "strict",
        effective: "strict",
      },
    }

    expect(applyAnswerGuardChange(preferences, { peer: null }).peer).toEqual({
      chosen: null,
      default: "strict",
      floor: "secrets",
      effective: "strict",
    })
    expect(applyAnswerGuardChange(strictFloor, { peer: null }).peer).toEqual({
      chosen: null,
      default: "secrets",
      floor: "strict",
      effective: "strict",
    })
    expect(
      applyAnswerGuardChange(strictFloor, { peer: "secrets" }).peer
    ).toEqual({
      chosen: "secrets",
      default: "secrets",
      floor: "strict",
      effective: "strict",
    })
  })

  it("leaves what it is not asked to change, and the settings it was given", () => {
    const frozen = structuredClone(preferences)

    expect(applyAnswerGuardChange(preferences, {})).toEqual(preferences)
    expect(
      applyAnswerGuardChange(preferences, { owner: undefined, peer: undefined })
    ).toEqual(preferences)
    applyAnswerGuardChange(preferences, { owner: "strict", peer: null })
    expect(preferences).toEqual(frozen)
  })
})

describe("query keys", () => {
  it("start with the settings domain and keep one organization from answering for another", () => {
    expect(answerGuardKeys.preferences("org-1")).toEqual([
      "settings",
      "answer-guard",
      "org-1",
    ])
    expect(answerGuardKeys.preferences("org-2")).not.toEqual(
      answerGuardKeys.preferences("org-1")
    )
  })
})
