import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { ApiError } from "../lib/api/client";
import { authErrorMessage, resetPassword } from "../lib/auth/client";
import {
	fieldErrors,
	type ResetPasswordInput,
	resetPasswordSchema,
} from "../lib/auth/schemas";
import { searchToken } from "../lib/auth/session";

interface ResetPasswordSearch {
	token?: string;
}

export const Route = createFileRoute("/auth/reset-password")({
	validateSearch: (search: Record<string, unknown>): ResetPasswordSearch => ({
		token: searchToken(search.token),
	}),
	component: ResetPasswordPage,
	head: () => ({
		meta: [{ title: "Choose a new password · The Weirdly ODD" }],
	}),
});

type Reset = "form" | "done" | "expired" | "invalid";

function ResetPasswordPage() {
	const { token } = Route.useSearch();
	const [reset, setReset] = useState<Reset>(token ? "form" : "invalid");
	const [errors, setErrors] = useState<
		Partial<Record<keyof ResetPasswordInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [submitting, setSubmitting] = useState(false);

	async function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!token) return;
		setFormError("");

		const data = new FormData(event.currentTarget);
		const result = resetPasswordSchema.safeParse({
			password: data.get("password"),
		});
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}

		setErrors({});
		setSubmitting(true);
		try {
			await resetPassword(token, result.data.password);
			setReset("done");
		} catch (error) {
			if (error instanceof ApiError && error.status === 410) {
				setReset("expired");
			} else if (error instanceof ApiError && error.status === 404) {
				setReset("invalid");
			} else {
				setFormError(authErrorMessage(error, "Could not update your password"));
			}
		} finally {
			setSubmitting(false);
		}
	}

	if (reset === "done") {
		return (
			<AuthShell>
				<h1 className="auth-heading">Password updated</h1>
				<p className="auth-subheading">
					Sign in with your new password. Your other sessions will be signed
					out.
				</p>
				<Button asChild className="auth-button auth-button-primary" size="lg">
					<Link to="/auth/sign-in">Sign in</Link>
				</Button>
			</AuthShell>
		);
	}

	if (reset === "expired" || reset === "invalid") {
		return (
			<AuthShell>
				<h1 className="auth-heading">
					{reset === "expired" ? "Link expired" : "Link not valid"}
				</h1>
				<p className="auth-subheading">
					{reset === "expired"
						? "Reset links work for 30 minutes."
						: "It was already used, or the link is incomplete."}
				</p>
				<Button asChild className="auth-button auth-button-primary" size="lg">
					<Link to="/auth/forgot-password">Send a new link</Link>
				</Button>
			</AuthShell>
		);
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Choose a new password</h1>
			<p className="auth-subheading">
				Use at least 12 characters, with upper and lowercase letters, a number,
				and a symbol.
			</p>

			{formError ? (
				<Alert className="auth-alert" variant="destructive">
					<AlertDescription>{formError}</AlertDescription>
				</Alert>
			) : null}

			<form className="auth-form" onSubmit={submit} noValidate>
				<FormField
					autoComplete="new-password"
					error={errors.password}
					id="reset-password"
					label="New password"
					name="password"
					type="password"
				/>
				<Button
					className="auth-button auth-button-primary"
					disabled={submitting}
					size="lg"
					type="submit"
				>
					{submitting ? "Updating…" : "Update password"}
				</Button>
			</form>
		</AuthShell>
	);
}
