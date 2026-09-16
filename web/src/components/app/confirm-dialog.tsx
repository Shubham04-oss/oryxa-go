import { type ReactElement, type ReactNode, useId, useState } from "react";
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
import { apiErrorMessage } from "@/lib/api/client";

interface ConfirmDialogProps {
	confirmLabel: string;
	/** Text to type before confirming, for what cannot be undone. */
	confirmation?: string;
	description: ReactNode;
	/** Words for a failure the service's own message does not explain. */
	explainFailure?: (error: unknown) => string | undefined;
	onConfirm: () => Promise<unknown>;
	title: string;
	trigger: ReactElement;
}

export function ConfirmDialog({
	confirmLabel,
	confirmation,
	description,
	explainFailure,
	onConfirm,
	title,
	trigger,
}: ConfirmDialogProps) {
	const inputId = useId();
	const [open, setOpen] = useState(false);
	const [typed, setTyped] = useState("");
	const [error, setError] = useState("");
	const [pending, setPending] = useState(false);

	function changeOpen(next: boolean) {
		if (pending) return;
		setOpen(next);
		setTyped("");
		setError("");
	}

	async function confirm() {
		setError("");
		setPending(true);
		try {
			await onConfirm();
			setOpen(false);
			setTyped("");
		} catch (caught) {
			setError(
				explainFailure?.(caught) ??
					apiErrorMessage(caught, "That did not work. Try again."),
			);
		} finally {
			setPending(false);
		}
	}

	const ready = confirmation === undefined || typed.trim() === confirmation;

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>{title}</DialogTitle>
					<DialogDescription>{description}</DialogDescription>
				</DialogHeader>
				{confirmation === undefined ? null : (
					<div className="dialog-field">
						<Label htmlFor={inputId}>Type {confirmation} to confirm</Label>
						<Input
							autoComplete="off"
							id={inputId}
							onChange={(event) => setTyped(event.target.value)}
							value={typed}
						/>
					</div>
				)}
				{error ? <AppAlert>{error}</AppAlert> : null}
				<DialogFooter>
					<DialogClose asChild>
						<Button disabled={pending} type="button" variant="ghost">
							Cancel
						</Button>
					</DialogClose>
					<Button
						disabled={!ready || pending}
						onClick={confirm}
						type="button"
						variant="destructive"
					>
						{pending ? "Working…" : confirmLabel}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
