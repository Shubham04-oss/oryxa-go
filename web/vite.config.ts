import { cloudflare } from "@cloudflare/vite-plugin";
import tailwindcss from "@tailwindcss/vite";
import { tanstackStart } from "@tanstack/react-start/plugin/vite";
import viteReact from "@vitejs/plugin-react";
import { defineConfig, loadEnv } from "vite";

// The app calls every service under /v1 on its own origin, so session cookies
// stay first-party and no service needs CORS. One oryxa process serves every
// service's routes, so the dev server sends all of /v1 to it.
const config = defineConfig(({ mode }) => {
	const target =
		loadEnv(mode, process.cwd(), "").ORYXA_URL || "http://localhost:8080";
	return {
		resolve: { tsconfigPaths: true },
		server: { proxy: { "^/v1/": { changeOrigin: true, target } } },
		plugins: [
			cloudflare({ viteEnvironment: { name: "ssr" } }),
			tailwindcss(),
			tanstackStart(),
			viteReact(),
		],
	};
});

export default config;
