import { useState } from "react"
import { useQuery, useQueryClient } from "@tanstack/react-query"
import {
  ArrowLeft,
  ArrowUpRight,
  CheckCircle2,
  Download,
  FileStack,
  FileText,
  MessageSquareText,
  Sparkles,
} from "lucide-react"
import { IdentityAvatar, TeamspaceAvatar } from "./workspace-avatar"
import { TeamspaceConversation } from "./teamspace-room"
import { TeamSynthesis } from "./team-synthesis"
import { RoomLive } from "./live-work"
import {
  agentIdentity,
  personIdentity,
  statusLabel,
  timeAgo,
} from "./teamspace-format"
import { useTeamspaceRoom } from "@/hooks/use-teamspace-room"
import { useVisibleHeight } from "@/hooks/use-visible-height"
import { ApiError } from "@/lib/api/client"
import { isUnavailableHere } from "@/lib/agents/policy"
import {
  downloadFile,
  filesQueryOptions,
  formatBytes,
} from "@/lib/files/client"
import type {
  Teamspace,
  TeamspaceEntry,
  TeamspaceEntryKind,
} from "@/lib/teamspaces/client"
import { safeArtifactHref } from "@/lib/teamspaces/policy"
import {
  teamspaceEntriesQueryOptions,
  teamspaceKeys,
  teamspaceQueryOptions,
} from "@/lib/teamspaces/queries"
import { cn } from "@/lib/utils"

type WorkTab = "Overview" | "Tasks" | "Files" | "Live" | "Activity"
const tabs: WorkTab[] = ["Overview", "Tasks", "Files", "Live", "Activity"]

/**
 * A teamspace as focused work (DESIGN.md §3): no left sidebar, the room as
 * a naked conversation, a compact header, and work tabs above the panel.
 */
