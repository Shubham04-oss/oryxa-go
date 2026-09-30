import { QueryClient } from "@tanstack/react-query"
import { apiReadRetryDelay, retryApiRead } from "@/lib/api/client"

// Start calls getRouter per request, so each SSR request gets its own cache.
export function getContext() {
  const queryClient = new QueryClient({
    defaultOptions: {
      queries: {
        staleTime: 30_000,
        retry: retryApiRead,
        retryDelay: apiReadRetryDelay,
      },
    },
  })

  return { queryClient }
}
