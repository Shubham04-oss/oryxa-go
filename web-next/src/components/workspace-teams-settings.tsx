import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, ChevronDown, ChevronRight, X } from "lucide-react"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { Input } from "@/components/ui/input"
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table"
import { ApiError } from "@/lib/api/client"
import type { Organization, Role } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { getMembers } from "@/lib/auth/invitations"
import {
  addTeamMember,
  changeTeamMemberRole,
  createTeam,
  deleteTeam,
  getDirectory,
  getTeam,
  listTeamMembers,
  listTeams,
  removeTeamMember,
  renameTeam,
} from "@/lib/settings/client"
import type { DirectoryPerson, Team } from "@/lib/settings/client"
import { teamspacesQueryOptions } from "@/lib/teamspaces/queries"
import { IdentityAvatar } from "./workspace-avatar"
import {
  SettingsActions,
  SettingsButton,
  SettingsCard,
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
  SettingsSelect,
} from "./settings-kit"

function personName(person: { display_name?: string; email: string }) {
  return person.display_name || person.email
}

function initials(name: string) {
  return name
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join("")
}

function PersonAvatar({ person }: { person: DirectoryPerson }) {
  const name = personName(person)
  return (
    <IdentityAvatar
      identity={{
        id: person.user_id,
        name,
        initials: initials(name),
        kind: "user",
      }}
    />
  )
}

/**
 * Settings → Teams: the organisation's teams as a table (who is in each, who
 * leads it, and its teamspaces), New team in a dialog, and each team's own
 * page for its name, members and deletion.
 *
 * Who is in each team comes from the directory in one call; a lead is a team
 * admin.
 */
