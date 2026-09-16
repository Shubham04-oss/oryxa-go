import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, useNavigate } from "@tanstack/react-router";
import {
	Download,
	FileArchive,
	File as FileGlyph,
	FileImage,
	Files,
	FileText,
	type LucideIcon,
	Trash2,
} from "lucide-react";
import { type ReactNode, useState } from "react";
import { QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { UploadFileDialog } from "@/components/files/upload-file-dialog";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { apiErrorMessage } from "@/lib/api/client";
import { managesMembers } from "@/lib/auth/client";
import { useMembership, useWorkspace } from "@/lib/auth/session";
import {
	canDeleteFile,
	deleteFile,
	type FileFilter,
	type FileViewer,
	fileContentUrl,
	filesQueryKey,
	filesQueryOptions,
	formatBytes,
	getFile,
	type StoredFile,
} from "@/lib/files/client";
import { daysUntil, formatDate } from "@/lib/format";
import { teamsQueryOptions, teamTrails } from "@/lib/teams/client";

type FilesView = "all" | "member" | "team" | "organization";

const views: { label: string; value: FilesView }[] = [
	{ label: "Everything", value: "all" },
	{ label: "Mine", value: "member" },
	{ label: "Teams", value: "team" },
	{ label: "Organization", value: "organization" },
];

const emptyDescriptions: Record<FilesView, string> = {
	all: "Upload a file to share context with your assistants and your team.",
	member: "Files only you and organization admins can see show up here.",
	organization: "Files everyone in the organization can see show up here.",
	team: "Files for this team show up here.",
};

interface FilesSearch {
	team?: string;
	view?: FilesView;
}

export const Route = createFileRoute("/app/files")({
	validateSearch: (search: Record<string, unknown>): FilesSearch => ({
		team:
			typeof search.team === "string" && search.team !== ""
				? search.team
				: undefined,
		view: views.find((view) => view.value === search.view)?.value,
	}),
	component: FilesPage,
	head: () => ({ meta: [{ title: "Files · The Weirdly ODD" }] }),
});

function fileFilter(view: FilesView, teamId: string | undefined): FileFilter {
	if (view === "all") {
		return {};
	}
	return view === "team" ? { scope: "team", teamId } : { scope: view };
}

function FilesPage() {
	const { organization, user } = useWorkspace();
	const { team: teamId, view = "all" } = Route.useSearch();
	const navigate = useNavigate({ from: Route.fullPath });
	const isOrgAdmin = managesMembers(useMembership(organization.id)?.role);
	const teams = useQuery(teamsQueryOptions(organization.id));
	const visibleTeams = teams.data ?? [];
	const trails = teamTrails(visibleTeams);
	const chosenTeam =
		view === "team"
			? (visibleTeams.find((team) => team.id === teamId) ?? visibleTeams[0])
			: undefined;
	const files = useQuery({
		...filesQueryOptions(organization.id, fileFilter(view, chosenTeam?.id)),
		enabled: view !== "team" || chosenTeam !== undefined,
	});
	const viewer: FileViewer = {
		isOrgAdmin,
		manageableTeamIds: new Set(
			visibleTeams.filter((team) => team.can_manage).map((team) => team.id),
		),
		userId: user.id,
	};

	let content: ReactNode;
	if (view === "team" && teams.isSuccess && visibleTeams.length === 0) {
		content = (
			<EmptyState
				description="Team files show up here once you are on a team."
				icon={Files}
				title="No teams to show"
			/>
		);
	} else if (files.isPending) {
		content = <SettingsListSkeleton />;
	} else if (files.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={files.error}
					fallback="Could not load files"
					onRetry={files.refetch}
				/>
			</div>
		);
	} else if (files.data.length === 0) {
		content = (
			<EmptyState
				description={emptyDescriptions[view]}
				icon={Files}
				title="No files here yet"
			/>
		);
	} else {
		content = (
			<div className="settings-list">
				{[...files.data]
					.sort((a, b) => b.created_at.localeCompare(a.created_at))
					.map((file) => (
						<FileRow
							canDelete={canDeleteFile(file, viewer)}
							file={file}
							key={file.id}
							teamName={file.team_id ? trails.get(file.team_id) : undefined}
						/>
					))}
			</div>
		);
	}

	return (
		<WorkspacePage
			actions={
				<UploadFileDialog
					canUseOrganization={isOrgAdmin}
					defaultScope={view === "all" ? "member" : view}
					defaultTeamId={chosenTeam?.id}
					key={`${view}-${chosenTeam?.id ?? ""}`}
					organizationId={organization.id}
					teams={visibleTeams}
				/>
			}
			description="Shared context and artifacts for your work. Files are removed when their time runs out."
			title="Files"
		>
			<div className="page-toolbar">
				<fieldset className="segmented">
					<legend className="sr-only">Show</legend>
					{views.map((option) => (
						<Button
							aria-pressed={view === option.value}
							data-active={view === option.value}
							key={option.value}
							onClick={() =>
								navigate({
									search: {
										team: option.value === "team" ? chosenTeam?.id : undefined,
										view: option.value === "all" ? undefined : option.value,
									},
								})
							}
							size="sm"
							type="button"
							variant="ghost"
						>
							{option.label}
						</Button>
					))}
				</fieldset>
				{view === "team" && visibleTeams.length > 0 ? (
					<Select
						onValueChange={(value) =>
							navigate({ search: { team: value, view: "team" } })
						}
						value={chosenTeam?.id ?? ""}
					>
						<SelectTrigger
							aria-label="Team"
							className="page-toolbar-select"
							size="sm"
						>
							<SelectValue />
						</SelectTrigger>
						<SelectContent>
							{visibleTeams.map((team) => (
								<SelectItem key={team.id} value={team.id}>
									{trails.get(team.id) ?? team.name}
								</SelectItem>
							))}
						</SelectContent>
					</Select>
				) : null}
			</div>
			<Card className="workspace-panel">{content}</Card>
		</WorkspacePage>
	);
}

