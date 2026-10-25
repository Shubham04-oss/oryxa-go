import type { QueryClient } from "@tanstack/react-query"
import { deleteThreadChat, renameThread, threadKeys } from "./client"
import type { Thread } from "./client"
import { renameCachedThread } from "./rename"

// Rename and delete for one of the member's conversations with their primary
// agent, shared by every place that offers them (the open chat's menu, the
// Chats list's rows), so each does the same thing to the server and the
// cache.

/**
 * Deletes a conversation: the live chat object first, which deletes the Go
 * thread after it (DESIGN.md §3), then every cached list and detail.
 */
export async function deleteConversation(
  queryClient: QueryClient,
  organizationId: string,
  threadId: string
): Promise<void> {
  await deleteThreadChat(organizationId, threadId)
  const lists = threadKeys.list(organizationId)
  await queryClient.cancelQueries({ queryKey: lists })
  queryClient.setQueriesData<Thread[]>({ queryKey: lists }, (old) =>
    old?.filter((thread) => thread.id !== threadId)
  )
  queryClient.removeQueries({
    queryKey: threadKeys.detail(organizationId, threadId),
  })
  void queryClient.invalidateQueries({ queryKey: lists })
}

/** Renames a conversation; Go's copy replaces the optimistic one in every list and detail entry. */
export async function renameConversation(
  queryClient: QueryClient,
  organizationId: string,
  threadId: string,
  title: string
): Promise<void> {
  await renameCachedThread({
    queryClient,
    listKey: threadKeys.list(organizationId),
    detailKey: threadKeys.detail(organizationId, threadId),
    threadId,
    title,
    rename: (normalized) => renameThread(organizationId, threadId, normalized),
  })
}
