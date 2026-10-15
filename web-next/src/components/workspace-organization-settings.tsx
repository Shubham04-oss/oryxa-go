import { useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Input } from "@/components/ui/input"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { getMembers, removeOrganizationMember } from "@/lib/auth/invitations"
import {
  deleteOrganization,
  organizationProfileSchema,
  updateOrganization,
} from "@/lib/auth/settings"
import {
  SettingsActions,
  SettingsButton,
  SettingsGroup,
  SettingsMessage,
  SettingsNote,
  SettingsRow,
  SettingsValue,
} from "./settings-kit"

const createdDate = new Intl.DateTimeFormat("en-GB", {
  day: "numeric",
  month: "short",
  year: "numeric",
})

/** "a.io, b.de" → ["a.io", "b.de"]; Go checks each is a DNS name. */
function parseDomains(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((item) => item.trim().toLowerCase().replace(/^@/, ""))
    .filter(Boolean)
}

/**
 * Settings → Organisation: the workspace's name, its address (the slug) and
 * its email domains, then leaving or deleting it. Owners change them.
 *
 * Changing the mark, the data region, recording retention and export have no
 * route yet, so they are not offered.
 */
export function WorkspaceOrganizationSettings({
  organization,
  role,
  preview,
  session,
}: {
  organization?: Organization
  role?: Role
  preview: boolean
  session?: Session
}) {
  const queryClient = useQueryClient()
  const shown =
    organization ||
    (preview
      ? { id: "preview-organization", name: "Oryxa", slug: "oryxa" }
      : undefined)
  const editable = !preview && role === "owner" && Boolean(organization)
  const [editing, setEditing] = useState<"name" | "domains" | null>(null)
  const [name, setName] = useState("")
  const [domains, setDomains] = useState("")
  const [error, setError] = useState("")
  const [saved, setSaved] = useState(false)
  const [dangerAction, setDangerAction] = useState<"leave" | "delete" | null>(
    null
  )
  const [confirmation, setConfirmation] = useState("")

  // Who created it, by name, when the members list can say.
  const members = useQuery({
    queryKey: ["settings", "members", organization?.id],
    queryFn: () => getMembers(organization!.id),
    enabled: !preview && Boolean(organization?.created_by),
    retry: false,
  })
  const creator = members.data?.find(
    (member) => member.user_id === organization?.created_by
  )

  const save = useMutation({
    mutationFn: (input: { name: string; slug: string; domains?: string[] }) =>
      updateOrganization(organization!.id, input),
    onSuccess: async (updated) => {
      queryClient.setQueryData<Session>(["auth", "session"], (current) =>
        current
          ? {
              ...current,
              organizations: current.organizations.map((item) =>
                item.id === updated.id ? { ...item, ...updated } : item
              ),
            }
          : current
      )
      setEditing(null)
      setSaved(true)
      await queryClient.invalidateQueries({ queryKey: ["auth", "session"] })
    },
  })
  const leave = useMutation({
    mutationFn: () =>
      removeOrganizationMember(organization!.id, session!.user.id),
    onSuccess: () => window.location.assign("/"),
  })
  const remove = useMutation({
    mutationFn: () => deleteOrganization(organization!.id),
    onSuccess: () => window.location.assign("/"),
  })
  // Once the request is sent there is nothing to take back: aborting the
  // fetch would only stop the page from hearing what Go did, and the page
  // navigates away on success. So the confirmation is locked until Go answers
  // (or, on success, until the navigation lands), and Cancel is available
  // again only after a failure.
  const leaving =
    leave.isPending || leave.isSuccess || remove.isPending || remove.isSuccess

  if (!shown) {
    return <SettingsNote>No organisation is selected.</SettingsNote>
  }
  const current = shown

  function startEditing(field: "name" | "domains") {
    setName(current.name)
    setDomains((organization?.domains ?? []).join(", "))
    setError("")
    setSaved(false)
    save.reset()
    setEditing(field)
  }

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!editable) return
    const result = organizationProfileSchema.safeParse({
      name: editing === "name" ? name : current.name,
      slug: current.slug,
    })
    if (!result.success) {
      setError(result.error.issues[0]?.message ?? "Check the name.")
      return
    }
    setError("")
    save.mutate(
      editing === "domains"
        ? { ...result.data, domains: parseDomains(domains) }
        : result.data
    )
  }

  const created = organization?.created_at
    ? [
        `Created ${createdDate.format(new Date(organization.created_at))}`,
        creator ? `by ${creator.display_name || creator.email}` : undefined,
      ]
        .filter(Boolean)
        .join(" ")
    : undefined
  const roleLine = editable
    ? undefined
    : preview
      ? "Preview only. Sign in to make changes."
      : "Only owners can change these details."
  const ownDomains = organization?.domains ?? []

  const editForm = (field: "name" | "domains") => (
    <form
      onSubmit={submit}
      noValidate
      aria-label={field === "name" ? "Rename organisation" : "Email domains"}
    >
      <SettingsRow
        title={field === "name" ? "Name" : "Email domains"}
        description={
          field === "name"
            ? "Shown across the workspace, in invitations and links."
            : "Separate several with commas, such as northstar.io."
        }
      >
        <Input
          className="settings-input"
          aria-label={field === "name" ? "Name" : "Email domains"}
          aria-invalid={Boolean(error)}
          value={field === "name" ? name : domains}
          autoFocus
          onChange={(event) =>
            field === "name"
              ? setName(event.target.value)
              : setDomains(event.target.value)
          }
        />
      </SettingsRow>
      {(error || save.isError) && (
        <SettingsMessage tone="error">
          {error ||
            authErrorMessage(
              save.error,
              "Could not save the organisation. Try again."
            )}
        </SettingsMessage>
      )}
      <SettingsActions>
        <SettingsButton size="default" onClick={() => setEditing(null)}>
          Cancel
        </SettingsButton>
        <SettingsButton
          type="submit"
          size="default"
          tone="primary"
          disabled={save.isPending}
        >
          {save.isPending ? "Saving…" : "Save changes"}
        </SettingsButton>
      </SettingsActions>
    </form>
  )

  return (
    <>
      <SettingsGroup label="Workspace">
        {editing === "name" ? (
          editForm("name")
        ) : (
          <SettingsRow
            media={
              <span className="settings-org-mark" aria-hidden="true">
                {current.name.charAt(0).toUpperCase()}
              </span>
            }
            title={current.name}
            description={created ?? roleLine}
          >
            {editable && (
              <SettingsButton onClick={() => startEditing("name")}>
                Rename
              </SettingsButton>
            )}
          </SettingsRow>
        )}
        <SettingsRow
          title="Address"
          description="The workspace's handle, used in links and invitations."
        >
          <SettingsValue mono>{current.slug}</SettingsValue>
        </SettingsRow>
        {organization &&
          (editing === "domains" ? (
            editForm("domains")
          ) : (
            <SettingsRow
              title="Email domains"
              description={`The email domains that belong to ${current.name}.`}
            >
              {ownDomains.length > 0 ? (
                <SettingsValue mono>{ownDomains.join(", ")}</SettingsValue>
              ) : (
                <SettingsValue>None yet</SettingsValue>
              )}
              {editable && (
                <SettingsButton onClick={() => startEditing("domains")}>
                  Edit
                </SettingsButton>
              )}
            </SettingsRow>
          ))}
        {saved && !editing && (
          <SettingsMessage tone="notice">Saved.</SettingsMessage>
        )}
        {created && roleLine && <SettingsNote>{roleLine}</SettingsNote>}
      </SettingsGroup>

      {!preview && session && role && (
        <SettingsGroup label="Danger zone">
          <SettingsRow
            tone="danger"
            title={
              role === "owner"
                ? `Delete ${current.name}`
                : `Leave ${current.name}`
            }
            description={
              role === "owner"
                ? "Deletes it for everyone, with its teams, files, connections and subscription. Only owners can do this."
                : "Removes your access, and your personal files and connections here."
            }
          >
            {!dangerAction && (
              <SettingsButton
                onClick={() =>
                  setDangerAction(role === "owner" ? "delete" : "leave")
                }
              >
                {role === "owner"
                  ? "Delete organisation"
                  : "Leave organisation"}
              </SettingsButton>
            )}
          </SettingsRow>
          {dangerAction === "delete" && (
            <SettingsRow
              title="Confirm"
              description={`Type ${current.name} to confirm permanent deletion.`}
            >
              <Input
                className="settings-input"
                aria-label="Type organisation name to confirm"
                value={confirmation}
                disabled={leaving}
                onChange={(event) => setConfirmation(event.target.value)}
              />
            </SettingsRow>
          )}
          {dangerAction && (
            <SettingsActions
              start={
                dangerAction === "leave"
                  ? `Leave ${current.name}? Someone would need to invite you back.`
                  : undefined
              }
            >
              <SettingsButton
                size="default"
                disabled={leaving}
                onClick={() => {
                  setDangerAction(null)
                  setConfirmation("")
                }}
              >
                Cancel
              </SettingsButton>
              <SettingsButton
                size="default"
                tone="danger"
                disabled={
                  (dangerAction === "delete" &&
                    confirmation !== organization?.name) ||
                  leaving
                }
                onClick={() =>
                  dangerAction === "delete" ? remove.mutate() : leave.mutate()
                }
              >
                {dangerAction === "delete"
                  ? "Delete organisation"
                  : "Leave organisation"}
              </SettingsButton>
            </SettingsActions>
          )}
          {(leave.isError || remove.isError) && (
            <SettingsMessage tone="error">
              {authErrorMessage(
                leave.error || remove.error,
                "Could not update organisation access."
              )}
            </SettingsMessage>
          )}
        </SettingsGroup>
      )}
    </>
  )
}
