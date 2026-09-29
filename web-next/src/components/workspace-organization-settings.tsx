import { useEffect, useState } from "react"
import type { FormEvent } from "react"
import { useMutation, useQueryClient } from "@tanstack/react-query"
import { Building2 } from "lucide-react"
import { Input } from "@/components/ui/input"
import type { Organization, Role, Session } from "@/lib/auth/client"
import { authErrorMessage } from "@/lib/auth/client"
import { removeOrganizationMember } from "@/lib/auth/invitations"
import {
  deleteOrganization,
  organizationProfileSchema,
  updateOrganization,
} from "@/lib/auth/settings"

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
  const visibleOrganization =
    organization ||
    (preview
      ? { id: "preview-organization", name: "Oryxa", slug: "oryxa" }
      : undefined)
  const editable = !preview && role === "owner" && Boolean(organization)
  const [name, setName] = useState(visibleOrganization?.name || "")
  const [slug, setSlug] = useState(visibleOrganization?.slug || "")
  const [errors, setErrors] = useState<{ name?: string; slug?: string }>({})
  const [saved, setSaved] = useState(false)
  const [dangerAction, setDangerAction] = useState<"leave" | "delete" | null>(
    null
  )
  const [confirmation, setConfirmation] = useState("")

  useEffect(() => {
    setName(visibleOrganization?.name || "")
    setSlug(visibleOrganization?.slug || "")
  }, [
    visibleOrganization?.id,
    visibleOrganization?.name,
    visibleOrganization?.slug,
  ])

  const save = useMutation({
    mutationFn: (input: { name: string; slug: string }) =>
      updateOrganization(organization!.id, input),
    onSuccess: async (updated) => {
      queryClient.setQueryData<Session>(["auth", "session"], (current) =>
        current
          ? {
              ...current,
              organizations: current.organizations.map((item) =>
                item.id === updated.id ? updated : item
              ),
            }
          : current
      )
      setName(updated.name)
      setSlug(updated.slug)
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

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!editable) return
    setSaved(false)
    const result = organizationProfileSchema.safeParse({ name, slug })
    if (!result.success) {
      setErrors({
        name: result.error.issues.find((issue) => issue.path[0] === "name")
          ?.message,
        slug: result.error.issues.find((issue) => issue.path[0] === "slug")
          ?.message,
      })
      return
    }
    setErrors({})
    save.mutate(result.data)
  }

  if (!visibleOrganization) {
    return (
      <div className="workspace-settings-account">
        <p>No organisation is selected.</p>
      </div>
    )
  }

  const changed =
    name.trim() !== visibleOrganization.name ||
    slug.trim() !== visibleOrganization.slug

  return (
    <div className="workspace-settings-account workspace-settings-organization">
      <div className="workspace-settings-account-card">
        <span className="workspace-settings-org-mark">
          <Building2 size={22} />
        </span>
        <div>
          <strong>{visibleOrganization.name}</strong>
          <small>
            /{visibleOrganization.slug} ·{" "}
            {role || (preview ? "Owner · preview" : "Checking role")}
          </small>
        </div>
      </div>
      <form className="workspace-settings-form" onSubmit={submit} noValidate>
        <div className="workspace-settings-section-title">
          <h3>Organisation profile</h3>
          <p>The identity shown across your workspace.</p>
        </div>
        <label htmlFor="settings-organization-name">Name</label>
        <Input
          id="settings-organization-name"
          value={name}
          disabled={!editable}
          aria-invalid={Boolean(errors.name)}
          onChange={(event) => {
            setName(event.target.value)
            setSaved(false)
          }}
        />
        {errors.name && (
          <p className="workspace-settings-error" role="alert">
            {errors.name}
          </p>
        )}
        <label htmlFor="settings-organization-slug">Handle</label>
        <Input
          id="settings-organization-slug"
          value={slug}
          disabled={!editable}
          aria-invalid={Boolean(errors.slug)}
          onChange={(event) => {
            setSlug(event.target.value)
            setSaved(false)
          }}
        />
        {errors.slug && (
          <p className="workspace-settings-error" role="alert">
            {errors.slug}
          </p>
        )}
        <p className="workspace-settings-hint">
          Used in navigation, invitations, and links.
        </p>
        {save.isError && (
          <p className="workspace-settings-error" role="alert">
            {authErrorMessage(
              save.error,
              "Could not save the organisation. Try again."
            )}
          </p>
        )}
        <div className="workspace-settings-form-footer">
          <span role={saved ? "status" : undefined}>
            {saved
              ? "Saved."
              : preview
                ? "Preview only. Sign in to make changes."
                : editable
                  ? "Visible to every organisation member."
                  : "Only owners can change these details."}
          </span>
          {editable && (
            <button type="submit" disabled={!changed || save.isPending}>
              {save.isPending ? "Saving…" : "Save changes"}
            </button>
          )}
        </div>
      </form>
      {!preview && session && role && (
        <section
          className="workspace-settings-section"
          aria-label="Organisation access"
        >
          <div className="workspace-settings-section-title">
            <h3>
              {role === "owner" ? "Delete organisation" : "Leave organisation"}
            </h3>
            <p>
              {role === "owner"
                ? "Deletes this organisation for everyone, including its teams, files, connections, and subscription."
                : "Leaving removes your access and your personal files and connections here."}
            </p>
          </div>
          {dangerAction ? (
            <div className="workspace-settings-confirm">
              <p>
                {dangerAction === "delete"
                  ? `Type ${organization?.name} to confirm permanent deletion.`
                  : `Leave ${organization?.name}? Someone would need to invite you back.`}
              </p>
              {dangerAction === "delete" && (
                <input
                  aria-label="Type organisation name to confirm"
                  value={confirmation}
                  onChange={(event) => setConfirmation(event.target.value)}
                />
              )}
              <button
                type="button"
                disabled={leaving}
                onClick={() => {
                  setDangerAction(null)
                  setConfirmation("")
                }}
              >
                Cancel
              </button>
              <button
                type="button"
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
              </button>
            </div>
          ) : (
            <button
              className="workspace-settings-action"
              type="button"
              onClick={() =>
                setDangerAction(role === "owner" ? "delete" : "leave")
              }
            >
              {role === "owner" ? "Delete organisation" : "Leave organisation"}
            </button>
          )}
          {(leave.isError || remove.isError) && (
            <p className="workspace-settings-error" role="alert">
              {authErrorMessage(
                leave.error || remove.error,
                "Could not update organisation access."
              )}
            </p>
          )}
        </section>
      )}
    </div>
  )
}
