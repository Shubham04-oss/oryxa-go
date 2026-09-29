// @vitest-environment jsdom

import { afterEach, describe, expect, it } from "vitest"
import { cleanup, render, screen } from "@testing-library/react"
import RoomMarkdown from "./room-markdown"

afterEach(cleanup)

function renderAnswer(text: string) {
  const { container } = render(<RoomMarkdown text={text} />)
  return container
}

describe("a room's Markdown", () => {
  it("renders an agent's answer as Markdown, its links opening in a new tab", () => {
    const answer = renderAnswer(
      "Here is **the plan**: [the brief](https://example.com/brief)."
    )

    expect(screen.getByText("the plan").tagName).toBe("STRONG")
    const link = screen.getByRole("link", { name: "the brief" })
    expect(link.getAttribute("href")).toBe("https://example.com/brief")
    expect(link.getAttribute("target")).toBe("_blank")
    expect(link.getAttribute("rel")).toBe("noopener noreferrer")
    expect(answer.querySelector(".workspace-room-markdown")?.textContent).toBe(
      "Here is the plan: the brief."
    )
  })

  it("offers an image as a link instead of loading it", () => {
    const answer = renderAnswer(
      "Look at this:\n\n![chart](https://tracker.example/pixel.png)"
    )

    // Nothing in the answer can make the browser fetch from another server.
    expect(answer.querySelector("img")).toBeNull()
    const link = screen.getByRole("link", { name: "chart" })
    expect(link.getAttribute("href")).toBe("https://tracker.example/pixel.png")
    expect(link.getAttribute("target")).toBe("_blank")
    expect(link.getAttribute("rel")).toBe("noopener noreferrer")
  })

  it("labels an image with no alt text by its address", () => {
    const answer = renderAnswer("![](https://tracker.example/pixel.png)")

    expect(answer.querySelector("img")).toBeNull()
    const link = screen.getByRole("link", {
      name: "https://tracker.example/pixel.png",
    })
    expect(link.getAttribute("href")).toBe("https://tracker.example/pixel.png")
  })

  it("does the same for a reference-style image", () => {
    const answer = renderAnswer(
      ["![diagram][d]", "", "[d]: https://tracker.example/diagram.png"].join(
        "\n"
      )
    )

    expect(answer.querySelector("img")).toBeNull()
    expect(
      screen.getByRole("link", { name: "diagram" }).getAttribute("href")
    ).toBe("https://tracker.example/diagram.png")
  })

  it("drops an image with no address a browser could open", () => {
    const answer = renderAnswer(
      [
        "![nothing]()",
        "",
        "![inline](data:image/png;base64,iVBORw0KGgo=)",
        "",
        "![script](javascript:alert(1))",
      ].join("\n")
    )

    expect(answer.querySelector("img")).toBeNull()
    expect(screen.queryAllByRole("link")).toHaveLength(0)
    expect(answer.querySelector("[href^='javascript'], [src]")).toBeNull()
  })

  it("leaves raw HTML in an answer out, images included", () => {
    const answer = renderAnswer(
      '<img src="https://tracker.example/pixel.png"><b>raw</b><script>alert(1)</script>'
    )

    expect(answer.querySelector("img, b, script")).toBeNull()
  })
})