export function TeamspaceFocus({
  organizationId,
  teamspaceId,
  viewerId,
  onBack,
}: {
  organizationId: string
  teamspaceId: string
  viewerId?: string
  onBack: () => void
}) {
  const queryClient = useQueryClient()
  const detail = useQuery({
    ...teamspaceQueryOptions(organizationId, teamspaceId),
    // What the list already knows shows at once; the fresh read replaces it.
    placeholderData: () =>
      queryClient
        .getQueryData<Teamspace[]>(teamspaceKeys.list(organizationId))
        ?.find((space) => space.id === teamspaceId),
  })
  const gone = detail.error instanceof ApiError && detail.error.status === 404
  const space = gone ? undefined : detail.data
  // Mounted for as long as this view is: reading the room is what keeps the
  // member present in it.
  const room = useTeamspaceRoom(organizationId, space, viewerId)
  const [tab, setTab] = useState<WorkTab>("Overview")
  const [isComposing, setIsComposing] = useState(false)
  const visibleHeight = useVisibleHeight(isComposing)

  if (!space) {
    return (
      <div className="workspace-focus">
        <header className="workspace-focus-header">
          <button
            className="workspace-icon-button"
            type="button"
            aria-label="Back to teamspaces"
            onClick={onBack}
          >
            <ArrowLeft size={17} strokeWidth={1.7} />
          </button>
          <div>
            <h1>Teamspace</h1>
          </div>
        </header>
        <main className="workspace-focus-unavailable">
          <p role={detail.isPending ? "status" : "alert"}>
            {detail.isPending
              ? "Opening the teamspace…"
              : gone
                ? "This teamspace is no longer available to you."
                : isUnavailableHere(detail.error)
                  ? "Teamspaces aren't set up on this server."
                  : "Couldn't open the teamspace."}
          </p>
          {!detail.isPending && !gone && !isUnavailableHere(detail.error) && (
            <button
              className="workspace-teamspace-secondary-button"
              type="button"
              onClick={() => void detail.refetch()}
            >
              Try again
            </button>
          )}
        </main>
      </div>
    )
  }

  return (
    <div
      className="workspace-focus workspace-teamspace-focus"
      data-composing={isComposing ? "true" : "false"}
      style={
        isComposing && visibleHeight ? { height: visibleHeight } : undefined
      }
    >
      <div className="workspace-teamspace-scene" aria-hidden="true" />
      <header className="workspace-focus-header workspace-teamspace-hero">
        <button
          className="workspace-icon-button"
          type="button"
          aria-label="Back to teamspaces"
          onClick={onBack}
        >
          <ArrowLeft size={17} strokeWidth={1.7} />
        </button>
        <TeamspaceAvatar
          name={space.name}
          className="workspace-focus-team-avatar"
        />
        <div>
          <h1>{space.name}</h1>
          <p>
            <span>Teamspace · {statusLabel(space)}</span>
            <span className="workspace-teamspace-hero-more">
              {" "}
              · {space.team_name} team · {space.whisperers.length}{" "}
              {space.whisperers.length === 1 ? "person" : "people"}
            </span>
          </p>
        </div>
      </header>

      <main className="workspace-focus-layout">
        <TeamspaceConversation
          space={space}
          room={room}
          viewerId={viewerId}
          onComposingChange={setIsComposing}
        />

        <div className="workspace-work-column">
          <div
            className="workspace-work-tabs"
            role="group"
            aria-label="Work views"
          >
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
            aria-label={`${space.name} work panel`}
          >
            {tab === "Overview" && (
              <OverviewPanel
                organizationId={organizationId}
                space={space}
                viewerId={viewerId}
              />
            )}
            {tab === "Tasks" && (
              <>
                <WorkHeading eyebrow="Tasks" title="Tasks" />
                <div className="workspace-work-body">
                  <div className="workspace-work-card">
                    <p>Teamspaces don't track tasks yet.</p>
                  </div>
                </div>
              </>
            )}
            {tab === "Files" && (
              <FilesPanel organizationId={organizationId} space={space} />
            )}
            {tab === "Live" && (
              <>
                <WorkHeading
                  eyebrow="Live"
                  title="Live work"
                  description="Sandboxes and browsers agents run for this room, and what they do there."
                />
                <div className="workspace-work-body">
                  <RoomLive
                    organizationId={organizationId}
                    room={{ kind: "teamspace", id: space.id }}
                    viewerId={viewerId}
                  />
                </div>
              </>
            )}
            {tab === "Activity" && (
              <ActivityPanel organizationId={organizationId} space={space} />
            )}
          </section>
        </div>
      </main>
    </div>
  )
}

function WorkHeading({
  eyebrow,
  title,
  description,
  status,
}: {
  eyebrow: string
  title: string
  description?: string
  status?: string
}) {
  return (
    <div className="workspace-work-heading">
      <div>
        <span className="workspace-work-eyebrow">{eyebrow}</span>
        <h2>{title}</h2>
        {description && <p>{description}</p>}
      </div>
      {status && <span className="workspace-work-status">{status}</span>}
    </div>
  )
}

