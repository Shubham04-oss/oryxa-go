import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ChevronDown, Mail, X } from "lucide-react"
import { Button } from "@/components/ui/button"
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
import { getBillingSummary } from "@/lib/settings/client"
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

const roleNames: Record<Role, string> = {
  owner: "Owner",
  admin: "Admin",
  member: "Member",
}

function initials(name: string) {
  return name
    .split(/\s+/)
    .slice(0, 2)
    .map((part) => part.charAt(0).toUpperCase())
    .join("")
}

/** "a@x.io, b@x.io" or one per line → each address once. */
function parseAddresses(value: string): string[] {
  return [
    ...new Set(
      value
        .split(/[\s,;]+/)
        .map((item) => item.trim())
        .filter(Boolean)
    ),
  ]
}

function expiresIn(value: string): string {
  const days = Math.ceil((new Date(value).getTime() - Date.now()) / 86_400_000)
  if (Number.isNaN(days)) return ""
  return days <= 1 ? "expires today" : `expires in ${days} days`
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
    <>
      <TableRow className="settings-tr">
        <TableCell>
          <span className="settings-person">
            <IdentityAvatar
              identity={{
                id: member.user_id,
                name,
                initials: initials(name),
                kind: "user",
              }}
            />
            <span>
              <strong>{name}</strong>
              {member.display_name && <small>{member.email}</small>}
            </span>
          </span>
        </TableCell>
        <TableCell>
          {/* Unlike Remove, an owner may change their own role here too, as
              the old app allowed. */}
          {viewerRole === "owner" && !preview ? (
            <DropdownMenu>
              <DropdownMenuTrigger
                className="settings-role-trigger"
                aria-label={`Role for ${name}`}
                disabled={changeRole.isPending}
              >
                {roleNames[member.role]}
                <ChevronDown size={14} aria-hidden="true" />
              </DropdownMenuTrigger>
              <DropdownMenuContent className="settings-popup">
                {/* A menu only commits on an explicit click or activation,
                    so browsing the roles with the keyboard sends nothing. */}
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
            <span className="settings-value">{roleNames[member.role]}</span>
          )}
        </TableCell>
        <TableCell className="settings-td-action">
          {canRemove && !confirmRemove && (
            <Button
              variant="ghost"
              size="icon-sm"
              className="settings-icon-danger"
              aria-label={`Remove ${name}`}
              onClick={() => setConfirmRemove(true)}
            >
              <X />
            </Button>
          )}
        </TableCell>
      </TableRow>
      {(confirmRemove || changeRole.isError || remove.isError) && (
        <TableRow className="settings-tr-detail">
          <TableCell colSpan={3}>
            {confirmRemove && (
              <SettingsActions
                start={`Remove ${name} from this organisation? They lose access to its teams and connections.`}
              >
                <SettingsButton
                  size="default"
                  onClick={() => setConfirmRemove(false)}
                >
                  Cancel
                </SettingsButton>
                <SettingsButton
                  size="default"
                  tone="danger"
                  disabled={remove.isPending}
                  onClick={() => remove.mutate()}
                >
                  {remove.isPending ? "Removing…" : "Remove"}
                </SettingsButton>
              </SettingsActions>
            )}
            {(changeRole.isError || remove.isError) && (
              <SettingsMessage tone="error">
                {changeRole.error instanceof ApiError &&
                changeRole.error.status === 409
                  ? "An organisation needs an owner. Make someone else an owner first."
                  : "Could not update this member."}
              </SettingsMessage>
            )}
          </TableCell>
        </TableRow>
      )}
    </>
  )
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

