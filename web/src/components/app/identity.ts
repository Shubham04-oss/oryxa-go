import type { Role, User } from "@/lib/auth/client";

/** One or two initials to mark an organization or person: "Odd Works" is OW. */
export function initials(name: string): string {
	const words = name.trim().split(/\s+/).filter(Boolean);
	const marked = words.length > 1 ? [words[0], words[words.length - 1]] : words;
	return (
		marked.map((word) => Array.from(word)[0].toUpperCase()).join("") || "?"
	);
}

export function displayName(user: Pick<User, "display_name" | "email">) {
	return user.display_name?.trim() || user.email;
}

const roleLabels: Record<Role, string> = {
	admin: "Admin",
	member: "Member",
	owner: "Owner",
};

export function roleLabel(role: Role): string {
	return roleLabels[role];
}
