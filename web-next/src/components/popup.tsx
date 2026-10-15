// The workspace's pop-ups (founder's redesign, 15 Oct 2026, "Pop-ups" on the
// design canvas): one themed dialog on the library's (ui/dialog, Base UI's
// focus trap and Escape), with a slight band of a brand scene fading into the
// surface, a close X, an icon, a kicker, a head and a foot. A warning or a
// destructive confirm is the plain alert dialog: never a scene.
import type { ReactNode, RefObject } from "react"
import { X } from "lucide-react"
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Button } from "@/components/ui/button"
import "./popup.css"

/**
 * The brand scene a pop-up opens with: each kind of pop-up has its own.
 * The sea of bottles for what a parley left behind, the harbour for
 * people coming aboard, the crow's nest for a welcome, the deck for plans.
 */
export type PopupScene = "sea" | "harbour" | "crows-nest" | "deck"

export function Popup({
  open,
  onOpenChange,
  scene,
  wide = false,
  initialFocus,
  className,
  children,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  /** The scene band at the top; leave it out for a plain pop-up. */
  scene?: PopupScene
  wide?: boolean
  /** What takes focus when it opens: by default its first control. */
  initialFocus?: RefObject<HTMLElement | null>
  className?: string
  children: ReactNode
}) {
  return (
    <Dialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <DialogContent
        className={["popup", className].filter(Boolean).join(" ")}
        data-wide={wide ? "true" : undefined}
        data-plain={scene ? undefined : "true"}
        showCloseButton={false}
        initialFocus={initialFocus}
      >
        {scene && (
          <div className="popup-scene" data-scene={scene} aria-hidden="true" />
        )}
        <div className="popup-body">{children}</div>
        <DialogClose
          className="popup-close"
          aria-label="Close"
          data-on-scene={scene ? "true" : undefined}
        >
          <X size={14} strokeWidth={2.2} aria-hidden="true" />
        </DialogClose>
      </DialogContent>
    </Dialog>
  )
}

/** The pop-up's mark: an avatar or icon on a raised tile, over the scene. */
export function PopupIcon({ children }: { children: ReactNode }) {
  return (
    <span className="popup-icon" aria-hidden="true">
      {children}
    </span>
  )
}

/** The kicker, the title (the pop-up's name) and what it's about. */
export function PopupHead({
  kicker,
  title,
  children,
}: {
  kicker?: ReactNode
  title: ReactNode
  children?: ReactNode
}) {
  return (
    <div className="popup-head">
      {kicker && <span className="popup-kicker">{kicker}</span>}
      <DialogTitle className="popup-title">{title}</DialogTitle>
      {children && (
        <DialogDescription className="popup-text">{children}</DialogDescription>
      )}
    </div>
  )
}

/** Lines on a quiet tile: notes, decisions, people. */
export function PopupList({
  label,
  children,
}: {
  label: string
  children: ReactNode
}) {
  return (
    <ul className="popup-list" aria-label={label}>
      {children}
    </ul>
  )
}

/** One line: who or what leads it, the words, and a tag such as Decision. */
export function PopupLine({
  lead,
  tag,
  children,
}: {
  lead?: ReactNode
  tag?: string
  children: ReactNode
}) {
  return (
    <li className="popup-line">
      {lead}
      <span className="popup-grow">{children}</span>
      {tag && <span className="popup-tag">{tag}</span>}
    </li>
  )
}

/** The actions, with an optional quiet note on the left. */
export function PopupFoot({
  note,
  children,
}: {
  note?: ReactNode
  children: ReactNode
}) {
  return (
    <div className="popup-foot">
      <span className="popup-grow popup-note">{note}</span>
      {children}
    </div>
  )
}

export type PopupTone = "primary" | "quiet" | "danger"

/** A pop-up's button: the library's, in the pop-up's pill. */
export function PopupButton({
  tone = "quiet",
  className,
  ...props
}: React.ComponentProps<typeof Button> & { tone?: PopupTone }) {
  return (
    <Button
      variant="ghost"
      className={["popup-btn", className].filter(Boolean).join(" ")}
      data-tone={tone}
      {...props}
    />
  )
}

/**
 * A plain confirm (alertdialog): no scene, an icon, what will happen, and
 * Cancel beside the action. Destructive actions take tone "danger".
 */
export function PopupConfirm({
  open,
  onOpenChange,
  icon,
  title,
  children,
  confirmLabel,
  pendingLabel,
  pending = false,
  tone = "danger",
  error,
  onConfirm,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  icon?: ReactNode
  title: ReactNode
  children?: ReactNode
  confirmLabel: string
  pendingLabel?: string
  pending?: boolean
  tone?: "danger" | "primary"
  error?: string
  onConfirm: () => void
}) {
  return (
    <AlertDialog open={open} onOpenChange={(next) => onOpenChange(next)}>
      <AlertDialogContent className="popup" data-plain="true">
        <div className="popup-body">
          {icon && <PopupIcon>{icon}</PopupIcon>}
          <div className="popup-head">
            <AlertDialogTitle className="popup-title">{title}</AlertDialogTitle>
            {children && (
              <AlertDialogDescription className="popup-text">
                {children}
              </AlertDialogDescription>
            )}
          </div>
          {error && (
            <p className="popup-error" role="alert">
              {error}
            </p>
          )}
          <div className="popup-foot">
            <span className="popup-grow" />
            <AlertDialogCancel
              variant="ghost"
              className="popup-btn"
              data-tone="quiet"
            >
              Cancel
            </AlertDialogCancel>
            <PopupButton tone={tone} disabled={pending} onClick={onConfirm}>
              {pending && pendingLabel ? pendingLabel : confirmLabel}
            </PopupButton>
          </div>
        </div>
      </AlertDialogContent>
    </AlertDialog>
  )
}
