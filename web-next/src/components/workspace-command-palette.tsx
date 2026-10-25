// ⌘K, "Jump to" (board NavCommand): go to a teamspace, a request, a chat
// with the member's agent or a file, or do something with what they typed:
// start a parley in the teamspace it names, ask their agent about it, or
// have their agent open a page. Everything listed is filtered here from the
// lists the workspace already reads; nothing is searched server-side, so a
// list that is capped says so.
import { useEffect, useState } from "react"
import type { KeyboardEvent, ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import {
  Bot,
  FileText,
  Globe,
  Inbox as InboxIcon,
  MessageSquare,
  Mic,
} from "lucide-react"
import {
  Command,
  CommandDialog,
  CommandEmpty,
  CommandGroup,
  CommandInput,
  CommandItem,
  CommandList,
  CommandShortcut,
} from "@/components/ui/command"
import { useCall } from "./call-provider"
import { listTime } from "./direct-message-format"
import { askerName } from "./request-card-format"
import { INBOX_LIMIT, useInbox } from "@/hooks/use-needs-you"
import { TeamspaceAvatar } from "./workspace-avatar"
import { agentsQueryOptions } from "@/lib/agents/queries"
import { primaryAgent } from "@/lib/agents/policy"
import { downloadFile, filesQueryOptions } from "@/lib/files/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import { threadListQueryOptions } from "@/lib/threads/client"

/** How many of each kind the palette lists at once. */
const PER_KIND = 5

export type CommandPaletteProps = {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The organization to read, only with a session. */
  organizationId?: string
  viewerId?: string
  viewerName?: string
  teamspaces: readonly Teamspace[]
  /** A member's name by id, for who asked a request. */
  nameOf: (userId: string) => string | undefined
  onOpenTeamspace: (id: string) => void
  onOpenRequest: (id: string) => void
  onOpenChat: (threadId: string) => void
  /** Starts a new chat with the member's agent, asking this. */
  onAsk: (message: string) => void
}

/** Whether the palette is open; ⌘K or Ctrl+K opens and closes it anywhere. */
export function useCommandPalette(): [boolean, (open: boolean) => void] {
  const [open, setOpen] = useState(false)
  useEffect(() => {
    function onKeyDown(event: globalThis.KeyboardEvent) {
      if (
        (event.metaKey || event.ctrlKey) &&
        !event.shiftKey &&
        !event.altKey &&
        event.key.toLowerCase() === "k"
      ) {
        event.preventDefault()
        setOpen((current) => !current)
      }
    }
    window.addEventListener("keydown", onKeyDown)
    return () => window.removeEventListener("keydown", onKeyDown)
  }, [])
  return [open, setOpen]
}

/** Whether `text` holds every word of the query, in any case. */
export function matches(text: string, query: string): boolean {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean)
  const haystack = text.toLowerCase()
  return words.every((word) => haystack.includes(word))
}

/** What the member asks their agent to open and show them. */
export function browseAsk(query: string): string {
  return `Open ${query.trim()} in your browser and show me what's there.`
}

export function WorkspaceCommandPalette({
  open,
  onOpenChange,
  ...props
}: CommandPaletteProps) {
  return (
    <CommandDialog
      open={open}
      onOpenChange={onOpenChange}
      title="Jump to"
      description="Go to a teamspace, a request, a chat or a file, or ask your agent."
      className="workspace-palette"
    >
      {open && <PaletteBody {...props} close={() => onOpenChange(false)} />}
    </CommandDialog>
  )
}

