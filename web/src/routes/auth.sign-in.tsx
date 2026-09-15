import { createFileRoute, Link } from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { GoogleButton } from "../components/auth/google-button";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { AuthRequestError, signIn } from "../lib/auth/client";
import {
	fieldErrors,
	type SignInInput,
	signInSchema,
} from "../lib/auth/schemas";

export const Route = createFileRoute("/auth/sign-in")({
	component: SignInPage,
	head: () => ({ meta: [{ title: "Sign in · The Weirdly ODD" }] }),
});

function SignInPage() {
	const [errors, setErrors] = useState<
		Partial<Record<keyof SignInInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [submitting, setSubmitting] = useState(false);

	async function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setFormError("");

		const data = new FormData(event.currentTarget);
		const result = signInSchema.safeParse({
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
			await signIn(result.data);
			window.location.assign("/onboarding");
		} catch (error) {
			setFormError(
				error instanceof AuthRequestError ? error.message : "Could not sign in",
			);
			setSubmitting(false);
		}
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Welcome back</h1>
			<p className="auth-subheading">Sign in to The Weirdly ODD.</p>

			<GoogleButton returnTo="/onboarding" />
			<div className="auth-divider">or</div>

			{formError ? (
				<Alert className="auth-alert" variant="destructive">
					<AlertDescription>{formError}</AlertDescription>
				</Alert>
			) : null}

			<form className="auth-form" onSubmit={submit} noValidate>
				<FormField
					autoComplete="email"
					error={errors.email}
					id="sign-in-email"
					label="Email"
					name="email"
					placeholder="you@company.com"
					type="email"
				/>
				<FormField
					autoComplete="current-password"
					error={errors.password}
					id="sign-in-password"
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
					{submitting ? "Signing in…" : "Sign in"}
				</Button>
			</form>

			<p className="auth-footer">
				New here?{" "}
				<Link className="auth-link" to="/auth/sign-up">
					Create account
				</Link>
			</p>
		</AuthShell>
	);
}
