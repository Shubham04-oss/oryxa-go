import { createFileRoute, Link } from "@tanstack/react-router";
import { useEffect, useRef, useState } from "react";
import { AuthShell } from "../components/auth/auth-shell";
import { FormField } from "../components/auth/form-field";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { ApiError } from "../lib/api/client";
import {
	authErrorMessage,
	resendVerification,
	verifyEmail,
} from "../lib/auth/client";
import { type EmailInput, emailSchema, fieldErrors } from "../lib/auth/schemas";
import { searchToken } from "../lib/auth/session";

interface VerifyEmailSearch {
	token?: string;
}

export const Route = createFileRoute("/auth/verify-email")({
	validateSearch: (search: Record<string, unknown>): VerifyEmailSearch => ({
		token: searchToken(search.token),
	}),
	component: VerifyEmailPage,
	head: () => ({ meta: [{ title: "Verify email · The Weirdly ODD" }] }),
});

type Verification =
	| "verifying"
	| "verified"
	| "expired"
	| "invalid"
	| "unavailable";

function failedVerification(error: unknown): Verification {
	if (!(error instanceof ApiError) || error.status >= 500) {
		return "unavailable";
	}
	return error.status === 410 ? "expired" : "invalid";
}

function VerifyEmailPage() {
	const { token } = Route.useSearch();
	const [verification, setVerification] = useState<Verification>(
		token ? "verifying" : "invalid",
	);
	const [attempt, setAttempt] = useState(0);
	const redeemedAttempt = useRef(-1);

	useEffect(() => {
		// A link verifies once, so an effect that runs again must not redeem it again.
		if (!token || redeemedAttempt.current === attempt) {
			return;
		}
		redeemedAttempt.current = attempt;
		setVerification("verifying");
		verifyEmail(token).then(
			() => setVerification("verified"),
			(error: unknown) => setVerification(failedVerification(error)),
		);
	}, [attempt, token]);

	if (verification === "verified") {
		return (
			<AuthShell>
				<h1 className="auth-heading">Email verified</h1>
				<p className="auth-subheading">
					Your account is ready. Sign in to get started.
				</p>
				<Button asChild className="auth-button auth-button-primary" size="lg">
					<Link to="/auth/sign-in">Sign in</Link>
				</Button>
			</AuthShell>
		);
	}

	if (verification === "expired" || verification === "invalid") {
		return <ResendVerification reason={verification} />;
	}

	if (verification === "unavailable") {
		return (
			<AuthShell>
				<h1 className="auth-heading">Could not verify yet</h1>
				<p className="auth-subheading">
					The Weirdly ODD could not be reached. Try again in a moment.
				</p>
				<Button
					className="auth-button auth-button-primary"
					onClick={() => setAttempt((current) => current + 1)}
					size="lg"
					type="button"
				>
					Try again
				</Button>
			</AuthShell>
		);
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">Verifying your email</h1>
			<p className="auth-subheading">This takes a moment.</p>
		</AuthShell>
	);
}

function ResendVerification({ reason }: { reason: "expired" | "invalid" }) {
	const [errors, setErrors] = useState<
		Partial<Record<keyof EmailInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [sentTo, setSentTo] = useState("");
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
			await resendVerification(result.data.email);
			setSentTo(result.data.email);
		} catch (error) {
			setFormError(authErrorMessage(error, "Could not send a new link"));
		} finally {
			setSubmitting(false);
		}
	}

	return (
		<AuthShell>
			<h1 className="auth-heading">
				{reason === "expired" ? "Link expired" : "Link not valid"}
			</h1>
			<p className="auth-subheading">
				{reason === "expired"
					? "Verification links work for 24 hours."
					: "It was already used, or the link is incomplete."}{" "}
				Send yourself a new one.
			</p>

			{sentTo ? (
				<Alert className="auth-success">
					<AlertDescription>
						Check {sentTo} for a new verification link.
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
							id="verify-email"
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
							{submitting ? "Sending…" : "Send new link"}
						</Button>
					</form>
				</>
			)}

			<p className="auth-footer">
				Already verified?{" "}
				<Link className="auth-link" to="/auth/sign-in">
					Sign in
				</Link>
			</p>
		</AuthShell>
	);
}
