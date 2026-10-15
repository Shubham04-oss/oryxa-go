import type { ReactNode } from "react"
import { ThinkingOrb } from "thinking-orbs"
import { ArrowUpRight } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Card,
  CardAction,
  CardContent,
  CardHeader,
  CardTitle,
} from "@/components/ui/card"
import { PersonAvatar } from "./direct-message"
import type { DmPeople, DmPerson } from "./direct-message-people"
import { usePeerOnline } from "./direct-message-presence"
import { firstName } from "./direct-message-format"
import { FileKindBadge } from "./file-kind"
import { agoPhrase, scheduleWords } from "./today-format"
import type { AgentFile, Brief } from "./today-format"
import { ToolkitLogo } from "./workspace-tool-logo"
import { toolkitIcon } from "./workspace-tools-data"
import { toolkitName } from "@/lib/apps/client"
import type { AgentWorkflow } from "@/lib/agents/agent-workflows"
import { isUnavailableHere } from "@/lib/agents/policy"
import { ApiError } from "@/lib/api/client"
import type { Corpus } from "@/lib/rag/client"
import "./home-glance.css"

/** A read as the glance needs it: whether it answered, failed or still loads. */
export type GlanceRead = {
  isPending: boolean
  isSuccess: boolean
  isError: boolean
  error: unknown
  refetch: () => unknown
}

/** One of the member's recent chats with their agent, as My workspace lists them. */
export type GlanceConversation = {
  threadId: string | null
  title: string
  detail: string
  running: boolean
}

/** How many teammates the row shows before "All". */
const TEAMMATES_SHOWN = 6

/** An app as its brand writes it ("GitHub"), else as the catalog does. */
function appName(toolkit: string): string {
  return toolkitIcon(toolkit)?.title || toolkitName(toolkit)
}

/**
 * At a glance (AgentPanelGlance board): My workspace's widgets as compact
 * library cards in one column, in the agent panel on every page. What needs
 * the member is not here: it is the panel's chip and count, and Requests.
 * Every card reads real data, and says in one line when it has none.
 */
