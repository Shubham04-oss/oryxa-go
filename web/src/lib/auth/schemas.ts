import { z } from "zod";

const email = z.string().trim().email("Enter a valid email");

const password = z
	.string()
	.min(12, "Use at least 12 characters")
	.regex(/[a-z]/, "Add a lowercase letter")
	.regex(/[A-Z]/, "Add an uppercase letter")
	.regex(/[0-9]/, "Add a number")
	.regex(/[^A-Za-z0-9]/, "Add a symbol");

export const signInSchema = z.object({
	email,
	password: z.string().min(1, "Enter your password"),
});

export const signUpSchema = z.object({
	displayName: z
		.string()
		.trim()
		.min(2, "Enter your name")
		.max(80, "Name is too long"),
	email,
	password,
});

export const organizationSchema = z.object({
	name: z
		.string()
		.trim()
		.min(1, "Enter your organization name")
		.max(120, "Organization name is too long"),
});

export const inviteMemberSchema = z.object({
	email,
	role: z.enum(["member", "admin"]),
});

export type SignInInput = z.infer<typeof signInSchema>;
export type SignUpInput = z.infer<typeof signUpSchema>;
export type OrganizationInput = z.infer<typeof organizationSchema>;
export type InviteMemberInput = z.infer<typeof inviteMemberSchema>;

export function fieldErrors<T extends Record<string, unknown>>(
	result: z.ZodSafeParseError<T>,
): Partial<Record<keyof T, string>> {
	const errors: Partial<Record<keyof T, string>> = {};
	for (const issue of result.error.issues) {
		const field = issue.path[0] as keyof T | undefined;
		if (field !== undefined && errors[field] === undefined) {
			errors[field] = issue.message;
		}
	}
	return errors;
}
