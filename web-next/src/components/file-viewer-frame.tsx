// One frame for an open file, wherever it opens (founder's redesign, 14–15
// Oct 2026): a bar with the way back, where the file sits, its kind, name and
// facts, the viewer's own tools, Download, Open original and More, over an
// inset tile holding the file. The frame is only the file's column: where a
// chat shares the screen with it, the chat sits to its left (AgentSession).
import { createContext, useContext, useState } from "react"
import type { ReactNode } from "react"
import { createPortal } from "react-dom"
import { ArrowLeft, Download, ExternalLink, MoreHorizontal } from "lucide-react"
import { Button, buttonVariants } from "@/components/ui/button"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import type { ScreenItem } from "@/lib/agents/screen-context"
import { FileKindBadge } from "./file-kind"
import "./file-viewer-frame.css"

export type FileViewerCrumb = {
  label: string
  /** Leaves the file for this place; the last crumb (the file) has none. */
  onSelect?: () => void
}

/** How the file is saved: a link to follow, or a step that checks first. */
export type FileViewerDownload =
  { href: string; fileName?: string } | { onDownload: () => void }

/** Where a viewer inside the frame puts its own controls: the bar. */
// undefined: no frame around the viewer; null: the frame's bar is not up yet.
const ToolsSlot = createContext<HTMLElement | null | undefined>(undefined)

/**
 * A viewer's controls (pages, zoom, find). Inside a frame they sit in its
 * bar; anywhere else, above the viewer.
 */
export function ViewerTools({ children }: { children: ReactNode }) {
  const slot = useContext(ToolsSlot)
  if (slot) return createPortal(children, slot)
  if (slot === null) return null
  return (
    <div className="file-viewer-tools" role="toolbar" aria-label="View">
      {children}
    </div>
  )
}

// A link that looks like the bar's buttons: a ghost icon-sm from ui/button.
const iconLink = buttonVariants({ variant: "ghost", size: "icon-sm" })

export function FileViewerFrame({
  name,
  kindName,
  meta,
  crumbs,
  backLabel,
  onBack,
  tools,
  actions,
  download,
  originalHref,
  menu,
  context,
  children,
}: {
  /** The file's name, as the member knows it. */
  name: string
  /** A name whose extension gives the kind badge, when `name` has none. */
  kindName?: string
  /** One line of facts: who added it, its size or pages, where it's from. */
  meta?: string
  /** Where the file sits, e.g. Library / Product docs; the file comes last. */
  crumbs?: readonly FileViewerCrumb[]
  backLabel: string
  onBack: () => void
  /** The viewer's own controls (pages, zoom, find), in the bar. */
  tools?: ReactNode
  /** The frame's actions before Download, such as Edit. */
  actions?: ReactNode
  download?: FileViewerDownload
  /** Where the file lives outside Oryxa, opened in a new tab. */
  originalHref?: string
  /** Items for the More menu; none leaves it out. */
  menu?: ReactNode
  /** What the agent is told is open. */
  context?: ScreenItem
  children: ReactNode
}) {
  const [toolsSlot, setToolsSlot] = useState<HTMLElement | null>(null)
  return (
    <div className="file-viewer">
      <header className="file-viewer-bar">
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={backLabel}
          onClick={onBack}
        >
          <ArrowLeft strokeWidth={1.7} />
        </Button>
        <FileKindBadge name={kindName ?? name} />
        <div className="file-viewer-title">
          {crumbs && crumbs.length > 0 && (
            <nav className="file-viewer-crumbs" aria-label="Where this file is">
              <ol>
                {crumbs.map((crumb, index) => (
                  <li key={`${index}-${crumb.label}`}>
                    {crumb.onSelect ? (
                      <Button
                        variant="link"
                        size="xs"
                        className="file-viewer-crumb"
                        onClick={crumb.onSelect}
                      >
                        {crumb.label}
                      </Button>
                    ) : (
                      <span>{crumb.label}</span>
                    )}
                  </li>
                ))}
              </ol>
            </nav>
          )}
          <strong>{name}</strong>
          {meta && <small>{meta}</small>}
        </div>
        <div
          ref={setToolsSlot}
          className="file-viewer-tools"
          role="toolbar"
          aria-label="View"
        >
          {tools}
        </div>
        <div className="file-viewer-actions">
          {actions}
          {download &&
            ("href" in download ? (
              <a
                className={iconLink}
                href={download.href}
                target="_blank"
                rel="noopener noreferrer"
                download={download.fileName}
                aria-label={`Download ${name}`}
                title="Download"
              >
                <Download strokeWidth={1.7} />
              </a>
            ) : (
              <Button
                variant="ghost"
                size="icon-sm"
                aria-label={`Download ${name}`}
                title="Download"
                onClick={download.onDownload}
              >
                <Download strokeWidth={1.7} />
              </Button>
            ))}
          {originalHref && (
            <a
              className={iconLink}
              href={originalHref}
              target="_blank"
              rel="noopener noreferrer"
              aria-label="Open original"
              title="Open original"
            >
              <ExternalLink strokeWidth={1.7} />
            </a>
          )}
          {menu && (
            <DropdownMenu>
              <DropdownMenuTrigger
                render={
                  <Button
                    variant="ghost"
                    size="icon-sm"
                    aria-label={`More for ${name}`}
                  />
                }
              >
                <MoreHorizontal strokeWidth={1.8} />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end">{menu}</DropdownMenuContent>
            </DropdownMenu>
          )}
        </div>
      </header>
      <div
        className="file-viewer-body"
        data-screen-context-kind={context?.kind}
        data-screen-context-id={context?.id}
        data-screen-context-name={context?.name}
        data-screen-context-selected={context ? "true" : undefined}
      >
        <div className="workspace-document-stage">
          <ToolsSlot.Provider value={toolsSlot}>{children}</ToolsSlot.Provider>
        </div>
      </div>
    </div>
  )
}
