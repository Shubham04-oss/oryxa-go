import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactElement, useId, useMemo, useState } from "react";
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
import { Textarea } from "@/components/ui/textarea";
import type { ManagedAgent } from "@/lib/agents/client";
import { agentsQueryKey } from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import type { TeamSummary } from "@/lib/teams/client";
import {
	addTeamspaceContribution,
	createTeamspace,
	type DelegationMode,
	type EntryKind,
	type OrganizationAgent,
	publishTeamspaceEntry,
	type Teamspace,
	teamspaceEntriesQueryOptions,
	teamspacesQueryKey,
	updateOrganizationAgent,
	updateTeamspace,
} from "@/lib/teamspaces/client";
import { safeArtifactHref } from "@/lib/teamspaces/policy";

interface CreateTeamspaceDialogProps {
	onCreated: (space: Teamspace) => void;
	organizationId: string;
	teams: TeamSummary[];
	trigger: ReactElement;
}

export function CreateTeamspaceDialog({
	onCreated,
	organizationId,
	teams,
	trigger,
}: CreateTeamspaceDialogProps) {
	const queryClient = useQueryClient();
	const teamId = useId();
	const nameId = useId();
	const descriptionId = useId();
	const [open, setOpen] = useState(false);
	const [selectedTeam, setSelectedTeam] = useState("");
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [error, setError] = useState("");
	const manageable = teams.filter((team) => team.can_manage);
	const create = useMutation({
		mutationFn: () =>
			createTeamspace(organizationId, {
				description: description.trim(),
				name: name.trim(),
				teamId: selectedTeam,
			}),
		onSuccess: async (space) => {
			await queryClient.invalidateQueries({
				queryKey: teamspacesQueryKey(organizationId),
			});
			setOpen(false);
			onCreated(space);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setSelectedTeam(manageable[0]?.id ?? "");
			setName("");
			setDescription("");
			setError("");
			create.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!selectedTeam || !name.trim()) {
			setError("Choose a team and give its shared space a name.");
			return;
		}
		setError("");
		create.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>New Teamspace</DialogTitle>
					<DialogDescription>
						Create a shared work ledger for one human team. Every active
						member’s primary agent appears as a whisperer automatically.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{create.isError ? (
						<AppAlert>
							{apiErrorMessage(create.error, "Could not create Teamspace")}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={teamId}>Human team</Label>
						<Select onValueChange={setSelectedTeam} value={selectedTeam}>
							<SelectTrigger className="w-full" id={teamId}>
								<SelectValue placeholder="Choose a team" />
							</SelectTrigger>
							<SelectContent>
								{manageable.map((team) => (
									<SelectItem key={team.id} value={team.id}>
										{team.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							placeholder="Product launch room"
							value={name}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={descriptionId}>Purpose</Label>
						<Textarea
							id={descriptionId}
							onChange={(event) => setDescription(event.target.value)}
							placeholder="What belongs in this shared space, and what should remain private?"
							value={description}
						/>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={create.isPending} type="submit">
							{create.isPending ? "Creating…" : "Create Teamspace"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface ContributeAgentDialogProps {
	agents: ManagedAgent[];
	organizationId: string;
	space: Teamspace;
	trigger: ReactElement;
}

export function ContributeAgentDialog({
	agents,
	organizationId,
	space,
	trigger,
}: ContributeAgentDialogProps) {
	const queryClient = useQueryClient();
	const agentId = useId();
	const roleId = useId();
	const modeId = useId();
	const candidates = useMemo(
		() =>
			agents.filter(
				(agent) =>
					agent.kind === "specialist" &&
					agent.status === "active" &&
					!space.contributions.some(
						(contribution) => contribution.agent_id === agent.id,
					),
			),
		[agents, space.contributions],
	);
	const [open, setOpen] = useState(false);
	const [selectedAgent, setSelectedAgent] = useState("");
	const [role, setRole] = useState("");
	const [mode, setMode] = useState<DelegationMode>("request");
	const [error, setError] = useState("");
	const contribute = useMutation({
		mutationFn: () =>
			addTeamspaceContribution(organizationId, space.id, {
				agentId: selectedAgent,
				delegationMode: mode,
				role: role.trim(),
			}),
		onSuccess: async () => {
			await Promise.all([
				queryClient.invalidateQueries({
					queryKey: teamspacesQueryKey(organizationId),
				}),
				queryClient.invalidateQueries({
					queryKey: agentsQueryKey(organizationId),
				}),
			]);
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setSelectedAgent(candidates[0]?.id ?? "");
			setRole("");
			setMode("request");
			setError("");
			contribute.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!selectedAgent || !role.trim()) {
			setError("Choose a specialist and define its bounded role here.");
			return;
		}
		setError("");
		contribute.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Contribute a specialist</DialogTitle>
					<DialogDescription>
						Share capability, not private instructions. This specialist still
						runs as you and keeps your tool and confirmation boundaries.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{contribute.isError ? (
						<AppAlert>
							{apiErrorMessage(
								contribute.error,
								"Could not contribute specialist",
							)}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={agentId}>Specialist</Label>
						<Select onValueChange={setSelectedAgent} value={selectedAgent}>
							<SelectTrigger className="w-full" id={agentId}>
								<SelectValue placeholder="Choose a specialist" />
							</SelectTrigger>
							<SelectContent>
								{candidates.map((agent) => (
									<SelectItem key={agent.id} value={agent.id}>
										{agent.name}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					<div className="dialog-field">
						<Label htmlFor={roleId}>Role in this Teamspace</Label>
						<Input
							id={roleId}
							onChange={(event) => setRole(event.target.value)}
							placeholder="Review implementation security"
							value={role}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={modeId}>Delegation authority</Label>
						<Select
							onValueChange={(value) => setMode(value as DelegationMode)}
							value={mode}
						>
							<SelectTrigger className="w-full" id={modeId}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="request">Ask me before work</SelectItem>
								<SelectItem value="automatic">
									Automatic bounded work
								</SelectItem>
							</SelectContent>
						</Select>
						<p className="dialog-note">
							Automatic permits the organization agent to start only the role
							you defined. It does not grant new tools or reveal private
							context.
						</p>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={contribute.isPending} type="submit">
							{contribute.isPending ? "Contributing…" : "Contribute"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface EntryAuthor {
	id: string;
	label: string;
}

interface PublishEntryDialogProps {
	authors: EntryAuthor[];
	organizationId: string;
	space: Teamspace;
	trigger: ReactElement;
}

export function PublishEntryDialog({
	authors,
	organizationId,
	space,
	trigger,
}: PublishEntryDialogProps) {
	const queryClient = useQueryClient();
	const authorId = useId();
	const kindId = useId();
	const titleId = useId();
	const summaryId = useId();
	const toolsId = useId();
	const artifactNameId = useId();
	const artifactUriId = useId();
	const artifactMediaTypeId = useId();
	const [open, setOpen] = useState(false);
	const [author, setAuthor] = useState("");
	const [kind, setKind] = useState<EntryKind>("update");
	const [title, setTitle] = useState("");
	const [summary, setSummary] = useState("");
	const [tools, setTools] = useState("");
	const [artifactName, setArtifactName] = useState("");
	const [artifactUri, setArtifactUri] = useState("");
	const [artifactMediaType, setArtifactMediaType] = useState("");
	const [error, setError] = useState("");
	const publish = useMutation({
		mutationFn: () =>
			publishTeamspaceEntry(organizationId, space.id, {
				authorAgentId: author,
				artifacts:
					kind === "artifact"
						? [
								{
									media_type: artifactMediaType.trim() || undefined,
									name: artifactName.trim(),
									uri: artifactUri.trim(),
								},
							]
						: undefined,
				kind,
				summary: summary.trim(),
				title: title.trim(),
				toolsUsed: tools
					.split(",")
					.map((tool) => tool.trim())
					.filter(Boolean),
			}),
		onSuccess: async () => {
			await Promise.all([
				queryClient.invalidateQueries({
					queryKey: teamspaceEntriesQueryOptions(organizationId, space.id)
						.queryKey,
				}),
				queryClient.invalidateQueries({
					queryKey: teamspacesQueryKey(organizationId),
				}),
			]);
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setAuthor(authors[0]?.id ?? "");
			setKind("update");
			setTitle("");
			setSummary("");
			setTools("");
			setArtifactName("");
			setArtifactUri("");
			setArtifactMediaType("");
			setError("");
			publish.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!author || !title.trim() || !summary.trim()) {
			setError("Choose an author and add a concise title and shared summary.");
			return;
		}
		if (
			kind === "artifact" &&
			(!artifactName.trim() || !safeArtifactHref(artifactUri.trim()))
		) {
			setError(
				"Add an artifact name and an HTTPS address or first-party download path.",
			);
			return;
		}
		setError("");
		publish.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>Publish to shared context</DialogTitle>
					<DialogDescription>
						Only this deliberate summary enters the Teamspace and organization
						context. Private conversations and reasoning stay private.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{publish.isError ? (
						<AppAlert>
							{apiErrorMessage(publish.error, "Could not publish shared work")}
						</AppAlert>
					) : null}
					<div className="dialog-field-row">
						<div className="dialog-field">
							<Label htmlFor={authorId}>Publishing agent</Label>
							<Select onValueChange={setAuthor} value={author}>
								<SelectTrigger className="w-full" id={authorId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{authors.map((item) => (
										<SelectItem key={item.id} value={item.id}>
											{item.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
						<div className="dialog-field">
							<Label htmlFor={kindId}>Kind</Label>
							<Select
								onValueChange={(value) => setKind(value as EntryKind)}
								value={kind}
							>
								<SelectTrigger className="w-full" id={kindId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="update">Update</SelectItem>
									<SelectItem value="result">Result</SelectItem>
									<SelectItem value="decision">Decision</SelectItem>
									<SelectItem value="handoff">Handoff</SelectItem>
									<SelectItem value="artifact">Artifact</SelectItem>
								</SelectContent>
							</Select>
						</div>
					</div>
					<div className="dialog-field">
						<Label htmlFor={titleId}>Title</Label>
						<Input
							id={titleId}
							onChange={(event) => setTitle(event.target.value)}
							placeholder="Architecture review complete"
							value={title}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={summaryId}>Shared summary</Label>
						<Textarea
							className="agent-instructions-field"
							id={summaryId}
							onChange={(event) => setSummary(event.target.value)}
							placeholder="State the useful outcome, evidence, boundaries, and what another collaborator needs next."
							value={summary}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={toolsId}>Tools used</Label>
						<Input
							id={toolsId}
							onChange={(event) => setTools(event.target.value)}
							placeholder="github.search, linear.issue (comma-separated)"
							value={tools}
						/>
					</div>
					{kind === "artifact" ? (
						<div className="teamspace-artifact-fields">
							<div className="dialog-field">
								<Label htmlFor={artifactNameId}>Artifact name</Label>
								<Input
									id={artifactNameId}
									onChange={(event) => setArtifactName(event.target.value)}
									placeholder="Architecture review"
									value={artifactName}
								/>
							</div>
							<div className="dialog-field">
								<Label htmlFor={artifactUriId}>
									HTTPS address or download path
								</Label>
								<Input
									id={artifactUriId}
									onChange={(event) => setArtifactUri(event.target.value)}
									placeholder="https://… or /v1/files/…"
									value={artifactUri}
								/>
							</div>
							<div className="dialog-field">
								<Label htmlFor={artifactMediaTypeId}>
									Media type (optional)
								</Label>
								<Input
									id={artifactMediaTypeId}
									onChange={(event) => setArtifactMediaType(event.target.value)}
									placeholder="application/pdf"
									value={artifactMediaType}
								/>
							</div>
						</div>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Keep private
							</Button>
						</DialogClose>
						<Button disabled={publish.isPending} type="submit">
							{publish.isPending ? "Publishing…" : "Publish shared summary"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface OrganizationAgentDialogProps {
	agent: OrganizationAgent;
	organizationId: string;
	trigger: ReactElement;
}

export function OrganizationAgentDialog({
	agent,
	organizationId,
	trigger,
}: OrganizationAgentDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const instructionsId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState(agent.name);
	const [description, setDescription] = useState(agent.description ?? "");
	const [instructions, setInstructions] = useState(agent.instructions);
	const save = useMutation({
		mutationFn: () =>
			updateOrganizationAgent(organizationId, {
				description: description.trim(),
				instructions: instructions.trim(),
				name: name.trim(),
			}),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: teamspacesQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName(agent.name);
			setDescription(agent.description ?? "");
			setInstructions(agent.instructions);
			save.reset();
		}
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>Organization agent</DialogTitle>
					<DialogDescription>
						This agent synthesizes deliberate shared publications. It cannot
						read private member conversations, credentials, or connections.
					</DialogDescription>
				</DialogHeader>
				<form
					className="dialog-form"
					onSubmit={(event) => {
						event.preventDefault();
						if (name.trim() && instructions.trim()) save.mutate();
					}}
				>
					{save.isError ? (
						<AppAlert>
							{apiErrorMessage(
								save.error,
								"Could not update organization agent",
							)}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							value={name}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={descriptionId}>Purpose</Label>
						<Input
							id={descriptionId}
							onChange={(event) => setDescription(event.target.value)}
							value={description}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={instructionsId}>Shared-context instructions</Label>
						<Textarea
							className="agent-instructions-field"
							id={instructionsId}
							onChange={(event) => setInstructions(event.target.value)}
							value={instructions}
						/>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={save.isPending || !name.trim() || !instructions.trim()}
							type="submit"
						>
							{save.isPending ? "Saving…" : "Save instructions"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface EditTeamspaceDialogProps {
	organizationId: string;
	space: Teamspace;
	trigger: ReactElement;
}

export function EditTeamspaceDialog({
	organizationId,
	space,
	trigger,
}: EditTeamspaceDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState(space.name);
	const [description, setDescription] = useState(space.description ?? "");
	const [status, setStatus] = useState<Teamspace["status"]>(space.status);
	const save = useMutation({
		mutationFn: () =>
			updateTeamspace(organizationId, space.id, {
				description: description.trim(),
				name: name.trim(),
				status,
			}),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: teamspacesQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	return (
		<Dialog onOpenChange={setOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Teamspace settings</DialogTitle>
					<DialogDescription>
						Pausing preserves the shared ledger but blocks new collaboration.
					</DialogDescription>
				</DialogHeader>
				<form
					className="dialog-form"
					onSubmit={(event) => {
						event.preventDefault();
						if (name.trim()) save.mutate();
					}}
				>
					{save.isError ? (
						<AppAlert>
							{apiErrorMessage(save.error, "Could not update Teamspace")}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							value={name}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={descriptionId}>Purpose</Label>
						<Textarea
							id={descriptionId}
							onChange={(event) => setDescription(event.target.value)}
							value={description}
						/>
					</div>
					<div className="dialog-field">
						<Label>Status</Label>
						<Select
							onValueChange={(value) => setStatus(value as Teamspace["status"])}
							value={status}
						>
							<SelectTrigger className="w-full">
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								<SelectItem value="active">Active</SelectItem>
								<SelectItem value="disabled">Paused</SelectItem>
							</SelectContent>
						</Select>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={save.isPending || !name.trim()} type="submit">
							{save.isPending ? "Saving…" : "Save changes"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
