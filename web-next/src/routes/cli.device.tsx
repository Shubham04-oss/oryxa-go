import { createFileRoute } from "@tanstack/react-router"
import { CliDevicePage } from "@/components/auth/cli-device-page"
import { searchToken } from "@/lib/auth/schemas"

export const Route = createFileRoute("/cli/device")({
  validateSearch: (search: Record<string, unknown>) => ({
    code: searchToken(search.code),
  }),
  component: CliDeviceRoute,
  head: () => ({
    meta: [
      { title: "Sign in the CLI · Oryxa" },
      { name: "referrer", content: "no-referrer" },
    ],
  }),
})

function CliDeviceRoute() {
  const { code } = Route.useSearch()
  return <CliDevicePage code={code} />
}
