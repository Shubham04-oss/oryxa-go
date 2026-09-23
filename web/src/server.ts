import handler from "@tanstack/react-start/server-entry";
import { getAgentByName, routeAgentRequest } from "agents";
import { ThreadChatAgent } from "./thread-chat-agent";

interface Env extends Cloudflare.Env {
	ORYXA_API_ORIGIN: string;
	ThreadChatAgent: DurableObjectNamespace<ThreadChatAgent>;
}

export { ThreadChatAgent };

const chatPath = /^\/agents\/thread-chat-agent\/([^/]+)(?:\/get-messages)?\/?$/;

export default {
	async fetch(request: Request, env: Env): Promise<Response> {
		const url = new URL(request.url);
		const match = chatPath.exec(url.pathname);
		if (match) {
			const origin = request.headers.get("Origin");
			const isWebSocket =
				request.headers.get("Upgrade")?.toLowerCase() === "websocket";
			if (
				(isWebSocket || request.method === "DELETE") &&
				origin !== url.origin
			) {
				return new Response("Origin not allowed", { status: 403 });
			}
			let name: string;
			try {
				name = decodeURIComponent(match[1]);
			} catch {
				return new Response("Invalid conversation", { status: 400 });
			}
			const [organizationId, threadId, extra] = name.split("~");
			if (
				!organizationId ||
				!threadId ||
				extra ||
				!/^[\w-]+$/.test(organizationId) ||
				!/^[\w-]+$/.test(threadId)
			) {
				return new Response("Invalid conversation", { status: 400 });
			}
			if (request.method !== "GET" && request.method !== "DELETE") {
				return new Response("Method not allowed", { status: 405 });
			}
			const cookie = request.headers.get("Cookie");
			if (!cookie) return new Response("Unauthorized", { status: 401 });
			const threadUrl = new URL(
				`/v1/organizations/${encodeURIComponent(organizationId)}/threads/${encodeURIComponent(threadId)}`,
				env.ORYXA_API_ORIGIN,
			);
			let auth: Response;
			try {
				auth = await fetch(threadUrl, { headers: { Cookie: cookie } });
			} catch {
				return new Response("The agent service is unavailable", {
					status: 503,
				});
			}
			if (!auth.ok) {
				const status = [401, 403, 404, 503].includes(auth.status)
					? auth.status
					: 502;
				return new Response("Conversation access denied", { status });
			}
			if (request.method === "DELETE") {
				if (url.pathname.endsWith("get-messages"))
					return new Response("Method not allowed", { status: 405 });
				const agent = await getAgentByName(
					env.ThreadChatAgent,
					`${organizationId}~${threadId}`,
				);
				const purged = await agent.fetch(
					new Request(new URL(`${url.pathname}/purge`, request.url), {
						method: "POST",
						headers: { "X-Oryxa-Internal-Purge": "1" },
					}),
				);
				if (!purged.ok)
					return new Response("Could not remove chat history", { status: 502 });
				return fetch(threadUrl, {
					method: "DELETE",
					headers: { Cookie: cookie, ...(origin ? { Origin: origin } : {}) },
				});
			}
			if (
				url.pathname.endsWith("get-messages") &&
				request.headers.get("Upgrade")?.toLowerCase() === "websocket"
			) {
				return new Response("Method not allowed", { status: 405 });
			}
			const headers = new Headers(request.headers);
			headers.set("X-Oryxa-Cookie", cookie);
			headers.set("X-Oryxa-Organization", organizationId);
			headers.set("X-Oryxa-Thread", threadId);
			const routed = await routeAgentRequest(
				new Request(request, { headers }),
				env,
			);
			return (
				routed ??
				new Response("Conversation agent unavailable", { status: 404 })
			);
		}
		if (url.pathname.startsWith("/agents/"))
			return new Response("Not found", { status: 404 });
		return handler.fetch(request);
	},
};
