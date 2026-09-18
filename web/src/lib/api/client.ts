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
	/** JSON to send, or `FormData` for a multipart upload. */
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

/** Builds an API path, encoding each interpolated value as a path segment. */
export function apiPath(
	strings: TemplateStringsArray,
	...values: string[]
): string {
	return strings.reduce(
		(path, part, index) =>
			`${path}${encodeURIComponent(values[index - 1])}${part}`,
	);
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

// The browser writes a multipart body's boundary into its Content-Type itself,
// so only JSON gets a header of its own.
function requestBody(body: unknown): BodyInit | undefined {
	if (body === undefined || body instanceof FormData) {
		return body;
	}
	return JSON.stringify(body);
}

/**
 * Sends one request with the session cookie, refreshing an expired access
 * token and retrying once when allowed. Returns the raw `Response` so a
 * caller that wants something other than a JSON body — an event stream, a
 * download — can read it directly; `apiRequest` is the JSON wrapper most
 * calls want.
 */
export async function apiFetch(
	path: string,
	{
		body,
		method,
		refreshSession: canRefresh = true,
		signal,
	}: ApiRequestOptions = {},
): Promise<Response> {
	const send = () =>
		fetch(apiUrl(path), {
			body: requestBody(body),
			credentials: "include",
			headers:
				body === undefined || body instanceof FormData
					? undefined
					: { "Content-Type": "application/json" },
			method: method ?? (body === undefined ? "GET" : "POST"),
			signal,
		});

	let response = await send();
	if (response.status === 401 && canRefresh && (await refreshSession())) {
		response = await send();
	}
	return response;
}

export async function apiRequest<T = void>(
	path: string,
	options: ApiRequestOptions = {},
): Promise<T> {
	const response = await apiFetch(path, options);
	if (!response.ok) {
		throw await requestError(response);
	}
	if (!response.headers.get("Content-Type")?.includes("application/json")) {
		return undefined as T;
	}
	return (await response.json()) as T;
}

/** Parses a failed response's body into an `ApiError`, for callers that read `Response` themselves. */
export async function requestError(response: Response): Promise<ApiError> {
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

const statusMessages: Partial<Record<number, string>> = {
	401: "Your session has ended. Sign in again to continue.",
	403: "You do not have permission to do that.",
	404: "That no longer exists, or you do not have access to it.",
	429: "Too many requests. Try again in a minute.",
};

// Auth, teams and files answer every conflict with the same words.
const unexplainedConflict = "resource state conflicts with the request";

/**
 * A failed request in words a person can act on. Services explain bad input
 * and most conflicts themselves; other statuses get a plain sentence.
 */
export function apiErrorMessage(error: unknown, fallback: string): string {
	if (!(error instanceof ApiError) || error.status >= 500) {
		return fallback;
	}
	if (error.message === unexplainedConflict) {
		return "Something changed in the meantime. Refresh and try again.";
	}
	return statusMessages[error.status] ?? sentence(error.message);
}

function sentence(message: string): string {
	const text = message.trim();
	if (text === "") {
		return "The request failed.";
	}
	const capitalized = `${text[0].toUpperCase()}${text.slice(1)}`;
	return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`;
}
