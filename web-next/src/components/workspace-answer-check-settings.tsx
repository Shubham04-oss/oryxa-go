import { useEffect, useId, useRef, useState } from "react"
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import {
  answerGuardPreferencesQueryOptions,
  applyAnswerGuardChange,
  isBelowFloor,
  setAnswerGuardPreferences,
} from "@/lib/answer-guard/client"
import type {
  AnswerGuardChange,
  GuardLevel,
  GuardOrigin,
  GuardOriginPreference,
} from "@/lib/answer-guard/client"
import { authErrorMessage } from "@/lib/auth/client"
import type { Organization } from "@/lib/auth/client"
import {
  SettingsButton,
  SettingsChips,
  SettingsGroup,
  SettingsMessage,
  SettingsRow,
  SettingsRowBody,
} from "./settings-kit"

/** What a row can be set to: a level, or the default, which Go keeps as no choice at all. */
type Choice = GuardLevel | "default"

/** A change on its way to Go, and the row it came from. */
type Saving = { origin: GuardOrigin; change: AnswerGuardChange }

const rows: ReadonlyArray<{ origin: GuardOrigin; title: string }> = [
  { origin: "owner", title: "Answers you start yourself" },
  { origin: "peer", title: "Answers other people read" },
]

const levels: ReadonlyArray<{
  level: GuardLevel
  label: string
  detail: string
}> = [
  {
    level: "secrets",
    label: "Secrets only",
    detail: "Passwords, keys, and card, bank and ID numbers.",
  },
  {
    level: "strict",
    label: "All personal details",
    detail: "Also emails, phone numbers and addresses.",
  },
]

const levelPhrase: Record<GuardLevel, string> = {
  secrets: "secrets only",
  strict: "all personal details",
}

const intro =
  "When your agent answers someone, oryxa checks the answer first and hides these. You see them; the person asking doesn't."

/** What each level hides, in the words a member reads (the guard's own lists). */
const hidden: Record<GuardLevel, readonly string[]> = {
  secrets: ["Passwords", "Keys", "Card, bank and ID numbers"],
  strict: [
    "Passwords",
    "Keys",
    "Card, bank and ID numbers",
    "Email addresses",
    "Phone numbers",
    "Addresses",
  ],
}

const sessionKey = ["auth", "session"] as const

function isSessionEnded(error: unknown): boolean {
  return error instanceof ApiError && error.status === 401
}

/**
 * Settings → Account → What your agents share: how much of an answer the
 * answer guard hides, for the answers a member starts themselves and for the
 * ones other people read. Any member may change their own; their
 * organization's minimum can only raise what applies, never lower it.
 *
 * Nothing shows until Go has answered, and nothing shows if it does not serve
 * these settings (a 404, or a server that keeps none): the section is not
 * there rather than broken. Signed out, in the dev-only layout preview, there
 * is nothing to read, so there is nothing to show either.
 */
export function WorkspaceAnswerCheckSettings({
  organization,
  preview,
}: {
  organization?: Organization
  preview: boolean
}) {
  if (preview || !organization) return null
  return (
    <AnswerCheckSection
      key={organization.id}
      organizationId={organization.id}
    />
  )
}

