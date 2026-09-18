import { useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { ArrowRight, Check, Plus, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { FormField } from "../components/auth/form-field";
import { Alert, AlertDescription } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Card } from "../components/ui/card";
import { Label } from "../components/ui/label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "../components/ui/select";
import {
	authErrorMessage,
	createOrganization,
	inviteMember,
	type Organization,
} from "../lib/auth/client";
import {
	fieldErrors,
	type InviteMemberInput,
	inviteMemberSchema,
	type OrganizationInput,
	organizationSchema,
} from "../lib/auth/schemas";
import {
	ensureSession,
	reloadSession,
	selectOrganization,
} from "../lib/auth/session";

export const Route = createFileRoute("/onboarding")({
	// Creating an organization needs the signed-in member; like /app, the page
	// loads their session on the client.
	ssr: false,
	beforeLoad: async ({ context, location }) => {
		await ensureSession(context.queryClient, location.href);
	},
	component: OnboardingIntroPage,
	head: () => ({ meta: [{ title: "Welcome · The Weirdly ODD" }] }),
	pendingComponent: OnboardingPending,
});

type OnboardingStep = "intro" | "organization" | "member" | "complete";
type InviteDraft = InviteMemberInput & {
	id: string;
	status: "pending" | "sent";
};

const emptyInvite: InviteMemberInput = { email: "", role: "member" };

function OnboardingIntroPage() {
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const organizationNameRef = useRef<HTMLInputElement>(null);
	const memberEmailRef = useRef<HTMLInputElement>(null);
	const [step, setStep] = useState<OnboardingStep>("intro");
	const [organizationDraft, setOrganizationDraft] =
		useState<OrganizationInput>();
	const [createdOrganization, setCreatedOrganization] =
		useState<Organization>();
	const [organizationErrors, setOrganizationErrors] = useState<
		Partial<Record<keyof OrganizationInput, string>>
	>({});
	const [inviteDraft, setInviteDraft] =
		useState<InviteMemberInput>(emptyInvite);
	const [invitations, setInvitations] = useState<InviteDraft[]>([]);
	const [inviteErrors, setInviteErrors] = useState<
		Partial<Record<keyof InviteMemberInput, string>>
	>({});
	const [formError, setFormError] = useState("");
	const [submitting, setSubmitting] = useState(false);

	useEffect(() => {
		if (step === "organization") {
			organizationNameRef.current?.focus();
		} else if (step === "member") {
			memberEmailRef.current?.focus();
		}
	}, [step]);

	function submitOrganization(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setFormError("");

		const data = new FormData(event.currentTarget);
		const result = organizationSchema.safeParse({ name: data.get("name") });
		if (!result.success) {
			setOrganizationErrors(fieldErrors(result));
			return;
		}

		setOrganizationErrors({});
		setOrganizationDraft(result.data);
		setStep("member");
	}

	function addMember(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setFormError("");

		const result = inviteMemberSchema.safeParse(inviteDraft);
		if (!result.success) {
			setInviteErrors(fieldErrors(result));
			return;
		}

		const normalizedEmail = result.data.email.toLowerCase();
		if (
			invitations.some(
				(invitation) => invitation.email.toLowerCase() === normalizedEmail,
			)
		) {
			setInviteErrors({ email: "Already added" });
			return;
		}

		setInvitations((current) => [
			...current,
			{
				...result.data,
				id: crypto.randomUUID(),
				status: "pending",
			},
		]);
		setInviteErrors({});
		setInviteDraft(emptyInvite);
		memberEmailRef.current?.focus();
	}

	function removeMember(id: string) {
		setInvitations((current) =>
			current.filter((invitation) => invitation.id !== id),
		);
	}

	async function finishSetup() {
		if (!organizationDraft) return;

		setFormError("");
		setSubmitting(true);
		try {
			const organization =
				createdOrganization ?? (await createOrganization(organizationDraft));
			setCreatedOrganization(organization);
			selectOrganization(organization.id);

			for (const invitation of invitations) {
				if (invitation.status === "sent") continue;
				await inviteMember(organization.id, {
					email: invitation.email,
					role: invitation.role,
				});
				setInvitations((current) =>
					current.map((currentInvitation) =>
						currentInvitation.id === invitation.id
							? { ...currentInvitation, status: "sent" }
							: currentInvitation,
					),
				);
			}

			await reloadSession(queryClient);
			setStep("complete");
		} catch (error) {
			setFormError(authErrorMessage(error, "Could not finish setup"));
		} finally {
			setSubmitting(false);
		}
	}

	const isActive = step !== "intro";

	return (
		<main
			className={`onboarding-canvas${isActive ? " onboarding-canvas-active" : ""}`}
		>
			<header className="onboarding-brand">
				<div className="onboarding-mark" aria-hidden="true">
					O
				</div>
				<h1 className="onboarding-title" id="onboarding-title">
					The Weirdly <span>ODD</span>
				</h1>
			</header>

			<section className="onboarding-intro" aria-labelledby="onboarding-title">
				<p className="onboarding-copy">
					Giving you a weird way to do—and be weirdly good at your thing.
				</p>
				<Button
					className="onboarding-next"
					type="button"
					disabled={isActive}
					onClick={() => setStep("organization")}
					aria-label="Set up your organization"
					size="icon-lg"
					variant="outline"
				>
					<ArrowRight />
				</Button>
			</section>

			{step === "organization" ? (
				<section
					key="organization"
					className="onboarding-organization"
					aria-labelledby="organization-title"
				>
					<Card className="onboarding-form-card">
						<h2 className="auth-heading" id="organization-title">
							Name your organization
						</h2>

						<form
							className="auth-form onboarding-form"
							onSubmit={submitOrganization}
							noValidate
						>
							<FormField
								autoComplete="organization"
								error={organizationErrors.name}
								id="organization-name"
								inputRef={organizationNameRef}
								label="Organization name"
								name="name"
								placeholder="Your organization"
							/>
							<Button
								className="auth-button auth-button-primary"
								size="lg"
								type="submit"
							>
								Continue
							</Button>
						</form>
					</Card>
				</section>
			) : null}

			{step === "member" ? (
				<section
					key="member"
					className="onboarding-organization"
					aria-labelledby="member-title"
				>
					<Card className="onboarding-form-card onboarding-member-card">
						<h2 className="auth-heading" id="member-title">
							Add members
						</h2>

						{formError ? (
							<Alert
								className="auth-alert onboarding-form-error"
								variant="destructive"
							>
								<AlertDescription>{formError}</AlertDescription>
							</Alert>
						) : null}

						<form
							className="auth-form onboarding-form"
							onSubmit={addMember}
							noValidate
						>
							<div className="onboarding-member-fields">
								<FormField
									autoComplete="email"
									error={inviteErrors.email}
									id="member-email"
									inputRef={memberEmailRef}
									label="Email"
									name="email"
									onChange={(event) =>
										setInviteDraft((current) => ({
											...current,
											email: event.target.value,
										}))
									}
									placeholder="member@company.com"
									type="email"
									value={inviteDraft.email}
								/>
								<div className="field">
									<Label className="field-label" htmlFor="member-role">
										Role
									</Label>
									<Select
										value={inviteDraft.role}
										onValueChange={(role) =>
											setInviteDraft((current) => ({
												...current,
												role: role as InviteMemberInput["role"],
											}))
										}
									>
										<SelectTrigger className="field-input" id="member-role">
											<SelectValue />
										</SelectTrigger>
										<SelectContent>
											<SelectItem value="member">Member</SelectItem>
											<SelectItem value="admin">Admin</SelectItem>
										</SelectContent>
									</Select>
								</div>
							</div>

							<Button
								className="onboarding-add-member"
								type="submit"
								variant="outline"
							>
								<Plus data-icon="inline-start" />
								Add member
							</Button>
						</form>

						{invitations.length > 0 ? (
							<ul
								className="onboarding-member-list"
								aria-label="Members to invite"
							>
								{invitations.map((invitation) => (
									<li key={invitation.id}>
										<div className="onboarding-member-copy">
											<span>{invitation.email}</span>
											<small>
												{invitation.role}
												{invitation.status === "sent" ? " · sent" : ""}
											</small>
										</div>
										<Button
											aria-label={`Remove ${invitation.email}`}
											disabled={submitting || invitation.status === "sent"}
											onClick={() => removeMember(invitation.id)}
											size="icon-sm"
											type="button"
											variant="ghost"
										>
											{invitation.status === "sent" ? <Check /> : <Trash2 />}
										</Button>
									</li>
								))}
							</ul>
						) : null}

						<div className="onboarding-actions">
							<Button
								className="auth-button auth-button-primary"
								disabled={submitting}
								onClick={finishSetup}
								size="lg"
								type="button"
							>
								{submitting ? "Finishing…" : "Continue"}
							</Button>
							{invitations.length === 0 ? (
								<Button
									className="onboarding-skip"
									disabled={submitting}
									onClick={finishSetup}
									type="button"
									variant="ghost"
								>
									Skip for now
								</Button>
							) : null}
						</div>
					</Card>
				</section>
			) : null}

			{step === "complete" && createdOrganization ? (
				<section
					key="complete"
					className="onboarding-organization"
					aria-label="Setup complete"
				>
					<Card className="onboarding-form-card">
						<Alert className="auth-success">
							<Check />
							<AlertDescription>
								{createdOrganization.name} is ready.
							</AlertDescription>
						</Alert>
						<p className="onboarding-complete-note">
							Connect your apps any time from{" "}
							<Link to="/app/connections">Connections</Link>.
						</p>
						<div className="onboarding-actions">
							<Button
								className="auth-button auth-button-primary"
								onClick={() => navigate({ to: "/app" })}
								size="lg"
								type="button"
							>
								Open workspace
							</Button>
						</div>
					</Card>
				</section>
			) : null}
		</main>
	);
}

function OnboardingPending() {
	return <main aria-busy="true" className="onboarding-canvas" />;
}
