import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Plus, UsersRound } from "lucide-react"
import { ApiError } from "@/lib/api/client"
import type { Organization, Role } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { getMembers } from "@/lib/auth/invitations"
import {
  addTeamMember,
  changeTeamMemberRole,
  createTeam,
  deleteTeam,
  getTeam,
  listTeamMembers,
  listTeams,
  removeTeamMember,
  renameTeam,
} from "@/lib/settings/client"

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
  const [newName, setNewName] = useState("")
  const [parentId, setParentId] = useState("")
  const [editingName, setEditingName] = useState("")
  const [addId, setAddId] = useState("")
  const [confirmDelete, setConfirmDelete] = useState(false)
  const [confirmRemoveMemberId, setConfirmRemoveMemberId] = useState<
    string | null
  >(null)
  const teams = useQuery({
    queryKey: ["settings", "teams", orgId],
    queryFn: () => listTeams(orgId!),
    enabled: !preview && Boolean(orgId),
    retry: false,
  })
  const selected = teams.data?.find((team) => team.id === selectedId)
  const detail = useQuery({
    queryKey: ["settings", "teams", orgId, selectedId, "detail"],
    queryFn: () => getTeam(orgId!, selectedId!),
    enabled: !preview && Boolean(orgId && selectedId),
    retry: false,
  })
  const teamMembers = useQuery({
    queryKey: ["settings", "teams", orgId, selectedId, "members"],
    queryFn: () => listTeamMembers(orgId!, selectedId!),
    enabled: !preview && Boolean(orgId && selectedId),
    retry: false,
  })
  const orgMembers = useQuery({
    queryKey: ["organizations", orgId, "members"],
    queryFn: () => getMembers(orgId!),
    enabled:
      !preview && Boolean(orgId && selectedId && detail.data?.can_manage),
    retry: false,
  })
  const refreshTeams = () =>
    queryClient.invalidateQueries({ queryKey: ["settings", "teams", orgId] })
  const refreshMembers = () =>
    queryClient.invalidateQueries({
      queryKey: ["settings", "teams", orgId, selectedId, "members"],
    })
  const create = useMutation({
    mutationFn: (name: string) =>
      createTeam(orgId!, name, parentId || undefined),
    onSuccess: async (team) => {
      setNewName("")
      setParentId("")
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
    onSuccess: refreshMembers,
  })
  const removeMember = useMutation({
    mutationFn: (id: string) => removeTeamMember(orgId!, selectedId!, id),
    onSuccess: async () => {
      setConfirmRemoveMemberId(null)
      await refreshMembers()
    },
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

  if (preview || !organization)
    return (
      <div className="workspace-settings-account">
        <div className="workspace-settings-section">
          <div className="workspace-settings-section-title">
            <h3>Organisation teams</h3>
            <p>Layout preview · Sign in to load and manage real teams.</p>
          </div>
          {["Product", "Design"].map((name) => (
            <div className="workspace-settings-team-row" key={name}>
              <UsersRound size={17} />
              <span>
                <strong>{name}</strong>
                <small>Example team</small>
              </span>
            </div>
          ))}
        </div>
      </div>
    )

  return (
    <div className="workspace-settings-account">
      {selectedId ? (
        <>
          <button
            className="workspace-settings-back-inline"
            type="button"
            onClick={() => openTeam()}
          >
            <ArrowLeft size={15} /> All teams
          </button>
          <section
            className="workspace-settings-section"
            aria-label="Team details"
          >
            <div className="workspace-settings-section-title">
              <h3>{selected?.name || "Team"}</h3>
              <p>
                {detail.data?.can_manage
                  ? "Manage this team and its members."
                  : "Your place in this team."}
              </p>
            </div>
            {detail.isPending && <p role="status">Loading team…</p>}
            {detail.isError && (
              <p className="workspace-settings-error" role="alert">
                {authErrorMessage(detail.error, "Could not load the team.")}
              </p>
            )}
            {detail.data?.can_manage && (
              <>
                <form
                  className="workspace-settings-inline-form"
                  onSubmit={(event) => {
                    event.preventDefault()
                    if (editingName.trim()) rename.mutate()
                  }}
                >
                  <label>
                    Team name{" "}
                    <input
                      value={editingName}
                      maxLength={120}
                      onChange={(event) => setEditingName(event.target.value)}
                    />
                  </label>
                  <button
                    type="submit"
                    disabled={
                      !editingName.trim() ||
                      editingName.trim() === selected?.name ||
                      rename.isPending
                    }
                  >
                    Save name
                  </button>
                </form>
                {rename.isError && (
                  <p className="workspace-settings-error" role="alert">
                    {authErrorMessage(
                      rename.error,
                      "Could not rename the team."
                    )}
                  </p>
                )}
              </>
            )}
          </section>
          <section
            className="workspace-settings-section"
            aria-label="Team members"
          >
            <div className="workspace-settings-section-title">
              <h3>Members</h3>
              <p>People who can work with this team.</p>
            </div>
            {teamMembers.isPending && <p role="status">Loading members…</p>}
            {teamMembers.isError && (
              <p className="workspace-settings-error" role="alert">
                {authErrorMessage(teamMembers.error, "Could not load members.")}
              </p>
            )}
            {teamMembers.data?.map((member) => (
              <div key={member.user_id}>
                <div className="workspace-settings-row">
                  <div>
                    <strong>{member.display_name || member.email}</strong>
                    <small>{member.email}</small>
                  </div>
                  {detail.data?.can_manage ? (
                    <div className="workspace-settings-member-actions">
                      <select
                        aria-label={`Role for ${member.display_name || member.email}`}
                        value={member.role}
                        disabled={changeRole.isPending}
                        onChange={(event) =>
                          changeRole.mutate({
                            id: member.user_id,
                            nextRole: event.target.value as "admin" | "member",
                          })
                        }
                      >
                        <option value="member">Member</option>
                        <option value="admin">Admin</option>
                      </select>
                      <button
                        type="button"
                        disabled={removeMember.isPending}
                        onClick={() => setConfirmRemoveMemberId(member.user_id)}
                      >
                        Remove
                      </button>
                    </div>
                  ) : (
                    <span className="workspace-settings-tag">
                      {member.role}
                    </span>
                  )}
                </div>
                {confirmRemoveMemberId === member.user_id && (
                  <div className="workspace-settings-confirm">
                    <p>
                      Remove {member.display_name || member.email} from this
                      team?
                    </p>
                    <button
                      type="button"
                      onClick={() => setConfirmRemoveMemberId(null)}
                    >
                      Cancel
                    </button>
                    <button
                      type="button"
                      disabled={removeMember.isPending}
                      onClick={() => removeMember.mutate(member.user_id)}
                    >
                      {removeMember.isPending ? "Removing…" : "Remove member"}
                    </button>
                  </div>
                )}
              </div>
            ))}
            {detail.data?.can_manage && (
              <div className="workspace-settings-inline-form">
                <label>
                  Add a member{" "}
                  <select
                    aria-label="Add a member"
                    value={addId}
                    onChange={(event) => setAddId(event.target.value)}
                  >
                    <option value="">Select a person</option>
                    {orgMembers.data
                      ?.filter(
                        (member) =>
                          !teamMembers.data?.some(
                            (present) => present.user_id === member.user_id
                          )
                      )
                      .map((member) => (
                        <option value={member.user_id} key={member.user_id}>
                          {member.display_name || member.email}
                        </option>
                      ))}
                  </select>
                </label>
                <button
                  type="button"
                  disabled={!addId || add.isPending}
                  onClick={() => add.mutate()}
                >
                  Add
                </button>
              </div>
            )}
            {(add.isError || changeRole.isError || removeMember.isError) && (
              <p className="workspace-settings-error" role="alert">
                {authErrorMessage(
                  add.error || changeRole.error || removeMember.error,
                  "Could not update team members."
                )}
              </p>
            )}
          </section>
          {detail.data?.can_manage && (
            <section
              className="workspace-settings-section"
              aria-label="Delete team"
            >
              <div className="workspace-settings-section-title">
                <h3>Delete team</h3>
                <p>Deleting a team also removes teams nested under it.</p>
              </div>
              {confirmDelete ? (
                <div className="workspace-settings-confirm">
                  <p>Delete {selected?.name}? This cannot be undone.</p>
                  <button type="button" onClick={() => setConfirmDelete(false)}>
                    Cancel
                  </button>
                  <button
                    type="button"
                    disabled={remove.isPending}
                    onClick={() => remove.mutate()}
                  >
                    {remove.isPending ? "Deleting…" : "Delete team"}
                  </button>
                </div>
              ) : (
                <button
                  className="workspace-settings-action"
                  type="button"
                  onClick={() => setConfirmDelete(true)}
                >
                  Delete team
                </button>
              )}
              {remove.isError && (
                <p className="workspace-settings-error" role="alert">
                  {authErrorMessage(remove.error, "Could not delete the team.")}
                </p>
              )}
            </section>
          )}
        </>
      ) : (
        <section
          className="workspace-settings-section"
          aria-label="Organisation teams"
        >
          <div className="workspace-settings-section-title">
            <h3>Organisation teams</h3>
            <p>Teams bring people together for shared work.</p>
          </div>
          {canCreate && (
            <form
              className="workspace-settings-inline-form"
              onSubmit={submitNew}
            >
              <label>
                New team{" "}
                <input
                  value={newName}
                  maxLength={120}
                  onChange={(event) => setNewName(event.target.value)}
                  placeholder="Team name"
                />
              </label>
              <label>
                Inside
                <select
                  value={parentId}
                  onChange={(event) => setParentId(event.target.value)}
                >
                  <option value="">Organisation</option>
                  {teams.data?.map((team) => (
                    <option value={team.id} key={team.id}>
                      {team.name}
                    </option>
                  ))}
                </select>
              </label>
              <button
                type="submit"
                disabled={!newName.trim() || create.isPending}
              >
                <Plus size={14} /> Create
              </button>
            </form>
          )}
          {create.isError && (
            <p className="workspace-settings-error" role="alert">
              {authErrorMessage(create.error, "Could not create the team.")}
            </p>
          )}
          {teams.isPending && <p role="status">Loading teams…</p>}
          {teams.isError && (
            <p className="workspace-settings-error" role="alert">
              {teams.error instanceof ApiError && teams.error.status === 404
                ? "Teams are not available in this environment."
                : authErrorMessage(teams.error, "Could not load teams.")}{" "}
              <button type="button" onClick={() => void teams.refetch()}>
                Retry
              </button>
            </p>
          )}
          {teams.data?.length === 0 && (
            <p className="workspace-settings-hint">No teams yet.</p>
          )}
          {teams.data?.map((team) => (
            <button
              className="workspace-settings-team-row"
              type="button"
              key={team.id}
              onClick={() => openTeam(team)}
            >
              <UsersRound size={17} />
              <span>
                <strong>{team.name}</strong>
                <small>
                  {team.role ? `You are a ${team.role}` : "Not a member"}
                </small>
              </span>
              <span aria-hidden="true">→</span>
            </button>
          ))}
        </section>
      )}
    </div>
  )
}
