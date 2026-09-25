import { describe, expect, it } from "vitest"
import {
  directMessageName,
  inboxName,
  isNameId,
  parseDirectMessageName,
  parseInboxName,
  parseSideConversationName,
  peerIn,
  sideConversationName,
} from "./names"

describe("DM names", () => {
  it("names one object per pair, whoever opens it", () => {
    expect(directMessageName("org_sim", "user-member", "user-admin")).toBe(
      "org_sim~user-admin~user-member"
    )
    expect(directMessageName("org_sim", "user-admin", "user-member")).toBe(
      "org_sim~user-admin~user-member"
    )
    // Cognito subjects sort by their characters, as Go's strings do.
    expect(
      directMessageName(
        "org_2fQx9LmR7vTk4WcA",
        "6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f",
        "0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d"
      )
    ).toBe(
      "org_2fQx9LmR7vTk4WcA~0a1b2c3d-4e5f-4a6b-8c7d-9e0f1a2b3c4d~6c1d5f0e-2b7a-4c8e-9f3d-1a2b3c4d5e6f"
    )
  })

  it("parses back only a name the builder could have made", () => {
    expect(parseDirectMessageName("org_sim~user-admin~user-member")).toEqual({
      organizationId: "org_sim",
      userA: "user-admin",
      userB: "user-member",
    })
    for (const name of [
      "org_sim~user-member~user-admin",
      "org_sim~user-admin~user-admin",
      "org_sim~user-admin",
      "org_sim~user-admin~user-member~x",
      "org_sim~~user-member",
      "org sim~user-admin~user-member",
      "org_sim%7Euser-admin%7Euser-member",
      `org_sim~${"a".repeat(129)}~user-member`,
    ]) {
      expect(parseDirectMessageName(name)).toBeUndefined()
    }
  })

  it("finds the other person, and nobody for a third", () => {
    const ids = { userA: "user-admin", userB: "user-member" }
    expect(peerIn(ids, "user-admin")).toBe("user-member")
    expect(peerIn(ids, "user-member")).toBe("user-admin")
    expect(peerIn(ids, "user-owner")).toBeUndefined()
  })
})

describe("the other names", () => {
  it("names an inbox and a side conversation, and parses them back strictly", () => {
    expect(inboxName("org_sim", "user-member")).toBe("org_sim~user-member")
    expect(parseInboxName("org_sim~user-member")).toEqual({
      organizationId: "org_sim",
      userId: "user-member",
    })
    for (const name of ["org_sim", "org_sim~", "org_sim~a~b", "org.sim~a"]) {
      expect(parseInboxName(name)).toBeUndefined()
    }
    const side = sideConversationName("org_sim", "tsp_1", "user-b", "user-a")
    expect(side).toBe("org_sim~tsp_1~user-a~user-b")
    expect(parseSideConversationName(side)).toEqual({
      organizationId: "org_sim",
      teamspaceId: "tsp_1",
      userA: "user-a",
      userB: "user-b",
    })
    expect(
      parseSideConversationName("org_sim~tsp_1~user-b~user-a")
    ).toBeUndefined()
    expect(isNameId("")).toBe(false)
    expect(isNameId(undefined)).toBe(false)
    expect(isNameId("user_1-x")).toBe(true)
  })
})
