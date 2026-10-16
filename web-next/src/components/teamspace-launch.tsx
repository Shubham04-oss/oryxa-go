import { useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import {
  CalendarDays,
  Ellipsis,
  Pencil,
  Plus,
  Trash2,
  UserRound,
} from "lucide-react"
import { Button } from "@/components/ui/button"
import { Checkbox } from "@/components/ui/checkbox"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuSeparator,
  DropdownMenuSub,
  DropdownMenuSubContent,
  DropdownMenuSubTrigger,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { NativeSelect, NativeSelectOption } from "@/components/ui/native-select"
import {
  Popover,
  PopoverContent,
  PopoverTitle,
  PopoverTrigger,
} from "@/components/ui/popover"
import { IdentityAvatar } from "./workspace-avatar"
import {
  dayLabel,
  daysAway,
  daysUntil,
  memberName,
  parseDay,
  personIdentity,
} from "./teamspace-format"
import { ApiError } from "@/lib/api/client"
import { listTeamMembers } from "@/lib/settings/client"
import {
  createTeamspaceTask,
  deleteTeamspaceTask,
  updateTeamspace,
  updateTeamspaceTask,
} from "@/lib/teamspaces/client"
import type {
  CreateTeamspaceTaskInput,
  Teamspace,
  TeamspaceTask,
  UpdateTeamspaceTaskInput,
} from "@/lib/teamspaces/client"
import {
  afterTeamspaceTaskChange,
  afterUpdateTeamspace,
  teamspaceKeys,
  teamspaceTasksQueryOptions,
} from "@/lib/teamspaces/queries"

type Person = { id: string; name: string }

const pausedMessage =
  "This teamspace was paused, so its checklist is read-only now."
const pausedTargetMessage =
  "This teamspace was paused, so its target can't change now."

/** A write's refusal in one sentence, and whether the teamspace was paused under it. */
function writeError(
  error: unknown,
  fallback: string,
  whenPaused = pausedMessage
) {
  const paused =
    error instanceof ApiError &&
    error.status === 409 &&
    error.code === "teamspace_disabled"
  return { paused, message: paused ? whenPaused : fallback }
}

/**
 * The Launch card beside a teamspace's room (TeamspaceHome board): what the
 * team is working towards ("Launch", "4 November", "in 21 days"), how much
 * of the checklist is done, and the checklist itself. Members tick, add,
 * rename and reassign tasks; whoever created a task, or manages the team,
 * deletes it; managers set the date. A paused teamspace's card is read-only,
 * since Go refuses its writes 409. Each part shows only once Go serves it
 * (`target_date`, `task_counts`), so a Go without them shows no card.
 */
export function TeamspaceLaunchCard({
  organizationId,
  space,
  viewerId,
}: {
  organizationId: string
  space: Teamspace
  viewerId?: string
}) {
  const queryClient = useQueryClient()
  const paused = space.status !== "active"
  // Go sends both target fields (null when unset) once it keeps them.
  const servesTarget =
    space.target_date !== undefined || space.target_label !== undefined
  const servesTasks = space.task_counts !== undefined
  const canSetTarget = servesTarget && space.can_manage && !paused
  const canWrite = space.is_member && !paused
  const tasks = useQuery({
    ...teamspaceTasksQueryOptions(organizationId, space.id),
    // The checklist is members-only: a manager off the team is refused 403.
    enabled: servesTasks && space.is_member,
    retry: false,
  })
  const [adding, setAdding] = useState(false)
  const [error, setError] = useState<string>()

  const refused =
    tasks.error instanceof ApiError &&
    (tasks.error.status === 403 || tasks.error.status === 404)
  const list = tasks.data ?? []
  const counts = tasks.isSuccess
    ? { done: list.filter((task) => task.done).length, total: list.length }
    : space.task_counts
  const showChecklist =
    servesTasks &&
    space.is_member &&
    !refused &&
    (canWrite || (counts?.total ?? 0) > 0)
  const day = parseDay(space.target_date)
  const label = space.target_label?.trim()
  const people = usePeople(organizationId, space, viewerId, showChecklist)
  if (!day && !label && !canSetTarget && !showChecklist) return null

  // Read the teamspace again, so the whole page sees it paused.
  function readPaused() {
    return queryClient.invalidateQueries({
      queryKey: teamspaceKeys.teamspace(organizationId, space.id),
      exact: true,
    })
  }

  async function settle(failure?: unknown, fallback?: string) {
    if (failure === undefined) {
      setError(undefined)
    } else {
      const refusal = writeError(failure, fallback ?? "")
      setError(refusal.message)
      if (refusal.paused) await readPaused()
    }
    await afterTeamspaceTaskChange(queryClient, organizationId, space.id)
  }

  // The target editor goes once the teamspace reads paused, so the card
  // says why rather than the popover.
  async function targetPaused() {
    setError(pausedTargetMessage)
    await readPaused()
  }

  return (
    <section
      className="workspace-work-card workspace-teamspace-launch"
      aria-label={label || (day ? "Target" : "Checklist")}
    >
      <div className="workspace-teamspace-launch-head">
        <h3>{label || (day ? "Target" : "Checklist")}</h3>
        {counts && counts.total > 0 && !refused && (
          <span className="workspace-teamspace-launch-count">
            {counts.done} of {counts.total} done
          </span>
        )}
        {canSetTarget && (
          <TargetEditor
            organizationId={organizationId}
            space={space}
            onPaused={targetPaused}
          />
        )}
      </div>
      {day && (
        <p className="workspace-teamspace-launch-date">
          <strong>{dayLabel(day)}</strong>
          <span>{daysAway(daysUntil(day))}</span>
        </p>
      )}
      {showChecklist && counts && counts.total > 0 && (
        <div className="workspace-teamspace-launch-bar" aria-hidden="true">
          <i style={{ width: `${(counts.done / counts.total) * 100}%` }} />
        </div>
      )}
      {showChecklist &&
        (tasks.isPending ? (
          <p role="status">Loading the checklist…</p>
        ) : tasks.isError ? (
          <div className="workspace-teamspace-launch-error">
            <p role="alert">Couldn't load the checklist.</p>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => void tasks.refetch()}
            >
              Try again
            </Button>
          </div>
        ) : (
          <>
            {list.length > 0 ? (
              <ul className="workspace-teamspace-checklist">
                {list.map((task) => (
                  <TaskRow
                    key={task.id}
                    organizationId={organizationId}
                    space={space}
                    task={task}
                    people={people}
                    viewerId={viewerId}
                    canWrite={canWrite}
                    onSettled={settle}
                  />
                ))}
              </ul>
            ) : (
              <p className="workspace-teamspace-launch-empty">
                Nothing on the checklist yet.
              </p>
            )}
            {canWrite &&
              (adding ? (
                <AddTaskForm
                  organizationId={organizationId}
                  space={space}
                  people={people}
                  onDone={() => setAdding(false)}
                  onSettled={settle}
                />
              ) : (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="workspace-teamspace-launch-add"
                  onClick={() => {
                    setError(undefined)
                    setAdding(true)
                  }}
                >
                  <Plus data-icon="inline-start" aria-hidden="true" />
                  Add a task
                </Button>
              ))}
          </>
        ))}
      {error && (
        <p className="workspace-teamspace-error" role="alert">
          {error}
        </p>
      )}
    </section>
  )
}

/**
 * Who a task may be given to: the team's members (the same read Invite and
 * Settings → Teams make), else the teammates with a seat in the room.
 */
function usePeople(
  organizationId: string,
  space: Teamspace,
  viewerId: string | undefined,
  enabled: boolean
): Person[] {
  const members = useQuery({
    queryKey: ["settings", "teams", organizationId, space.team_id, "members"],
    queryFn: () => listTeamMembers(organizationId, space.team_id),
    enabled,
    retry: false,
  })
  const named = (id: string, name: string | undefined) => ({
    id,
    name: id === viewerId ? "You" : name || "Teammate",
  })
  if (members.isSuccess) {
    return members.data.map((member) =>
      named(
        member.user_id,
        member.display_name || memberName(space, member.user_id) || member.email
      )
    )
  }
  return space.whisperers.map((whisperer) =>
    named(whisperer.user_id, whisperer.user_display_name)
  )
}

function personName(people: Person[], space: Teamspace, userId: string) {
  return (
    people.find((person) => person.id === userId)?.name ??
    memberName(space, userId) ??
    "a former teammate"
  )
}

function TaskRow({
  organizationId,
  space,
  task,
  people,
  viewerId,
  canWrite,
  onSettled,
}: {
  organizationId: string
  space: Teamspace
  task: TeamspaceTask
  people: Person[]
  viewerId?: string
  canWrite: boolean
  onSettled: (failure?: unknown, fallback?: string) => Promise<void>
}) {
  const queryClient = useQueryClient()
  const [editing, setEditing] = useState(false)
  const [title, setTitle] = useState(task.title)
  // Enter and Escape take the field away, and a browser may blur it as it
  // goes; the edit finishes once, whichever comes first.
  const closing = useRef(false)
  const tasksKey = teamspaceKeys.tasks(organizationId, space.id)
  const update = useMutation({
    mutationFn: (input: UpdateTeamspaceTaskInput) =>
      updateTeamspaceTask(organizationId, space.id, task.id, input),
    onMutate: async (input) => {
      // A tick shows at once; the answer (or a refusal's re-read) settles it.
      await queryClient.cancelQueries({ queryKey: tasksKey })
      queryClient.setQueryData<TeamspaceTask[]>(tasksKey, (current) =>
        current?.map((item) =>
          item.id === task.id
            ? {
                ...item,
                ...(input.done !== undefined ? { done: input.done } : {}),
                ...(input.title !== undefined ? { title: input.title } : {}),
                ...(input.owner_user_id !== undefined
                  ? { owner_user_id: input.owner_user_id }
                  : {}),
              }
            : item
        )
      )
    },
    onSuccess: () => onSettled(),
    onError: (failure) =>
      onSettled(failure, "Couldn't save that change. Try again."),
  })
  const remove = useMutation({
    mutationFn: () => deleteTeamspaceTask(organizationId, space.id, task.id),
    onSuccess: () => onSettled(),
    onError: (failure) =>
      onSettled(failure, "Couldn't delete that task. Try again."),
  })
  const canDelete =
    canWrite &&
    (space.can_manage ||
      (viewerId !== undefined && task.created_by_user_id === viewerId))
  const owner = task.owner_user_id
  const due = parseDay(task.due_date)
  const busy = update.isPending || remove.isPending

  function finishEdit(save: boolean) {
    if (closing.current) return
    closing.current = true
    setEditing(false)
    const next = title.trim().replace(/\s+/g, " ")
    if (!save || !next || next === task.title) {
      setTitle(task.title)
      return
    }
    update.mutate({ title: next })
  }

  return (
    <li className="workspace-teamspace-task" data-done={task.done}>
      <Checkbox
        checked={task.done}
        disabled={!canWrite || busy}
        aria-label={task.title}
        onCheckedChange={(checked) => update.mutate({ done: checked })}
      />
      {editing ? (
        <Input
          className="workspace-teamspace-task-input"
          aria-label="Task title"
          value={title}
          maxLength={200}
          autoFocus
          onChange={(event) => setTitle(event.target.value)}
          onBlur={() => finishEdit(true)}
          onKeyDown={(event) => {
            if (event.key === "Enter") {
              event.preventDefault()
              finishEdit(true)
            } else if (event.key === "Escape") {
              event.preventDefault()
              finishEdit(false)
            }
          }}
        />
      ) : (
        <span className="workspace-teamspace-task-title">
          <span>{task.title}</span>
          {due && (
            <small>
              Due {dayLabel(due, { short: true })}
              {owner && ` · ${personName(people, space, owner)}`}
            </small>
          )}
          {!due && owner && <small>{personName(people, space, owner)}</small>}
        </span>
      )}
      {owner && !editing && (
        <IdentityAvatar
          identity={personIdentity(owner, personName(people, space, owner))}
        />
      )}
      {canWrite && !editing && (
        <DropdownMenu>
          <DropdownMenuTrigger
            render={<Button variant="ghost" size="icon-sm" disabled={busy} />}
            aria-label={`More for ${task.title}`}
          >
            <Ellipsis aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end">
            <DropdownMenuItem
              onClick={() => {
                closing.current = false
                setTitle(task.title)
                setEditing(true)
              }}
            >
              <Pencil aria-hidden="true" />
              Edit title
            </DropdownMenuItem>
            <DropdownMenuSub>
              <DropdownMenuSubTrigger>
                <UserRound aria-hidden="true" />
                Assign to
              </DropdownMenuSubTrigger>
              <DropdownMenuSubContent>
                <DropdownMenuRadioGroup
                  value={owner ?? ""}
                  onValueChange={(value: string) => {
                    if (value === (owner ?? "")) return
                    update.mutate({ owner_user_id: value || null })
                  }}
                >
                  <DropdownMenuRadioItem value="">No one</DropdownMenuRadioItem>
                  {people.map((person) => (
                    <DropdownMenuRadioItem key={person.id} value={person.id}>
                      {person.name}
                    </DropdownMenuRadioItem>
                  ))}
                </DropdownMenuRadioGroup>
              </DropdownMenuSubContent>
            </DropdownMenuSub>
            {canDelete && (
              <>
                <DropdownMenuSeparator />
                <DropdownMenuItem
                  variant="destructive"
                  onClick={() => remove.mutate()}
                >
                  <Trash2 aria-hidden="true" />
                  Delete task
                </DropdownMenuItem>
              </>
            )}
          </DropdownMenuContent>
        </DropdownMenu>
      )}
    </li>
  )
}

function AddTaskForm({
  organizationId,
  space,
  people,
  onDone,
  onSettled,
}: {
  organizationId: string
  space: Teamspace
  people: Person[]
  onDone: () => void
  onSettled: (failure?: unknown, fallback?: string) => Promise<void>
}) {
  const id = useId()
  const [title, setTitle] = useState("")
  const [owner, setOwner] = useState("")
  const [due, setDue] = useState("")
  const create = useMutation({
    mutationFn: (input: CreateTeamspaceTaskInput) =>
      createTeamspaceTask(organizationId, space.id, input),
    onSuccess: async () => {
      setTitle("")
      setOwner("")
      setDue("")
      await onSettled()
    },
    onError: (failure) =>
      onSettled(failure, "Couldn't add that task. Try again."),
  })
  const cleaned = title.trim().replace(/\s+/g, " ")

  return (
    <form
      className="workspace-teamspace-task-form"
      aria-label="Add a task"
      onSubmit={(event) => {
        event.preventDefault()
        if (!cleaned || create.isPending) return
        create.mutate({
          title: cleaned,
          owner_user_id: owner || undefined,
          due_date: due || undefined,
        })
      }}
    >
      <Label htmlFor={`${id}-title`}>Task</Label>
      <Input
        id={`${id}-title`}
        value={title}
        maxLength={200}
        autoFocus
        placeholder="Legal sign-off"
        onChange={(event) => setTitle(event.target.value)}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            event.preventDefault()
            onDone()
          }
        }}
      />
      <div className="workspace-teamspace-task-form-row">
        <span>
          <Label htmlFor={`${id}-owner`}>Owner</Label>
          <NativeSelect
            id={`${id}-owner`}
            value={owner}
            onChange={(event) => setOwner(event.target.value)}
          >
            <NativeSelectOption value="">No one</NativeSelectOption>
            {people.map((person) => (
              <NativeSelectOption key={person.id} value={person.id}>
                {person.name}
              </NativeSelectOption>
            ))}
          </NativeSelect>
        </span>
        <span>
          <Label htmlFor={`${id}-due`}>Due</Label>
          <Input
            id={`${id}-due`}
            type="date"
            value={due}
            onChange={(event) => setDue(event.target.value)}
          />
        </span>
      </div>
      <div className="workspace-teamspace-form-actions">
        <Button type="button" variant="outline" onClick={onDone}>
          Cancel
        </Button>
        <Button type="submit" disabled={!cleaned || create.isPending}>
          {create.isPending ? "Adding…" : "Add task"}
        </Button>
      </div>
    </form>
  )
}

