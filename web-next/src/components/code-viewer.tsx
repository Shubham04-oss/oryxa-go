// A code file (board ViewerCode): line numbers, light highlighting by a small
// pinned highlighter (highlight.js core with a handful of languages, loaded
// only when code opens), and Wrap and Copy in the frame's bar.
import { useEffect, useMemo, useState } from "react"
import { Check, Copy, WrapText } from "lucide-react"
import { Button } from "@/components/ui/button"
import { ViewerTools } from "./file-viewer-frame"
import "./code-viewer.css"

/** A file's language, by its extension, as the highlighter names it. */
const languages: Record<string, string> = {
  js: "javascript",
  jsx: "javascript",
  mjs: "javascript",
  cjs: "javascript",
  ts: "typescript",
  tsx: "typescript",
  py: "python",
  go: "go",
  rs: "rust",
  java: "java",
  rb: "ruby",
  json: "json",
  yaml: "yaml",
  yml: "yaml",
  sql: "sql",
  sh: "bash",
  bash: "bash",
  zsh: "bash",
  html: "xml",
  htm: "xml",
  xml: "xml",
  svg: "xml",
  css: "css",
}

/** The extensions shown as code. */
export const codeExtensions: ReadonlySet<string> = new Set(
  Object.keys(languages)
)

export function codeLanguage(name: string): string | undefined {
  const extension = name.split(".").at(-1)?.toLowerCase() ?? ""
  return languages[extension]
}

function unavailableOnTheServer(): never {
  throw new Error("The code viewer runs in the browser.")
}
// Every import keeps its own SSR conditional (document-preview.tsx), so the
// Worker's build leaves the highlighter out.
const loadHighlighter = () =>
  import.meta.env.SSR
    ? unavailableOnTheServer()
    : Promise.all([
        import("highlight.js/lib/core"),
        import("highlight.js/lib/languages/javascript"),
        import("highlight.js/lib/languages/typescript"),
        import("highlight.js/lib/languages/python"),
        import("highlight.js/lib/languages/go"),
        import("highlight.js/lib/languages/rust"),
        import("highlight.js/lib/languages/java"),
        import("highlight.js/lib/languages/ruby"),
        import("highlight.js/lib/languages/json"),
        import("highlight.js/lib/languages/yaml"),
        import("highlight.js/lib/languages/sql"),
        import("highlight.js/lib/languages/bash"),
        import("highlight.js/lib/languages/xml"),
        import("highlight.js/lib/languages/css"),
      ]).then(([core, ...modules]) => {
        const highlighter = core.default
        const names = [
          "javascript",
          "typescript",
          "python",
          "go",
          "rust",
          "java",
          "ruby",
          "json",
          "yaml",
          "sql",
          "bash",
          "xml",
          "css",
        ]
        names.forEach((name, index) => {
          if (!highlighter.getLanguage(name))
            highlighter.registerLanguage(name, modules[index].default)
        })
        return highlighter
      })

/** The highlighter's markup for `text`, or nothing when it can't say. */
function useHighlighted(text: string, language: string | undefined) {
  const [html, setHtml] = useState<string>()
  useEffect(() => {
    setHtml(undefined)
    if (!language) return
    let cancelled = false
    void loadHighlighter()
      .then((highlighter) => {
        // highlight.js escapes the source; what it adds is its own spans.
        const result = highlighter.highlight(text, {
          language,
          ignoreIllegals: true,
        })
        if (!cancelled) setHtml(result.value)
      })
      .catch(() => {
        // Without the highlighter the code still shows, plain.
      })
    return () => {
      cancelled = true
    }
  }, [text, language])
  return html
}

export function CodeViewer({ text, name }: { text: string; name: string }) {
  const [wrap, setWrap] = useState(false)
  const [copied, setCopied] = useState(false)
  const language = codeLanguage(name)
  const html = useHighlighted(text, language)
  const lineCount = useMemo(() => text.split("\n").length, [text])

  async function copy() {
    try {
      await navigator.clipboard.writeText(text)
      setCopied(true)
      setTimeout(() => setCopied(false), 1500)
    } catch {
      setCopied(false)
    }
  }

  return (
    <div className="code-viewer" data-wrap={wrap ? "true" : undefined}>
      <ViewerTools>
        <Button
          variant="ghost"
          size="sm"
          aria-pressed={wrap}
          onClick={() => setWrap((current) => !current)}
        >
          <WrapText />
          Wrap
        </Button>
        <Button variant="ghost" size="sm" onClick={() => void copy()}>
          {copied ? <Check /> : <Copy />}
          {copied ? "Copied" : "Copy"}
        </Button>
      </ViewerTools>
      <div className="code-scroll">
        {!wrap && (
          <ol className="code-lines" aria-hidden="true">
            {Array.from({ length: lineCount }, (_, line) => (
              <li key={line}>{line + 1}</li>
            ))}
          </ol>
        )}
        <pre className="code-text">
          {html ? (
            <code
              className="hljs"
              // The highlighter's own escaped output (see useHighlighted).
              dangerouslySetInnerHTML={{ __html: html }}
            />
          ) : (
            <code>{text}</code>
          )}
        </pre>
      </div>
    </div>
  )
}
