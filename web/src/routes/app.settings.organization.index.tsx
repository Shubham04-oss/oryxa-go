import { useMutation, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import { useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import {
	SettingsPageBody,
	SettingsRow,
	SettingsSection,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { apiErrorMessage } from "@/lib/api/client";
import { deleteOrganization, updateOrganization } from "@/lib/auth/client";
import {
	type OrganizationProfileInput,
	organizationProfileSchema,
	organizationQueryKey,
} from "@/lib/auth/organization";
import { fieldErrors } from "@/lib/auth/schemas";
import {
	reloadSession,
	selectOrganization,
	useMembership,
	useWorkspace,
} from "@/lib/auth/session";

export const Route = createFileRoute("/app/settings/organization/")({
	component: OrganizationGeneralPage,
	head: () => ({ meta: [{ title: "Organization · The Weirdly ODD" }] }),
});

function OrganizationGeneralPage() {
	const { organization } = useWorkspace();
	const isOwner = useMembership(organization.id)?.role === "owner";

	return (
		<WorkspacePage
			description={`Identity and ownership for ${organization.name}.`}
			title="Organization"
		>
			<SettingsPageBody>
				<OrganizationProfile editable={isOwner} key={organization.id} />
				{isOwner ? <DeleteOrganization /> : null}
			</SettingsPageBody>
		</WorkspacePage>
	);
}

function OrganizationProfile({ editable }: { editable: boolean }) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const [name, setName] = useState(organization.name);
	const [slug, setSlug] = useState(organization.slug);
	const [errors, setErrors] = useState<
		Partial<Record<keyof OrganizationProfileInput, string>>
	>({});
	const [saved, setSaved] = useState(false);
	const save = useMutation({
		mutationFn: (input: OrganizationProfileInput) =>
			updateOrganization(organization.id, input),
		onSuccess: async (updated) => {
			setName(updated.name);
			setSlug(updated.slug);
			await Promise.all([
				reloadSession(queryClient),
				queryClient.invalidateQueries({
					queryKey: organizationQueryKey(organization.id),
				}),
			]);
			setSaved(true);
		},
	});
	const changed = name !== organization.name || slug !== organization.slug;

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setSaved(false);
		const result = organizationProfileSchema.safeParse({ name, slug });
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}
		setErrors({});
		save.mutate(result.data);
	}

	return (
		<form noValidate onSubmit={submit}>
			<SettingsSection
				description="The name and handle shown throughout the workspace."
				footer={
					editable ? (
						<>
							<span>
								{saved ? "Saved." : "Visible to every organization member."}
							</span>
							<Button disabled={!changed || save.isPending} type="submit">
								{save.isPending ? "Saving…" : "Save changes"}
							</Button>
						</>
					) : (
						<span>Only organization owners can change these.</span>
					)
				}
				id="organization-profile"
				title="Profile"
			>
				{save.isError ? (
					<div className="settings-section-alert">
						<AppAlert>
							{apiErrorMessage(save.error, "Could not save the organization")}
						</AppAlert>
					</div>
				) : null}
				<SettingsRow
					description="Used in navigation, invitations, and emails."
					htmlFor="organization-name"
					title="Organization name"
				>
					<div className="settings-field">
						<Input
							aria-invalid={Boolean(errors.name)}
							disabled={!editable}
							id="organization-name"
							onChange={(event) => {
								setName(event.target.value);
								setSaved(false);
							}}
							value={name}
						/>
						{errors.name ? <p className="field-error">{errors.name}</p> : null}
					</div>
				</SettingsRow>
				<SettingsRow
					description="Lowercase letters, numbers, and dashes, for links and system events."
					htmlFor="organization-handle"
					title="Handle"
				>
					<div className="settings-field">
						<div className="settings-input-prefix">
							<span>/</span>
							<Input
								aria-invalid={Boolean(errors.slug)}
								disabled={!editable}
								id="organization-handle"
								onChange={(event) => {
									setSlug(event.target.value);
									setSaved(false);
								}}
								value={slug}
							/>
						</div>
						{errors.slug ? <p className="field-error">{errors.slug}</p> : null}
					</div>
				</SettingsRow>
			</SettingsSection>
		</form>
	);
}

function DeleteOrganization() {
	const { organization, organizations } = useWorkspace();
	const queryClient = useQueryClient();
	const navigate = useNavigate();

	async function remove() {
		await deleteOrganization(organization.id);
		const next = organizations.find(
			(candidate) => candidate.id !== organization.id,
		);
		if (next) {
			selectOrganization(next.id);
		}
		await reloadSession(queryClient);
		await navigate({ to: "/app" });
	}

	return (
		<SettingsSection
			description="Deleting removes the organization for everyone, with its teams, files, connections, and subscription."
			id="organization-delete"
			title="Delete organization"
		>
			<SettingsRow
				description="This cannot be undone. Every member loses access at once."
				title={`Delete ${organization.name}`}
			>
				<ConfirmDialog
					confirmLabel="Delete organization"
					confirmation={organization.name}
					description={`Everything in ${organization.name} is deleted for every member, and its subscription is cancelled.`}
					onConfirm={remove}
					title={`Delete ${organization.name}?`}
					trigger={
						<Button type="button" variant="destructive">
							Delete organization
						</Button>
					}
				/>
			</SettingsRow>
		</SettingsSection>
	);
}
