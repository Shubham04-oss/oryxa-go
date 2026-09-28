import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronDown, Mail, UserRoundPlus, UsersRound, X } from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuRadioGroup,
  DropdownMenuRadioItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { ApiError } from "@/lib/api/client"
import type { Role } from "@/lib/auth/client"
import {
  acceptReceivedInvitation,
  changeMemberRole,
  getInvitations,
  getMembers,
  getReceivedInvitations,
  removeOrganizationMember,
  revokeInvitation,
  sendInvitation,
} from "@/lib/auth/invitations"
import type { Invitation, Member } from "@/lib/auth/invitations"
import { invitationSchema } from "@/lib/auth/schemas"
import { IdentityAvatar } from "./workspace-avatar"

function initials(name: string) {
  return name
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join("")
}

function MemberRow({
  member,
  organizationId,
  viewerRole,
  viewerId,
  preview,
}: {
  member: Member
  organizationId: string
  viewerRole: Role
  viewerId?: string
  preview: boolean
}) {
  const name = member.display_name || member.email
  const queryClient = useQueryClient()
  const [confirmRemove, setConfirmRemove] = useState(false)
  const refresh = () =>
    queryClient.invalidateQueries({
      queryKey: ["organizations", organizationId, "members"],
    })
  const changeRole = useMutation({
    mutationFn: (role: Role) =>
      changeMemberRole(organizationId, member.user_id, role),
    onSuccess: async () => {
      await refresh()
      if (member.user_id === viewerId) {
        await queryClient.invalidateQueries({
          queryKey: ["auth", "role", organizationId],
        })
      }
    },
  })
  const remove = useMutation({
    mutationFn: () => removeOrganizationMember(organizationId, member.user_id),
    onSuccess: refresh,
  })
  const canRemove =
    !preview &&
    member.user_id !== viewerId &&
    (viewerRole === "owner" ||
      (viewerRole === "admin" && member.role === "member"))
  return (
    <li className="workspace-person-row">
      <IdentityAvatar
        identity={{
          id: member.user_id,
          name,
          initials: initials(name),
          kind: "user",
        }}
      />
      <span className="workspace-person-main">
        <strong>{name}</strong>
        {member.display_name && <small>{member.email}</small>}
      </span>
      {/* Unlike Remove (below), the old app lets an owner change their own
          role from this same picker, so this stays open on the viewer's
          own row too. */}
      {viewerRole === "owner" && !preview ? (
        <DropdownMenu>
          <DropdownMenuTrigger
            className="workspace-person-role workspace-person-role-trigger"
            aria-label={`Role for ${name}`}
            disabled={changeRole.isPending}
          >
            {member.role}
            <ChevronDown size={11} aria-hidden="true" />
          </DropdownMenuTrigger>
          <DropdownMenuContent>
            {/* A native select fires `change` on every arrow-key press while
                closed on some platforms, which used to send a request per
                key. A menu only commits on an explicit click or activation,
                so browsing the roles with the keyboard is free. */}
            <DropdownMenuRadioGroup
              value={member.role}
              onValueChange={(value) => {
                if (value !== member.role) changeRole.mutate(value as Role)
              }}
            >
              <DropdownMenuRadioItem value="owner" closeOnClick>
                Owner
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="admin" closeOnClick>
                Admin
              </DropdownMenuRadioItem>
              <DropdownMenuRadioItem value="member" closeOnClick>
                Member
              </DropdownMenuRadioItem>
            </DropdownMenuRadioGroup>
          </DropdownMenuContent>
        </DropdownMenu>
      ) : (
        <span className="workspace-person-role">{member.role}</span>
      )}
      {canRemove && (
        <button
          className="workspace-invite-revoke"
          type="button"
          aria-label={`Remove ${name}`}
          onClick={() => setConfirmRemove(true)}
        >
          <X size={15} />
        </button>
      )}
      {confirmRemove && (
        <div className="workspace-person-confirm">
          <p>
            Remove {name} from this organisation? They lose access to its teams
            and connections.
          </p>
          <button type="button" onClick={() => setConfirmRemove(false)}>
            Cancel
          </button>
          <button
            type="button"
            disabled={remove.isPending}
            onClick={() => remove.mutate()}
          >
            {remove.isPending ? "Removing…" : "Remove"}
          </button>
        </div>
      )}
      {(changeRole.isError || remove.isError) && (
        <p className="workspace-people-error" role="alert">
          {changeRole.error instanceof ApiError &&
          changeRole.error.status === 409
            ? "An organisation needs an owner. Make someone else an owner first."
            : "Could not update this member."}
        </p>
      )}
    </li>
  )
}

function dateLabel(value: string) {
  const date = new Date(value)
  return Number.isNaN(date.valueOf())
    ? ""
    : new Intl.DateTimeFormat(undefined, {
        day: "numeric",
        month: "short",
        year: "numeric",
      }).format(date)
}

