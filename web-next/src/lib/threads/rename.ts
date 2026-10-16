import type { QueryClient, QueryKey } from "@tanstack/react-query"
import { ApiError } from "@/lib/api/client"
import { normalizeThreadTitle, withRenamedThread } from "./client"
import type { Thread, ThreadDetail } from "./client"

/**
 * Renames a thread with the caches it is shown from updated first, so the
 * header and the list change at once: every list entry under `listKey` and
 * every detail entry under `detailKey` (prefixes). Go's answer then replaces
 * the optimistic copy; a refusal puts the old titles back and rethrows.
 * Works for the primary agent's threads and a specialist's alike, which
 * differ only in their keys and route.
 */
export async function renameCachedThread({
  queryClient,
  listKey,
  detailKey,
  threadId,
  title,
  rename,
}: {
  queryClient: QueryClient
  listKey: QueryKey
  detailKey: QueryKey
  threadId: string
  title: string
  rename: (title: string) => Promise<Thread>
}): Promise<Thread> {
  const normalized = normalizeThreadTitle(title)
  // A list or detail fetch already on the wire would land after the rename
  // and write the old title back, so both are cancelled before the snapshot.
  await Promise.all([
    queryClient.cancelQueries({ queryKey: listKey }),
    queryClient.cancelQueries({ queryKey: detailKey }),
  ])
  const lists = queryClient.getQueriesData<Thread[]>({ queryKey: listKey })
  const details = queryClient.getQueriesData<ThreadDetail>({
    queryKey: detailKey,
  })
  const setTitle = (renamed: Pick<Thread, "id"> & Partial<Thread>) => {
    queryClient.setQueriesData<Thread[]>({ queryKey: listKey }, (old) =>
      withRenamedThread(old, renamed)
    )
    queryClient.setQueriesData<ThreadDetail>({ queryKey: detailKey }, (old) =>
      old && old.id === threadId
        ? { ...old, title: renamed.title ?? old.title }
        : old
    )
  }
  // A rename moves `updated_at` in Go, which lifts the thread to the top.
  setTitle({
    id: threadId,
    title: normalized,
    updated_at: new Date().toISOString(),
  })
  try {
    const renamed = await rename(normalized)
    setTitle(renamed)
    return renamed
  } catch (error) {
    for (const [key, data] of lists) queryClient.setQueryData(key, data)
    for (const [key, data] of details) queryClient.setQueryData(key, data)
    throw error
  } finally {
    void queryClient.invalidateQueries({ queryKey: listKey })
    void queryClient.invalidateQueries({ queryKey: detailKey })
  }
}

/** What the member is told when a rename is refused, in plain words. */
export function renameFailureText(error: unknown, agentName?: string): string {
  if (error instanceof ApiError) {
    if (error.status === 409)
      return `${agentName || "Your agent"} is answering — try again in a moment`
    if (error.status === 404) return "That chat no longer exists."
    // A Go without the rename route (before #386 is deployed).
    if (error.status === 405) return "Renaming isn't available yet."
  }
  return "Couldn't rename this chat. Try again."
}