/**
 * For whoever manages the team: the date and what it is, in a small
 * popover. Clearing sends null for both, from its own line under the fields
 * (never beside Save). A 409 `teamspace_disabled` closes the popover and
 * hands the card the reason, since the button goes once the teamspace
 * reads paused.
 */
function TargetEditor({
  organizationId,
  space,
  onPaused,
}: {
  organizationId: string
  space: Teamspace
  onPaused: () => Promise<void>
}) {
  const queryClient = useQueryClient()
  const id = useId()
  const [open, setOpen] = useState(false)
  const [date, setDate] = useState(space.target_date ?? "")
  const [label, setLabel] = useState(space.target_label ?? "")
  const save = useMutation({
    mutationFn: (input: {
      target_date: string | null
      target_label: string | null
    }) => updateTeamspace(organizationId, space.id, input),
    onSuccess: async (updated) => {
      setOpen(false)
      await afterUpdateTeamspace(queryClient, organizationId, updated)
    },
    onError: async (failure) => {
      if (!writeError(failure, "").paused) return
      setOpen(false)
      await onPaused()
    },
  })
  const isSet = Boolean(space.target_date || space.target_label)
  const failure = save.isError
    ? save.error instanceof ApiError && save.error.status === 400
      ? "That date or label isn't one Go accepts."
      : "Couldn't save the target. Try again."
    : undefined

  return (
    <Popover
      open={open}
      onOpenChange={(next) => {
        if (next) {
          setDate(space.target_date ?? "")
          setLabel(space.target_label ?? "")
          save.reset()
        }
        setOpen(next)
      }}
    >
      <PopoverTrigger
        render={<Button type="button" variant="ghost" size="icon-sm" />}
        aria-label={isSet ? "Change the target date" : "Set a target date"}
      >
        <CalendarDays aria-hidden="true" />
      </PopoverTrigger>
      <PopoverContent align="end" className="workspace-teamspace-target">
        <form
          onSubmit={(event) => {
            event.preventDefault()
            save.mutate({
              target_date: date || null,
              target_label: label.trim() || null,
            })
          }}
        >
          <PopoverTitle>Target</PopoverTitle>
          <Label htmlFor={`${id}-label`}>What it is</Label>
          <Input
            id={`${id}-label`}
            value={label}
            maxLength={60}
            placeholder="Launch"
            onChange={(event) => setLabel(event.target.value)}
          />
          <Label htmlFor={`${id}-date`}>Date</Label>
          <Input
            id={`${id}-date`}
            type="date"
            value={date}
            onChange={(event) => setDate(event.target.value)}
          />
          {failure && (
            <p className="workspace-teamspace-error" role="alert">
              {failure}
            </p>
          )}
          {isSet && (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              className="workspace-teamspace-target-clear"
              disabled={save.isPending}
              onClick={() =>
                save.mutate({ target_date: null, target_label: null })
              }
            >
              Clear the target
            </Button>
          )}
          <div className="workspace-teamspace-form-actions">
            <Button
              type="button"
              variant="outline"
              onClick={() => setOpen(false)}
            >
              Cancel
            </Button>
            <Button type="submit" disabled={save.isPending}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  )
}
