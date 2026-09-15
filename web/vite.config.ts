import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { defineConfig, loadEnv, type ProxyOptions } from "vite";

// The app calls every service under /v1 on its own origin, so session cookies
// stay first-party and no service needs CORS. The dev server routes those
// paths to the local services. The first matching pattern wins, so the
// organization routes other services own come before auth's catch-all.
function serviceProxy(
	env: Record<string, string>,
): Record<string, ProxyOptions> {
	const service = (name: string, fallback: string): ProxyOptions => ({
		changeOrigin: true,
		target: env[name] || fallback,
	});
	const connections = service(
		"CONNECTIONS_SERVICE_URL",
		"http://localhost:8086",
	);

	return {
		"^/v1/organizations/[^/]+/(teams|tools|members/search)([/?]|$)": service(
			"TEAMS_SERVICE_URL",
			"http://localhost:8083",
		),
		"^/v1/organizations/[^/]+/files([/?]|$)": service(
			"FILES_SERVICE_URL",
			"http://localhost:8085",
		),
		"^/v1/organizations/[^/]+/(apps|connections)([/?]|$)": connections,
		"^/v1/oauth/": connections,
		"^/v1/billing/": service("BILLING_SERVICE_URL", "http://localhost:8081"),
		"^/v1/": service("AUTH_SERVICE_URL", "http://localhost:8080"),
	};
}

const config = defineConfig(({ mode }) => ({
	resolve: { tsconfigPaths: true },
	server: { proxy: serviceProxy(loadEnv(mode, process.cwd(), "")) },
	plugins: [
		cloudflare({ viteEnvironment: { name: "ssr" } }),
		tailwindcss(),
		tanstackStart(),
		viteReact(),
	],
}));

export default config;
