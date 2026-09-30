import type {
  QueryFunctionContext,
  UseQueryOptions,
} from "@tanstack/react-query"
import { retryUnlessRefused } from "@/lib/threads/client"

/** Keep invalidation prefixes, but never reuse another viewer's private data. */
export function homeRead<T, TKey extends readonly unknown[]>(
  options: Pick<UseQueryOptions<T, unknown, T, TKey>, "queryKey" | "queryFn">,
  viewerId: string
) {
  const queryFn = options.queryFn
  return {
    queryKey: [...options.queryKey, "home", viewerId],
    queryFn: (context: QueryFunctionContext) => {
      if (typeof queryFn !== "function")
        throw new Error("No Home read configured")
      return queryFn({ ...context, queryKey: options.queryKey })
    },
    retry: retryUnlessRefused,
    staleTime: 0,
    refetchOnMount: "always" as const,
    refetchOnWindowFocus: "always" as const,
    refetchIntervalInBackground: false,
  }
}

/** Keep mounted drafts during a refresh; an error revokes the cached display. */
export function authorizedData<T>(query: {
  data?: T
  isSuccess: boolean
}): T | undefined {
  return query.isSuccess ? query.data : undefined
}
