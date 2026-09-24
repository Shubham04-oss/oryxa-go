// What Team → People derives its directory from: the spaces a member shares
// with others, and who is in each. Real teamspaces feed it when signed in;
// the dev-only People preview (`?preview=people`) keeps its examples, which
// that view labels as preview.
import { people } from "./workspace-avatar"
import type { AvatarIdentity } from "./workspace-avatar"
import { personIdentity } from "./teamspace-format"
import type { Teamspace } from "@/lib/teamspaces/client"

export type SharedSpace = {
  id: string
  title: string
  members: AvatarIdentity[]
}

/** Teamspaces as People sees them: each teammate with a seat in the room. */
export function sharedSpacesOf(spaces: readonly Teamspace[]): SharedSpace[] {
  return spaces.map((space) => ({
    id: space.id,
    title: space.name,
    members: space.whisperers.map((whisperer) =>
      personIdentity(
        whisperer.user_id,
        whisperer.user_display_name || "Teammate"
      )
    ),
  }))
}

export const previewSharedSpaces: SharedSpace[] = [
  {
    id: "preview-product-sync",
    title: "Product sync",
    members: [people.avery, people.jordan, people.sam],
  },
  {
    id: "preview-launch-planning",
    title: "Launch planning",
    members: [people.jordan, people.avery, people.sam, people.noah],
  },
  {
    id: "preview-design-reviews",
    title: "Design reviews",
    members: [people.sam, people.avery, people.jordan],
  },
]
