import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { authErrorMessage, requestPasswordReset } from "../lib/auth/client";
import { type EmailInput, emailSchema, fieldErrors } from "../lib/auth/schemas";

export const Route = createFileRoute("/auth/forgot-password")({
	component: ForgotPasswordPage,
	head: () => ({ meta: [{ title: "Reset password · The Weirdly ODD" }] }),
});

function ForgotPasswordPage() {
	const [errors, setErrors] = useState<
		Partial<Record<keyof EmailInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [requestedFor, setRequestedFor] = useState("");
	const [submitting, setSubmitting] = useState(false);

	async function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setFormError("");

		const data = new FormData(event.currentTarget);
		const result = emailSchema.safeParse({ email: data.get("email") });
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}

		setErrors({});
		setSubmitting(true);
		try {
			await requestPasswordReset(result.data.email);
			setRequestedFor(result.data.email);
		} catch (error) {
			setFormError(authErrorMessage(error, "Could not send a reset link"));
		} finally {
			setSubmitting(false);
		}
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Reset your password</h1>
			<p className="auth-subheading">
				We will email you a link to choose a new one.
			</p>

			{requestedFor ? (
				<Alert className="auth-success">
					<AlertDescription>
						If {requestedFor} has an account, a reset link is on its way. It
						works for 30 minutes.
					</AlertDescription>
				</Alert>
			) : (
				<>
					{formError ? (
						<Alert className="auth-alert" variant="destructive">
							<AlertDescription>{formError}</AlertDescription>
						</Alert>
					) : null}

					<form className="auth-form" onSubmit={submit} noValidate>
						<FormField
							autoComplete="email"
							error={errors.email}
							id="reset-email"
							label="Email"
							name="email"
							placeholder="you@company.com"
							type="email"
						/>
						<Button
							className="auth-button auth-button-primary"
							disabled={submitting}
							size="lg"
							type="submit"
						>
							{submitting ? "Sending…" : "Send reset link"}
						</Button>
					</form>
				</>
			)}

			<p className="auth-footer">
				Remembered it?{" "}
				<Link className="auth-link" to="/auth/sign-in">
					Sign in
				</Link>
			</p>
		</AuthShell>
	);
}