interface FileRowProps {
	canDelete: boolean;
	file: StoredFile;
	teamName: string | undefined;
}

function FileRow({ canDelete, file, teamName }: FileRowProps) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const [downloadError, setDownloadError] = useState("");
	const Icon = fileIcon(file.content_type);
	const days = daysUntil(file.expires_at);

	async function download() {
		setDownloadError("");
		try {
			// The download itself is a plain navigation that cannot refresh an
			// expired session, so a request that can goes first.
			await getFile(organization.id, file.id);
			const link = document.createElement("a");
			link.href = fileContentUrl(organization.id, file.id);
			link.download = file.filename;
			link.click();
		} catch (error) {
			setDownloadError(apiErrorMessage(error, "Could not download the file"));
		}
	}

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<Icon />
			</span>
			<div className="settings-list-copy">
				<strong>{file.filename}</strong>
				<small>
					{[
						formatBytes(file.size_bytes),
						placementName(file, teamName),
						`added ${formatDate(file.created_at)}`,
					].join(" · ")}
				</small>
				{downloadError ? (
					<small className="settings-list-error">{downloadError}</small>
				) : null}
			</div>
			<StatusBadge tone={days <= 3 ? "warning" : "neutral"}>
				{expiryText(days)}
			</StatusBadge>
			<Button
				aria-label={`Download ${file.filename}`}
				onClick={download}
				size="icon-sm"
				type="button"
				variant="ghost"
			>
				<Download />
			</Button>
			{canDelete ? (
				<ConfirmDialog
					confirmLabel="Delete file"
					description={`${file.filename} is deleted for everyone who can see it.`}
					onConfirm={async () => {
						await deleteFile(organization.id, file.id);
						await queryClient.invalidateQueries({
							queryKey: filesQueryKey(organization.id),
						});
					}}
					title="Delete this file?"
					trigger={
						<Button
							aria-label={`Delete ${file.filename}`}
							size="icon-sm"
							type="button"
							variant="ghost"
						>
							<Trash2 />
						</Button>
					}
				/>
			) : null}
		</div>
	);
}

function fileIcon(contentType: string): LucideIcon {
	if (contentType.startsWith("image/")) {
		return FileImage;
	}
	if (contentType === "application/zip") {
		return FileArchive;
	}
	if (
		contentType.startsWith("text/") ||
		contentType === "application/json" ||
		contentType === "application/pdf"
	) {
		return FileText;
	}
	return FileGlyph;
}

function placementName(file: StoredFile, teamName: string | undefined): string {
	switch (file.scope) {
		case "team":
			return teamName ? `Team: ${teamName}` : "Team";
		case "organization":
			return "Organization";
		default:
			return "Personal";
	}
}

function expiryText(days: number): string {
	if (days <= 0) {
		return "Expires today";
	}
	return days === 1 ? "Expires tomorrow" : `Expires in ${days} days`;
}
