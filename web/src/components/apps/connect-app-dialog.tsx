import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { type ReactElement, type ReactNode, useState } from "react";
import { AppAlert } from "@/components/app/app-alert";
import { Button } from "@/components/ui/button";
import {
	Dialog,
	DialogContent,
	DialogDescription,
	DialogHeader,
	DialogTitle,
	DialogTrigger,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { useDebouncedValue } from "@/hooks/use-debounced-value";
import {
	appsErrorMessage,
	appsQueryKey,
	catalogQueryOptions,
	connect,
	connectReturnTo,
	nonConnectableReason,
	type Toolkit,
} from "@/lib/apps/client";
import { ToolkitIcon } from "./toolkit-icon";

interface ConnectAppDialogProps {
	organizationId: string;
	trigger: ReactElement;
}

const catalogPageSize = 40;

export function ConnectAppDialog({
	organizationId,
	trigger,
}: ConnectAppDialogProps) {
	const queryClient = useQueryClient();
	const [open, setOpen] = useState(false);
	const [query, setQuery] = useState("");
	const debounced = useDebouncedValue(query);
	const catalog = useQuery({
		...catalogQueryOptions({ limit: catalogPageSize, search: debounced }),
		enabled: open,
	});
	const connecting = useMutation({
		mutationFn: (toolkit: string) =>
			connect(organizationId, {
				returnTo: connectReturnTo(window.location.origin),
				toolkit,
			}),
		onSuccess: async ({ redirect_url }) => {
			await queryClient.invalidateQueries({
				queryKey: appsQueryKey(organizationId),
			});
			window.location.assign(redirect_url);
		},
	});

	function changeOpen(next: boolean) {
		if (connecting.isPending) {
			return;
		}
		setOpen(next);
		if (next) {
			setQuery("");
			connecting.reset();
		}
	}

	let body: ReactNode;
	if (catalog.isPending) {
		body = <p className="dialog-note">Loading apps…</p>;
	} else if (catalog.isError) {
		body = (
			<AppAlert>
				{appsErrorMessage(catalog.error, "Could not load the catalog")}
			</AppAlert>
		);
	} else if (catalog.data.toolkits.length === 0) {
		body = <p className="dialog-note">No apps match your search.</p>;
	} else {
		body = (
			<>
				<ul className="apps-catalog-grid">
					{catalog.data.toolkits.map((item) => (
						<CatalogTile
							connecting={
								connecting.isPending && connecting.variables === item.toolkit
							}
							disabled={connecting.isPending}
							key={item.toolkit}
							onConnect={() => connecting.mutate(item.toolkit)}
							toolkit={item}
						/>
					))}
				</ul>
				{catalog.data.nextCursor ? (
					<p className="dialog-note">
						More apps match — refine your search to find them.
					</p>
				) : null}
			</>
		);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="apps-catalog-dialog">
				<DialogHeader>
					<DialogTitle>Connect an app</DialogTitle>
					<DialogDescription>
						Composio hosts the sign-in; Oryxa only keeps the connection.
					</DialogDescription>
				</DialogHeader>
				<Input
					aria-label="Search apps"
					onChange={(event) => setQuery(event.target.value)}
					placeholder="Search apps…"
					value={query}
				/>
				{connecting.isError ? (
					<AppAlert>
						{appsErrorMessage(
							connecting.error,
							"Could not start the connection",
						)}
					</AppAlert>
				) : null}
				{body}
			</DialogContent>
		</Dialog>
	);
}

interface CatalogTileProps {
	connecting: boolean;
	disabled: boolean;
	onConnect: () => void;
	toolkit: Toolkit;
}

function CatalogTile({
	connecting,
	disabled,
	onConnect,
	toolkit,
}: CatalogTileProps) {
	const reason = nonConnectableReason(toolkit);

	return (
		<li className="apps-catalog-tile">
			<ToolkitIcon logo={toolkit.logo} />
			<div className="apps-catalog-tile-copy">
				<strong>{toolkit.name}</strong>
				<small>{toolkit.description}</small>
			</div>
			{reason ? (
				<small className="apps-catalog-tile-note">{reason}</small>
			) : (
				<Button
					disabled={disabled}
					onClick={onConnect}
					size="sm"
					type="button"
					variant="outline"
				>
					{connecting ? "Connecting…" : "Connect"}
				</Button>
			)}
		</li>
	);
}
