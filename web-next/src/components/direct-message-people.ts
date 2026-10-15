// Who a member can message: everyone in their organization (the members
// list), with the teamspaces they share as context. An inbox knows people
// only by id, so the DM views take their names from here too.
import { useMemo } from "react"
import { useQuery } from "@tanstack/react-query"
import { firstName, roleLabel } from "./direct-message-format"
import { membersQueryOptions } from "@/lib/auth/queries"
import type { Role } from "@/lib/auth/client"
import type { Teamspace } from "@/lib/teamspaces/client"

export type DmPerson = {
  id: string
  name: string
  email: string
  role: Role
  /** The teamspaces the viewer and this person both have a seat in. */
  spaces: string[]
  /**
   * Their agents, as far as the viewer can know them: the agent seated for
   * them in a teamspace they share (its whisperer), once each. Empty when
   * they share no teamspace: the members list names no one's agent.
   */
  agents: DmPersonAgent[]
}

/** Someone's agent, as a shared teamspace seats it. */
export type DmPersonAgent = {
  id: string
  name: string
  /** A teamspace they share where it sits: where a request to it is filed. */
  teamspaceId: string
}

/** The other person in a DM, as its views name them: in full, and in a sentence ("Say hello to Priya"). */
export type DmPeer = { id: string; name: string; short: string }

export type DmPeople = {
  /** `none` without an organization to read. */
  status: "none" | "loading" | "ready" | "error"
  /** Everyone in the organization but the viewer, by name. */
  people: DmPerson[]
  find: (id: string) => DmPerson | undefined
  /**
   * What to call someone by id: their name; "Former member" once the list
   * shows they have left; "Someone" when it could not be read; nothing
   * while it loads.
   */
  nameOf: (id: string) => string | undefined
  /** Someone to open a DM with, once the list can say who they are. */
  peer: (id: string) => DmPeer | undefined
  refetch: () => void
}

const noSpaces: readonly Teamspace[] = []

export function useDmPeople(
  organizationId: string | undefined,
  viewerId: string | undefined,
  spaces: readonly Teamspace[] = noSpaces
): DmPeople {
  const members = useQuery({
    ...membersQueryOptions(organizationId ?? ""),
    enabled: Boolean(organizationId && viewerId),
  })
  const { data, refetch } = members
  const status: DmPeople["status"] =
    !organizationId || !viewerId
      ? "none"
      : data
        ? "ready"
        : members.isError
          ? "error"
          : "loading"

  const people = useMemo(() => {
    if (!data || !viewerId) return []
    return data
      .filter((member) => member.user_id !== viewerId)
      .map((member) => {
        const shared = spaces.filter(
          (space) =>
            space.whisperers.some((seat) => seat.user_id === viewerId) &&
            space.whisperers.some((seat) => seat.user_id === member.user_id)
        )
        const agents = new Map<string, DmPersonAgent>()
        for (const space of shared) {
          for (const seat of space.whisperers) {
            if (seat.user_id !== member.user_id || agents.has(seat.agent_id)) {
              continue
            }
            agents.set(seat.agent_id, {
              id: seat.agent_id,
              name: seat.agent_name,
              teamspaceId: space.id,
            })
          }
        }
        return {
          id: member.user_id,
          name: member.display_name?.trim() || member.email,
          email: member.email,
          role: member.role,
          spaces: shared.map((space) => space.name),
          agents: [...agents.values()],
        }
      })
      .sort((left, right) => left.name.localeCompare(right.name))
  }, [data, spaces, viewerId])

  return useMemo(() => {
    const byId = new Map(people.map((person) => [person.id, person]))
    return {
      status,
      people,
      find: (id) => byId.get(id),
      nameOf: (id) =>
        byId.get(id)?.name ??
        (status === "ready"
          ? "Former member"
          : status === "error"
            ? "Someone"
            : undefined),
      peer: (id) => {
        const known = byId.get(id)
        if (known) return { id, name: known.name, short: firstName(known.name) }
        if (status === "ready") {
          return { id, name: "Former member", short: "this person" }
        }
        if (status === "error") return { id, name: "Someone", short: "Someone" }
        return undefined
      },
      refetch: () => void refetch(),
    }
  }, [people, refetch, status])
}

/** People whose name or email holds `query`, ignoring case. */
export function filterPeople(
  people: readonly DmPerson[],
  query: string
): DmPerson[] {
  const needle = query.trim().toLocaleLowerCase()
  if (!needle) return [...people]
  return people.filter(
    (person) =>
      person.name.toLocaleLowerCase().includes(needle) ||
      person.email.toLocaleLowerCase().includes(needle)
  )
}

/** What a list says about someone under their name: their role, and the teamspaces you share. */
export function personContext(person: DmPerson): string {
  const role = roleLabel(person.role)
  return person.spaces.length > 0
    ? `${role} · ${person.spaces.join(", ")}`
    : role
}
