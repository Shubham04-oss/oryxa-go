import { AIChatAgent } from "@cloudflare/ai-chat";
import { getCurrentAgent } from "agents";
import {
	createUIMessageStream,
	createUIMessageStreamResponse,
	type UIMessage,
} from "ai";
import {
	sessionExpiredCode,
	withoutSupersededDeltas,
} from "./lib/threads/chat-messages";
import type { ConfirmationAnswer, Item, RunInput } from "./lib/threads/client";
import { createEventStreamReader } from "./lib/threads/event-stream";

interface ThreadChatEnv extends Cloudflare.Env {
	ORYXA_API_ORIGIN: string;
}

interface ChatConnectionState {
	cookie: string;
	organizationId: string;
	threadId: string;
	origin: string;
}

function confirmationInput(value: unknown): ConfirmationAnswer[] | undefined {
	if (!Array.isArray(value) || value.length === 0) return undefined;
	if (
		!value.every(
			(answer) =>
				answer &&
				typeof answer === "object" &&
				typeof answer.id === "string" &&
				typeof answer.confirmed === "boolean",
		)
	)
		return undefined;
	return value as ConfirmationAnswer[];
}

function inputFromMessages(
	messages: typeof ThreadChatAgent.prototype.messages,
	body: Record<string, unknown> | undefined,
): RunInput {
	if (body?.confirmations !== undefined) {
		const confirmations = confirmationInput(body.confirmations);
		if (!confirmations) throw new Error("Invalid confirmation answers.");
		return { confirmations };
	}
	const lastUser = [...messages]
		.reverse()
		.find((message) => message.role === "user");
	const text = lastUser?.parts
		.filter((part) => part.type === "text")
		.map((part) => part.text)
		.join("")
		.trim();
	if (!text) throw new Error("Send a message to start a run.");
	return { message: text };
}

/** A run Go would not start, with a code when the page can do something about it. */
class RunStartError extends Error {
	readonly code: string | undefined;

	constructor(message: string, code?: string) {
		super(message);
		this.code = code;
	}
}

async function runStartError(response: Response): Promise<Error> {
	// The cookie this socket was opened with has expired, and only the page
	// can refresh it. It does, reopens the socket and sends again, so the
	// member reads this only when that did not help either.
	if (response.status === 401)
		return new RunStartError(
			"Your session has ended. Sign in again to continue.",
			sessionExpiredCode,
		);
	let code = "";
	try {
		const body = (await response.json()) as { error?: { code?: string } };
		code = body.error?.code ?? "";
	} catch {
		// A gateway can return a non-JSON error.
	}
	if (code === "run_in_progress")
		return new Error(
			"Your primary agent is still answering in this conversation.",
		);
	if (code === "confirmation_pending")
		return new Error("Answer the confirmation above to continue.");
	if (code === "agents_unavailable")
		return new Error("Your primary agent isn't available right now.");
	return new Error(`The agent could not start (${response.status}).`);
}

export class ThreadChatAgent extends AIChatAgent<ThreadChatEnv> {
	override async onRequest(request: Request): Promise<Response> {
		if (
			new URL(request.url).pathname.endsWith("/purge") &&
			request.method === "POST" &&
			request.headers.get("X-Oryxa-Internal-Purge") === "1"
		) {
			this.resetTurnState();
			await this.persistMessages([], [], { _deleteStaleRows: true });
			return new Response(null, { status: 204 });
		}
		return new Response("Not found", { status: 404 });
	}

	// The Go ADK run can execute non-idempotent tools. Never replay it after a
	// Durable Object restart; the next connect can read its stored events.
	protected override async onChatRecovery() {
		return { continue: false };
	}

	// Every chunk Go streams arrives as its own `delta` part, and the finished
	// `message` after them holds all their text again. The socket still
	// streams every delta; only what is stored leaves out those a finished
	// message has replaced.
	protected override sanitizeMessageForPersistence(
		message: UIMessage,
	): UIMessage {
		return withoutSupersededDeltas(message);
	}

	override async onConnect(
		connection: Parameters<AIChatAgent["onConnect"]>[0],
		context: Parameters<AIChatAgent["onConnect"]>[1],
	) {
		const { request } = context;
		const cookie = request.headers.get("X-Oryxa-Cookie");
		const organizationId = request.headers.get("X-Oryxa-Organization");
		const threadId = request.headers.get("X-Oryxa-Thread");
		if (!cookie || !organizationId || !threadId) {
			connection.close(1008, "Unauthorized");
			return;
		}
		connection.setState({
			cookie,
			organizationId,
			threadId,
			origin: request.headers.get("Origin") ?? "",
		} satisfies ChatConnectionState);
	}

	override async onChatMessage(
		_onFinish: Parameters<AIChatAgent["onChatMessage"]>[0],
		options?: Parameters<AIChatAgent["onChatMessage"]>[1],
	): Promise<Response> {
		const connection = getCurrentAgent().connection;
		const state = connection?.state as ChatConnectionState | null | undefined;
		if (!state?.cookie)
			throw new Error("The chat connection is no longer authorized.");
		const input = inputFromMessages(this.messages, options?.body);
		const path = `/v1/organizations/${encodeURIComponent(state.organizationId)}/threads/${encodeURIComponent(state.threadId)}/runs`;
		const url = new URL(path, this.env.ORYXA_API_ORIGIN);
		const stream = createUIMessageStream({
			execute: async ({ writer }) => {
				try {
					const response = await fetch(url, {
						method: "POST",
						headers: {
							"Content-Type": "application/json",
							Cookie: state.cookie,
							...(state.origin ? { Origin: state.origin } : {}),
						},
						body: JSON.stringify(input),
						signal: options?.abortSignal,
					});
					if (!response.ok) {
						throw await runStartError(response);
					}
					if (!response.body)
						throw new Error("The agent did not return a stream.");
					if ("confirmations" in input) {
						for (const answer of input.confirmations) {
							writer.write({
								type: "data-oryxa",
								data: {
									type: "confirmation",
									call_id: answer.id,
									status: answer.confirmed ? "approved" : "declined",
								} satisfies Item,
							});
						}
					}
					const parser = createEventStreamReader();
					const reader = response.body.getReader();
					const write = (event: { data: string }) => {
						if (!event.data) return;
						const item = JSON.parse(event.data) as Item;
						// The user's message is already in SQLite from useAgentChat.
						if (item.type !== "message" || item.author !== "member") {
							writer.write({ type: "data-oryxa", data: item });
						}
					};
					try {
						for (;;) {
							const { done, value } = await reader.read();
							if (done) break;
							for (const event of parser.push(value)) write(event);
						}
						for (const event of parser.end()) write(event);
					} finally {
						reader.releaseLock();
					}
				} catch (error) {
					if (options?.abortSignal?.aborted) return;
					writer.write({
						type: "data-oryxa",
						data: {
							type: "run_failed",
							error:
								error instanceof Error
									? error.message
									: "The agent could not answer.",
							code: error instanceof RunStartError ? error.code : undefined,
						} satisfies Item,
					});
				}
			},
		});
		return createUIMessageStreamResponse({ stream });
	}
}
