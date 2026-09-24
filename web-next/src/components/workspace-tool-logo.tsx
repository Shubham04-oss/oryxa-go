import { toolkitIcon } from "./workspace-tools-data"
import type { Toolkit } from "./workspace-tools-data"

/** An app's own mark on a light tile: its brand icon, the catalog's logo, or its initial. */
export function ToolkitLogo({
  toolkit,
}: {
  toolkit: Pick<Toolkit, "logo" | "name" | "toolkit">
}) {
  const icon = toolkitIcon(toolkit.toolkit)
  const remoteLogo = toolkit.logo && /^https?:\/\//.test(toolkit.logo)

  return (
    <span className="workspace-tool-logo" aria-hidden="true">
      {icon ? (
        <svg viewBox="0 0 24 24" fill={`#${icon.hex}`}>
          <path d={icon.path} />
        </svg>
      ) : remoteLogo ? (
        <img src={toolkit.logo} alt="" loading="lazy" />
      ) : (
        <span>{toolkit.name.slice(0, 1).toUpperCase()}</span>
      )}
    </span>
  )
}