export function WorkspaceTeamsSettings({
  organization,
  role,
  preview,
}: {
  organization?: Organization
  role?: Role
  preview: boolean
}) {
  const queryClient = useQueryClient()
  const orgId = organization?.id
  const canCreate = role === "owner" || role === "admin"
  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [creating, setCreating] = useState(false)
  const [newName, setNewName] = useState("")
  const [parentId, setParentId] = useState("")
  const [editingName, setEditingName] = useState("")
  const [addId, setAddId] = useState("")
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmRemoveMemberId, setConfirmRemoveMemberId] = useState<
    string | null
  >(null)
  const live = !preview && Boolean(orgId)
  const teams = useQuery({
    queryKey: ["settings", "teams", orgId],
    queryFn: () => listTeams(orgId!),
    enabled: live,
    retry: false,
  })
  const directory = useQuery({
    queryKey: ["settings", "directory", orgId],
    queryFn: () => getDirectory(orgId!),
    enabled: live && !selectedId,
    retry: false,
  })
  const teamspaces = useQuery({
    ...teamspacesQueryOptions(orgId ?? ""),
    enabled: live && !selectedId,
    retry: false,
  })
  const selected = teams.data?.find((team) => team.id === selectedId)
  const detail = useQuery({
    queryKey: ["settings", "teams", orgId, selectedId, "detail"],
    queryFn: () => getTeam(orgId!, selectedId!),
    enabled: live && Boolean(selectedId),
    retry: false,
  })
  const teamMembers = useQuery({
    queryKey: ["settings", "teams", orgId, selectedId, "members"],
    queryFn: () => listTeamMembers(orgId!, selectedId!),
    enabled: live && Boolean(selectedId),
    retry: false,
  })
  const orgMembers = useQuery({
    queryKey: ["organizations", orgId, "members"],
    queryFn: () => getMembers(orgId!),
    enabled: live && Boolean(selectedId && detail.data?.can_manage),
    retry: false,
  })
  const refreshTeams = () =>
    queryClient.invalidateQueries({ queryKey: ["settings", "teams", orgId] })
  const refreshDetail = () =>
    queryClient.invalidateQueries({
      queryKey: ["settings", "teams", orgId, selectedId, "detail"],
    })
  const refreshMembers = () =>
    queryClient.invalidateQueries({
      queryKey: ["settings", "teams", orgId, selectedId, "members"],
    })
  // A role change or removal that Go refuses with 403/404/409 usually means
  // this member's own place in the team just changed underneath them, so
  // re-read the detail (it holds `can_manage`, which gates every admin
  // control here) and the member list the same way a success would, instead
  // of leaving the page's stale view in charge.
  const refreshAfterRefusal = (error: unknown) => {
    if (error instanceof ApiError && [403, 404, 409].includes(error.status)) {
      void refreshDetail()
      void refreshMembers()
    }
  }
  const create = useMutation({
    mutationFn: (name: string) =>
      createTeam(orgId!, name, parentId || undefined),
    onSuccess: async (team) => {
      setNewName("")
      setParentId("")
      setCreating(false)
      await refreshTeams()
      openTeam(team)
    },
  })
  const rename = useMutation({
    mutationFn: () => renameTeam(orgId!, selectedId!, editingName.trim()),
    onSuccess: refreshTeams,
  })
  const remove = useMutation({
    mutationFn: () => deleteTeam(orgId!, selectedId!),
    onSuccess: async () => {
      openTeam()
      await refreshTeams()
    },
  })
  const add = useMutation({
    mutationFn: () => addTeamMember(orgId!, selectedId!, addId, "member"),
    onSuccess: async () => {
      setAddId("")
      await refreshMembers()
    },
  })
  const changeRole = useMutation({
    mutationFn: ({
      id,
      nextRole,
    }: {
      id: string
      nextRole: "admin" | "member"
    }) => changeTeamMemberRole(orgId!, selectedId!, id, nextRole),
    onSuccess: async () => {
      await refreshMembers()
      await refreshDetail()
    },
    onError: refreshAfterRefusal,
  })
  const removeMember = useMutation({
    mutationFn: (id: string) => removeTeamMember(orgId!, selectedId!, id),
    onSuccess: async () => {
      setConfirmRemoveMemberId(null)
      await refreshMembers()
      await refreshDetail()
    },
    onError: refreshAfterRefusal,
  })

  // What is open below the list belongs to one team: a removal waiting to be
  // confirmed, a member picked to add or an error must not follow the member
  // into the next team they open.
  function openTeam(team?: { id: string; name: string }) {
    setSelectedId(team?.id ?? null)
    setEditingName(team?.name ?? "")
    setAddId("")
    setConfirmDelete(false)
    setConfirmRemoveMemberId(null)
    rename.reset()
    add.reset()
    changeRole.reset()
    removeMember.reset()
    remove.reset()
  }

  function submitNew(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const value = newName.trim()
    if (value && value.length <= 120 && canCreate) create.mutate(value)
  }

  if (preview || !organization) {
    return (
      <SettingsGroup label="Teams">
        <SettingsNote>
          Layout preview. Sign in to load and manage real teams.
        </SettingsNote>
      </SettingsGroup>
    )
  }

  if (selectedId) {
    const canManage = detail.data?.can_manage === true
    const addable =
      orgMembers.data?.filter(
        (member) =>
          !teamMembers.data?.some(
            (present) => present.user_id === member.user_id
          )
      ) ?? []
    return (
      <>
        <div>
          <Button
            variant="ghost"
            size="sm"
            className="settings-back"
            onClick={() => openTeam()}
          >
            <ArrowLeft /> All teams
          </Button>
        </div>
        <SettingsGroup label={selected?.name || "Team"}>
          <SettingsRow
            title={selected?.name || "Team"}
            description={
              canManage
                ? "Manage this team and its members."
                : "Your place in this team."
            }
          />
          {detail.isPending && <SettingsNote>Loading team…</SettingsNote>}
          {detail.isError && (
            <SettingsMessage tone="error">
              {authErrorMessage(detail.error, "Could not load the team.")}
            </SettingsMessage>
          )}
          {canManage && (
            <form
              onSubmit={(event) => {
                event.preventDefault()
                if (editingName.trim()) rename.mutate()
              }}
            >
              <SettingsRow title="Team name">
                <Input
                  className="settings-input"
                  aria-label="Team name"
                  value={editingName}
                  maxLength={120}
                  onChange={(event) => setEditingName(event.target.value)}
                />
              </SettingsRow>
              {rename.isError && (
                <SettingsMessage tone="error">
                  {authErrorMessage(rename.error, "Could not rename the team.")}
                </SettingsMessage>
              )}
              <SettingsActions>
                <SettingsButton
                  type="submit"
                  size="default"
                  tone="primary"
                  disabled={
                    !editingName.trim() ||
                    editingName.trim() === selected?.name ||
                    rename.isPending
                  }
                >
                  Save name
                </SettingsButton>
              </SettingsActions>
            </form>
          )}
        </SettingsGroup>

        <SettingsGroup label="Members">
          {teamMembers.isPending && (
            <SettingsNote>Loading members…</SettingsNote>
          )}
          {teamMembers.isError && (
            <SettingsMessage tone="error">
              {authErrorMessage(teamMembers.error, "Could not load members.")}
            </SettingsMessage>
          )}
          {teamMembers.data?.map((member) => {
            const name = personName(member)
            return (
              <div key={member.user_id}>
                <SettingsRow title={name} description={member.email}>
                  {canManage ? (
                    <>
                      <DropdownMenu>
                        <DropdownMenuTrigger
                          className="settings-role-trigger"
                          aria-label={`Role for ${name}`}
                          disabled={changeRole.isPending}
                        >
                          {member.role === "admin" ? "Admin" : "Member"}
                          <ChevronDown size={14} aria-hidden="true" />
                        </DropdownMenuTrigger>
                        <DropdownMenuContent className="settings-popup">
                          {/* A menu only commits on an explicit choice, so
                              browsing it with the keyboard sends nothing. */}
                          <DropdownMenuRadioGroup
                            value={member.role}
                            onValueChange={(value) => {
                              if (value !== member.role) {
                                changeRole.mutate({
                                  id: member.user_id,
                                  nextRole: value as "admin" | "member",
                                })
                              }
                            }}
                          >
                            <DropdownMenuRadioItem value="member" closeOnClick>
                              Member
                            </DropdownMenuRadioItem>
                            <DropdownMenuRadioItem value="admin" closeOnClick>
                              Admin
                            </DropdownMenuRadioItem>
                          </DropdownMenuRadioGroup>
                        </DropdownMenuContent>
                      </DropdownMenu>
                      <Button
                        variant="ghost"
                        size="icon-sm"
                        className="settings-icon-danger"
                        aria-label={`Remove ${name} from the team`}
                        disabled={removeMember.isPending}
                        onClick={() => setConfirmRemoveMemberId(member.user_id)}
                      >
                        <X />
                      </Button>
                    </>
                  ) : (
                    <span className="settings-value">
                      {member.role === "admin" ? "Admin" : "Member"}
                    </span>
                  )}
                </SettingsRow>
                {confirmRemoveMemberId === member.user_id && (
                  <SettingsActions start={`Remove ${name} from this team?`}>
                    <SettingsButton
                      size="default"
                      onClick={() => setConfirmRemoveMemberId(null)}
                    >
                      Cancel
                    </SettingsButton>
                    <SettingsButton
                      size="default"
                      tone="danger"
                      disabled={removeMember.isPending}
                      onClick={() => removeMember.mutate(member.user_id)}
                    >
                      {removeMember.isPending ? "Removing…" : "Remove member"}
                    </SettingsButton>
                  </SettingsActions>
                )}
              </div>
            )
          })}
          {canManage && (
            <SettingsRow title="Add a member">
              <SettingsSelect
                label="Add a member"
                value={addId}
                options={[
                  { value: "", label: "Select a person" },
                  ...addable.map((member) => ({
                    value: member.user_id,
                    label: personName(member),
                  })),
                ]}
                onValueChange={setAddId}
              />
              <SettingsButton
                disabled={!addId || add.isPending}
                onClick={() => add.mutate()}
              >
                Add
              </SettingsButton>
            </SettingsRow>
          )}
          {(add.isError || changeRole.isError || removeMember.isError) && (
            <SettingsMessage tone="error">
              {authErrorMessage(
                add.error || changeRole.error || removeMember.error,
                "Could not update team members."
              )}
            </SettingsMessage>
          )}
        </SettingsGroup>

        {canManage && (
          <SettingsGroup label="Danger zone">
            <SettingsRow
              tone="danger"
              title="Delete team"
              description="Deleting a team also removes teams nested under it."
            >
              {!confirmDelete && (
                <SettingsButton onClick={() => setConfirmDelete(true)}>
                  Delete team
                </SettingsButton>
              )}
            </SettingsRow>
            {confirmDelete && (
              <SettingsActions
                start={`Delete ${selected?.name}? This cannot be undone.`}
              >
                <SettingsButton
                  size="default"
                  onClick={() => setConfirmDelete(false)}
                >
                  Cancel
                </SettingsButton>
                <SettingsButton
                  size="default"
                  tone="danger"
                  disabled={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  {remove.isPending ? "Deleting…" : "Delete team"}
                </SettingsButton>
              </SettingsActions>
            )}
            {remove.isError && (
              <SettingsMessage tone="error">
                {authErrorMessage(remove.error, "Could not delete the team.")}
              </SettingsMessage>
            )}
          </SettingsGroup>
        )}
      </>
    )
  }

  // Who is in each team, and who leads it, from the directory.
  const people = directory.data?.members ?? []
  function inTeam(team: Team) {
    return people.filter((person) =>
      (person.teams ?? []).some((ref) => ref.id === team.id)
    )
  }
  function leads(team: Team) {
    return people.filter((person) =>
      (person.teams ?? []).some(
        (ref) => ref.id === team.id && ref.role === "admin" && !ref.member_path
      )
    )
  }
  const spacesOf = (team: Team) =>
    (teamspaces.data ?? []).filter((space) => space.team_id === team.id)
  const known = directory.isSuccess

  return (
    <>
      <SettingsGroup
        label={`Teams${teams.data ? ` · ${teams.data.length}` : ""}`}
        bare
      >
        <SettingsCard>
          {teams.isPending ? (
            <SettingsNote>Loading teams…</SettingsNote>
          ) : teams.isError ? (
            <SettingsMessage tone="error">
              {teams.error instanceof ApiError && teams.error.status === 404
                ? "Teams are not available in this environment."
                : authErrorMessage(teams.error, "Could not load teams.")}{" "}
              <SettingsButton onClick={() => void teams.refetch()}>
                Retry
              </SettingsButton>
            </SettingsMessage>
          ) : teams.data.length === 0 ? (
            <SettingsNote>No teams yet.</SettingsNote>
          ) : (
            <Table className="settings-table">
              <TableHeader>
                <TableRow>
                  <TableHead>Team</TableHead>
                  <TableHead>Lead</TableHead>
                  <TableHead>Members</TableHead>
                  <TableHead>Teamspace</TableHead>
                  <TableHead>
                    <span className="sr-only">Open</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {teams.data.map((team) => {
                  const members = inTeam(team)
                  const lead = leads(team).at(0)
                  const spaces = spacesOf(team)
                  return (
                    <TableRow
                      key={team.id}
                      className="settings-tr-link"
                      onClick={() => openTeam(team)}
                    >
                      <TableCell>
                        <span className="settings-person">
                          <span>
                            <strong>{team.name}</strong>
                            <small>
                              {known
                                ? `${members.length} ${members.length === 1 ? "person" : "people"}`
                                : team.role
                                  ? `You are ${team.role === "admin" ? "a lead" : "a member"}`
                                  : "Not a member"}
                            </small>
                          </span>
                        </span>
                      </TableCell>
                      <TableCell>
                        {lead ? (
                          <span className="settings-agent">
                            <PersonAvatar person={lead} />
                            {personName(lead).split(/\s+/)[0]}
                          </span>
                        ) : (
                          <span className="settings-value">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {members.length > 0 ? (
                          <span
                            className="settings-faces"
                            aria-label={members.map(personName).join(", ")}
                          >
                            {members.slice(0, 4).map((person) => (
                              <PersonAvatar
                                key={person.user_id}
                                person={person}
                              />
                            ))}
                            {members.length > 4 && (
                              <small>+{members.length - 4}</small>
                            )}
                          </span>
                        ) : (
                          <span className="settings-value">—</span>
                        )}
                      </TableCell>
                      <TableCell>
                        {spaces.length > 0 ? (
                          <span className="settings-badges">
                            {spaces.slice(0, 2).map((space) => (
                              <Badge
                                key={space.id}
                                variant="outline"
                                className="settings-chip"
                              >
                                {space.name}
                              </Badge>
                            ))}
                            {spaces.length > 2 && (
                              <small>+{spaces.length - 2}</small>
                            )}
                          </span>
                        ) : (
                          <span className="settings-value">—</span>
                        )}
                      </TableCell>
                      <TableCell className="settings-td-action">
                        <Button
                          variant="ghost"
                          size="icon-sm"
                          aria-label={`Open ${team.name}`}
                          onClick={(event) => {
                            event.stopPropagation()
                            openTeam(team)
                          }}
                        >
                          <ChevronRight />
                        </Button>
                      </TableCell>
                    </TableRow>
                  )
                })}
              </TableBody>
            </Table>
          )}
        </SettingsCard>
      </SettingsGroup>

      {canCreate && (
        <div className="settings-page-actions">
          <SettingsButton
            size="default"
            tone="primary"
            onClick={() => {
              create.reset()
              setCreating(true)
            }}
          >
            New team
          </SettingsButton>
        </div>
      )}

      <Dialog open={creating} onOpenChange={setCreating}>
        <DialogContent
          className="settings-popup settings-form-dialog"
          showCloseButton={false}
        >
          <DialogTitle>New team</DialogTitle>
          <DialogDescription>
            Teams group people. A team can own teamspaces and get parley emails.
          </DialogDescription>
          <form className="settings-form-dialog-body" onSubmit={submitNew}>
            <label className="settings-field">
              <span>Name</span>
              <Input
                className="settings-input"
                value={newName}
                maxLength={120}
                placeholder="Team name"
                autoFocus
                onChange={(event) => setNewName(event.target.value)}
              />
            </label>
            <div className="settings-field">
              <span>Inside</span>
              <SettingsSelect
                label="Inside"
                value={parentId}
                options={[
                  { value: "", label: "The organisation" },
                  ...(teams.data ?? []).map((team) => ({
                    value: team.id,
                    label: team.name,
                  })),
                ]}
                onValueChange={setParentId}
              />
            </div>
            {create.isError && (
              <SettingsMessage tone="error">
                {authErrorMessage(create.error, "Could not create the team.")}
              </SettingsMessage>
            )}
            <div className="settings-dialog-footer">
              <SettingsButton size="default" onClick={() => setCreating(false)}>
                Cancel
              </SettingsButton>
              <SettingsButton
                type="submit"
                size="default"
                tone="primary"
                disabled={!newName.trim() || create.isPending}
              >
                {create.isPending ? "Creating…" : "Create"}
              </SettingsButton>
            </div>
          </form>
        </DialogContent>
      </Dialog>
    </>
  )
}
