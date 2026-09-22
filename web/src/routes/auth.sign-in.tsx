import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { GoogleButton } from "../components/auth/google-button";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { ApiError } from "../lib/api/client";
import { authErrorMessage, signIn } from "../lib/auth/client";
import { passwordOnlyDemo } from "../lib/auth/demo";
import {
	fieldErrors,
	type SignInInput,
	signInSchema,
} from "../lib/auth/schemas";
import { isInvitationRedirect, safeRedirect } from "../lib/auth/session";

interface SignInSearch {
	redirect?: string;
}

export const Route = createFileRoute("/auth/sign-in")({
	validateSearch: (search: Record<string, unknown>): SignInSearch => ({
		redirect: safeRedirect(search.redirect),
	}),
	component: SignInPage,
	head: () => ({ meta: [{ title: "Sign in · The Weirdly ODD" }] }),
});

function SignInPage() {
	const { redirect } = Route.useSearch();
	const destination = redirect ?? "/app";
	const navigate = useNavigate();
	const queryClient = useQueryClient();
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
			// Nothing cached may belong to whoever used this browser before.
			queryClient.clear();
			await navigate({ href: destination });
		} catch (error) {
			setFormError(
				error instanceof ApiError && error.status === 401
					? "Email or password is incorrect."
					: authErrorMessage(error, "Could not sign in"),
			);
			setSubmitting(false);
		}
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Welcome back</h1>
			<p className="auth-subheading">
				{isInvitationRedirect(redirect)
					? "Sign in with the email address your invitation was sent to."
					: passwordOnlyDemo
						? "Demo sign-in uses email and password. New here? Create an account below."
						: "Sign in to The Weirdly ODD."}
			</p>

			{!passwordOnlyDemo ? (
				<>
					<GoogleButton returnTo={destination} />
					<div className="auth-divider">or</div>
				</>
			) : null}

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
				<div className="auth-field-link">
					<Link className="auth-link" to="/auth/forgot-password">
						Forgot password?
					</Link>
				</div>
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
				<Link className="auth-link" search={{ redirect }} to="/auth/sign-up">
					Create account
				</Link>
			</p>
		</AuthShell>
	);
}
