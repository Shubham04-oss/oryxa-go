// The workspace's toasts (founder's redesign, 15 Oct 2026, "Pop-ups ·
// toasts" on the design canvas), on the library's sonner Toaster: a plain
// one for small confirmations, and one with a person and a thin strip of
// scene when someone does something the member can join.
import { toast } from "sonner"
import { IdentityAvatar } from "./workspace-avatar"
import { personIdentity } from "./teamspace-format"
import "./popup.css"

/** "Link copied", or what else was copied. */
export function notifyCopied(what = "Link") {
  toast(`${what} copied`, { className: "workspace-toast" })
}

/** Someone started a parley in a room the member has open, with Join. */
export function notifyParleyStarted({
  member,
  name,
  roomName,
  onJoin,
}: {
  member: string
  name: string
  roomName: string
  onJoin: () => void
}) {
  toast(`${name} started a parley`, {
    id: `parley:${roomName}`,
    className: "workspace-toast workspace-toast-scene",
    description: roomName,
    icon: <IdentityAvatar identity={personIdentity(member, name)} />,
    action: { label: "Join", onClick: onJoin },
  })
}
