import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactElement, useId, useMemo, useState } from "react";
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
	createAgent,
	createAgentTeam,
	createAgentWorkflow,
	type ManagedAgent,
	updateAgent,
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
	const queryClient = useQueryClient();
	const nameId = useId();
	const descriptionId = useId();
	const instructionsId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState(agent?.name ?? "");
	const [description, setDescription] = useState(agent?.description ?? "");
	const [instructions, setInstructions] = useState(agent?.instructions ?? "");
	const [error, setError] = useState("");
	const save = useMutation({
		mutationFn: (input: AgentInput) =>
			agent
				? updateAgent(organizationId, agent.id, input)
				: createAgent(organizationId, input),
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
			setName(agent?.name ?? "");
			setDescription(agent?.description ?? "");
			setInstructions(agent?.instructions ?? "");
			setError("");
			save.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!name.trim() || !description.trim() || !instructions.trim()) {
			setError("Name, purpose, and operating instructions are required.");
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
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>
						{agent ? `Edit ${agent.name}` : "New specialist"}
					</DialogTitle>
					<DialogDescription>
						Give this agent one stable responsibility. It inherits your
						connected apps but remains inside these instructions.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{save.isError ? (
						<AppAlert>
							{apiErrorMessage(save.error, "Could not save agent")}
						</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							autoComplete="off"
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							placeholder="Revenue analyst"
							value={name}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={descriptionId}>Purpose</Label>
						<Input
							id={descriptionId}
							onChange={(event) => setDescription(event.target.value)}
							placeholder="Qualifies inbound opportunities and summarizes account risk."
							value={description}
						/>
					</div>
					<div className="dialog-field">
						<Label htmlFor={instructionsId}>Operating instructions</Label>
						<Textarea
							className="agent-instructions-field"
							id={instructionsId}
							onChange={(event) => setInstructions(event.target.value)}
							placeholder="Define responsibilities, boundaries, expected inputs, tool use, and the exact result this specialist should return."
							value={instructions}
						/>
						<p className="dialog-note">
							Be explicit about what it must not do and when it should stop or
							ask.
						</p>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={save.isPending} type="submit">
							{save.isPending
								? "Saving…"
								: agent
									? "Save changes"
									: "Create specialist"}
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
	trigger: ReactElement;
}

export function AgentTeamDialog({
	agents,
	organizationId,
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
	const create = useMutation({
		mutationFn: (input: AgentTeamInput) =>
			createAgentTeam(organizationId, input),
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
			setName("");
			setDescription("");
			setRoles({});
			setError("");
			create.reset();
		}
	}

	function toggle(agent: ManagedAgent, checked: boolean) {
		setRoles((current) => {
			const next = { ...current };
			if (checked) next[agent.id] = agent.description;
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
		create.mutate({
			description: description.trim(),
			members,
			name: name.trim(),
		});
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="agent-dialog-wide">
				<DialogHeader>
					<DialogTitle>New agent team</DialogTitle>
					<DialogDescription>
						Your primary agent coordinates the specialists you select here.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					{error ? <AppAlert>{error}</AppAlert> : null}
					{create.isError ? (
						<AppAlert>
							{apiErrorMessage(create.error, "Could not create team")}
						</AppAlert>
					) : null}
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
						<Label>Specialists and roles</Label>
						<div className="agent-picker">
							{specialists.length === 0 ? (
								<p className="dialog-note">
									Create a specialist before composing a team.
								</p>
							) : (
								specialists.map((agent) => {
									const selected = roles[agent.id] !== undefined;
									return (
										<div className="agent-picker-row" key={agent.id}>
											<Checkbox
												aria-label={`Include ${agent.name}`}
												checked={selected}
												onCheckedChange={(value) =>
													toggle(agent, value === true)
												}
											/>
											<div>
												<strong>{agent.name}</strong>
												<small>{agent.description}</small>
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
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={create.isPending || specialists.length === 0}
							type="submit"
						>
							{create.isPending ? "Creating…" : "Create team"}
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
									<div>
										<strong>{member.name}</strong>
										<small>{member.role}</small>
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