function AnswerCheckSection({ organizationId }: { organizationId: string }) {
  const queryClient = useQueryClient()
  const groupName = useId()
  const options = answerGuardPreferencesQueryOptions(organizationId)
  const query = useQuery(options)
  // The pick that has not been answered yet. It sits over what Go last said,
  // and rendering it (not the cache) is what shows a pick in the very render
  // of the click: a controlled radio is put back to its props right after
  // its event, and the cache only tells a page a moment later.
  const [saving, setSaving] = useState<Saving | null>(null)
  // One save at a time. Set in the click itself, not on a render, so a second
  // pick cannot slip in before the first has been seen to be in flight.
  const busy = useRef(false)
  const save = useMutation({
    mutationFn: ({ change }: Saving) =>
      setAnswerGuardPreferences(organizationId, change),
    onSuccess: (result) => queryClient.setQueryData(options.queryKey, result),
    onError: (error) => {
      // The session ended between the page opening and this pick. Ask the
      // session query, which the workspace already sends to sign-in on a 401.
      if (isSessionEnded(error)) {
        void queryClient.invalidateQueries({ queryKey: sessionKey })
      }
    },
    // A failed save needs no undoing: the pick was only ever laid over what
    // Go said, and clearing it shows that again. A saved one has already put
    // Go's answer in the cache by now, so clearing it shows that instead.
    onSettled: () => {
      busy.current = false
      setSaving(null)
    },
  })

  useEffect(() => {
    if (isSessionEnded(query.error)) {
      void queryClient.invalidateQueries({ queryKey: sessionKey })
    }
  }, [query.error, queryClient])

  function choose(origin: GuardOrigin, choice: Choice) {
    if (busy.current) return
    busy.current = true
    const level = choice === "default" ? null : choice
    const next: Saving = {
      origin,
      change: origin === "owner" ? { owner: level } : { peer: level },
    }
    setSaving(next)
    save.mutate(next)
  }

  const label = "What it hides when it answers"

  if (!query.data) {
    // Still asking: the section may turn out not to be served, so it does not
    // appear only to go again.
    if (!query.isError) return null
    return (
      <SettingsGroup label={label}>
        <SettingsRow
          title="Checked before anything is shared"
          description={intro}
        />
        <SettingsMessage tone="error">
          {authErrorMessage(query.error, "Could not load these settings.")}{" "}
          <SettingsButton
            disabled={query.isFetching}
            onClick={() => void query.refetch()}
          >
            {query.isFetching ? "Retrying…" : "Retry"}
          </SettingsButton>
        </SettingsMessage>
      </SettingsGroup>
    )
  }
  if (!query.data.available) return null

  const { preferences } = query.data
  const shown = saving
    ? applyAnswerGuardChange(preferences, saving.change)
    : preferences
  const failedRow = save.isError ? save.variables.origin : undefined

  return (
    <SettingsGroup label={label}>
      <SettingsRow
        title="Checked before anything is shared"
        description={intro}
      >
        <SettingsRowBody>
          <SettingsChips
            label="Hidden from people who ask"
            items={hidden[shown.peer.effective]}
          />
        </SettingsRowBody>
      </SettingsRow>
      {rows.map(({ origin, title: rowTitle }) => (
        <AnswerCheckRow
          key={origin}
          title={rowTitle}
          name={`${groupName}-${origin}`}
          value={shown[origin]}
          busy={saving !== null}
          error={
            failedRow === origin
              ? authErrorMessage(save.error, "Could not save your choice.")
              : undefined
          }
          onChoose={(choice) => choose(origin, choice)}
        />
      ))}
      {preferences.regulated && (
        <SettingsRow description="Your organization is regulated: health, HR and pay details wait for you before anyone else sees them." />
      )}
      {preferences.relevance === "hold" && (
        <SettingsRow description="Answers that don't match what was asked are held for you first." />
      )}
    </SettingsGroup>
  )
}

/**
 * One origin's choice, as a labelled radio group. The radios are never
 * disabled while a save is on its way: a disabled radio drops the keyboard's
 * focus, and a member pressing an arrow key twice would find the second
 * press gone. The group says it is busy instead, and a pick made meanwhile
 * is ignored.
 */
function AnswerCheckRow({
  title,
  name,
  value,
  busy,
  error,
  onChoose,
}: {
  title: string
  name: string
  value: GuardOriginPreference
  busy: boolean
  error?: string
  onChoose: (choice: Choice) => void
}) {
  const id = useId()
  const selected: Choice = value.chosen ?? "default"
  // The organization's minimum wins over a choice below it, so a level below
  // it is not offered; a choice the member made before it was raised stays
  // shown, and they can move off it.
  const floored = levels.some(({ level }) => isBelowFloor(level, value.floor))
  const choices: ReadonlyArray<{
    choice: Choice
    label: string
    detail?: string
    disabled: boolean
  }> = [
    ...levels.map(({ level, label, detail }) => ({
      choice: level,
      label,
      detail,
      disabled: isBelowFloor(level, value.floor),
    })),
    {
      choice: "default",
      label: `Default (${levelPhrase[value.default]})`,
      disabled: false,
    },
  ]

  return (
    <div className="settings-row settings-choices-row">
      <fieldset
        role="radiogroup"
        aria-labelledby={`${id}-title`}
        aria-describedby={floored ? `${id}-floor` : undefined}
        aria-busy={busy}
        className="settings-choices"
      >
        <legend id={`${id}-title`} className="settings-choices-title">
          {title}
        </legend>
        <div className="settings-choices-grid">
          {choices.map((option) => (
            <label
              key={option.choice}
              className="settings-choice"
              data-disabled={option.disabled ? "true" : undefined}
            >
              <input
                type="radio"
                name={name}
                value={option.choice}
                checked={selected === option.choice}
                disabled={option.disabled}
                onChange={() => onChoose(option.choice)}
                aria-labelledby={`${id}-${option.choice}-label`}
                aria-describedby={
                  option.detail ? `${id}-${option.choice}-detail` : undefined
                }
              />
              <span className="settings-choice-text">
                <span id={`${id}-${option.choice}-label`}>{option.label}</span>
                {option.detail && (
                  <small id={`${id}-${option.choice}-detail`}>
                    {option.detail}
                  </small>
                )}
              </span>
            </label>
          ))}
        </div>
      </fieldset>
      {floored && (
        <p id={`${id}-floor`} className="settings-note">
          Your organization requires all personal details to be hidden here.
        </p>
      )}
      {error && <SettingsMessage tone="error">{error}</SettingsMessage>}
    </div>
  )
}