function PaletteBody({
  organizationId,
  viewerId,
  viewerName,
  teamspaces,
  nameOf,
  onOpenTeamspace,
  onOpenRequest,
  onOpenChat,
  onAsk,
  close,
}: Omit<CommandPaletteProps, "open" | "onOpenChange"> & { close: () => void }) {
  const [query, setQuery] = useState("")
  const [selected, setSelected] = useState("")
  const enabled = Boolean(organizationId && viewerId)
  const org = organizationId ?? ""
  const requests = useInbox(organizationId, viewerId)
  const threads = useQuery({
    ...threadListQueryOptions(org, viewerId),
    enabled,
  })
  const files = useQuery({ ...filesQueryOptions(org), enabled })
  const agents = useQuery({ ...agentsQueryOptions(org), enabled })
  const { active, join } = useCall()

  const now = Date.now()
  const typed = query.trim()
  const agentName =
    (agents.data && primaryAgent(agents.data)?.name) || "your agent"
  const reader = { viewerId: viewerId ?? "", nameOf, now }

  const spaces = teamspaces
    .filter((space) => matches(space.name, typed))
    .slice(0, PER_KIND)
  const asks = (requests.data ?? [])
    .filter((request) => matches(`${request.task} ${request.purpose}`, typed))
    .slice(0, PER_KIND)
  const chats = (threads.data ?? [])
    .filter(
      (thread) =>
        (thread.origin ?? "chat") === "chat" &&
        thread.title &&
        matches(thread.title, typed)
    )
    .slice(0, PER_KIND)
  const found = typed
    ? (files.data ?? [])
        .filter((file) => matches(file.filename, typed))
        .slice(0, PER_KIND)
    : []
  const requestsCapped = (requests.data?.length ?? 0) >= INBOX_LIMIT

  // A parley starts in the teamspace the query names, when one does and the
  // member is not already in a call.
  const parleySpace =
    typed && viewerId && organizationId && !active ? spaces.at(0) : undefined

  function go(action: () => void) {
    close()
    action()
  }

  function startParley() {
    if (!parleySpace || !organizationId || !viewerId) return
    const space = parleySpace
    go(
      () =>
        void join({
          organizationId,
          room: { kind: "teamspace", id: space.id },
          roomName: space.name,
          member: viewerId,
          name: viewerName,
          mic: true,
          camera: false,
        })
    )
  }

  function ask() {
    if (!typed || !enabled) return
    go(() => onAsk(typed))
  }

  function onKeyDown(event: KeyboardEvent) {
    const mod = event.metaKey || event.ctrlKey
    if (mod && event.key === "Enter") {
      event.preventDefault()
      ask()
    } else if (mod && event.shiftKey && event.key.toLowerCase() === "h") {
      event.preventDefault()
      startParley()
    }
  }

  return (
    <Command
      shouldFilter={false}
      value={selected}
      onValueChange={setSelected}
      onKeyDown={onKeyDown}
      label="Jump to"
    >
      <CommandInput
        placeholder="Search or jump to"
        value={query}
        onValueChange={setQuery}
      />
      <CommandList className="workspace-palette-list">
        <CommandEmpty>
          {enabled
            ? "Nothing here matches."
            : "Sign in to a workspace to search it."}
        </CommandEmpty>
        {(spaces.length > 0 ||
          asks.length > 0 ||
          chats.length > 0 ||
          found.length > 0) && (
          <CommandGroup heading="Go to">
            {spaces.map((space) => (
              <Option
                key={`teamspace-${space.id}`}
                value={`teamspace-${space.id}`}
                icon={
                  <TeamspaceAvatar
                    name={space.name}
                    className="workspace-palette-mark"
                  />
                }
                label={space.name}
                hint={`Teamspace · ${space.whisperers.length} ${space.whisperers.length === 1 ? "person" : "people"}`}
                onSelect={() => go(() => onOpenTeamspace(space.id))}
              />
            ))}
            {asks.map((request) => (
              <Option
                key={`request-${request.id}`}
                value={`request-${request.id}`}
                icon={<InboxIcon />}
                label={request.task}
                hint={`Request from ${askerName(request, reader)}`}
                onSelect={() => go(() => onOpenRequest(request.id))}
              />
            ))}
            {chats.map((thread) => (
              <Option
                key={`chat-${thread.id}`}
                value={`chat-${thread.id}`}
                icon={<MessageSquare />}
                label={thread.title}
                hint={`Chat with ${agentName} · ${listTime(thread.updated_at, now)}`}
                onSelect={() => go(() => onOpenChat(thread.id))}
              />
            ))}
            {found.map((file) => (
              <Option
                key={`file-${file.id}`}
                value={`file-${file.id}`}
                icon={<FileText />}
                label={file.filename}
                hint="File · download"
                onSelect={() =>
                  go(() => {
                    void downloadFile(org, file.id).then((url) =>
                      window.location.assign(url)
                    )
                  })
                }
              />
            ))}
          </CommandGroup>
        )}
        {typed && enabled && (
          <CommandGroup heading="Do">
            {parleySpace && (
              <Option
                value="do-parley"
                icon={<Mic />}
                label={`Start a parley in ${parleySpace.name}`}
                shortcut="⌘⇧H"
                onSelect={startParley}
              />
            )}
            <Option
              value="do-ask"
              icon={<Bot />}
              label={`Ask ${agentName} about “${typed}”`}
              shortcut="⌘↵"
              onSelect={ask}
            />
            <Option
              value="do-browse"
              icon={<Globe />}
              label={`Have ${agentName} open a page and show you`}
              hint="Browser preview"
              onSelect={() => go(() => onAsk(browseAsk(typed)))}
            />
          </CommandGroup>
        )}
      </CommandList>
      {requestsCapped && (
        <p className="workspace-palette-note">
          Searching your latest {INBOX_LIMIT} requests.
        </p>
      )}
      <div className="workspace-palette-foot" aria-hidden="true">
        <span>
          <kbd>↑↓</kbd> move
        </span>
        <span>
          <kbd>↵</kbd> open
        </span>
        <span>
          <kbd>⌘↵</kbd> ask {agentName}
        </span>
        <span>
          <kbd>esc</kbd> close
        </span>
      </div>
    </Command>
  )
}

function Option({
  value,
  icon,
  label,
  hint,
  shortcut,
  onSelect,
}: {
  value: string
  icon: ReactNode
  label: string
  hint?: string
  shortcut?: string
  onSelect: () => void
}) {
  return (
    <CommandItem
      value={value}
      className="workspace-palette-option"
      onSelect={onSelect}
    >
      {icon}
      <span className="workspace-palette-label">{label}</span>
      {hint && <span className="workspace-palette-hint">{hint}</span>}
      {shortcut && <CommandShortcut>{shortcut}</CommandShortcut>}
    </CommandItem>
  )
}
