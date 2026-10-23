import { createFileRoute } from "@tanstack/react-router"
import { CliDevicePage } from "@/components/auth/cli-device-page"

export const Route = createFileRoute("/cli/device")({
  component: CliDeviceRoute,
  head: () => ({
    meta: [
      { title: "Sign in the CLI · Oryxa" },
      { name: "referrer", content: "no-referrer" },
    ],
  }),
})

// The code is never taken from the link: a link with the code filled in is
// what an attacker sends an admin, and typing it is what makes one notice.
function CliDeviceRoute() {
  return <CliDevicePage />
}