export function HomeGlance({
  now,
  agentName,
  updatesThreadId,
  updates,
  briefs,
  tools,
  connections,
  people,
  conversations,
  threads,
  knowledge,
  corpora,
  files,
  filesRead,
  conversationTitle,
  workflows,
  workflowsRead,
  onOpenConversation,
  onNewChat,
  onOpenTools,
  onOpenKnowledge,
  onOpenPeople,
  onMessage,
  onOpenWorkflows,
}: {
  now: number
  /** The member's primary agent, who made the files. */
  agentName?: string
  updatesThreadId?: string
  updates: GlanceRead
  briefs: Brief[]
  /** The member's active app connections, by toolkit. */
  tools: string[]
  connections: GlanceRead
  people?: DmPeople
  conversations: GlanceConversation[]
  threads: GlanceRead
  knowledge: Corpus[]
  corpora: GlanceRead
  files: AgentFile[]
  /** Whether every chat the files are read from has answered. */
  filesRead: boolean
  conversationTitle: (threadId: string) => string
  /** The member's own workflows. */
  workflows: AgentWorkflow[]
  workflowsRead: GlanceRead
  onOpenConversation?: (threadId: string) => void
  onNewChat?: () => void
  onOpenTools: () => void
  onOpenKnowledge: () => void
  onOpenPeople?: () => void
  onMessage?: (peerId: string) => void
  onOpenWorkflows?: () => void
}) {
  const latest = briefs.at(0)
  const teammates = people ? teammatesOf(people.people) : []
  return (
    <div className="home-glance-cards" aria-label="At a glance">
      <GlanceCard
        title="From your apps"
        action={<HeadAction label="Manage" onClick={onOpenTools} />}
      >
        {tools.length > 0 && (
          <ul className="home-glance-apps" aria-label="Your connected apps">
            {tools.map((toolkit) => (
              <li key={toolkit} title={appName(toolkit)}>
                <ToolkitLogo toolkit={{ toolkit, name: appName(toolkit) }} />
                <span className="sr-only">{appName(toolkit)}</span>
              </li>
            ))}
          </ul>
        )}
        {latest && updatesThreadId ? (
          <GlanceRow
            mark={
              latest.toolkits[0] ? (
                <ToolkitLogo
                  toolkit={{
                    toolkit: latest.toolkits[0],
                    name: appName(latest.toolkits[0]),
                  }}
                />
              ) : undefined
            }
            text={latest.text}
            meta={agoPhrase(latest.at, now)}
            disabled={!onOpenConversation}
            onClick={() => onOpenConversation?.(updatesThreadId)}
          />
        ) : (
          <Notice
            read={updatesThreadId ? updates : threads}
            name="updates"
            empty={
              connections.isSuccess && tools.length === 0
                ? "Connect a tool and its news lands here."
                : "No updates from your apps yet."
            }
          />
        )}
      </GlanceCard>

      {people && (
        <GlanceCard
          title="Teammates"
          action={
            onOpenPeople && teammates.length > 0 ? (
              <HeadAction label="All" onClick={onOpenPeople} />
            ) : undefined
          }
        >
          {teammates.length > 0 ? (
            <ul className="home-glance-mates">
              {teammates.slice(0, TEAMMATES_SHOWN).map((person) => (
                <li key={person.id}>
                  <Teammate person={person} onMessage={onMessage} />
                </li>
              ))}
            </ul>
          ) : people.status === "loading" ? (
            <p className="home-glance-quiet" role="status">
              Loading teammates…
            </p>
          ) : people.status === "error" ? (
            <p className="home-glance-quiet" role="status">
              Couldn’t load teammates.
            </p>
          ) : (
            <p className="home-glance-quiet">
              Invite your team and they show up here.
            </p>
          )}
        </GlanceCard>
      )}

      <GlanceCard
        title="Conversations"
        action={
          onNewChat ? (
            <HeadAction label="New chat" onClick={onNewChat} />
          ) : undefined
        }
      >
        {conversations.length > 0 ? (
          <ul className="home-glance-list" aria-label="Recent conversations">
            {conversations.map((chat) => (
              <li key={chat.threadId ?? "starting"}>
                <GlanceRow
                  mark={
                    chat.running ? (
                      <ThinkingOrb
                        size={20}
                        state="working"
                        aria-hidden="true"
                      />
                    ) : undefined
                  }
                  text={chat.title}
                  meta={chat.detail}
                  disabled={!chat.threadId || !onOpenConversation}
                  onClick={() => {
                    if (chat.threadId) onOpenConversation?.(chat.threadId)
                  }}
                />
              </li>
            ))}
          </ul>
        ) : (
          <Notice
            read={threads}
            name="conversations"
            empty="Your conversations land here."
          />
        )}
      </GlanceCard>

      <GlanceCard
        title="Knowledge"
        action={<HeadAction label="Library" onClick={onOpenKnowledge} />}
      >
        {knowledge.length > 0 ? (
          <ul className="home-glance-list">
            {knowledge.map((base) => (
              <li key={base.id}>
                <GlanceRow
                  text={`${base.name} · ${base.document_count}`}
                  label={`${base.name}, ${base.document_count} document${base.document_count === 1 ? "" : "s"}`}
                  meta={agoPhrase(base.updated_at, now)}
                  onClick={onOpenKnowledge}
                />
              </li>
            ))}
          </ul>
        ) : (
          <Notice
            read={corpora}
            name="knowledge bases"
            empty="Nothing to look up yet."
          />
        )}
      </GlanceCard>

      <GlanceCard title={`Files ${agentName ?? "your agent"} made`}>
        {files.length > 0 ? (
          <ul className="home-glance-list">
            {files.map((file) => (
              <li key={file.key}>
                <GlanceRow
                  mark={<FileKindBadge name={file.name} size="small" />}
                  text={file.name}
                  label={[file.name, conversationTitle(file.threadId)]
                    .filter(Boolean)
                    .join(", from ")}
                  meta={agoPhrase(file.at, now)}
                  disabled={!onOpenConversation}
                  onClick={() => onOpenConversation?.(file.threadId)}
                />
              </li>
            ))}
          </ul>
        ) : (
          filesRead && (
            <p className="home-glance-quiet">
              Files your agent makes in recent conversations land here.
            </p>
          )
        )}
      </GlanceCard>

      <GlanceCard
        title="Recurring work"
        action={
          onOpenWorkflows && workflows.length > 0 ? (
            <HeadAction
              label={`All ${workflows.length}`}
              ariaLabel={`All ${workflows.length} recurring workflows`}
              onClick={onOpenWorkflows}
            />
          ) : undefined
        }
      >
        {workflows.length > 0 ? (
          <ul className="home-glance-list">
            {workflows.slice(0, 3).map((workflow) => (
              <li key={workflow.id}>
                <GlanceRow
                  text={workflow.name}
                  meta={
                    workflow.status === "disabled"
                      ? "Paused"
                      : workflow.schedule &&
                          workflow.schedule.state === "enabled"
                        ? scheduleWords(
                            workflow.schedule.expression,
                            workflow.schedule.timezone
                          )
                        : "Runs when you start it"
                  }
                  disabled={!onOpenWorkflows}
                  onClick={onOpenWorkflows}
                />
              </li>
            ))}
          </ul>
        ) : (
          <Notice
            read={workflowsRead}
            name="recurring work"
            empty="Nothing runs on a schedule yet."
          />
        )}
      </GlanceCard>
    </div>
  )
}

