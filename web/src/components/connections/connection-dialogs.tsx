import { useMutation, useQueryClient } from "@tanstack/react-query";
import { type ReactElement, useId, useState } from "react";
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
import { apiErrorMessage } from "@/lib/api/client";
import {
	type App,
	authorizeConnection,
	type Connection,
	type CredentialType,
	connectionsQueryKey,
	connectMethod,
	connectReturnUri,
	createKeyConnection,
	keyCredential,
	type PlacementOption,
	replaceConnectionCredential,
	updateConnection,
} from "@/lib/connections/client";
import { credentialTypeNames } from "@/lib/tools/catalog";

const placementHints: Record<PlacementOption["scope"], string> = {
	member: "Only you can use it.",
	organization: "Everyone in the organization can use it.",
	team: "Everyone who can see the team can use it.",
};

interface ConnectToolDialogProps {
	app: App;
	onConnected: (connection: Connection) => void;
	organizationId: string;
	/** Where the viewer may put the connection; there is at least one. */
	placements: PlacementOption[];
	toolName: string;
	trigger: ReactElement;
}

export function ConnectToolDialog({
	app,
	onConnected,
	organizationId,
	placements,
	toolName,
	trigger,
}: ConnectToolDialogProps) {
	const queryClient = useQueryClient();
	const placementId = useId();
	const method = connectMethod(app.credential_type);
	const secretName = credentialTypeNames[app.credential_type];
	const [open, setOpen] = useState(false);
	const [placement, setPlacement] = useState(placements[0]?.value ?? "");
	const [secret, setSecret] = useState("");
	const [username, setUsername] = useState("");
	const [label, setLabel] = useState("");
	const [problem, setProblem] = useState("");
	const [leaving, setLeaving] = useState(false);
	const chosen = placements.find((option) => option.value === placement);
	const connect = useMutation({
		mutationFn: async (
			where: PlacementOption,
		): Promise<Connection | undefined> => {
			const target = { scope: where.scope, team_id: where.team_id };
			if (method === "consent") {
				const { authorization_url } = await authorizeConnection(
					organizationId,
					{
						...target,
						app_id: app.id,
						return_uri: connectReturnUri(
							window.location.origin,
							organizationId,
						),
					},
				);
				// The provider sends the browser back to the connections page.
				setLeaving(true);
				window.location.assign(authorization_url);
				return undefined;
			}
			return createKeyConnection(organizationId, {
				...target,
				app_id: app.id,
				credential: keyCredential(
					app.credential_type,
					secret.trim(),
					username.trim(),
				),
				label: label.trim() || undefined,
			});
		},
		onSuccess: async (connection) => {
			if (!connection) return;
			await queryClient.invalidateQueries({
				queryKey: connectionsQueryKey(organizationId),
			});
			setOpen(false);
			onConnected(connection);
		},
	});

	function changeOpen(next: boolean) {
		if (connect.isPending || leaving) return;
		setOpen(next);
		if (next) {
			setPlacement(placements[0]?.value ?? "");
			setSecret("");
			setUsername("");
			setLabel("");
			setProblem("");
			connect.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!chosen) {
			setProblem("Choose who the connection is for.");
			return;
		}
		if (app.credential_type === "basic" && username.trim() === "") {
			setProblem("Enter the username first.");
			return;
		}
		if (method === "key" && secret.trim() === "") {
			setProblem(`Paste the ${secretName.toLowerCase()} first.`);
			return;
		}
		setProblem("");
		connect.mutate(chosen);
	}

	let submitText = method === "consent" ? `Continue to ${toolName}` : "Connect";
	if (connect.isPending || leaving) {
		submitText = method === "consent" ? `Opening ${toolName}…` : "Checking…";
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Connect {toolName}</DialogTitle>
					<DialogDescription>
						{method === "consent"
							? `You approve access on ${toolName}, then come back here.`
							: `${toolName} checks the ${secretName.toLowerCase()} before it is saved.`}
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					{connect.isError ? (
						<AppAlert>
							{apiErrorMessage(connect.error, `Could not connect ${toolName}`)}
						</AppAlert>
					) : null}
					{placements.length > 1 ? (
						<div className="dialog-field">
							<Label htmlFor={placementId}>Connect for</Label>
							<Select onValueChange={setPlacement} value={placement}>
								<SelectTrigger className="w-full" id={placementId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{placements.map((option) => (
										<SelectItem key={option.value} value={option.value}>
											{option.label}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
							{chosen ? (
								<p className="dialog-field-hint">
									{placementHints[chosen.scope]}
								</p>
							) : null}
						</div>
					) : null}
					{method === "key" ? (
						<>
							<SecretFields
								credentialType={app.credential_type}
								onSecretChange={setSecret}
								onUsernameChange={setUsername}
								secret={secret}
								username={username}
							/>
							<LabelField onChange={setLabel} value={label} />
						</>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={connect.isPending || leaving} type="submit">
							{submitText}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface ConnectionDialogProps {
	connection: Connection;
	organizationId: string;
	toolName: string;
	trigger: ReactElement;
}

export function RenameConnectionDialog({
	connection,
	organizationId,
	toolName,
	trigger,
}: ConnectionDialogProps) {
	const queryClient = useQueryClient();
	const [open, setOpen] = useState(false);
	const [label, setLabel] = useState(connection.label ?? "");
	const rename = useMutation({
		mutationFn: (value: string) =>
			updateConnection(organizationId, connection.id, { label: value }),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: connectionsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setLabel(connection.label ?? "");
			rename.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		rename.mutate(label.trim());
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Label this {toolName} connection</DialogTitle>
					<DialogDescription>
						A label tells accounts on the same tool apart.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{rename.isError ? (
						<AppAlert>
							{apiErrorMessage(rename.error, "Could not save the label")}
						</AppAlert>
					) : null}
					<LabelField onChange={setLabel} value={label} />
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={rename.isPending} type="submit">
							{rename.isPending ? "Saving…" : "Save"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

export function ReplaceKeyDialog({
	connection,
	organizationId,
	toolName,
	trigger,
}: ConnectionDialogProps) {
	const queryClient = useQueryClient();
	const secretName = credentialTypeNames[connection.credential_type];
	const [open, setOpen] = useState(false);
	const [secret, setSecret] = useState("");
	const [username, setUsername] = useState("");
	const [problem, setProblem] = useState("");
	const replace = useMutation({
		mutationFn: () =>
			replaceConnectionCredential(
				organizationId,
				connection.id,
				keyCredential(
					connection.credential_type,
					secret.trim(),
					username.trim(),
				),
			),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: connectionsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		if (replace.isPending) return;
		setOpen(next);
		if (next) {
			setSecret("");
			setUsername("");
			setProblem("");
			replace.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (secret.trim() === "") {
			setProblem(`Paste the new ${secretName.toLowerCase()} first.`);
			return;
		}
		setProblem("");
		replace.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Replace the {secretName.toLowerCase()}</DialogTitle>
					<DialogDescription>
						It must belong to the same {toolName} account as before.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					{replace.isError ? (
						<AppAlert>
							{apiErrorMessage(replace.error, "Could not replace it")}
						</AppAlert>
					) : null}
					<SecretFields
						credentialType={connection.credential_type}
						onSecretChange={setSecret}
						onUsernameChange={setUsername}
						secret={secret}
						username={username}
					/>
					<DialogFooter>
						<DialogClose asChild>
							<Button
								disabled={replace.isPending}
								type="button"
								variant="ghost"
							>
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={replace.isPending} type="submit">
							{replace.isPending ? "Checking…" : "Replace"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

interface SecretFieldsProps {
	credentialType: CredentialType;
	onSecretChange: (value: string) => void;
	onUsernameChange: (value: string) => void;
	secret: string;
	username: string;
}

function SecretFields({
	credentialType,
	onSecretChange,
	onUsernameChange,
	secret,
	username,
}: SecretFieldsProps) {
	const usernameId = useId();
	const secretId = useId();
	return (
		<>
			{credentialType === "basic" ? (
				<div className="dialog-field">
					<Label htmlFor={usernameId}>Username</Label>
					<Input
						autoComplete="off"
						id={usernameId}
						onChange={(event) => onUsernameChange(event.target.value)}
						value={username}
					/>
				</div>
			) : null}
			<div className="dialog-field">
				<Label htmlFor={secretId}>
					{credentialType === "basic"
						? "Password"
						: credentialTypeNames[credentialType]}
				</Label>
				<Input
					autoComplete="off"
					id={secretId}
					onChange={(event) => onSecretChange(event.target.value)}
					type="password"
					value={secret}
				/>
			</div>
		</>
	);
}

function LabelField({
	onChange,
	value,
}: {
	onChange: (value: string) => void;
	value: string;
}) {
	const labelId = useId();
	return (
		<div className="dialog-field">
			<Label htmlFor={labelId}>Label</Label>
			<Input
				autoComplete="off"
				id={labelId}
				onChange={(event) => onChange(event.target.value)}
				placeholder="Work account"
				value={value}
			/>
			<p className="dialog-field-hint">Optional.</p>
		</div>
	);
}
