import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Upload } from "lucide-react";
import { useId, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogClose,
	DialogContent,
	DialogDescription,
	DialogFooter,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import {
	Select,
	SelectContent,
	SelectItem,
	SelectTrigger,
	SelectValue,
} from "@/components/ui/select";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import {
	type FileScope,
	filesQueryKey,
	formatBytes,
	retentionChoices,
	type UploadInput,
	uploadFile,
	uploadProblem,
} from "@/lib/files/client";
import { type TeamSummary, teamTrails } from "@/lib/teams/client";

const scopeHints: Record<FileScope, string> = {
	member: "You, and organization admins.",
	organization: "Everyone in the organization.",
	team: "Everyone who can see the team.",
};

interface UploadFileDialogProps {
	/** Only organization admins add organization files. */
	canUseOrganization: boolean;
	defaultScope: FileScope;
	defaultTeamId?: string;
	organizationId: string;
	/** Teams the viewer can see, which they may add files to. */
	teams: TeamSummary[];
}

export function UploadFileDialog({
	canUseOrganization,
	defaultScope,
	defaultTeamId,
	organizationId,
	teams,
}: UploadFileDialogProps) {
	const queryClient = useQueryClient();
	const fileId = useId();
	const scopeId = useId();
	const teamFieldId = useId();
	const retentionId = useId();
	const trails = teamTrails(teams);
	const scopes: { label: string; value: FileScope }[] = [
		{ label: "Only me", value: "member" },
		...(teams.length > 0 ? [{ label: "A team", value: "team" as const }] : []),
		...(canUseOrganization
			? [{ label: "The organization", value: "organization" as const }]
			: []),
	];
	const startingScope = scopes.some((option) => option.value === defaultScope)
		? defaultScope
		: "member";
	const startingTeam = defaultTeamId ?? teams[0]?.id ?? "";
	const [open, setOpen] = useState(false);
	const [file, setFile] = useState<File>();
	const [scope, setScope] = useState<FileScope>(startingScope);
	const [teamId, setTeamId] = useState(startingTeam);
	const [retention, setRetention] = useState("30");
	const [problem, setProblem] = useState("");
	const upload = useMutation({
		mutationFn: (input: UploadInput) => uploadFile(organizationId, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: filesQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		if (upload.isPending) return;
		setOpen(next);
		if (next) {
			setFile(undefined);
			setScope(startingScope);
			setTeamId(startingTeam);
			setRetention("30");
			setProblem("");
			upload.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!file) {
			setProblem("Choose a file to upload.");
			return;
		}
		const tooBig = uploadProblem(file);
		if (tooBig) {
			setProblem(tooBig);
			return;
		}
		if (scope === "team" && teamId === "") {
			setProblem("Choose the team this file is for.");
			return;
		}
		setProblem("");
		upload.mutate({
			file,
			retentionDays: Number(retention),
			scope,
			teamId: scope === "team" ? teamId : undefined,
		});
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>
				<Button type="button">
					<Upload data-icon="inline-start" />
					Upload
				</Button>
			</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Upload a file</DialogTitle>
					<DialogDescription>
						Up to 8 MB. Files are removed when their time runs out.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					{upload.isError ? (
						<AppAlert>{uploadFailure(upload.error)}</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={fileId}>File</Label>
						<Input
							id={fileId}
							onChange={(event) => {
								setFile(event.target.files?.[0]);
								setProblem("");
							}}
							type="file"
						/>
						{file ? (
							<p className="dialog-field-hint">
								{file.name} · {formatBytes(file.size)}
							</p>
						) : null}
					</div>
					<div className="dialog-field">
						<Label htmlFor={scopeId}>Who can see it</Label>
						<Select
							onValueChange={(value) => setScope(value as FileScope)}
							value={scope}
						>
							<SelectTrigger className="w-full" id={scopeId}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{scopes.map((option) => (
									<SelectItem key={option.value} value={option.value}>
										{option.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
						<p className="dialog-field-hint">{scopeHints[scope]}</p>
					</div>
					{scope === "team" ? (
						<div className="dialog-field">
							<Label htmlFor={teamFieldId}>Team</Label>
							<Select onValueChange={setTeamId} value={teamId}>
								<SelectTrigger className="w-full" id={teamFieldId}>
									<SelectValue placeholder="Choose a team" />
								</SelectTrigger>
								<SelectContent>
									{teams.map((team) => (
										<SelectItem key={team.id} value={team.id}>
											{trails.get(team.id) ?? team.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={retentionId}>Keep it for</Label>
						<Select onValueChange={setRetention} value={retention}>
							<SelectTrigger className="w-full" id={retentionId}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{retentionChoices.map((days) => (
									<SelectItem key={days} value={String(days)}>
										{days} days
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button disabled={upload.isPending} type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={upload.isPending} type="submit">
							{upload.isPending ? "Uploading…" : "Upload"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function uploadFailure(error: unknown): string {
	if (error instanceof ApiError && error.status === 403) {
		return "Only organization admins can add organization files.";
	}
	return apiErrorMessage(error, "Could not upload the file");
}