const previewMembers: Member[] = [
  {
    organization_id: "preview-organization",
    user_id: "avery",
    email: "avery@example.com",
    display_name: "Avery Morgan",
    role: "owner",
    joined_at: "2026-09-01T00:00:00Z",
  },
  {
    organization_id: "preview-organization",
    user_id: "jordan",
    email: "jordan@example.com",
    display_name: "Jordan Lee",
    role: "admin",
    joined_at: "2026-09-03T00:00:00Z",
  },
  {
    organization_id: "preview-organization",
    user_id: "sam",
    email: "sam@example.com",
    display_name: "Sam Kim",
    role: "member",
    joined_at: "2026-09-05T00:00:00Z",
  },
]

const previewInvitations: Invitation[] = [
  {
    id: "preview-invitation",
    organization_id: "preview-organization",
    email: "priya@example.com",
    role: "member",
    status: "pending",
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString(),
  },
]

export function PeopleManagement({
  organizationId,
  role,
  viewerId,
  preview = false,
}: {
  organizationId?: string
  role: Role
  viewerId?: string
  preview?: boolean
}) {
  const queryClient = useQueryClient()
  const canManage = role === "owner" || role === "admin"
  const [email, setEmail] = useState("")
  const [inviteRole, setInviteRole] = useState<"admin" | "member">("member")
  const [formError, setFormError] = useState("")
  const [notice, setNotice] = useState("")

  const members = useQuery({
    queryKey: ["organizations", organizationId, "members"],
    queryFn: () => getMembers(organizationId!),
    enabled: Boolean(organizationId) && !preview,
    initialData: preview ? previewMembers : undefined,
    retry: false,
  })
  const invitations = useQuery({
    queryKey: ["organizations", organizationId, "invitations"],
    queryFn: () => getInvitations(organizationId!),
    enabled: Boolean(organizationId) && canManage && !preview,
    initialData: preview ? previewInvitations : undefined,
    retry: false,
  })
  const received = useQuery({
    queryKey: ["auth", "received-invitations"],
    queryFn: getReceivedInvitations,
    enabled: Boolean(organizationId) && !preview,
    retry: false,
  })
  const pendingInvitations =
    invitations.data?.filter(
      (invitation) =>
        invitation.status === "pending" &&
        new Date(invitation.expires_at).getTime() > Date.now()
    ) || []

  const send = useMutation({
    mutationFn: ({
      address,
      nextRole,
    }: {
      address: string
      nextRole: "admin" | "member"
    }) => sendInvitation(organizationId!, address, nextRole),
    onSuccess: async (invitation) => {
      setEmail("")
      setFormError("")
      setNotice(`Invitation created for ${invitation.email}.`)
      await queryClient.invalidateQueries({
        queryKey: ["organizations", organizationId, "invitations"],
      })
    },
    onError: (error) => {
      setNotice("")
      setFormError(
        error instanceof ApiError && error.status < 500
          ? error.message
          : "Could not create the invitation. Try again."
      )
    },
  })
  const revoke = useMutation({
    mutationFn: (invitationId: string) =>
      revokeInvitation(organizationId!, invitationId),
    onSuccess: async () => {
      setNotice("Invitation revoked.")
      await queryClient.invalidateQueries({
        queryKey: ["organizations", organizationId, "invitations"],
      })
    },
    onError: () => setFormError("Could not revoke the invitation. Try again."),
  })
  const join = useMutation({
    mutationFn: acceptReceivedInvitation,
    onSuccess: async (membership) => {
      await queryClient.invalidateQueries({ queryKey: ["auth", "session"] })
      window.location.assign(
        `/?org=${encodeURIComponent(membership.organization_id)}`
      )
    },
  })

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (preview) return
    setNotice("")
    const parsed = invitationSchema.safeParse({ email, role: inviteRole })
    if (!parsed.success) {
      setFormError(parsed.error.issues[0]?.message || "Check the invitation.")
      return
    }
    if (!organizationId || !canManage) return
    setFormError("")
    send.mutate({ address: parsed.data.email, nextRole: parsed.data.role })
  }

  return (
    <div className="workspace-people-page">
      {!organizationId ? (
        <section className="workspace-people-card">
          <h3>Connect a workspace to manage its people</h3>
          <p>Members and invitations will appear here.</p>
        </section>
      ) : (
        <>
          {Boolean(received.data?.length) && (
            <section
              className="workspace-people-card workspace-people-received"
              aria-label="Your invitations"
            >
              <p className="workspace-eyebrow">WAITING FOR YOU</p>
              <h3>Another team has made room.</h3>
              <ul className="workspace-person-list">
                {received.data?.map((invitation) => (
                  <li className="workspace-person-row" key={invitation.id}>
                    <span className="workspace-pending-avatar">
                      <Mail size={16} />
                    </span>
                    <span className="workspace-person-main">
                      <strong>{invitation.organization_name}</strong>
                      <small>Join as {invitation.role}</small>
                    </span>
                    <button
                      className="workspace-invite-join"
                      type="button"
                      disabled={join.isPending}
                      onClick={() => join.mutate(invitation.id)}
                    >
                      {join.isPending && join.variables === invitation.id
                        ? "Joining…"
                        : "Join"}
                    </button>
                  </li>
                ))}
              </ul>
              {join.isError && (
                <p className="workspace-people-error" role="alert">
                  {join.error instanceof ApiError && join.error.status === 402
                    ? "That workspace has reached its member limit. Ask an owner to raise it."
                    : "Could not join that workspace. Ask for a fresh invitation."}
                </p>
              )}
            </section>
          )}
          {canManage && (
            <section
              className="workspace-people-card"
              aria-label="Invite a teammate"
            >
              <div className="workspace-people-card-head">
                <span className="workspace-people-icon">
                  <UserRoundPlus size={19} />
                </span>
                <div>
                  <p className="workspace-eyebrow">INVITE</p>
                  <h3>Make room for someone new.</h3>
                </div>
              </div>
              <form
                className="workspace-invite-form"
                onSubmit={submit}
                noValidate
              >
                <label>
                  <span>Email address</span>
                  <input
                    type="email"
                    autoComplete="email"
                    placeholder="teammate@company.com"
                    value={email}
                    onChange={(event) => setEmail(event.target.value)}
                  />
                </label>
                <label>
                  <span>Role</span>
                  <select
                    value={inviteRole}
                    onChange={(event) =>
                      setInviteRole(event.target.value as "admin" | "member")
                    }
                  >
                    <option value="member">Member</option>
                    {role === "owner" && <option value="admin">Admin</option>}
                  </select>
                </label>
                <button type="submit" disabled={preview || send.isPending}>
                  <Mail size={16} />{" "}
                  {send.isPending ? "Sending…" : "Send invite"}
                </button>
              </form>
              {formError && (
                <p className="workspace-people-error" role="alert">
                  {formError}
                </p>
              )}
              {notice && (
                <p className="workspace-people-notice" role="status">
                  {notice}
                </p>
              )}
              <p className="workspace-people-footnote">
                {preview
                  ? "Preview data only. Sign in to manage real invitations."
                  : "Invitations expire after seven days. Only the invited email can accept."}
              </p>
            </section>
          )}

          <div className="workspace-people-columns">
            <section
              className="workspace-people-card"
              aria-label="Workspace members"
            >
              <div className="workspace-people-list-head">
                <div>
                  <p className="workspace-eyebrow">02 / PEOPLE</p>
                  <h3>
                    Members <span>{members.data?.length ?? "—"}</span>
                  </h3>
                </div>
                <UsersRound size={19} strokeWidth={1.7} />
              </div>
              {members.isPending ? (
                <p className="workspace-people-empty">Loading members…</p>
              ) : members.error ? (
                <p className="workspace-people-error" role="alert">
                  Could not load members.
                </p>
              ) : members.data.length ? (
                <ul className="workspace-person-list">
                  {members.data.map((member) => (
                    <MemberRow
                      key={member.user_id}
                      member={member}
                      organizationId={organizationId}
                      viewerRole={role}
                      viewerId={viewerId}
                      preview={preview}
                    />
                  ))}
                </ul>
              ) : (
                <p className="workspace-people-empty">No members yet.</p>
              )}
            </section>

            <section
              className="workspace-people-card"
              aria-label="Pending invitations"
            >
              <div className="workspace-people-list-head">
                <div>
                  <p className="workspace-eyebrow">03 / INVITATIONS</p>
                  <h3>
                    On their way{" "}
                    <span>{canManage ? pendingInvitations.length : "—"}</span>
                  </h3>
                </div>
                <Mail size={19} strokeWidth={1.7} />
              </div>
              {!canManage ? (
                <p className="workspace-people-empty">
                  Workspace admins manage invitations.
                </p>
              ) : invitations.isPending ? (
                <p className="workspace-people-empty">Loading invitations…</p>
              ) : invitations.error ? (
                <p className="workspace-people-error" role="alert">
                  Could not load invitations.
                </p>
              ) : pendingInvitations.length ? (
                <ul className="workspace-person-list">
                  {pendingInvitations.map((invitation) => (
                    <li className="workspace-person-row" key={invitation.id}>
                      <span className="workspace-pending-avatar">
                        <Mail size={16} />
                      </span>
                      <span className="workspace-person-main">
                        <strong>{invitation.email}</strong>
                        <small>
                          {invitation.role} · Expires{" "}
                          {dateLabel(invitation.expires_at)}
                        </small>
                      </span>
                      {(role === "owner" || invitation.role === "member") && (
                        <button
                          className="workspace-invite-revoke"
                          type="button"
                          aria-label={`Revoke invitation for ${invitation.email}`}
                          title="Revoke invitation"
                          disabled={preview || revoke.isPending}
                          onClick={() => revoke.mutate(invitation.id)}
                        >
                          <X size={16} />
                        </button>
                      )}
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="workspace-people-empty">
                  No pending invitations.
                </p>
              )}
            </section>
          </div>
        </>
      )}
    </div>
  )
}
