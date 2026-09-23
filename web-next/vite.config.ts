import { defineConfig, loadEnv } from "vite"
import { devtools } from "@tanstack/devtools-vite"
import { tanstackStart } from "@tanstack/react-start/plugin/vite"
import viteReact from "@vitejs/plugin-react"
import tailwindcss from "@tailwindcss/vite"

const config = defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), "")
  const target = env.ORYXA_URL || "http://localhost:8080"
  const plugins = [tailwindcss(), tanstackStart(), viteReact()]

  if (env.VITE_SHOW_DEVTOOLS === "true") plugins.unshift(devtools())

  return {
    resolve: { tsconfigPaths: true },
    server: {
      proxy: { "^/v1/": { changeOrigin: true, target } },
    },
    plugins,
  }
})

export default config
