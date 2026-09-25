import { describe, expect, it } from "vitest"
import {
  hasLanded,
  parseRoomClientMessage,
  parseRoomServerMessage,
  parseTeamspaceRoomAgentName,
  teamspaceRoomAgentName,
} from "./room-agent"
import type { RoomMessage } from "./client"

function message(
  seq: number,
  overrides: Partial<RoomMessage> = {}
): RoomMessage {
  return {
    id: `rmg_${seq}`,
    room_id: "tsp_launch",
    seq,
    author_handle: "member",
    author_kind: "person",
    author_user_id: "user-member",
    body: `line ${seq}`,
    created_at: "2026-09-23T09:10:00Z",
    ...overrides,
  }
}

describe("the room's name", () => {
  it("is one object per teamspace, and splits back into the same ids", () => {
    const name = teamspaceRoomAgentName("org_sim", "tsp_Jixq-Qo1")
    expect(name).toBe("org_sim~tsp_Jixq-Qo1")
    expect(parseTeamspaceRoomAgentName(name)).toEqual({
      organizationId: "org_sim",
      teamspaceId: "tsp_Jixq-Qo1",
    })
  })

  it("refuses a name its builder could not have made", () => {
    for (const name of [
      "org_sim",
      "org_sim~",
      "~tsp_launch",
      "org_sim~tsp_launch~user-ada",
      "org_sim%7Etsp_launch",
      "org.sim~tsp_launch",
      "org_sim~tsp launch",
    ]) {
      expect(parseTeamspaceRoomAgentName(name)).toBeUndefined()
    }
  })
})

describe("what a page says", () => {
  it("takes a visibility change and a post, and nothing else", () => {
    expect(
      parseRoomClientMessage('{"type":"room-visibility","visible":false}')
    ).toEqual({ type: "room-visibility", visible: false })
    expect(
      parseRoomClientMessage(
        JSON.stringify({ type: "room-post", id: "post-1", body: " Hi " })
      )
    ).toEqual({ type: "room-post", id: "post-1", body: " Hi " })

    for (const said of [
      "not json",
      "[]",
      '{"type":"room-visibility","visible":"yes"}',
      '{"type":"room-post","id":"a b","body":"Hi"}',
      '{"type":"room-post","id":"post-1"}',
      '{"type":"cf_agent_state","state":{}}',
      JSON.stringify({ type: "room-post", id: "p", body: "x".repeat(70_000) }),
      new ArrayBuffer(4),
    ]) {
      expect(parseRoomClientMessage(said)).toBeUndefined()
    }
  })

  it("never reads an author from the page: a post goes as the socket's member", () => {
    expect(
      parseRoomClientMessage(
        '{"type":"room-post","id":"post-1","body":"Hi","author_user_id":"user-ada"}'
      )
    ).toEqual({ type: "room-post", id: "post-1", body: "Hi" })
  })
})

describe("what the object says", () => {
  it("reads its own messages and leaves the SDK's frames alone", () => {
    expect(
      parseRoomServerMessage(
        JSON.stringify({
          type: "room-snapshot",
          messages: [message(1)],
          pending: ["post-1", 3],
          results: [
            { id: "post-2", ok: true, messages: [] },
            { id: "post-3", ok: false, status: 403, message: "no" },
            { id: "post-4", ok: false },
          ],
        })
      )
    ).toEqual({
      type: "room-snapshot",
      messages: [message(1)],
      pending: ["post-1"],
      results: [
        { id: "post-2", ok: true, messages: [] },
        { id: "post-3", ok: false, status: 403, message: "no" },
      ],
    })
    expect(
      parseRoomServerMessage(
        '{"type":"room-status","status":"paused","message":"this room is paused"}'
      )
    ).toEqual({
      type: "room-status",
      status: "paused",
      message: "this room is paused",
    })
    for (const other of [
      '{"type":"cf_agent_state","state":{}}',
      '{"type":"room-status","status":"sleeping"}',
      '{"type":"room-posted","result":{"id":"post-1"}}',
      '{"type":"room-messages"}',
      "nonsense",
    ]) {
      expect(parseRoomServerMessage(other)).toBeUndefined()
    }
  })
})

describe("hasLanded", () => {
  it("finds the member's own words said after the post went out", () => {
    const post = { body: "  Status? ", after: 3 }
    expect(
      hasLanded([message(4, { body: "Status?" })], post, "user-member")
    ).toBe(true)
    // Said before it went out, by somebody else, or by an agent.
    expect(
      hasLanded([message(3, { body: "Status?" })], post, "user-member")
    ).toBe(false)
    expect(
      hasLanded(
        [message(4, { body: "Status?", author_user_id: "user-ada" })],
        post,
        "user-member"
      )
    ).toBe(false)
    expect(
      hasLanded(
        [message(4, { body: "Status?", author_kind: "agent" })],
        post,
        "user-member"
      )
    ).toBe(false)
    expect(hasLanded([message(4, { body: "Status?" })], post, undefined)).toBe(
      false
    )
  })
})