function OverviewPanel({
  organizationId,
  space,
  viewerId,
}: {
  organizationId: string
  space: Teamspace
  viewerId?: string
}) {
  const contributions = space.contributions
  return (
    <>
      <WorkHeading
        eyebrow="Overview"
        title={space.name}
        description={`${space.team_name} team · updated ${timeAgo(space.updated_at)}`}
        status={statusLabel(space)}
      />
      <div className="workspace-work-body">
        <TeamSynthesis organizationId={organizationId} teamspaceId={space.id} />
        <div className="workspace-work-card">
          <h3>Purpose</h3>
          <p className="workspace-teamspace-purpose">
            {space.description || "No description yet."}
          </p>
        </div>
        <div className="workspace-work-card">
          <h3>Contributed agents</h3>
          {contributions.length ? (
            <ul className="workspace-teamspace-roster">
              {contributions.map((contribution) => (
                <li key={contribution.agent_id}>
                  <IdentityAvatar
                    identity={agentIdentity(
                      contribution.agent_id,
                      contribution.name
                    )}
                  />
                  <span>
                    <strong>{contribution.name}</strong>
                    <small>{contribution.role}</small>
                  </span>
                  <em>
                    {contribution.status !== "active"
                      ? "Paused"
                      : contribution.delegation_mode === "automatic"
                        ? "Works on its own"
                        : "Asks its owner first"}
                  </em>
                </li>
              ))}
            </ul>
          ) : (
            <p className="workspace-work-empty">
              No one has contributed a specialist yet.
            </p>
          )}
        </div>
        <div className="workspace-work-card">
          <h3>People</h3>
          {space.whisperers.length ? (
            <ul className="workspace-teamspace-roster">
              {space.whisperers.map((whisperer) => {
                const name =
                  whisperer.user_id === viewerId
                    ? "You"
                    : whisperer.user_display_name || "Teammate"
                return (
                  <li key={whisperer.user_id}>
                    <IdentityAvatar
                      identity={personIdentity(
                        whisperer.user_id,
                        whisperer.user_display_name || "Teammate"
                      )}
                    />
                    <span>
                      <strong>{name}</strong>
                      <small>with {whisperer.agent_name}</small>
                    </span>
                  </li>
                )
              })}
            </ul>
          ) : (
            <p className="workspace-work-empty">
              No one on {space.team_name} has an active agent yet.
            </p>
          )}
        </div>
      </div>
    </>
  )
}

function FilesPanel({
  organizationId,
  space,
}: {
  organizationId: string
  space: Teamspace
}) {
  // Team files are this teamspace's files: a teamspace is its team's one
  // shared space, and files keeps what was shared with that team.
  const canSee = space.is_member || space.can_manage
  const files = useQuery({
    ...filesQueryOptions(organizationId, {
      scope: "team",
      teamId: space.team_id,
    }),
    enabled: canSee,
    retry: false,
  })
  const [downloading, setDownloading] = useState<string>()
  const [downloadError, setDownloadError] = useState<string>()

  async function download(fileId: string) {
    setDownloading(fileId)
    setDownloadError(undefined)
    try {
      window.location.assign(await downloadFile(organizationId, fileId))
    } catch {
      setDownloadError("Couldn't download that file. Try again.")
    } finally {
      setDownloading(undefined)
    }
  }

  return (
    <>
      <WorkHeading
        eyebrow="Files"
        title="Team files"
        description={`Shared with the ${space.team_name} team.`}
      />
      <div className="workspace-work-body">
        <div className="workspace-work-card">
          {!canSee ? (
            <p>Only members of the {space.team_name} team can see its files.</p>
          ) : files.isPending ? (
            <p role="status">Loading files…</p>
          ) : files.isError ? (
            <p role="alert">
              {files.error instanceof ApiError && files.error.status === 404
                ? `Only members of the ${space.team_name} team can see its files.`
                : files.error instanceof ApiError && files.error.status === 503
                  ? "Files aren't available on this server."
                  : "Couldn't load the team's files."}
            </p>
          ) : files.data.length === 0 ? (
            <p>No files have been shared with {space.team_name} yet.</p>
          ) : (
            <ul className="workspace-file-list">
              {files.data.map((file) => (
                <li
                  key={file.id}
                  data-screen-context-kind="team_file"
                  data-screen-context-id={file.id}
                  data-screen-context-name={file.filename}
                >
                  <FileText size={17} strokeWidth={1.7} />
                  <span className="workspace-teamspace-file">
                    <strong>{file.filename}</strong>
                    <small>
                      {formatBytes(file.size_bytes)} · added{" "}
                      {timeAgo(file.created_at)}
                    </small>
                  </span>
                  <button
                    type="button"
                    className="workspace-teamspace-icon-button"
                    aria-label={`Download ${file.filename}`}
                    disabled={downloading === file.id}
                    onClick={() => void download(file.id)}
                  >
                    <Download size={15} strokeWidth={1.7} />
                  </button>
                </li>
              ))}
            </ul>
          )}
          {downloadError && (
            <p className="workspace-teamspace-error" role="alert">
              {downloadError}
            </p>
          )}
        </div>
      </div>
    </>
  )
}

