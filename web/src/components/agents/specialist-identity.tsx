function avatarTone(seed: string): number {
	let value = 0;
	for (const character of seed) {
		value = (value * 31 + (character.codePointAt(0) ?? 0)) >>> 0;
	}
	return value % 6;
}

export function SpecialistAvatar({
	name,
	seed,
	size = "default",
}: {
	name: string;
	seed: string;
	size?: "default" | "small" | "large";
}) {
	const initials =
		name
			.trim()
			.split(/\s+/)
			.slice(0, 2)
			.map((part) => part[0]?.toLocaleUpperCase() ?? "")
			.join("") || "✦";
	return (
		<span
			aria-label={`${name || "New specialist"} avatar`}
			className="specialist-avatar"
			data-size={size}
			data-tone={avatarTone(seed)}
			role="img"
		>
			<span aria-hidden="true" className="specialist-avatar-orbit" />
			<span aria-hidden="true" className="specialist-avatar-initials">
				{initials}
			</span>
		</span>
	);
}
