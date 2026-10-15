import { useState } from "react"
import { MoreHorizontal, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"

/**
 * The open conversation's ⋯ menu: Delete, after a confirm. Rename is not
 * offered until Go can rename a thread (#322).
 */
export function AgentChatMenu({
  title,
  disabled,
  onDelete,
}: {
  title: string
  /** While a run works, or the chat has no conversation yet. */
  disabled: boolean
  onDelete: () => Promise<void>
}) {
  const [confirming, setConfirming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [failure, setFailure] = useState("")

  async function remove() {
    setDeleting(true)
    setFailure("")
    try {
      await onDelete()
      setConfirming(false)
    } catch (cause) {
      setFailure(
        cause instanceof Error && cause.message
          ? cause.message
          : "Couldn't delete this chat. Try again."
      )
    } finally {
      setDeleting(false)
    }
  }

  return (
    <>
      <DropdownMenu>
        <DropdownMenuTrigger
          render={
            <Button
              variant="ghost"
              size="icon-sm"
              aria-label="Chat options"
              disabled={disabled}
            />
          }
        >
          <MoreHorizontal aria-hidden="true" />
        </DropdownMenuTrigger>
        <DropdownMenuContent align="end">
          <DropdownMenuItem
            variant="destructive"
            onClick={() => {
              setFailure("")
              setConfirming(true)
            }}
          >
            <Trash2 aria-hidden="true" />
            Delete chat
          </DropdownMenuItem>
        </DropdownMenuContent>
      </DropdownMenu>
      <Dialog
        open={confirming}
        onOpenChange={(open) => {
          if (!deleting) setConfirming(open)
        }}
      >
        <DialogContent showCloseButton={false}>
          <DialogHeader>
            <DialogTitle>Delete this chat?</DialogTitle>
            <DialogDescription>
              “{title}” and the files it made are deleted for good. Your own
              saved files stay.
            </DialogDescription>
          </DialogHeader>
          {failure && (
            <p className="workspace-agent-error" role="alert">
              {failure}
            </p>
          )}
          <DialogFooter>
            <Button
              variant="outline"
              disabled={deleting}
              onClick={() => setConfirming(false)}
            >
              Cancel
            </Button>
            <Button
              variant="destructive"
              disabled={deleting}
              onClick={() => void remove()}
            >
              {deleting ? "Deleting…" : "Delete"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </>
  )
}
