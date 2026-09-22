import { Blocks } from "lucide-react";
import { useState } from "react";

interface ToolkitIconProps {
	logo?: string;
	/** "lg" is the card size; "sm" — the default — is the one lists use. */
	size?: "lg" | "sm";
}

/** An app's catalog logo, falling back to a generic mark when it has none or fails to load. */
export function ToolkitIcon({ logo, size = "sm" }: ToolkitIconProps) {
	const [failed, setFailed] = useState(false);

	return (
		<span
			aria-hidden="true"
			className="settings-list-icon apps-toolkit-icon"
			data-size={size}
		>
			{logo && !failed ? (
				<img alt="" onError={() => setFailed(true)} src={logo} />
			) : (
				<Blocks />
			)}
		</span>
	);
}
