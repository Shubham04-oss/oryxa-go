import { defineConfig, loadEnv } from "vite"
import { cloudflare } from "@cloudflare/vite-plugin"
import { devtools } from "@tanstack/devtools-vite"
import { tanstackStart } from "@tanstack/react-start/plugin/vite"
import viteReact from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"
import agents from "agents/vite"

const config = defineConfig(({ command, mode }) => {
  const env = loadEnv(mode, process.cwd(), "")
  const target = env.ORYXA_URL || "http://localhost:8080"
  // The Worker (src/server.ts, wrangler.jsonc) runs the SSR environment, so
  // `vite dev` serves the app and the chat's Durable Object from workerd.
  // Vitest runs in Node instead: the Cloudflare plugin refuses its SSR
  // settings, and the Worker's tests mock the Agents SDK.
  const worker = process.env.VITEST
    ? []
    : [
        ...agents(),
        cloudflare({
          viteEnvironment: { name: "ssr" },
          config: remoteEdge(command, env),
        }),
      ]
  const plugins = [...worker, tailwindcss(), tanstackStart(), viteReact()]

  if (env.VITE_SHOW_DEVTOOLS === "true") plugins.unshift(devtools())

  return {
    resolve: { tsconfigPaths: true },
    server: {
      // The local document server fetches signed artifact URLs through the
      // same /v1 proxy the browser uses from its Docker network.
      allowedHosts: ["host.docker.internal"],
      proxy: { "^/v1/": { changeOrigin: true, target } },
    },
    plugins,
  }
})

/**
 * `WEB_NEXT_REMOTE_EDGE=1 vite dev` makes the EDGE binding reach the deployed
 * `oryxa-edge-staging` instead of only a `wrangler dev` of it on this
 * machine, for running calls and the live panes against staging (B8). It
 * needs Cloudflare credentials here (`wrangler login` or
 * CLOUDFLARE_API_TOKEN). Without the switch, and in every build, the
 * binding is as wrangler.jsonc has it.
 */
type ConfigCustomizer = Extract<
  NonNullable<NonNullable<Parameters<typeof cloudflare>[0]>["config"]>,
  (...args: never[]) => unknown
>

function remoteEdge(
  command: string,
  env: Record<string, string>
): ConfigCustomizer | undefined {
  if (command !== "serve" || env.WEB_NEXT_REMOTE_EDGE !== "1") return undefined
  return (worker) => {
    worker.services = worker.services?.map((service) =>
      service.binding === "EDGE" ? { ...service, remote: true } : service
    )
  }
}

export default config
