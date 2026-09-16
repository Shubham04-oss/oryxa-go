import { QueryClient } from "@tanstack/react-query";
import { ApiError } from "@/lib/api/client";

export function getContext() {
	const queryClient = new QueryClient({
		defaultOptions: {
			queries: {
				// A 4xx answer will be the same next time; a network or server failure may not.
				retry: (failureCount, error) =>
					failureCount < 2 &&
					!(error instanceof ApiError && error.status < 500),
			},
		},
	});

	return {
		queryClient,
	};
}
export default function TanstackQueryProvider() {}
