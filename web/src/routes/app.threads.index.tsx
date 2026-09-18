import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { createFileRoute, Link, useNavigate } from "@tanstack/react-router";
import { MessageSquareText, Plus, Trash2 } from "lucide-react";
import type { ReactNode } from "react";
import { AppAlert, QueryError } from "@/components/app/app-alert";
import { ConfirmDialog } from "@/components/app/confirm-dialog";
import { EmptyState } from "@/components/app/empty-state";
import { SettingsListSkeleton } from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { apiErrorMessage } from "@/lib/api/client";
import { useWorkspace } from "@/lib/auth/session";
import { relativeTime } from "@/lib/format";
import {
	createThread,
	deleteThread,
	type Thread,
	threadsQueryKey,
	threadsQueryOptions,
} from "@/lib/threads/client";

export const Route = createFileRoute("/app/threads/")({
	component: ThreadsPage,
	head: () => ({ meta: [{ title: "Threads · The Weirdly ODD" }] }),
});

function ThreadsPage() {
	const { organization } = useWorkspace();
	const navigate = useNavigate();
	const queryClient = useQueryClient();
	const threads = useQuery(threadsQueryOptions(organization.id));

	const startThread = useMutation({
		mutationFn: () => createThread(organization.id),
		onSuccess: async (thread) => {
			await queryClient.invalidateQueries({
				queryKey: threadsQueryKey(organization.id),
			});
			await navigate({
				params: { threadId: thread.id },
				to: "/app/threads/$threadId",
			});
		},
	});

	let content: ReactNode;
	if (threads.isPending) {
		content = <SettingsListSkeleton />;
	} else if (threads.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={threads.error}
					fallback="Could not load threads"
					onRetry={threads.refetch}
				/>
			</div>
		);
	} else if (threads.data.length === 0) {
		content = (
			<EmptyState
				description="Start a conversation with Krude to see it here."
				icon={MessageSquareText}
				title="No threads yet"
			/>
		);
	} else {
		content = (
			<div className="settings-list">
				{threads.data.map((thread) => (
					<ThreadRow key={thread.id} thread={thread} />
				))}
			</div>
		);
	}

	return (
		<WorkspacePage
			actions={
				<Button
					disabled={startThread.isPending}
					onClick={() => startThread.mutate()}
					type="button"
				>
					<Plus data-icon="inline-start" />
					{startThread.isPending ? "Creating…" : "New thread"}
				</Button>
			}
			description="Every conversation with Krude, in one place."
			title="Threads"
		>
			{startThread.isError ? (
				<AppAlert>
					{apiErrorMessage(startThread.error, "Could not start a new thread")}
				</AppAlert>
			) : null}
			<Card className="workspace-panel">{content}</Card>
		</WorkspacePage>
	);
}

function ThreadRow({ thread }: { thread: Thread }) {
	const { organization } = useWorkspace();
	const queryClient = useQueryClient();
	const title = thread.title || "New thread";

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<MessageSquareText />
			</span>
			<div className="settings-list-copy">
				<Link
					className="settings-list-title-link"
					params={{ threadId: thread.id }}
					to="/app/threads/$threadId"
				>
					<strong>{title}</strong>
				</Link>
				<small>Updated {relativeTime(thread.updated_at)}</small>
			</div>
			<ConfirmDialog
				confirmLabel="Delete thread"
				description={`"${title}" is removed for everyone in ${organization.name}.`}
				onConfirm={async () => {
					await deleteThread(organization.id, thread.id);
					await queryClient.invalidateQueries({
						queryKey: threadsQueryKey(organization.id),
					});
				}}
				title="Delete this thread?"
				trigger={
					<Button
						aria-label={`Delete ${title}`}
						size="icon-sm"
						type="button"
						variant="ghost"
					>
						<Trash2 />
					</Button>
				}
			/>
		</div>
	);
}
