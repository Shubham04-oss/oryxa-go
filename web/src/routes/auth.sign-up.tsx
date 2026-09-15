import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { GoogleButton } from "../components/auth/google-button";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { AuthRequestError, signUp } from "../lib/auth/client";
import {
	fieldErrors,
	type SignUpInput,
	signUpSchema,
} from "../lib/auth/schemas";

export const Route = createFileRoute("/auth/sign-up")({
	component: SignUpPage,
	head: () => ({ meta: [{ title: "Create account · The Weirdly ODD" }] }),
});

function SignUpPage() {
	const [errors, setErrors] = useState<
		Partial<Record<keyof SignUpInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [submittedEmail, setSubmittedEmail] = useState("");
	const [submitting, setSubmitting] = useState(false);

	async function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setFormError("");

		const data = new FormData(event.currentTarget);
		const result = signUpSchema.safeParse({
			displayName: data.get("displayName"),
			email: data.get("email"),
			password: data.get("password"),
		});

		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}

		setErrors({});
		setSubmitting(true);
		try {
			await signUp(result.data);
			setSubmittedEmail(result.data.email);
		} catch (error) {
			setFormError(
				error instanceof AuthRequestError
					? error.message
					: "Could not create account",
			);
		} finally {
			setSubmitting(false);
		}
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Create account</h1>
			<p className="auth-subheading">Start with your work email.</p>

			{submittedEmail ? (
				<Alert className="auth-success">
					<AlertDescription>
						Check {submittedEmail} to verify your account.
					</AlertDescription>
				</Alert>
			) : (
				<>
					<GoogleButton returnTo="/onboarding" />
					<div className="auth-divider">or</div>

					{formError ? (
						<Alert className="auth-alert" variant="destructive">
							<AlertDescription>{formError}</AlertDescription>
						</Alert>
					) : null}

					<form className="auth-form" onSubmit={submit} noValidate>
						<FormField
							autoComplete="name"
							error={errors.displayName}
							id="sign-up-name"
							label="Name"
							name="displayName"
							placeholder="Your name"
						/>
						<FormField
							autoComplete="email"
							error={errors.email}
							id="sign-up-email"
							label="Email"
							name="email"
							placeholder="you@company.com"
							type="email"
						/>
						<FormField
							autoComplete="new-password"
							error={errors.password}
							id="sign-up-password"
							label="Password"
							name="password"
							type="password"
						/>
						<Button
							className="auth-button auth-button-primary"
							size="lg"
							type="submit"
							disabled={submitting}
						>
							{submitting ? "Creating…" : "Create account"}
						</Button>
					</form>
				</>
			)}

			<p className="auth-footer">
				Already have an account?{" "}
				<Link className="auth-link" to="/auth/sign-in">
					Sign in
				</Link>
			</p>
		</AuthShell>
	);
}