/**
 * Settings → People: invite by email (several at once, separated by commas),
 * the members as a table with their role, and the invitations on their way.
 *
 * Each member's agent is not listed yet: the members route does not say
 * which agent is theirs. Resending an invitation has no route either.
 */
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
  const [emails, setEmails] = useState("")
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
  // Seats: the plan's member limit, when billing says there is one.
  const billing = useQuery({
    queryKey: ["settings", "billing", organizationId],
    queryFn: () => getBillingSummary(organizationId!),
    enabled: Boolean(organizationId) && !preview,
    retry: false,
  })
  const memberLimit = billing.data?.entitlements.member_limit
  const pendingInvitations =
    invitations.data?.filter(
      (invitation) =>
        invitation.status === "pending" &&
        new Date(invitation.expires_at).getTime() > Date.now()
    ) || []
  const nameOf = (userId?: string) => {
    const member = members.data?.find((item) => item.user_id === userId)
    return member ? (member.display_name || member.email).split(/\s+/)[0] : ""
  }

  const send = useMutation({
    // One invitation per address, in order; what was sent stays sent.
    mutationFn: async ({
      addresses,
      nextRole,
    }: {
      addresses: string[]
      nextRole: "admin" | "member"
    }) => {
      const sent: string[] = []
      for (const address of addresses) {
        try {
          const invitation = await sendInvitation(
            organizationId!,
            address,
            nextRole
          )
          sent.push(invitation.email)
        } catch (error) {
          throw Object.assign(
            error instanceof Error ? error : new Error(String(error)),
            { sent, address }
          )
        }
      }
      return sent
    },
    onSuccess: async (sent) => {
      setEmails("")
      setFormError("")
      setNotice(
        sent.length === 1
          ? `Invitation created for ${sent[0]}.`
          : `Invitations created for ${sent.join(", ")}.`
      )
      await queryClient.invalidateQueries({
        queryKey: ["organizations", organizationId, "invitations"],
      })
    },
    onError: async (error) => {
      const { sent = [], address = "" } = error as Error & {
        sent?: string[]
        address?: string
      }
      setNotice(
        sent.length ? `Invitations created for ${sent.join(", ")}.` : ""
      )
      setEmails((current) =>
        parseAddresses(current)
          .filter((item) => !sent.includes(item))
          .join(", ")
      )
      const reason =
        error instanceof ApiError && error.status < 500
          ? error.message
          : "Could not create the invitation. Try again."
      setFormError(address ? `${address}: ${reason}` : reason)
      await queryClient.invalidateQueries({
        queryKey: ["organizations", organizationId, "invitations"],
      })
    },
  })
  const revoke = useMutation({
    mutationFn: (invitationId: string) =>
      revokeInvitation(organizationId!, invitationId),
    onSuccess: async () => {
      setNotice("Invitation cancelled.")
      await queryClient.invalidateQueries({
        queryKey: ["organizations", organizationId, "invitations"],
      })
    },
    onError: () => setFormError("Could not cancel the invitation. Try again."),
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
    const addresses = parseAddresses(emails)
    if (addresses.length === 0) {
      setFormError("Enter an email address.")
      return
    }
    for (const address of addresses) {
      const parsed = invitationSchema.safeParse({
        email: address,
        role: inviteRole,
      })
      if (!parsed.success) {
        setFormError(
          `${address}: ${parsed.error.issues[0]?.message || "Check the address."}`
        )
        return
      }
    }
    if (!organizationId || !canManage) return
    setFormError("")
    send.mutate({ addresses, nextRole: inviteRole })
  }

  if (!organizationId) {
    return (
      <SettingsNote>
        Connect a workspace to manage its people. Members and invitations will
        appear here.
      </SettingsNote>
    )
  }

  const seats =
    members.data && memberLimit
      ? ` · ${members.data.length} of ${memberLimit} seats`
      : ""

  return (
    <>
      {Boolean(received.data?.length) && (
        <SettingsGroup label="Waiting for you">
          {received.data?.map((invitation) => (
            <SettingsRow
              key={invitation.id}
              media={
                <span className="settings-pending-mark" aria-hidden="true">
                  <Mail size={16} />
                </span>
              }
              title={invitation.organization_name}
              description={`Join as ${roleNames[invitation.role].toLowerCase()}`}
            >
              <SettingsButton
                disabled={join.isPending}
                onClick={() => join.mutate(invitation.id)}
              >
                {join.isPending && join.variables === invitation.id
                  ? "Joining…"
                  : "Join"}
              </SettingsButton>
            </SettingsRow>
          ))}
          {join.isError && (
            <SettingsMessage tone="error">
              {join.error instanceof ApiError && join.error.status === 402
                ? "That workspace has reached its member limit. Ask an owner to raise it."
                : "Could not join that workspace. Ask for a fresh invitation."}
            </SettingsMessage>
          )}
        </SettingsGroup>
      )}

      {canManage && (
        <SettingsGroup label="Invite">
          <form onSubmit={submit} noValidate aria-label="Invite people">
            <div className="settings-invite">
              <Input
                className="settings-input"
                type="text"
                inputMode="email"
                autoComplete="email"
                aria-label="Email addresses"
                placeholder="Email addresses, separated by commas"
                value={emails}
                onChange={(event) => setEmails(event.target.value)}
              />
              <SettingsSelect
                label="Role"
                value={inviteRole}
                options={
                  role === "owner"
                    ? [
                        { value: "member", label: "Member" },
                        { value: "admin", label: "Admin" },
                      ]
                    : [{ value: "member", label: "Member" }]
                }
                onValueChange={setInviteRole}
              />
            </div>
            {formError && (
              <SettingsMessage tone="error">{formError}</SettingsMessage>
            )}
            {notice && (
              <SettingsMessage tone="notice">{notice}</SettingsMessage>
            )}
            <SettingsActions
              start={
                preview
                  ? "Preview data only. Sign in to manage real invitations."
                  : "Invitations expire after seven days. Only the invited email can accept."
              }
            >
              <SettingsButton
                type="submit"
                size="default"
                tone="primary"
                disabled={preview || send.isPending}
              >
                {send.isPending ? "Sending…" : "Send invite"}
              </SettingsButton>
            </SettingsActions>
          </form>
        </SettingsGroup>
      )}

      <SettingsGroup
        label={`Members${members.data ? ` · ${members.data.length}` : ""}${seats}`}
        bare
      >
        <SettingsCard>
          {members.isPending ? (
            <SettingsNote>Loading members…</SettingsNote>
          ) : members.error ? (
            <SettingsMessage tone="error">
              Could not load members.
            </SettingsMessage>
          ) : members.data.length ? (
            <Table className="settings-table">
              <TableHeader>
                <TableRow className="settings-tr">
                  <TableHead>Person</TableHead>
                  <TableHead>Role</TableHead>
                  <TableHead>
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
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
              </TableBody>
            </Table>
          ) : (
            <SettingsNote>No members yet.</SettingsNote>
          )}
        </SettingsCard>
      </SettingsGroup>

      <SettingsGroup
        label={`On their way${canManage ? ` · ${pendingInvitations.length}` : ""}`}
      >
        {!canManage ? (
          <SettingsNote>Workspace admins manage invitations.</SettingsNote>
        ) : invitations.isPending ? (
          <SettingsNote>Loading invitations…</SettingsNote>
        ) : invitations.error ? (
          <SettingsMessage tone="error">
            Could not load invitations.
          </SettingsMessage>
        ) : pendingInvitations.length ? (
          pendingInvitations.map((invitation) => {
            const by = nameOf(invitation.invited_by)
            return (
              <SettingsRow
                key={invitation.id}
                media={
                  <span className="settings-pending-mark" aria-hidden="true">
                    {invitation.email.charAt(0).toUpperCase()}
                  </span>
                }
                title={invitation.email}
                description={[
                  roleNames[invitation.role],
                  by ? `Invited by ${by}` : undefined,
                  expiresIn(invitation.expires_at),
                ]
                  .filter(Boolean)
                  .join(" · ")}
              >
                {(role === "owner" || invitation.role === "member") && (
                  <SettingsButton
                    aria-label={`Revoke invitation for ${invitation.email}`}
                    disabled={preview || revoke.isPending}
                    onClick={() => revoke.mutate(invitation.id)}
                  >
                    Cancel
                  </SettingsButton>
                )}
              </SettingsRow>
            )
          })
        ) : (
          <SettingsNote>No pending invitations.</SettingsNote>
        )}
      </SettingsGroup>
    </>
  )
}
