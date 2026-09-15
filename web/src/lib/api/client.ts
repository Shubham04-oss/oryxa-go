// Auth, teams, files and billing answer errors as {"error": "message"};
// connections nests a stable code: {"error": {"code": "…", "message": "…"}}.
interface ErrorBody {
	error?: string | { code?: string; message?: string };
}

export class ApiError extends Error {
	readonly code: string | undefined;
	readonly status: number;

	constructor(message: string, status: number, code?: string) {
		super(message);
		this.name = "ApiError";
		this.code = code;
		this.status = status;
	}
}

export interface ApiRequestOptions {
	body?: unknown;
	method?: "DELETE" | "GET" | "PATCH" | "POST" | "PUT";
	/**
	 * Refresh the session and retry once when the access token has expired.
	 * Sign-in and the other public auth calls turn this off: their 401 is the
	 * answer, not an expired session.
	 */
	refreshSession?: boolean;
	signal?: AbortSignal;
}

/** Resolves an API path against `VITE_API_URL`, or the app's own origin. */
export function apiUrl(path: string): string {
	const base: string = import.meta.env.VITE_API_URL ?? "";
	return `${base.replace(/\/$/, "")}${path}`;
}

let pendingRefresh: Promise<boolean> | undefined;

// Auth rotates the refresh token on every use, so requests whose access token
// expired together share one refresh instead of racing each other with it.
function refreshSession(): Promise<boolean> {
	pendingRefresh ??= fetch(apiUrl("/v1/auth/refresh"), {
		credentials: "include",
		method: "POST",
	})
		.then(
			(response) => response.ok,
			() => false,
		)
		.finally(() => {
			pendingRefresh = undefined;
		});
	return pendingRefresh;
}

export async function apiRequest<T = void>(
	path: string,
	{
		body,
		method,
		refreshSession: canRefresh = true,
		signal,
	}: ApiRequestOptions = {},
): Promise<T> {
	const send = () =>
		fetch(apiUrl(path), {
			body: body === undefined ? undefined : JSON.stringify(body),
			credentials: "include",
			headers:
				body === undefined ? undefined : { "Content-Type": "application/json" },
			method: method ?? (body === undefined ? "GET" : "POST"),
			signal,
		});

	let response = await send();
	if (response.status === 401 && canRefresh && (await refreshSession())) {
		response = await send();
	}
	if (!response.ok) {
		throw await requestError(response);
	}
	if (!response.headers.get("Content-Type")?.includes("application/json")) {
		return undefined as T;
	}
	return (await response.json()) as T;
}

async function requestError(response: Response): Promise<ApiError> {
	let body: ErrorBody = {};
	try {
		body = (await response.json()) as ErrorBody;
	} catch {
		// The status still gives callers a stable failure when the body is unavailable.
	}
	if (typeof body.error === "string") {
		return new ApiError(body.error, response.status);
	}
	return new ApiError(
		body.error?.message ?? "Request failed",
		response.status,
		body.error?.code,
	);
}
