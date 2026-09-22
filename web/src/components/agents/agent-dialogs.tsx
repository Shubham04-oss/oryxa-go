import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, Sparkles, UsersRound } from "lucide-react";
import { type ReactElement, useId, useMemo, useState } from "react";
import { SpecialistCreationDialog } from "@/components/agents/specialist-creation-dialog";
import { SpecialistAvatar } from "@/components/agents/specialist-identity";
import { AppAlert } from "@/components/app/app-alert";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
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
import {
	type AgentInput,
	type AgentTeam,
	type AgentTeamInput,
	type AgentWorkflowInput,
	agentsQueryKey,
	agentTeamsQueryKey,
	agentWorkflowsQueryKey,
	createAgentTeam,
	createAgentWorkflow,
	type ManagedAgent,
	updateAgent,
	updateAgentTeam,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";

interface AgentDialogProps {
	agent?: ManagedAgent;
	organizationId: string;
	trigger: ReactElement;
}

export function AgentDialog({
	agent,
	organizationId,
	trigger,
}: AgentDialogProps) {
	return agent ? (
		<ExistingAgentDialog
			agent={agent}
			organizationId={organizationId}
			trigger={trigger}
		/>
	) : (
		<SpecialistCreationDialog
			organizationId={organizationId}
			trigger={trigger}
		/>
	);
}

function ExistingAgentDialog({
	agent,
	organizationId,
	trigger,
}: AgentDialogProps & { agent: ManagedAgent }) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const instructionsId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState(agent?.name ?? "");
	const [description, setDescription] = useState(agent?.description ?? "");
	const [instructions, setInstructions] = useState(agent?.instructions ?? "");
	const [advanced, setAdvanced] = useState(true);
	const [error, setError] = useState("");
	const isPrimary = agent.kind === "primary";
	const save = useMutation({
		mutationFn: (input: AgentInput) =>
			updateAgent(organizationId, agent.id, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName(agent.name);
			setDescription(agent.description);
			setInstructions(agent.instructions);
			setAdvanced(true);
			setError("");
			save.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!name.trim() || !description.trim()) {
			setError("Give this agent a name and a clear purpose.");
			return;
		}
		if (!instructions.trim()) {
			setError("Operating instructions are required for an existing agent.");
			return;
		}
		setError("");
		save.mutate({
			description: description.trim(),
			instructions: instructions.trim(),
			name: name.trim(),
		});
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide specialist-dialog">
				<DialogHeader>
					<DialogTitle>{`Edit ${agent.name}`}</DialogTitle>
					<DialogDescription>
						{isPrimary
							? "Update the identity your team and collaborators see."
							: "Refine this specialist's purpose and operating guidance."}
					</DialogDescription>
				</DialogHeader>
				<form className="specialist-dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{save.isError ? (
						<AppAlert>
							{apiErrorMessage(save.error, "Could not save agent")}
						</AppAlert>
					) : null}
					<div className="specialist-dialog-layout">
						<div className="specialist-dialog-fields">
							<div className="dialog-field">
								<Label htmlFor={nameId}>Name</Label>
								<Input
									autoComplete="off"
									id={nameId}
									onChange={(event) => setName(event.target.value)}
									placeholder="e.g. Revenue analyst"
									value={name}
								/>
							</div>
							<div className="dialog-field">
								<Label htmlFor={descriptionId}>
									{isPrimary ? "Description" : "One clear purpose"}
								</Label>
								<Input
									id={descriptionId}
									onChange={(event) => setDescription(event.target.value)}
									placeholder="Qualify inbound opportunities and surface account risks"
									value={description}
								/>
							</div>
							<div className="specialist-guidance">
								<button
									aria-expanded={advanced}
									aria-controls={instructionsId}
									className="specialist-guidance-toggle"
									onClick={() => setAdvanced((value) => !value)}
									type="button"
								>
									<span>Operating instructions</span>
									<ChevronDown aria-hidden="true" />
								</button>
								{advanced ? (
									<div className="specialist-guidance-body" id={instructionsId}>
										<Label htmlFor={`${instructionsId}-text`}>
											Instructions
										</Label>
										<Textarea
											className="agent-instructions-field"
											id={`${instructionsId}-text`}
											onChange={(event) => setInstructions(event.target.value)}
											placeholder="Add specific boundaries, preferred tools, and what a finished result should contain."
											value={instructions}
										/>
										<p className="dialog-note">
											Changes take effect on future runs.
										</p>
									</div>
								) : null}
							</div>
						</div>
						{!isPrimary ? (
							<aside className="specialist-dialog-preview">
								<span className="specialist-preview-eyebrow">
									<Sparkles aria-hidden="true" /> Identity preview
								</span>
								<SpecialistAvatar
									name={name || "New specialist"}
									seed={agent.id}
									size="large"
								/>
								<strong>{name.trim() || "Your specialist"}</strong>
								<p>
									{description.trim() ||
										"One focused responsibility, ready to be shaped."}
								</p>
								<small>
									Identity stays with this specialist across its runs.
								</small>
							</aside>
						) : null}
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={save.isPending} type="submit">
							{save.isPending ? "Saving…" : "Save changes"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface AgentTeamDialogProps {
	agents: ManagedAgent[];
	organizationId: string;
	team?: AgentTeam;
	trigger: ReactElement;
}

export function AgentTeamDialog({
	agents,
	organizationId,
	team,
	trigger,
}: AgentTeamDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [roles, setRoles] = useState<Record<string, string>>({});
	const [error, setError] = useState("");
	const specialists = agents.filter((agent) => agent.kind === "specialist");
	const save = useMutation({
		mutationFn: (input: AgentTeamInput) =>
			team
				? updateAgentTeam(organizationId, team.id, input)
				: createAgentTeam(organizationId, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentTeamsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName(team?.name ?? "");
			setDescription(team?.description ?? "");
			setRoles(
				Object.fromEntries(
					team?.members.map((member) => [member.agent_id, member.role]) ?? [],
				),
			);
			setError("");
			save.reset();
		}
	}

	function toggle(agent: ManagedAgent, checked: boolean) {
		setRoles((current) => {
			const next = { ...current };
			if (checked)
				next[agent.id] =
					agent.description.trim().slice(0, 280) ||
					`Contribute ${agent.name}'s expertise`;
			else delete next[agent.id];
			return next;
		});
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const members = specialists
			.filter((agent) => roles[agent.id] !== undefined)
			.map((agent) => ({ agent_id: agent.id, role: roles[agent.id].trim() }));
		if (!name.trim() || !description.trim() || members.length === 0) {
			setError("Name, purpose, and at least one specialist are required.");
			return;
		}
		if (members.some((member) => !member.role)) {
			setError("Every selected specialist needs a role in this team.");
			return;
		}
		setError("");
		save.mutate({
			description: description.trim(),
			members,
			name: name.trim(),
		});
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-team-builder-dialog">
				<DialogHeader>
					<DialogTitle>
						{team ? "Edit agent team" : "Build an agent team"}
					</DialogTitle>
					<DialogDescription>
						Choose a shared mission and the specialists who will work together
						in its group chat.
					</DialogDescription>
				</DialogHeader>
				<form className="agent-team-builder" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{save.isError ? (
						<AppAlert>
							{apiErrorMessage(save.error, "Could not save team")}
						</AppAlert>
					) : null}
					<div className="agent-team-builder-layout">
						<div className="agent-team-builder-main">
							<div className="agent-team-builder-intro">
								<span className="agent-team-builder-icon">
									<UsersRound aria-hidden />
								</span>
								<div>
									<strong>Shared mission</strong>
									<p>
										Give the group one outcome. The primary agent coordinates;
										each specialist keeps their own instructions.
									</p>
								</div>
							</div>
							<div className="agent-dialog-grid">
								<div className="dialog-field">
									<Label htmlFor={nameId}>Name</Label>
									<Input
										id={nameId}
										onChange={(event) => setName(event.target.value)}
										placeholder="Go to market"
										value={name}
									/>
								</div>
								<div className="dialog-field">
									<Label htmlFor={descriptionId}>Purpose</Label>
									<Input
										id={descriptionId}
										onChange={(event) => setDescription(event.target.value)}
										placeholder="Prepare and review product launches."
										value={description}
									/>
								</div>
							</div>
							<div className="dialog-field">
								<Label>Choose collaborators</Label>
								<small className="agent-team-builder-help">
									Select specialists and clarify what each one owns. You can
									change the roster later.
								</small>
								<div className="agent-picker agent-team-builder-picker">
									{specialists.length === 0 ? (
										<p className="dialog-note">
											Create a specialist before composing a team.
										</p>
									) : (
										specialists.map((agent) => {
											const selected = roles[agent.id] !== undefined;
											return (
												<div
													className="agent-picker-row"
													data-selected={selected}
													key={agent.id}
												>
													<Checkbox
														aria-label={`Include ${agent.name}`}
														checked={selected}
														onCheckedChange={(value) =>
															toggle(agent, value === true)
														}
													/>
													<div className="agent-picker-identity">
														<SpecialistAvatar
															name={agent.name}
															seed={agent.id}
															size="small"
														/>
														<span>
															<strong>{agent.name}</strong>
															<small>{agent.description}</small>
														</span>
													</div>
													<Input
														aria-label={`${agent.name} role`}
														disabled={!selected}
														onChange={(event) =>
															setRoles((current) => ({
																...current,
																[agent.id]: event.target.value,
															}))
														}
														placeholder="What this specialist owns"
														value={roles[agent.id] ?? ""}
													/>
												</div>
											);
										})
									)}
								</div>
							</div>
						</div>
						<aside className="agent-team-builder-preview">
							<span className="agent-eyebrow">Live preview</span>
							<h3>{name.trim() || "Your team"}</h3>
							<p>
								{description.trim() || "The shared purpose will appear here."}
							</p>
							<div className="agent-team-builder-preview-roster">
								<span>Group chat participants</span>
								<div>
									<span className="agent-team-builder-preview-primary">
										<Sparkles aria-hidden />
									</span>
									<strong>Primary agent</strong>
									<small>Coordinator</small>
								</div>
								{specialists
									.filter((agent) => roles[agent.id] !== undefined)
									.map((agent) => (
										<div key={agent.id}>
											<SpecialistAvatar
												name={agent.name}
												seed={agent.id}
												size="small"
											/>
											<strong>{agent.name}</strong>
											<small>{roles[agent.id] || "Role needed"}</small>
										</div>
									))}
								{!Object.keys(roles).length ? (
									<p>Select a specialist to see the group take shape.</p>
								) : null}
							</div>
							<small>
								In chat, @mention a teammate directly or let the primary agent
								route the request.
							</small>
						</aside>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={save.isPending || specialists.length === 0}
							type="submit"
						>
							{save.isPending ? "Saving…" : team ? "Save team" : "Create team"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface AgentWorkflowDialogProps {
	organizationId: string;
	teams: AgentTeam[];
	trigger: ReactElement;
}

export function AgentWorkflowDialog({
	organizationId,
	teams,
	trigger,
}: AgentWorkflowDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const scheduleId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState("");
	const [description, setDescription] = useState("");
	const [teamId, setTeamId] = useState(teams[0]?.id ?? "");
	const [tasks, setTasks] = useState<Record<string, string>>({});
	const [schedule, setSchedule] = useState("");
	const [timezone, setTimezone] = useState(
		Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
	);
	const [error, setError] = useState("");
	const team = useMemo(
		() => teams.find((candidate) => candidate.id === teamId),
		[teamId, teams],
	);
	const create = useMutation({
		mutationFn: (input: AgentWorkflowInput) =>
			createAgentWorkflow(organizationId, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentWorkflowsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName("");
			setDescription("");
			setTeamId(teams[0]?.id ?? "");
			setTasks({});
			setSchedule("");
			setError("");
			create.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const assignments = (team?.members ?? [])
			.filter((member) => tasks[member.agent_id]?.trim())
			.map((member) => ({
				agent_id: member.agent_id,
				task: tasks[member.agent_id].trim(),
			}));
		if (
			!name.trim() ||
			!description.trim() ||
			!team ||
			assignments.length === 0
		) {
			setError(
				"Name, purpose, team, and at least one assignment are required.",
			);
			return;
		}
		const input: AgentWorkflowInput = {
			assignments,
			description: description.trim(),
			name: name.trim(),
			team_id: team.id,
		};
		if (schedule.trim()) {
			input.schedule = {
				expression: schedule.trim(),
				state: "enabled",
				timezone: timezone.trim() || "UTC",
			};
		}
		setError("");
		create.mutate(input);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>New workflow</DialogTitle>
					<DialogDescription>
						Run bounded specialist assignments now or on an
						EventBridge-compatible schedule.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{create.isError ? (
						<AppAlert>
							{apiErrorMessage(create.error, "Could not create workflow")}
						</AppAlert>
					) : null}
					<div className="agent-dialog-grid">
						<div className="dialog-field">
							<Label htmlFor={nameId}>Name</Label>
							<Input
								id={nameId}
								onChange={(event) => setName(event.target.value)}
								placeholder="Weekly launch review"
								value={name}
							/>
						</div>
						<div className="dialog-field">
							<Label>Agent team</Label>
							<Select
								onValueChange={(value) => {
									setTeamId(value);
									setTasks({});
								}}
								value={teamId}
							>
								<SelectTrigger className="w-full">
									<SelectValue placeholder="Choose a team" />
								</SelectTrigger>
								<SelectContent>
									{teams.map((candidate) => (
										<SelectItem key={candidate.id} value={candidate.id}>
											{candidate.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					</div>
					<div className="dialog-field">
						<Label htmlFor={descriptionId}>Purpose</Label>
						<Input
							id={descriptionId}
							onChange={(event) => setDescription(event.target.value)}
							placeholder="Collect launch risks and positioning recommendations."
							value={description}
						/>
					</div>
					<div className="dialog-field">
						<Label>Assignments</Label>
						<div className="agent-assignment-list">
							{team?.members.map((member) => (
								<div className="agent-assignment-row" key={member.agent_id}>
									<div className="agent-picker-identity">
										<SpecialistAvatar
											name={member.name}
											seed={member.agent_id}
											size="small"
										/>
										<span>
											<strong>{member.name}</strong>
											<small>{member.role}</small>
										</span>
									</div>
									<Textarea
										aria-label={`${member.name} assignment`}
										onChange={(event) =>
											setTasks((current) => ({
												...current,
												[member.agent_id]: event.target.value,
											}))
										}
										placeholder="Leave empty to skip this specialist."
										value={tasks[member.agent_id] ?? ""}
									/>
								</div>
							)) ?? (
								<p className="dialog-note">
									Choose a team to define assignments.
								</p>
							)}
						</div>
					</div>
					<div className="agent-dialog-grid">
						<div className="dialog-field">
							<Label htmlFor={scheduleId}>Schedule (optional)</Label>
							<Input
								id={scheduleId}
								onChange={(event) => setSchedule(event.target.value)}
								placeholder="rate(1 day) or cron(0 9 ? * MON *)"
								value={schedule}
							/>
						</div>
						<div className="dialog-field">
							<Label htmlFor={`${scheduleId}-timezone`}>Timezone</Label>
							<Input
								id={`${scheduleId}-timezone`}
								onChange={(event) => setTimezone(event.target.value)}
								value={timezone}
							/>
						</div>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={create.isPending || teams.length === 0}
							type="submit"
						>
							{create.isPending ? "Creating…" : "Create workflow"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
