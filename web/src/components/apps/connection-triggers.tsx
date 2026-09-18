import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { type ReactElement, useId, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import {
	Dialog,
	DialogContent,
	DialogDescription,
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
	appsErrorMessage,
	catalogTriggerTypesQueryOptions,
	createTrigger,
	removeTrigger,
	type Trigger,
	type TriggerConfigField,
	type TriggerType,
	triggerConfigCapability,
	triggerConfigFieldSubmission,
	triggersQueryOptions,
} from "@/lib/apps/client";

interface ConnectionTriggersProps {
	connectionId: string;
	organizationId: string;
	toolkit: string;
}

/** "Wake Krude when…": a connection's event subscriptions, and adding more. */
export function ConnectionTriggers({
	connectionId,
	organizationId,
	toolkit,
}: ConnectionTriggersProps) {
	const queryClient = useQueryClient();
	const triggers = useQuery(triggersQueryOptions(organizationId, connectionId));
	const types = useQuery(catalogTriggerTypesQueryOptions(toolkit));
	const typeBySlug = new Map(
		(types.data?.triggers ?? []).map((type) => [type.trigger, type]),
	);
	const subscribed = new Set(
		(triggers.data ?? []).map((trigger) => trigger.trigger),
	);
	const available = (types.data?.triggers ?? []).filter(
		(type) => !subscribed.has(type.trigger),
	);
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: triggersQueryOptions(organizationId, connectionId).queryKey,
		});

	return (
		<div className="apps-panel">
			<div className="apps-panel-header">
				<p className="apps-panel-note">
					Krude reads these events and reports them in your “Updates from your
					apps” thread.
				</p>
				<AddTriggerDialog
					available={available}
					connectionId={connectionId}
					loading={types.isPending}
					onAdded={refresh}
					organizationId={organizationId}
					trigger={
						<Button
							disabled={types.isPending || types.isError}
							size="sm"
							type="button"
							variant="ghost"
						>
							Add
						</Button>
					}
				/>
			</div>
			{triggers.isPending ? <SettingsListSkeleton /> : null}
			{triggers.isError ? (
				<AppAlert>
					{appsErrorMessage(triggers.error, "Could not load triggers")}
				</AppAlert>
			) : null}
			{types.isError ? (
				<AppAlert>
					{appsErrorMessage(types.error, "Could not load this app's events")}
				</AppAlert>
			) : null}
			{triggers.data?.length === 0 ? (
				<SettingsListEmpty>Not watching anything yet.</SettingsListEmpty>
			) : null}
			{triggers.data && triggers.data.length > 0 ? (
				<ul className="settings-list">
					{triggers.data.map((trigger) => (
						<TriggerRow
							key={trigger.id}
							onRemoved={refresh}
							organizationId={organizationId}
							trigger={trigger}
							type={typeBySlug.get(trigger.trigger)}
						/>
					))}
				</ul>
			) : null}
		</div>
	);
}

interface TriggerRowProps {
	onRemoved: () => void;
	organizationId: string;
	trigger: Trigger;
	type: TriggerType | undefined;
}

function TriggerRow({
	onRemoved,
	organizationId,
	trigger,
	type,
}: TriggerRowProps) {
	const name = type?.name ?? trigger.trigger;

	return (
		<li className="settings-list-row">
			<div className="settings-list-copy">
				<strong>{name}</strong>
				{type?.description ? <small>{type.description}</small> : null}
			</div>
			{trigger.status !== "active" ? (
				<StatusBadge
					tone={trigger.status === "disabled" ? "warning" : "neutral"}
				>
					{trigger.status === "disabled" ? "Disabled" : "Removed"}
				</StatusBadge>
			) : null}
			<ConfirmDialog
				confirmLabel="Stop"
				description={`Krude stops watching ${name} on this connection.`}
				onConfirm={async () => {
					await removeTrigger(organizationId, trigger.id);
					onRemoved();
				}}
				title="Stop watching this event?"
				trigger={
					<Button
						aria-label={`Stop watching ${name}`}
						size="icon-sm"
						type="button"
						variant="ghost"
					>
						<Trash2 />
					</Button>
				}
			/>
		</li>
	);
}