const entryKinds: Record<
  TeamspaceEntryKind,
  { label: string; icon: typeof FileStack }
> = {
  update: { label: "Update", icon: MessageSquareText },
  result: { label: "Result", icon: Sparkles },
  artifact: { label: "Artifact", icon: FileStack },
  handoff: { label: "Handoff", icon: ArrowUpRight },
  decision: { label: "Decision", icon: CheckCircle2 },
}

function ActivityPanel({
  organizationId,
  space,
}: {
  organizationId: string
  space: Teamspace
}) {
  const entries = useQuery({
    ...teamspaceEntriesQueryOptions(organizationId, space.id),
    // The ledger is for the team's members; anyone else is refused 403.
    enabled: space.is_member,
  })

  return (
    <>
      <WorkHeading
        eyebrow="Activity"
        title="Shared work"
        description="What the room decided and what teammates' agents published, newest first."
      />
      <div className="workspace-work-body">
        {!space.is_member ? (
          <div className="workspace-work-card">
            <p>
              Only members of the {space.team_name} team can read its shared
              work.
            </p>
          </div>
        ) : entries.isPending ? (
          <div className="workspace-work-card">
            <p role="status">Loading shared work…</p>
          </div>
        ) : entries.isError ? (
          <div className="workspace-work-card">
            <p role="alert">Couldn't load the shared work.</p>
            <button
              className="workspace-teamspace-secondary-button"
              type="button"
              onClick={() => void entries.refetch()}
            >
              Try again
            </button>
          </div>
        ) : entries.data.length === 0 ? (
          <div className="workspace-work-card">
            <p>
              Nothing has been shared yet. When the room decides something, it
              lands here.
            </p>
          </div>
        ) : (
          <ol className="workspace-teamspace-ledger" aria-label="Shared work">
            {entries.data.map((entry) => (
              <LedgerEntry entry={entry} key={entry.id} />
            ))}
          </ol>
        )}
      </div>
    </>
  )
}

function LedgerEntry({ entry }: { entry: TeamspaceEntry }) {
  const kind = entryKinds[entry.kind]
  const Icon = kind.icon
  return (
    <li
      className="workspace-work-card workspace-teamspace-entry"
      data-screen-context-kind="shared_work"
      data-screen-context-id={entry.id}
      data-screen-context-name={entry.title}
    >
      <div className="workspace-teamspace-entry-head">
        <span className="workspace-teamspace-entry-kind" data-kind={entry.kind}>
          <Icon size={13} strokeWidth={1.8} aria-hidden="true" />
          {kind.label}
        </span>
        <time dateTime={entry.created_at}>{timeAgo(entry.created_at)}</time>
      </div>
      <h3>{entry.title}</h3>
      {entry.summary !== entry.title && <p>{entry.summary}</p>}
      <div className="workspace-teamspace-entry-author">
        <IdentityAvatar
          identity={agentIdentity(
            entry.author_agent_id,
            entry.author_agent_name
          )}
        />
        <span>{entry.author_agent_name}</span>
        {entry.tools_used.length > 0 && (
          <small>
            {entry.tools_used.length} tool
            {entry.tools_used.length === 1 ? "" : "s"} used
          </small>
        )}
      </div>
      {entry.artifacts.length > 0 && (
        <ul className="workspace-teamspace-artifacts" aria-label="Artifacts">
          {entry.artifacts.map((artifact) => {
            const href = safeArtifactHref(artifact.uri)
            return (
              <li key={`${artifact.uri}-${artifact.name}`}>
                {href ? (
                  <a href={href} rel="noopener noreferrer" target="_blank">
                    {artifact.name}
                    <ArrowUpRight size={13} strokeWidth={1.7} />
                  </a>
                ) : (
                  <span title="This address isn't safe to open, so it isn't a link.">
                    {artifact.name}
                  </span>
                )}
              </li>
            )
          })}
        </ul>
      )}
    </li>
  )
}
