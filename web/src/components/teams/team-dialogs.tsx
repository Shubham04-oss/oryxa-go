import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactElement, useId, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { displayName, initials } from "@/components/app/identity";
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
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { fieldErrors } from "@/lib/auth/schemas";
import {
	addTeamMember,
	createTeam,
	memberSearchQueryOptions,
	renameTeam,
	type Team,
	type TeamNameInput,
	type TeamRole,
	type TeamSummary,
	teamMembersQueryOptions,
	teamNameSchema,
	teamsQueryKey,
	teamTrails,
} from "@/lib/teams/client";

const topLevel = "top-level";

interface CreateTeamDialogProps {
	/** Organization admins create top-level teams; team admins nest under theirs. */
	canCreateTopLevel: boolean;
	onCreated?: (team: Team) => void;
	organizationId: string;
	/** Starts nested under this team. */
	parentId?: string;
	/** Every team the viewer can see. */
	teams: TeamSummary[];
	trigger: ReactElement;
}

export function CreateTeamDialog({
	canCreateTopLevel,
	onCreated,
	organizationId,
	parentId,
	teams,
	trigger,
}: CreateTeamDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const parentFieldId = useId();
	const parents = teams.filter((team) => team.can_manage);
	const trails = teamTrails(teams);
	const initialParent =
		parentId ?? (canCreateTopLevel ? topLevel : (parents[0]?.id ?? topLevel));
	const [open, setOpen] = useState(false);
	const [name, setName] = useState("");
	const [parent, setParent] = useState(initialParent);
	const [errors, setErrors] = useState<
		Partial<Record<keyof TeamNameInput, string>>
	>({});
	const create = useMutation({
		mutationFn: (input: { name: string; parent_id?: string }) =>
			createTeam(organizationId, input),
		onSuccess: async (team) => {
			await queryClient.invalidateQueries({
				queryKey: teamsQueryKey(organizationId),
			});
			setOpen(false);
			onCreated?.(team);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName("");
			setParent(initialParent);
			setErrors({});
			create.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const result = teamNameSchema.safeParse({ name });
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}
		setErrors({});
		create.mutate({
			name: result.data.name,
			parent_id: parent === topLevel ? undefined : parent,
		});
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>New team</DialogTitle>
					<DialogDescription>
						You become the new team's first admin.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{create.isError ? (
						<AppAlert>{createTeamFailure(create.error)}</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							aria-invalid={Boolean(errors.name)}
							autoComplete="off"
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							placeholder="Engineering"
							value={name}
						/>
						{errors.name ? <p className="field-error">{errors.name}</p> : null}
					</div>
					{parents.length > 0 ? (
						<div className="dialog-field">
							<Label htmlFor={parentFieldId}>Inside</Label>
							<Select onValueChange={setParent} value={parent}>
								<SelectTrigger className="w-full" id={parentFieldId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{canCreateTopLevel ? (
										<SelectItem value={topLevel}>Top level</SelectItem>
									) : null}
									{parents.map((team) => (
										<SelectItem key={team.id} value={team.id}>
											{trails.get(team.id) ?? team.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={create.isPending} type="submit">
							{create.isPending ? "Creating…" : "Create team"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function createTeamFailure(error: unknown): string {
	if (error instanceof ApiError && error.status === 409) {
		return "A team with that name already exists in this organization.";
	}
	if (error instanceof ApiError && error.status === 403) {
		return "Only organization admins can create top-level teams.";
	}
	return apiErrorMessage(error, "Could not create the team");
}

interface RenameTeamDialogProps {
	organizationId: string;
	team: Team;
	trigger: ReactElement;
}

export function RenameTeamDialog({
	organizationId,
	team,
	trigger,
}: RenameTeamDialogProps) {
	const queryClient = useQueryClient();
	const nameId = useId();
	const [open, setOpen] = useState(false);
	const [name, setName] = useState(team.name);
	const [errors, setErrors] = useState<
		Partial<Record<keyof TeamNameInput, string>>
	>({});
	const rename = useMutation({
		mutationFn: (value: string) => renameTeam(organizationId, team.id, value),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: teamsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setName(team.name);
			setErrors({});
			rename.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const result = teamNameSchema.safeParse({ name });
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}
		setErrors({});
		rename.mutate(result.data.name);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Rename {team.name}</DialogTitle>
					<DialogDescription>
						Assistants find teams by name, so keep it recognizable.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{rename.isError ? (
						<AppAlert>{createTeamFailure(rename.error)}</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={nameId}>Name</Label>
						<Input
							aria-invalid={Boolean(errors.name)}
							autoComplete="off"
							id={nameId}
							onChange={(event) => setName(event.target.value)}
							value={name}
						/>
						{errors.name ? <p className="field-error">{errors.name}</p> : null}
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button
							disabled={rename.isPending || name.trim() === team.name}
							type="submit"
						>
							{rename.isPending ? "Saving…" : "Rename"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface AddTeamMemberDialogProps {
	/** Who is already on the team. */
	memberIds: ReadonlySet<string>;
	organizationId: string;
	teamId: string;
	teamName: string;
	trigger: ReactElement;
}

export function AddTeamMemberDialog({
	memberIds,
	organizationId,
	teamId,
	teamName,
	trigger,
}: AddTeamMemberDialogProps) {
	const queryClient = useQueryClient();
	const searchId = useId();
	const roleId = useId();
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const [role, setRole] = useState<TeamRole>("member");
	const debounced = useDebouncedValue(query);
	const results = useQuery({
		...memberSearchQueryOptions(organizationId, debounced),
		enabled: open && debounced.trim() !== "",
	});
	const add = useMutation({
		mutationFn: (userId: string) =>
			addTeamMember(organizationId, teamId, { role, user_id: userId }),
		onSuccess: () =>
			queryClient.invalidateQueries({
				queryKey: teamMembersQueryOptions(organizationId, teamId).queryKey,
			}),
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setQuery("");
			setRole("member");
			add.reset();
		}
	}

	let found: React.ReactNode;
	if (debounced.trim() === "") {
		found = <p className="dialog-note">Search by name or email.</p>;
	} else if (results.isPending) {
		found = <p className="dialog-note">Searching…</p>;
	} else if (results.isError) {
		found = (
			<AppAlert>{apiErrorMessage(results.error, "Could not search")}</AppAlert>
		);
	} else if (results.data.length === 0) {
		found = <p className="dialog-note">No one in the organization matches.</p>;
	} else {
		found = (
			<ul className="dialog-people">
				{results.data.map((person) => {
					const name = displayName(person);
					const onTeam = memberIds.has(person.user_id);
					const adding = add.isPending && add.variables === person.user_id;
					return (
						<li key={person.user_id}>
							<span aria-hidden="true" className="settings-avatar">
								{initials(name)}
							</span>
							<div className="settings-list-copy">
								<strong>{name}</strong>
								<small>{person.email}</small>
							</div>
							{onTeam ? (
								<small className="dialog-people-state">On the team</small>
							) : (
								<Button
									disabled={add.isPending}
									onClick={() => add.mutate(person.user_id)}
									size="sm"
									type="button"
									variant="ghost"
								>
									{adding ? "Adding…" : "Add"}
								</Button>
							)}
						</li>
					);
				})}
			</ul>
		);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Add people to {teamName}</DialogTitle>
					<DialogDescription>
						They need to belong to the organization already.
					</DialogDescription>
				</DialogHeader>
				<div className="dialog-form">
					{add.isError ? (
						<AppAlert>{addMemberFailure(add.error)}</AppAlert>
					) : null}
					<div className="dialog-field-row">
						<div className="dialog-field">
							<Label htmlFor={searchId}>Find people</Label>
							<Input
								autoComplete="off"
								id={searchId}
								onChange={(event) => setQuery(event.target.value)}
								placeholder="Name or email"
								value={query}
							/>
						</div>
						<div className="dialog-field">
							<Label htmlFor={roleId}>Add as</Label>
							<Select
								onValueChange={(value) => setRole(value as TeamRole)}
								value={role}
							>
								<SelectTrigger className="w-full" id={roleId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									<SelectItem value="member">Member</SelectItem>
									<SelectItem value="admin">Admin</SelectItem>
								</SelectContent>
							</Select>
						</div>
					</div>
					{found}
				</div>
				<DialogFooter>
					<DialogClose asChild>
						<Button type="button" variant="ghost">
							Done
						</Button>
					</DialogClose>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}

function addMemberFailure(error: unknown): string {
	if (error instanceof ApiError && error.status === 409) {
		return "They are already on the team.";
	}
	return apiErrorMessage(error, "Could not add them to the team");
}
