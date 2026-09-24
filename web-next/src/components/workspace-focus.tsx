import { useState } from "react"
import type { FormEvent } from "react"
import {
  ArrowLeft,
  ArrowUp,
  ArrowUpRight,
  FileText,
  Paperclip,
} from "lucide-react"
import { IdentityAvatar, people, TeamspaceAvatar } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { cn } from "@/lib/utils"

export type WorkId = "product-sync"

type WorkMessage = {
  author: string
  avatar: AvatarIdentity
  time: string
  text: string
}

export type WorkItem = {
  id: string
  title: string
  kind: string
  description: string
  status: string
  participants: AvatarIdentity[]
  summary: string
  tasks: string[]
  files: string[]
  activity: string[]
  messages: WorkMessage[]
}

export const workItems: Record<WorkId, WorkItem> = {
  "product-sync": {
    id: "product-sync",
    title: "Product sync",
    kind: "Teamspace",
    description:
      "A shared space for the product team's handoffs and decisions.",
    status: "Online",
    participants: [people.avery, people.jordan, people.sam],
    summary:
      "Review the handoff notes, confirm the checklist, and keep the conversation visible in the chat pane.",
    tasks: [
      "Confirm the launch checklist",
      "Review the handoff notes",
      "Share the final update",
    ],
    files: ["Onboarding checklist", "Handoff notes"],
    activity: [
      "Jordan added the final handoff notes",
      "Sam started reviewing the checklist",
    ],
    messages: [
      {
        author: "Avery Morgan",
        avatar: people.avery,
        time: "9:12 AM",
        text: "Can we confirm the onboarding checklist before the handoff?",
      },
      {
        author: "Jordan Lee",
        avatar: people.jordan,
        time: "9:14 AM",
        text: "Yes — I added the final notes and the review checklist is ready.",
      },
      {
        author: "Sam Kim",
        avatar: people.sam,
        time: "9:16 AM",
        text: "I'll review the checklist now and confirm with the team.",
      },
    ],
  },
}

type WorkTab = "Overview" | "Tasks" | "Files" | "Activity"
const tabs: WorkTab[] = ["Overview", "Tasks", "Files", "Activity"]
const tabTitles: Record<Exclude<WorkTab, "Overview">, string> = {
  Tasks: "Current work",
  Files: "Files and context",
  Activity: "Recent activity",
}