interface AddTriggerDialogProps {
	available: TriggerType[];
	connectionId: string;
	loading: boolean;
	onAdded: () => void;
	organizationId: string;
	trigger: ReactElement;
}

function AddTriggerDialog({
	available,
	connectionId,
	loading,
	onAdded,
	organizationId,
	trigger,
}: AddTriggerDialogProps) {
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const filtered = available.filter((type) =>
		`${type.name} ${type.trigger}`
			.toLowerCase()
			.includes(query.trim().toLowerCase()),
	);

	return (
		<Dialog onOpenChange={setOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Wake Krude on an event</DialogTitle>
					<DialogDescription>
						Choose what this connection should watch for.
					</DialogDescription>
				</DialogHeader>
				{available.length > 5 ? (
					<Input
						aria-label="Search events"
						onChange={(event) => setQuery(event.target.value)}
						placeholder="Search events…"
						value={query}
					/>
				) : null}
				{loading ? <p className="dialog-note">Loading…</p> : null}
				{!loading && filtered.length === 0 ? (
					<p className="dialog-note">
						{available.length === 0
							? "Every event is already watched."
							: "No events match."}
					</p>
				) : null}
				{filtered.length > 0 ? (
					<ul className="apps-catalog-list">
						{filtered.map((type) => (
							<AddTriggerRow
								connectionId={connectionId}
								key={type.trigger}
								onAdded={onAdded}
								organizationId={organizationId}
								type={type}
							/>
						))}
					</ul>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

interface AddTriggerRowProps {
	connectionId: string;
	onAdded: () => void;
	organizationId: string;
	type: TriggerType;
}

function AddTriggerRow({
	connectionId,
	onAdded,
	organizationId,
	type,
}: AddTriggerRowProps) {
	const capability = triggerConfigCapability(type.config_schema);
	const [expanded, setExpanded] = useState(false);
	const [values, setValues] = useState<
		Record<string, boolean | number | string>
	>({});
	const [problem, setProblem] = useState("");
	const add = useMutation({
		mutationFn: (config: Record<string, unknown>) =>
			createTrigger(organizationId, {
				config,
				connectionId,
				trigger: type.trigger,
			}),
		onSuccess: () => {
			onAdded();
			setExpanded(false);
			setValues({});
			setProblem("");
		},
	});

	if (capability.kind === "unsupported") {
		return (
			<li
				className="settings-list-row"
				title="This trigger needs setup this page cannot collect yet."
			>
				<div className="settings-list-copy">
					<strong>{type.name}</strong>
					<small>{type.description}</small>
				</div>
				<Button disabled size="sm" type="button" variant="ghost">
					Needs setup
				</Button>
			</li>
		);
	}

	function submit() {
		if (capability.kind !== "form") {
			add.mutate({});
			return;
		}
		const config: Record<string, unknown> = {};
		for (const field of capability.fields) {
			const outcome = triggerConfigFieldSubmission(field, values[field.name]);
			if (outcome.kind === "blocked") {
				setProblem(outcome.problem);
				return;
			}
			if (outcome.kind === "value") {
				config[field.name] = outcome.value;
			}
		}
		setProblem("");
		add.mutate(config);
	}

	return (
		<li className="apps-trigger-add-row">
			<div className="settings-list-row">
				<div className="settings-list-copy">
					<strong>{type.name}</strong>
					<small>{type.description}</small>
					{add.isError ? (
						<small className="settings-list-error">
							{appsErrorMessage(add.error, "Could not add this trigger")}
						</small>
					) : null}
				</div>
				{capability.kind === "none" ? (
					<Button
						disabled={add.isPending}
						onClick={submit}
						size="sm"
						type="button"
						variant="ghost"
					>
						{add.isPending ? "Adding…" : "Add"}
					</Button>
				) : (
					<Button
						onClick={() => {
							setExpanded((current) => !current);
							setProblem("");
						}}
						size="sm"
						type="button"
						variant="ghost"
					>
						{expanded ? "Cancel" : "Add"}
					</Button>
				)}
			</div>
			{capability.kind === "form" && expanded ? (
				<form
					className="apps-trigger-form"
					onSubmit={(event) => {
						event.preventDefault();
						submit();
					}}
				>
					{capability.fields.map((field) => (
						<TriggerConfigInput
							field={field}
							key={field.name}
							onChange={(value) =>
								setValues((current) => ({ ...current, [field.name]: value }))
							}
							value={values[field.name]}
						/>
					))}
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					<Button disabled={add.isPending} size="sm" type="submit">
						{add.isPending ? "Adding…" : "Subscribe"}
					</Button>
				</form>
			) : null}
		</li>
	);
}

/** A Select works in string keys; this is how an option's own typed value becomes one. */
function optionKey(value: boolean | number | string): string {
	return JSON.stringify(value);
}

interface TriggerConfigInputProps {
	field: TriggerConfigField;
	onChange: (value: boolean | number | string) => void;
	value: boolean | number | string | undefined;
}

function TriggerConfigInput({
	field,
	onChange,
	value,
}: TriggerConfigInputProps) {
	const id = useId();

	if (field.type === "boolean") {
		return (
			<label className="apps-trigger-field-checkbox" htmlFor={id}>
				<Checkbox
					checked={typeof value === "boolean" ? value : Boolean(field.default)}
					id={id}
					onCheckedChange={(checked) => onChange(checked === true)}
				/>
				{field.title}
			</label>
		);
	}

	const label = (
		<Label htmlFor={id}>
			{field.title}
			{field.required ? " *" : ""}
		</Label>
	);
	const hint = field.description ? (
		<p className="dialog-field-hint">{field.description}</p>
	) : null;

	// An enum is the values the app itself named, so it is chosen rather than
	// typed: nothing else would be accepted anyway. The Select itself only
	// deals in string keys, so each option's own JSON-typed value travels
	// alongside its label and is what onChange actually reports.
	if (field.type === "select" && field.options !== undefined) {
		const options = field.options;
		const chosen = value !== undefined ? value : field.default;
		const chosenKey = chosen === undefined ? undefined : optionKey(chosen);
		const selected = options.some(
			(option) => optionKey(option.value) === chosenKey,
		)
			? chosenKey
			: undefined;
		return (
			<div className="dialog-field">
				{label}
				<Select
					onValueChange={(key) => {
						const option = options.find(
							(candidate) => optionKey(candidate.value) === key,
						);
						if (option) {
							onChange(option.value);
						}
					}}
					value={selected ?? ""}
				>
					<SelectTrigger id={id}>
						<SelectValue placeholder="Choose…" />
					</SelectTrigger>
					<SelectContent>
						{options.map((option) => (
							<SelectItem
								key={optionKey(option.value)}
								value={optionKey(option.value)}
							>
								{option.label}
							</SelectItem>
						))}
					</SelectContent>
				</Select>
				{hint}
			</div>
		);
	}

	// A list is typed one per line, which needs no tag widget and no parsing
	// a member has to guess at.
	if (field.type === "strings") {
		return (
			<div className="dialog-field">
				{label}
				<Textarea
					id={id}
					onChange={(event) => onChange(event.target.value)}
					placeholder="One per line"
					required={field.required}
					rows={3}
					value={typeof value === "string" ? value : ""}
				/>
				{hint}
			</div>
		);
	}

	const isNumeric = field.type === "integer" || field.type === "number";
	return (
		<div className="dialog-field">
			{label}
			<Input
				id={id}
				onChange={(event) => onChange(event.target.value)}
				required={field.required}
				step={field.type === "integer" ? 1 : undefined}
				type={isNumeric ? "number" : "text"}
				value={typeof value === "string" ? value : ""}
			/>
			{hint}
		</div>
	);
}
