import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Trash2 } from "lucide-react";
import { type FormEvent, type ReactElement, useId, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import {
	SettingsListEmpty,
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
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
import { apiErrorMessage } from "@/lib/api/client";
import { connectionTargetsQueryOptions } from "@/lib/apps/client";
import { relativeTime } from "@/lib/format";
import {
	defaultSourceIntervalSeconds,
	intervalLabel,
	knowledgeQueryKey,
	registerSource,
	removeSource,
	runStatusLabel,
	type Source,
	type SourceProvider,
	sourceIntervalChoices,
	sourceRunsQueryOptions,
	sourceStatusLabel,
	sourceStatusTone,
	sourcesQueryOptions,
	sourceTargetField,
	sourceTargetLabel,
	sourceTargetPayload,
	sourceTargetProblem,
} from "@/lib/knowledge/client";

interface ConnectionSourcesProps {
	connectionId: string;
	organizationId: string;
	provider: SourceProvider;
}

/** Sources agents may search through this connection. */
export function ConnectionSources({
	connectionId,
	organizationId,
	provider,
}: ConnectionSourcesProps) {
	const queryClient = useQueryClient();
	const sources = useQuery(
		sourcesQueryOptions(organizationId, { connectionId }),
	);
	const refresh = () =>
		queryClient.invalidateQueries({
			queryKey: knowledgeQueryKey(organizationId),
		});

	return (
		<div className="apps-panel">
			<div className="apps-panel-header">
				<p className="apps-panel-note">
					Your agents can search what is indexed here alongside the rest of your
					organization's knowledge.
				</p>
				<IndexSourceDialog
					connectionId={connectionId}
					onIndexed={refresh}
					organizationId={organizationId}
					provider={provider}
					trigger={
						<Button size="sm" type="button" variant="ghost">
							Index
						</Button>
					}
				/>
			</div>
			{sources.isPending ? <SettingsListSkeleton /> : null}
			{sources.isError ? (
				<AppAlert>
					{apiErrorMessage(sources.error, "Could not load sources")}
				</AppAlert>
			) : null}
			{sources.data?.length === 0 ? (
				<SettingsListEmpty>Nothing indexed yet.</SettingsListEmpty>
			) : null}
			{sources.data && sources.data.length > 0 ? (
				<ul className="settings-list">
					{sources.data.map((source) => (
						<SourceRow
							key={source.id}
							onRemoved={refresh}
							organizationId={organizationId}
							source={source}
						/>
					))}
				</ul>
			) : null}
		</div>
	);
}

interface SourceRowProps {
	onRemoved: () => void;
	organizationId: string;
	source: Source;
}

function SourceRow({ onRemoved, organizationId, source }: SourceRowProps) {
	const label = sourceTargetLabel(source.provider, source.target);

	return (
		<li className="settings-list-row">
			<div className="settings-list-copy">
				<strong>{label}</strong>
				<small>
					{intervalLabel(source.interval_seconds)}
					{" · "}
					{source.last_synced_at
						? `last run ${relativeTime(source.last_synced_at)}`
						: "not run yet"}
				</small>
				{source.reason ? (
					<small className="settings-list-error">{source.reason}</small>
				) : null}
			</div>
			<StatusBadge tone={sourceStatusTone(source.status)}>
				{sourceStatusLabel(source.status)}
			</StatusBadge>
			<RunsDialog
				organizationId={organizationId}
				sourceId={source.id}
				trigger={
					<Button size="sm" type="button" variant="ghost">
						Runs
					</Button>
				}
			/>
			<ConfirmDialog
				confirmLabel="Remove"
				description={`Your agents stop indexing ${label}, and what is already indexed here is retracted.`}
				onConfirm={async () => {
					await removeSource(organizationId, source.id);
					onRemoved();
				}}
				title="Stop indexing this?"
				trigger={
					<Button
						aria-label={`Stop indexing ${label}`}
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

interface RunsDialogProps {
	organizationId: string;
	sourceId: string;
	trigger: ReactElement;
}

function RunsDialog({ organizationId, sourceId, trigger }: RunsDialogProps) {
	const [open, setOpen] = useState(false);
	const runs = useQuery({
		...sourceRunsQueryOptions(organizationId, sourceId),
		enabled: open,
	});

	return (
		<Dialog onOpenChange={setOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Recent runs</DialogTitle>
					<DialogDescription>
						How recent passes over this source went.
					</DialogDescription>
				</DialogHeader>
				{runs.isPending ? <p className="dialog-note">Loading…</p> : null}
				{runs.isError ? (
					<AppAlert>
						{apiErrorMessage(runs.error, "Could not load runs")}
					</AppAlert>
				) : null}
				{runs.data?.length === 0 ? (
					<p className="dialog-note">No runs yet.</p>
				) : null}
				{runs.data && runs.data.length > 0 ? (
					<ul className="apps-run-list">
						{runs.data.map((run) => (
							<li key={run.id}>
								<div className="settings-list-copy">
									<strong>
										{runStatusLabel(run.status)} ·{" "}
										{relativeTime(run.started_at)}
									</strong>
									<small>
										{run.documents_published} published ·{" "}
										{run.documents_removed} removed of {run.documents_seen} seen
									</small>
									{run.error ? (
										<small className="settings-list-error">{run.error}</small>
									) : null}
								</div>
							</li>
						))}
					</ul>
				) : null}
			</DialogContent>
		</Dialog>
	);
}

interface IndexSourceDialogProps {
	connectionId: string;
	onIndexed: () => void;
	organizationId: string;
	provider: SourceProvider;
	trigger: ReactElement;
}

function IndexSourceDialog({
	connectionId,
	onIndexed,
	organizationId,
	provider,
	trigger,
}: IndexSourceDialogProps) {
	const field = sourceTargetField(provider);
	const targetId = useId();
	const intervalId = useId();
	const [open, setOpen] = useState(false);
	const [target, setTarget] = useState("");
	const [typeItOut, setTypeItOut] = useState(false);
	const targets = useQuery({
		...connectionTargetsQueryOptions(organizationId, connectionId),
		enabled: open,
	});
	const discovered = targets.data ?? [];
	const offerPicker = !typeItOut && discovered.length > 0;
	const [intervalSeconds, setIntervalSeconds] = useState(
		defaultSourceIntervalSeconds,
	);
	const [problem, setProblem] = useState("");
	const index = useMutation({
		mutationFn: () =>
			registerSource(organizationId, {
				connectionId,
				intervalSeconds,
				provider,
				target: sourceTargetPayload(provider, target),
			}),
		onSuccess: () => {
			onIndexed();
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		if (index.isPending) {
			return;
		}
		setOpen(next);
		if (next) {
			setTarget("");
			setTypeItOut(false);
			setIntervalSeconds(defaultSourceIntervalSeconds);
			setProblem("");
			index.reset();
		}
	}

	function submit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const validationProblem = sourceTargetProblem(provider, target);
		if (validationProblem) {
			setProblem(validationProblem);
			return;
		}
		setProblem("");
		index.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Add to searchable knowledge</DialogTitle>
					<DialogDescription>{field.help}</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" onSubmit={submit}>
					<div className="dialog-field">
						<Label htmlFor={targetId}>
							{field.label}
							{field.required ? " *" : " (optional)"}
						</Label>
						{/*
						 * The app is asked what it holds, so nobody has to know a
						 * channel's id. Typing one stays possible: discovery can
						 * be refused a scope, or simply find nothing, and neither
						 * should stop a member indexing something they can name.
						 */}
						{offerPicker ? (
							<Select onValueChange={setTarget} value={target}>
								<SelectTrigger className="w-full" id={targetId}>
									<SelectValue
										placeholder={`Choose a ${field.label.toLowerCase()}…`}
									/>
								</SelectTrigger>
								<SelectContent>
									{targets.data?.map((choice) => (
										<SelectItem key={choice.id} value={choice.id}>
											{choice.name}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						) : (
							<Input
								id={targetId}
								onChange={(event) => {
									setTarget(event.target.value);
									// Typing here is the same choice as "Enter one by
									// hand instead": once made, the list loading in
									// should not swap away what is already on screen
									// and about to be sent.
									setTypeItOut(true);
								}}
								placeholder={field.placeholder}
								required={field.required}
								value={target}
							/>
						)}
						{targets.isPending ? (
							<p className="dialog-field-hint">Reading what this app holds…</p>
						) : null}
						{offerPicker ? (
							<button
								className="dialog-field-link"
								onClick={() => {
									setTypeItOut(true);
									setTarget("");
								}}
								type="button"
							>
								Enter one by hand instead
							</button>
						) : null}
					</div>
					<div className="dialog-field">
						<Label htmlFor={intervalId}>How often</Label>
						<Select
							onValueChange={(value) => setIntervalSeconds(Number(value))}
							value={String(intervalSeconds)}
						>
							<SelectTrigger className="w-full" id={intervalId}>
								<SelectValue />
							</SelectTrigger>
							<SelectContent>
								{sourceIntervalChoices.map((choice) => (
									<SelectItem
										key={choice.seconds}
										value={String(choice.seconds)}
									>
										{choice.label}
									</SelectItem>
								))}
							</SelectContent>
						</Select>
					</div>
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					{index.isError ? (
						<AppAlert>
							{apiErrorMessage(index.error, "Could not index this")}
						</AppAlert>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button disabled={index.isPending} type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={index.isPending} type="submit">
							{index.isPending ? "Indexing…" : "Index"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}