/**
 * Who the glance shows: the people the member shares a teamspace with
 * first, then everyone else, by name.
 */
function teammatesOf(people: DmPerson[]): DmPerson[] {
  return [...people].sort(
    (left, right) =>
      Number(right.spaces.length > 0) - Number(left.spaces.length > 0) ||
      left.name.localeCompare(right.name)
  )
}

function Teammate({
  person,
  onMessage,
}: {
  person: DmPerson
  onMessage?: (peerId: string) => void
}) {
  // Online means they have this DM open in a visible tab (DESIGN.md §3).
  const online = usePeerOnline(person.id)
  return (
    <Button
      variant="ghost"
      className="home-glance-mate"
      aria-label={`Message ${person.name}${online ? ", online" : ""}`}
      title={person.name}
      disabled={!onMessage}
      onClick={() => onMessage?.(person.id)}
    >
      <PersonAvatar id={person.id} name={person.name} online={online} />
      <span>{firstName(person.name)}</span>
    </Button>
  )
}

function GlanceCard({
  title,
  action,
  children,
}: {
  title: string
  action?: ReactNode
  children: ReactNode
}) {
  return (
    <Card
      size="sm"
      className="home-glance-card"
      role="region"
      aria-label={title}
    >
      <CardHeader className="home-glance-card-head">
        <CardTitle className="home-glance-card-title">
          <h2>{title}</h2>
        </CardTitle>
        {action && <CardAction>{action}</CardAction>}
      </CardHeader>
      <CardContent className="home-glance-card-body">{children}</CardContent>
    </Card>
  )
}

function HeadAction({
  label,
  ariaLabel,
  onClick,
}: {
  label: string
  ariaLabel?: string
  onClick: () => void
}) {
  return (
    <Button
      variant="ghost"
      size="sm"
      className="home-glance-head-action"
      aria-label={ariaLabel}
      onClick={onClick}
    >
      {label}
      <ArrowUpRight aria-hidden="true" />
    </Button>
  )
}

/** A row in a card: a small mark, one line of text and a quiet time or status. */
function GlanceRow({
  mark,
  text,
  label,
  meta,
  disabled,
  onClick,
}: {
  mark?: ReactNode
  text: string
  /** The row's accessible name, when its text alone says too little. */
  label?: string
  meta?: string
  disabled?: boolean
  onClick?: () => void
}) {
  return (
    <Button
      variant="ghost"
      className="home-glance-row"
      aria-label={label}
      disabled={disabled}
      onClick={onClick}
    >
      {mark && <span className="home-glance-mark">{mark}</span>}
      <span className="home-glance-text">{text}</span>
      {meta && <small>{meta}</small>}
    </Button>
  )
}

/** A service this server does not run: the card just says what lands there. */
function unavailable(error: unknown): boolean {
  return (
    isUnavailableHere(error) ||
    (error instanceof ApiError && error.status === 404)
  )
}

/** A card with no rows: loading, failed with Try again, or its one empty line. */
function Notice({
  read,
  name,
  empty,
}: {
  read: GlanceRead
  name: string
  empty: string
}) {
  if (read.isError && unavailable(read.error))
    return <p className="home-glance-quiet">{empty}</p>
  if (read.isError)
    return (
      <p className="home-glance-quiet" role="status">
        Couldn’t load {name}.{" "}
        <Button
          variant="link"
          size="sm"
          className="home-glance-retry"
          onClick={() => void read.refetch()}
        >
          Try again
        </Button>
      </p>
    )
  if (read.isPending)
    return (
      <p className="home-glance-quiet" role="status">
        Loading {name}…
      </p>
    )
  return <p className="home-glance-quiet">{empty}</p>
}
