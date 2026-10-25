import { useMemo } from "react"
import type { ReactNode } from "react"
import { useQuery } from "@tanstack/react-query"
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { threadDetailQueryOptions } from "@/lib/threads/client"
import { buildTimeline } from "@/lib/threads/timeline"
import type { RequestEntry } from "@/lib/threads/timeline"
import { threadRoom } from "@/lib/live/children"
import type { LiveTarget } from "@/lib/live/children"
import { AgentRequestCard } from "./agent-timeline"
import { LiveChildren, useRoomChildren } from "./live-work"
import { useRunningPredicate } from "./browser-pane-state"
import { PrimaryAgentFiles, useConversationFiles } from "./primary-agent-files"
import type { PrimaryAgentFileTarget } from "./primary-agent-files"
import "./agent-session.css"

export type SessionWorkTab = "overview" | "files" | "live" | "requests"

const tabs: SessionWorkTab[] = ["overview", "files", "live", "requests"]

export function isSessionWorkTab(value: unknown): value is SessionWorkTab {
  return tabs.includes(value as SessionWorkTab)
}

/** The requests the agent filed in this conversation, newest first. */
function useConversationRequests(
  organizationId: string,
  threadId: string | null | undefined
) {
  const detail = useQuery({
    ...threadDetailQueryOptions(organizationId, threadId ?? ""),
    enabled: Boolean(threadId),
  })
  const requests = useMemo(
    () =>
      detail.data
        ? buildTimeline(detail.data.items)
            .filter((entry): entry is RequestEntry => entry.kind === "request")
            .reverse()
        : [],
    [detail.data]
  )
  return { detail, requests }
}

function Quiet({ children, role }: { children: ReactNode; role?: string }) {
  return (
    <p className="workspace-agents-muted agent-session-quiet" role={role}>
      {children}
    </p>
  )
}

/**
 * The agent session's work column when no file or live pane is open: the
 * agent's overview, then this conversation's files, live work and the
 * requests its agent filed, as tabs. What a tab holds follows the
 * conversation the chat is on; opening a file or a live child swaps the
 * column for its viewer, and its Back comes back to the same tab.
 */
export function SessionWorkTabs({
  organizationId,
  threadId,
  viewerId,
  tab,
  onTabChange,
  overview,
  onOpenFile,
  onOpenLive,
}: {
  organizationId: string
  /** The chat's conversation: null for a new chat, undefined until it decides. */
  threadId: string | null | undefined
  viewerId?: string
  tab: SessionWorkTab
  onTabChange: (tab: SessionWorkTab) => void
  /** The agent's overview, the first tab. */
  overview: ReactNode
  onOpenFile: (file: PrimaryAgentFileTarget) => void
  onOpenLive: (target: LiveTarget) => void
}) {
  const room = threadRoom(threadId)
  const { files } = useConversationFiles(organizationId, threadId)
  // One reader of the room's list and log, for the tab's count and its
  // tiles alike, so the two never disagree (B16).
  const roomChildren = useRoomChildren(organizationId, room)
  const { children, log } = roomChildren
  const isRunning = useRunningPredicate(organizationId, room, log.events)
  const { detail, requests } = useConversationRequests(organizationId, threadId)
  const live = children.data ?? []
  // The same state the tiles and the pane show (B16).
  const running = live.filter(isRunning).length
  const count = (value: number) =>
    value > 0 && <span className="agent-session-tab-count">{value}</span>

  return (
    <Tabs
      className="agent-session-tabs"
      value={tab}
      onValueChange={(value) => {
        if (isSessionWorkTab(value)) onTabChange(value)
      }}
    >
      <TabsList className="agent-session-tab-list" aria-label="Session work">
        <TabsTrigger value="overview">Overview</TabsTrigger>
        <TabsTrigger value="files">Files{count(files.length)}</TabsTrigger>
        <TabsTrigger value="live">Live{count(running)}</TabsTrigger>
        <TabsTrigger value="requests">
          Requests{count(requests.length)}
        </TabsTrigger>
      </TabsList>
      <TabsContent className="agent-session-tab-panel" value="overview">
        {overview}
      </TabsContent>
      <TabsContent
        className="agent-session-tab-panel agent-session-tab-padded"
        value="files"
      >
        <PrimaryAgentFiles
          organizationId={organizationId}
          threadId={threadId}
          onOpenFile={onOpenFile}
          heading={null}
        />
      </TabsContent>
      <TabsContent
        className="agent-session-tab-panel agent-session-tab-padded"
        value="live"
      >
        {/* The tiles and the lines below follow the same list and log. */}
        {room && (
          <LiveChildren
            organizationId={organizationId}
            room={room}
            viewerId={viewerId}
            title="This conversation's sandbox and browser"
            shared={roomChildren}
            onOpen={(child) => onOpenLive({ room, child })}
          />
        )}
        {!room ? (
          <Quiet>
            Start a conversation to see its sandbox and browser here.
          </Quiet>
        ) : children.isPending ? (
          <Quiet role="status">Loading live work…</Quiet>
        ) : children.isError ? (
          <Quiet role="alert">
            Couldn't load this conversation's live work. Try again in a moment.
          </Quiet>
        ) : (
          live.length === 0 && (
            <Quiet>
              Nothing live in this chat. When your agent starts a sandbox or a
              browser for this conversation, it appears here.
            </Quiet>
          )
        )}
      </TabsContent>
      <TabsContent
        className="agent-session-tab-panel agent-session-tab-padded"
        value="requests"
      >
        {!threadId ? (
          <Quiet>Start a conversation to see the requests it files here.</Quiet>
        ) : detail.isPending ? (
          <Quiet role="status">Loading requests…</Quiet>
        ) : detail.isError ? (
          <Quiet role="alert">
            Couldn't load this conversation's requests.
          </Quiet>
        ) : requests.length === 0 ? (
          <Quiet>
            When your agent asks a teammate's agent for something in this
            conversation, the request and its answer appear here.
          </Quiet>
        ) : (
          <div className="agent-session-requests">
            {requests.map((entry) => (
              <AgentRequestCard
                key={entry.key}
                entry={entry}
                organizationId={organizationId}
              />
            ))}
          </div>
        )}
      </TabsContent>
    </Tabs>
  )
}
