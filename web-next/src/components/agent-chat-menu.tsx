import { useState } from "react"
import type { FormEvent } from "react"
import { MoreHorizontal, Pencil, Trash2 } from "lucide-react"
import { Button } from "@/components/ui/button"
import { Input } from "@/components/ui/input"
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
import {
  MAX_TITLE_LENGTH,
  normalizeThreadTitle,
  threadTitleProblem,
} from "@/lib/threads/client"
import { renameFailureText } from "@/lib/threads/rename"

/**
 * The open conversation's ⋯ menu: Rename, in a small dialog, and Delete,
 * after a confirm. Each is offered only when it is handed its action.
 */
export function AgentChatMenu({
  title,
  editableTitle = title,
  agentName,
  disabled,
  onRename,
  onDelete,
}: {
  title: string
  /** The title the rename field starts from: "" for a chat with none yet. */
  editableTitle?: string
  /** Who answers in the chat, for "Krude is answering". */
  agentName?: string
  /** While a run works, or the chat has no conversation yet. */
  disabled: boolean
  /** PATCHes the title (already checked); rejects with Go's refusal. */
  onRename?: (title: string) => Promise<void>
  onDelete?: () => Promise<void>
}) {
  const [confirming, setConfirming] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [failure, setFailure] = useState("")
  const [renaming, setRenaming] = useState(false)
  const [name, setName] = useState("")
  const [saving, setSaving] = useState(false)
  const [renameFailure, setRenameFailure] = useState("")
  const problem = threadTitleProblem(name)

  async function rename(event: FormEvent) {
    event.preventDefault()
    if (!onRename || problem) return
    if (normalizeThreadTitle(name) === normalizeThreadTitle(editableTitle)) {
      setRenaming(false)
      return
    }
    setSaving(true)
    setRenameFailure("")
    try {
      await onRename(name)
      setRenaming(false)
    } catch (cause) {
      setRenameFailure(renameFailureText(cause, agentName))
    } finally {
      setSaving(false)
    }
  }

  async function remove() {
    if (!onDelete) return
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
          {onRename && (
            <DropdownMenuItem
              onClick={() => {
                setName(editableTitle)
                setRenameFailure("")
                setRenaming(true)
              }}
            >
              <Pencil aria-hidden="true" />
              Rename
            </DropdownMenuItem>
          )}
          {onDelete && (
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
          )}
        </DropdownMenuContent>
      </DropdownMenu>
      <Dialog
        open={renaming}
        onOpenChange={(open) => {
          if (!saving) setRenaming(open)
        }}
      >
        <DialogContent showCloseButton={false}>
          <form className="grid gap-4" onSubmit={(event) => void rename(event)}>
            <DialogHeader>
              <DialogTitle>Rename chat</DialogTitle>
              <DialogDescription>
                Up to {MAX_TITLE_LENGTH} characters. Only you see it.
              </DialogDescription>
            </DialogHeader>
            <Input
              aria-label="Chat name"
              value={name}
              autoFocus
              aria-invalid={name !== "" && problem ? true : undefined}
              onChange={(event) => {
                setName(event.target.value)
                setRenameFailure("")
              }}
            />
            {(renameFailure || (name !== "" && problem)) && (
              <p className="workspace-agent-error" role="alert">
                {renameFailure || problem}
              </p>
            )}
            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                disabled={saving}
                onClick={() => setRenaming(false)}
              >
                Cancel
              </Button>
              <Button type="submit" disabled={saving || Boolean(problem)}>
                {saving ? "Saving…" : "Save"}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>
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
