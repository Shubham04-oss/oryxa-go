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
import { Textarea } from "@/components/ui/textarea";
import { apiErrorMessage } from "@/lib/api/client";
import {
	type CreateAppInput,
	type CredentialType,
	type CredentialTypeDescription,
	connectionsQueryKey,
	createApp,
} from "@/lib/connections/client";
import { credentialTypeNames, type ToolOption } from "@/lib/tools/catalog";
import { scopeProblem, splitScopes } from "@/lib/tools/scopes";

const methodHints: Record<CredentialType, string> = {
	api_key: "Each member connects with an API key from their own account.",
	basic: "Each member connects with a username and password.",
	bearer: "Each member connects with a personal access token.",
	installation:
		"Members connect GitHub organizations where your GitHub App is installed.",
	oauth2:
		"Members approve access on the provider, through an OAuth app you register.",
	oauth2_client_credentials:
		"An admin grants app-only access to a whole tenant; no member signs in.",
};

interface EnableToolDialogProps {
	/** Ways the tool is already enabled, which are not offered again. */
	enabledTypes: ReadonlySet<CredentialType>;
	organizationId: string;
	tool: ToolOption;
	trigger: ReactElement;
}

export function EnableToolDialog({
	enabledTypes,
	organizationId,
	tool,
	trigger,
}: EnableToolDialogProps) {
	const queryClient = useQueryClient();
	const methodId = useId();
	const methods = tool.credentialTypes.filter(
		(type) => !enabledTypes.has(type.credential_type),
	);
	const firstMethod = (methods[0] ?? tool.credentialTypes[0])?.credential_type;
	const [open, setOpen] = useState(false);
	const [credentialType, setCredentialType] =
		useState<CredentialType>(firstMethod);
	const [fields, setFields] = useState<Record<string, string>>({});
	const [problem, setProblem] = useState("");
	const create = useMutation({
		mutationFn: (input: CreateAppInput) => createApp(organizationId, input),
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: connectionsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	const method: CredentialTypeDescription | undefined =
		tool.credentialTypes.find(
			(candidate) => candidate.credential_type === credentialType,
		);
	const installation = credentialType === "installation";
	// Connections says what the app carries: a client to register, a domain of
	// the organization's own, and the scopes an admin may allow.
	const needsClient = method?.client ?? false;
	const domainRule = method?.domain;
	const allowedScopes = method?.allowed_scopes ?? [];
	const requiredScopes = method?.required_scopes ?? [];
	const scoped = credentialType === "oauth2" && allowedScopes.length > 0;

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setCredentialType(firstMethod);
			setFields({});
			setProblem("");
			create.reset();
		}
	}

	function setField(name: string, value: string) {
		setFields((current) => ({ ...current, [name]: value }));
	}

	function value(name: string): string {
		return fields[name]?.trim() ?? "";
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		const required = needsClient ? ["client_id", "client_secret"] : [];
		if (installation) {
			required.push("github_app_id", "github_app_slug", "private_key");
		}
		if (domainRule?.required) {
			required.push("domain");
		}
		if (required.some((name) => value(name) === "")) {
			setProblem("Fill in every field to enable it this way.");
			return;
		}
		const scopes = scoped ? splitScopes(value("scopes")) : [];
		const wrong = scoped && method ? scopeProblem(scopes, method) : undefined;
		if (wrong) {
			setProblem(wrong);
			return;
		}
		setProblem("");

		const input: CreateAppInput = {
			credential_type: credentialType,
			provider: tool.provider,
		};
		if (needsClient) {
			input.client_id = value("client_id");
			input.client_secret = value("client_secret");
		}
		if (installation) {
			input.github_app_id = value("github_app_id");
			input.github_app_slug = value("github_app_slug");
			input.private_key = value("private_key");
		}
		if (domainRule && value("domain") !== "") {
			input.domain = value("domain");
		}
		// Client-credential and installation apps carry fixed permissions.
		if (scopes.length > 0) {
			input.scopes = scopes;
		}
		create.mutate(input);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Enable {tool.name}</DialogTitle>
					<DialogDescription>{methodHints[credentialType]}</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{problem ? <AppAlert>{problem}</AppAlert> : null}
					{create.isError ? (
						<AppAlert>
							{apiErrorMessage(create.error, `Could not enable ${tool.name}`)}
						</AppAlert>
					) : null}
					{methods.length > 1 ? (
						<div className="dialog-field">
							<Label htmlFor={methodId}>How members connect</Label>
							<Select
								onValueChange={(next) =>
									setCredentialType(next as CredentialType)
								}
								value={credentialType}
							>
								<SelectTrigger className="w-full" id={methodId}>
									<SelectValue />
								</SelectTrigger>
								<SelectContent>
									{methods.map((type) => (
										<SelectItem
											key={type.credential_type}
											value={type.credential_type}
										>
											{credentialTypeNames[type.credential_type]}
										</SelectItem>
									))}
								</SelectContent>
							</Select>
						</div>
					) : null}
					{needsClient || domainRule ? null : (
						<p className="dialog-note">
							Nothing to set up here. Members add their own{" "}
							{credentialTypeNames[credentialType].toLowerCase()} when they
							connect.
						</p>
					)}
					{installation ? (
						<>
							<TextField
								label="GitHub App ID"
								name="github_app_id"
								onChange={setField}
								value={fields.github_app_id ?? ""}
							/>
							<TextField
								hint="The name in the app's address on GitHub."
								label="GitHub App slug"
								name="github_app_slug"
								onChange={setField}
								value={fields.github_app_slug ?? ""}
							/>
						</>
					) : null}
					{needsClient ? (
						<>
							<TextField
								label="Client ID"
								name="client_id"
								onChange={setField}
								value={fields.client_id ?? ""}
							/>
							<TextField
								label="Client secret"
								name="client_secret"
								onChange={setField}
								type="password"
								value={fields.client_secret ?? ""}
							/>
						</>
					) : null}
					{domainRule ? (
						<TextField
							hint={
								domainRule.default
									? `Optional. ${domainRule.default} unless your organization has its own, such as ${domainRule.example}.`
									: `Your organization's address at ${tool.name}, such as ${domainRule.example}.`
							}
							label="Domain"
							name="domain"
							onChange={setField}
							placeholder={domainRule.default || domainRule.example}
							value={fields.domain ?? ""}
						/>
					) : null}
					{installation ? (
						<TextField
							hint="The private key GitHub generated for the app."
							label="Private key"
							multiline
							name="private_key"
							onChange={setField}
							value={fields.private_key ?? ""}
						/>
					) : null}
					{scoped ? (
						<TextField
							hint={scopeHint(requiredScopes, allowedScopes.length)}
							label="Scopes"
							name="scopes"
							onChange={setField}
							placeholder={requiredScopes.join(" ")}
							value={fields.scopes ?? ""}
						/>
					) : null}
					{credentialType === "oauth2" && allowedScopes.length === 0 ? (
						<p className="dialog-note">
							{tool.name} has no scopes to choose. What the app may reach is set
							with {tool.name}, or by the member while they connect.
						</p>
					) : null}
					{credentialType === "oauth2" || installation ? (
						<p className="dialog-note">
							Register connections'{" "}
							<code>/v1/oauth/callback/{tool.provider}</code> as the app's
							redirect URL.
						</p>
					) : null}
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
						<Button disabled={create.isPending} type="submit">
							{create.isPending ? "Enabling…" : "Enable"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

/** What to type in the scopes field, and how much the provider offers. */
function scopeHint(required: string[], allowed: number): string {
	const always =
		required.length > 0
			? `${required.join(", ")} ${required.length === 1 ? "is" : "are"} always included. `
			: "";
	return `Optional. ${always}${allowed} scopes are offered; separate them with spaces.`;
}

interface TextFieldProps {
	hint?: string;
	label: string;
	multiline?: boolean;
	name: string;
	onChange: (name: string, value: string) => void;
	placeholder?: string;
	type?: "password" | "text";
	value: string;
}

function TextField({
	hint,
	label,
	multiline = false,
	name,
	onChange,
	placeholder,
	type = "text",
	value,
}: TextFieldProps) {
	const id = useId();
	return (
		<div className="dialog-field">
			<Label htmlFor={id}>{label}</Label>
			{multiline ? (
				<Textarea
					id={id}
					onChange={(event) => onChange(name, event.target.value)}
					placeholder={placeholder}
					rows={4}
					value={value}
				/>
			) : (
				<Input
					autoComplete="off"
					id={id}
					onChange={(event) => onChange(name, event.target.value)}
					placeholder={placeholder}
					type={type}
					value={value}
				/>
			)}
			{hint ? <p className="dialog-field-hint">{hint}</p> : null}
		</div>
	);
}