export function WorkspaceFocus({
  work,
  onBack,
}: {
  work: WorkItem
  onBack: () => void
}) {
  const [tab, setTab] = useState<WorkTab>("Overview")
  const [draft, setDraft] = useState("")
  const [messages, setMessages] = useState(work.messages)
  const [isComposing, setIsComposing] = useState(false)
  const visibleHeight = useVisibleHeight(isComposing)
  const panelTitle =
    tab === "Overview"
      ? work.id === "product-sync"
        ? "Onboarding checklist"
        : work.title
      : tabTitles[tab]

  function sendMessage(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const text = draft.trim()
    if (!text) return
    setMessages((current) => [
      ...current,
      {
        author: "You",
        avatar: people.avery,
        time: new Date().toLocaleTimeString([], {
          hour: "numeric",
          minute: "2-digit",
        }),
        text,
      },
    ])
    setDraft("")
  }

  return (
    <div
      className="workspace-focus"
      data-composing={isComposing ? "true" : "false"}
      style={
        isComposing && visibleHeight ? { height: visibleHeight } : undefined
      }
    >
      <header className="workspace-focus-header">
        <button
          className="workspace-icon-button"
          type="button"
          aria-label="Back to workspace"
          onClick={onBack}
        >
          <ArrowLeft size={17} strokeWidth={1.7} />
        </button>
        {work.kind === "Teamspace" && (
          <TeamspaceAvatar
            name={work.title}
            className="workspace-focus-team-avatar"
          />
        )}
        <div>
          <h1>{work.title}</h1>
          <p>{work.kind}</p>
        </div>
      </header>

      <main className="workspace-focus-layout">
        <section
          className="workspace-conversation"
          aria-label={`${work.title} conversation`}
        >
          <div className="workspace-conversation-presence">
            <span className="workspace-presence-pill">{work.status}</span>
            <div className="workspace-participants" aria-label="Participants">
              {work.participants.map((identity) => (
                <IdentityAvatar identity={identity} key={identity.id} />
              ))}
            </div>
          </div>

          <div className="workspace-messages" aria-live="polite">
            {messages.length === 0 && (
              <p className="workspace-messages-empty">
                No messages yet. Start the conversation here.
              </p>
            )}
            {messages.map((message, index) => (
              <div
                className="workspace-message"
                key={`${message.author}-${index}`}
              >
                <IdentityAvatar identity={message.avatar} />
                <div className="workspace-message-body">
                  <div className="workspace-message-meta">
                    <strong>{message.author}</strong>
                    <time>{message.time}</time>
                  </div>
                  <p>{message.text}</p>
                </div>
              </div>
            ))}
          </div>

          <form
            className="workspace-composer workspace-message-composer"
            onSubmit={sendMessage}
          >
            <span className="workspace-composer-attach" aria-hidden="true">
              <Paperclip size={17} strokeWidth={1.7} />
            </span>
            <label className="sr-only" htmlFor="workspace-message-draft">
              Message {work.title}
            </label>
            <input
              id="workspace-message-draft"
              value={draft}
              onChange={(event) => setDraft(event.target.value)}
              onFocus={() => setIsComposing(true)}
              onBlur={() => setIsComposing(false)}
              placeholder="Add a message..."
            />
            <button
              className="workspace-composer-send"
              type="submit"
              aria-label="Send message"
              disabled={!draft.trim()}
            >
              <ArrowUp size={16} strokeWidth={2} />
            </button>
          </form>
        </section>

        <div className="workspace-work-column">
          <div className="workspace-work-tabs" aria-label="Work views">
            {tabs.map((item) => (
              <button
                key={item}
                type="button"
                className={cn(
                  "workspace-work-tab",
                  tab === item && "is-active"
                )}
                aria-pressed={tab === item}
                onClick={() => setTab(item)}
              >
                {item}
              </button>
            ))}
          </div>
          <section
            className="workspace-work-panel"
            aria-label={`${work.title} work panel`}
          >
            <div className="workspace-work-heading">
              <div>
                <span className="workspace-work-eyebrow">{tab}</span>
                <h2>{panelTitle}</h2>
                <p>{work.description}</p>
              </div>
              {tab === "Overview" && (
                <span className="workspace-work-status">In progress</span>
              )}
            </div>
            <div className="workspace-work-body">
              {tab === "Overview" && (
                <>
                  <div className="workspace-work-card">
                    <p>{work.summary}</p>
                  </div>
                  <div className="workspace-work-card">
                    <h3>Open items</h3>
                    {work.tasks.length ? (
                      <ul className="workspace-work-list">
                        {work.tasks.map((task) => (
                          <li key={task}>{task}</li>
                        ))}
                      </ul>
                    ) : (
                      <p className="workspace-work-empty">No open items yet.</p>
                    )}
                  </div>
                </>
              )}
              {tab === "Tasks" && (
                <div className="workspace-work-card">
                  {work.tasks.length ? (
                    <ul className="workspace-work-list">
                      {work.tasks.map((task) => (
                        <li key={task}>{task}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No tasks yet.</p>
                  )}
                </div>
              )}
              {tab === "Files" && (
                <div className="workspace-work-card">
                  {work.files.length ? (
                    <ul className="workspace-file-list">
                      {work.files.map((file) => (
                        <li key={file}>
                          <FileText size={17} strokeWidth={1.7} />
                          <span>{file}</span>
                          <ArrowUpRight size={15} strokeWidth={1.7} />
                        </li>
                      ))}
                    </ul>
                  ) : (
                    <p>No files yet.</p>
                  )}
                </div>
              )}
              {tab === "Activity" && (
                <div className="workspace-work-card">
                  {work.activity.length ? (
                    <ul className="workspace-work-list">
                      {work.activity.map((item) => (
                        <li key={item}>{item}</li>
                      ))}
                    </ul>
                  ) : (
                    <p>No activity yet.</p>
                  )}
                </div>
              )}
            </div>
          </section>
        </div>
      </main>
    </div>
  )
}
