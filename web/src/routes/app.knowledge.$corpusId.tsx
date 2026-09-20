import { useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { ArrowLeft, FileText } from "lucide-react";
import type { ReactNode } from "react";
import { QueryError } from "@/components/app/app-alert";
import { EmptyState } from "@/components/app/empty-state";
import {
	SettingsListSkeleton,
	StatusBadge,
} from "@/components/app/organization-settings";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { useWorkspace } from "@/lib/auth/session";
import { formatDate } from "@/lib/format";
import {
	type CorpusDocument,
	chunkCountLabel,
	corpusQueryOptions,
	type DocumentStatus,
	documentCountLabel,
	documentsInfiniteQueryOptions,
	externalSourceLink,
	scopeLabel,
} from "@/lib/rag/client";

export const Route = createFileRoute("/app/knowledge/$corpusId")({
	component: KnowledgeCorpusPage,
	head: () => ({ meta: [{ title: "Knowledge · The Weirdly ODD" }] }),
});

function formatBytes(bytes: number): string {
	if (bytes < 1024) {
		return `${bytes} B`;
	}
	const units = ["KB", "MB", "GB"];
	let value = bytes / 1024;
	let unit = 0;
	while (value >= 1024 && unit < units.length - 1) {
		value /= 1024;
		unit += 1;
	}
	const rounded = value >= 10 ? Math.round(value) : Math.round(value * 10) / 10;
	return `${rounded} ${units[unit]}`;
}

function BackToKnowledge() {
	return (
		<Button asChild size="sm" type="button" variant="ghost">
			<Link to="/app/knowledge">
				<ArrowLeft data-icon="inline-start" />
				All collections
			</Link>
		</Button>
	);
}

function KnowledgeCorpusPage() {
	const { corpusId } = Route.useParams();
	const { organization } = useWorkspace();
	const corpus = useQuery(corpusQueryOptions(organization.id, corpusId));
	const documents = useInfiniteQuery(
		documentsInfiniteQueryOptions(organization.id, corpusId),
	);

	if (corpus.isError) {
		return (
			<WorkspacePage actions={<BackToKnowledge />} title="Knowledge collection">
				<QueryError
					error={corpus.error}
					fallback="Could not load this collection"
					onRetry={corpus.refetch}
				/>
			</WorkspacePage>
		);
	}

	const allDocuments =
		documents.data?.pages.flatMap((page) => page.documents) ?? [];

	let content: ReactNode;
	if (documents.isPending) {
		content = <SettingsListSkeleton />;
	} else if (documents.isError) {
		content = (
			<div className="settings-section-alert">
				<QueryError
					error={documents.error}
					fallback="Could not load this collection's documents"
					onRetry={documents.refetch}
				/>
			</div>
		);
	} else if (allDocuments.length === 0) {
		content = (
			<EmptyState
				description="Connect an app such as GitHub, Notion or Slack in Apps & tools, or add a document of your own, so your agents can search it."
				icon={FileText}
				title="No documents yet"
			/>
		);
	} else {
		content = (
			<div className="settings-list">
				{allDocuments.map((document) => (
					<DocumentRow document={document} key={document.id} />
				))}
			</div>
		);
	}

	return (
		<WorkspacePage
			actions={<BackToKnowledge />}
			description={
				corpus.data
					? `${scopeLabel(corpus.data.scope)} · ${documentCountLabel(corpus.data.document_count)}`
					: undefined
			}
			title={corpus.data?.name ?? "Knowledge collection"}
		>
			<Card className="workspace-panel">
				{content}
				{documents.hasNextPage ? (
					<div className="knowledge-load-more">
						<Button
							disabled={documents.isFetchingNextPage}
							onClick={() => documents.fetchNextPage()}
							size="sm"
							type="button"
							variant="ghost"
						>
							{documents.isFetchingNextPage ? "Loading…" : "Load more"}
						</Button>
					</div>
				) : null}
			</Card>
		</WorkspacePage>
	);
}

function DocumentRow({ document }: { document: CorpusDocument }) {
	const title = document.title?.trim() || "Untitled document";
	const link = externalSourceLink(document.source);
	const summary = [
		formatBytes(document.byte_size),
		chunkCountLabel(document.chunk_count),
		`added ${formatDate(document.created_at)}`,
	].join(" · ");

	return (
		<div className="settings-list-row">
			<span aria-hidden="true" className="settings-list-icon">
				<FileText />
			</span>
			<div className="settings-list-copy">
				{link ? (
					<a
						className="settings-list-title-link"
						href={link}
						rel="noreferrer"
						target="_blank"
					>
						<strong>{title}</strong>
					</a>
				) : (
					<strong>{title}</strong>
				)}
				<small>{summary}</small>
				{document.status === "failed" && document.failure_reason ? (
					<small className="settings-list-error">
						{document.failure_reason}
					</small>
				) : null}
			</div>
			<DocumentStatusBadge status={document.status} />
		</div>
	);
}

function DocumentStatusBadge({ status }: { status: DocumentStatus }) {
	switch (status) {
		case "ready":
			return <StatusBadge tone="active">Ready</StatusBadge>;
		case "failed":
			return <StatusBadge tone="danger">Failed</StatusBadge>;
		case "ingesting":
			return <StatusBadge tone="neutral">Ingesting…</StatusBadge>;
		default:
			return <StatusBadge tone="neutral">Pending</StatusBadge>;
	}
}
