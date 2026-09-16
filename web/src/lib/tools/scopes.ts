import type { CredentialTypeDescription } from "@/lib/connections/client";

/** The scopes typed into a field, however they were separated. */
export function splitScopes(value: string): string[] {
	return value.split(/[\s,]+/).filter(Boolean);
}

/**
 * Why connections would refuse these scopes, in the words of the person who
 * typed them. It checks what the service checks, so the dialog can say which
 * scope is wrong instead of relaying "scope … is missing".
 */
export function scopeProblem(
	scopes: string[],
	method: CredentialTypeDescription,
): string | undefined {
	const allowed = method.allowed_scopes ?? [];
	const exclusive = method.exclusive_scopes ?? [];
	const outside = scopes.filter(
		(scope) => allowed.length > 0 && !allowed.includes(scope),
	);
	if (outside.length > 0) {
		return `${list(outside)} ${outside.length === 1 ? "is not a scope" : "are not scopes"} this tool offers.`;
	}
	const alone = scopes.filter((scope) => exclusive.includes(scope));
	if (alone.length > 0 && scopes.length > 1) {
		return `${list(alone)} must be the only scope, because the provider refuses it beside others.`;
	}
	return undefined;
}

function list(values: string[]): string {
	if (values.length <= 2) {
		return values.join(" and ");
	}
	return `${values.slice(0, -1).join(", ")} and ${values[values.length - 1]}`;
}
