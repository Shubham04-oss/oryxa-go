import { describe, expect, it } from "vitest"
import { toolLabel } from "./workspace-tool-connection"

describe("toolLabel", () => {
  it("reads a tool's id without its app's name", () => {
    expect(toolLabel("github-create-issue", "github")).toBe("Create issue")
    expect(toolLabel("GITHUB_CREATE_ISSUE", "github")).toBe("Create issue")
    expect(toolLabel("google_drive-list-files", "google_drive")).toBe(
      "List files"
    )
    expect(toolLabel("send-email", "gmail")).toBe("Send email")
  })
})
