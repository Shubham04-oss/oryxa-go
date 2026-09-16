import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useId, useState } from "react";
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
import { ApiError, apiErrorMessage } from "@/lib/api/client";
import { inviteMember } from "@/lib/auth/client";
import { invitationsQueryOptions } from "@/lib/auth/organization";
import {
	fieldErrors,
	type InviteMemberInput,
	inviteMemberSchema,
} from "@/lib/auth/schemas";

interface InviteMemberDialogProps {
	/** Only owners invite admins. */
	canInviteAdmins: boolean;
	organizationId: string;
	organizationName: string;
}

export function InviteMemberDialog({
	canInviteAdmins,
	organizationId,
	organizationName,
}: InviteMemberDialogProps) {
	const queryClient = useQueryClient();
	const emailId = useId();
	const roleId = useId();
	const [open, setOpen] = useState(false);
	const [email, setEmail] = useState("");
	const [role, setRole] = useState<InviteMemberInput["role"]>("member");
	const [errors, setErrors] = useState<
		Partial<Record<keyof InviteMemberInput, string>>
	>({});
	const [sentTo, setSentTo] = useState("");
	const invite = useMutation({
		mutationFn: (input: InviteMemberInput) =>
			inviteMember(organizationId, input),
		onSuccess: async (invitation) => {
			await queryClient.invalidateQueries({
				queryKey: invitationsQueryOptions(organizationId).queryKey,
			});
			setSentTo(invitation.email);
			setEmail("");
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setEmail("");
			setRole("member");
			setErrors({});
			setSentTo("");
			invite.reset();
		}
	}

	function submit(event: React.FormEvent<HTMLFormElement>) {
		event.preventDefault();
		setSentTo("");
		const result = inviteMemberSchema.safeParse({ email, role });
		if (!result.success) {
			setErrors(fieldErrors(result));
			return;
		}
		setErrors({});
		invite.mutate(result.data);
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>
				<Button type="button">
					<Plus data-icon="inline-start" />
					Invite member
				</Button>
			</DialogTrigger>
			<DialogContent>
				<DialogHeader>
					<DialogTitle>Invite to {organizationName}</DialogTitle>
					<DialogDescription>
						They get an email with a link that works for 7 days.
					</DialogDescription>
				</DialogHeader>
				<form className="dialog-form" noValidate onSubmit={submit}>
					{sentTo ? (
						<AppAlert tone="success">Invitation sent to {sentTo}.</AppAlert>
					) : null}
					{invite.isError ? (
						<AppAlert>{inviteFailure(invite.error)}</AppAlert>
					) : null}
					<div className="dialog-field">
						<Label htmlFor={emailId}>Email</Label>
						<Input
							aria-invalid={Boolean(errors.email)}
							autoComplete="off"
							id={emailId}
							onChange={(event) => setEmail(event.target.value)}
							placeholder="member@company.com"
							type="email"
							value={email}
						/>
						{errors.email ? (
							<p className="field-error">{errors.email}</p>
						) : null}
					</div>
					<div className="dialog-field">
						<Label htmlFor={roleId}>Role</Label>
						<Select
							disabled={!canInviteAdmins}
							onValueChange={(value) =>
								setRole(value as InviteMemberInput["role"])
							}
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
						<p className="dialog-field-hint">
							{canInviteAdmins
								? "Admins manage members and teams."
								: "Admins invite members; owners invite admins."}
						</p>
					</div>
					<DialogFooter>
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Done
							</Button>
						</DialogClose>
						<Button disabled={invite.isPending} type="submit">
							{invite.isPending ? "Sending…" : "Send invitation"}
						</Button>
					</DialogFooter>
				</form>
			</DialogContent>
		</Dialog>
	);
}

function inviteFailure(error: unknown): string {
	if (error instanceof ApiError && error.status === 402) {
		return "This organization has reached the member limit its plan allows. Upgrade the plan to invite more people.";
	}
	if (error instanceof ApiError && error.status === 409) {
		return "That person is already a member or already has a pending invitation.";
	}
	return apiErrorMessage(error, "Could not send the invitation");
}
