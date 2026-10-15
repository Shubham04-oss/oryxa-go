import { useMemo, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Search } from "lucide-react"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { IdentityAvatar } from "./workspace-avatar"
import { personIdentity } from "./teamspace-format"
import { authErrorMessage } from "@/lib/auth/client"
import { getMembers } from "@/lib/auth/invitations"
import { addTeamMember, listTeamMembers } from "@/lib/settings/client"
import type { Teamspace } from "@/lib/teamspaces/client"
import { teamspaceKeys } from "@/lib/teamspaces/queries"

/**
 * Invite, from a teamspace's hero: adds someone from the workspace to the
 * teamspace's team, which is what opens the teamspace to them. Only for
 * whoever manages the team (the caller hides it otherwise; Go refuses too).
 * Inviting someone new to the workspace stays in Settings.
 */
export function TeamspaceInviteDialog({
  organizationId,
  space,
  onClose,
}: {
  organizationId: string
  space: Teamspace
  onClose: () => void
}) {
  const queryClient = useQueryClient()
  const [search, setSearch] = useState("")
  const [added, setAdded] = useState<string[]>([])
  // The same keys Settings → Teams reads, so either page sees the other's add.
  const teamMembersKey = [
    "settings",
    "teams",
    organizationId,
    space.team_id,
    "members",
  ]
  const members = useQuery({
    queryKey: ["organizations", organizationId, "members"],
    queryFn: () => getMembers(organizationId),
    retry: false,
  })
  const onTeam = useQuery({
    queryKey: teamMembersKey,
    queryFn: () => listTeamMembers(organizationId, space.team_id),
    retry: false,
  })
  const add = useMutation({
    mutationFn: (userId: string) =>
      addTeamMember(organizationId, space.team_id, userId, "member"),
    onSuccess: async (_, userId) => {
      setAdded((current) => [...current, userId])
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: teamMembersKey }),
        // The new teammate's agent takes a seat once the space is read again.
        queryClient.invalidateQueries({
          queryKey: teamspaceKeys.teamspace(organizationId, space.id),
          exact: true,
        }),
        queryClient.invalidateQueries({
          queryKey: teamspaceKeys.list(organizationId),
        }),
      ])
    },
  })

  const candidates = useMemo(() => {
    const present = new Set(onTeam.data?.map((member) => member.user_id))
    const needle = search.trim().toLowerCase()
    return (members.data ?? [])
      .filter(
        (member) =>
          !present.has(member.user_id) || added.includes(member.user_id)
      )
      .filter(
        (member) =>
          !needle ||
          (member.display_name ?? "").toLowerCase().includes(needle) ||
          member.email.toLowerCase().includes(needle)
      )
  }, [members.data, onTeam.data, search, added])

  const loading = members.isPending || onTeam.isPending
  const failed = members.isError || onTeam.isError

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent className="workspace-teamspace-sheet workspace-teamspace-invite">
        <div className="workspace-teamspace-sheet-head">
          <DialogTitle>Invite to {space.name}</DialogTitle>
          <DialogDescription>
            Adding someone to the {space.team_name} team opens this teamspace to
            them. To bring in someone new to the workspace, invite them from
            Settings first.
          </DialogDescription>
        </div>
        <label className="workspace-teamspace-invite-search">
          <Search size={15} strokeWidth={1.7} aria-hidden="true" />
          <span className="sr-only">Search people</span>
          <Input
            value={search}
            placeholder="Search people"
            onChange={(event) => setSearch(event.target.value)}
          />
        </label>
        {loading ? (
          <p role="status">Loading people…</p>
        ) : failed ? (
          <p role="alert">
            {authErrorMessage(
              members.error || onTeam.error,
              "Couldn't load the workspace's people."
            )}
          </p>
        ) : candidates.length === 0 ? (
          <p>
            {search.trim()
              ? "No one matches that search."
              : `Everyone in the workspace is already on the ${space.team_name} team.`}
          </p>
        ) : (
          <ul
            className="workspace-teamspace-invite-list"
            aria-label="People to add"
          >
            {candidates.map((member) => {
              const name = member.display_name || member.email
              const done = added.includes(member.user_id)
              const adding = add.isPending && add.variables === member.user_id
              return (
                <li key={member.user_id}>
                  <IdentityAvatar
                    identity={personIdentity(member.user_id, name)}
                  />
                  <span>
                    <strong>{name}</strong>
                    {member.display_name && <small>{member.email}</small>}
                  </span>
                  <button
                    type="button"
                    className="workspace-teamspace-pill"
                    disabled={done || add.isPending}
                    aria-label={done ? `${name} added` : `Add ${name}`}
                    onClick={() => add.mutate(member.user_id)}
                  >
                    {done ? "Added" : adding ? "Adding…" : "Add"}
                  </button>
                </li>
              )
            })}
          </ul>
        )}
        {add.isError && (
          <p className="workspace-teamspace-error" role="alert">
            {authErrorMessage(add.error, "Couldn't add them to the team.")}
          </p>
        )}
      </DialogContent>
    </Dialog>
  )
}
