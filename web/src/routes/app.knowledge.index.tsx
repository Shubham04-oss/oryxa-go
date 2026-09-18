import { useQuery } from "@tanstack/react-query";
import { createFileRoute, Link } from "@tanstack/react-router";
import { BookOpen, Plug } from "lucide-react";
import type { ReactNode } from "react";
import { QueryError } from "@/components/app/app-alert";
import { EmptyState } from "@/components/app/empty-state";
import { WorkspacePage } from "@/components/app/workspace-page";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useWorkspace } from "@/lib/auth/session";
import {
	type Corpus,
	chunkCountLabel,
	corporaQueryOptions,
	documentCountLabel,
	isFromConnectedApps,
	scopeLabel,
} from "@/lib/rag/client";

export const Route = createFileRoute("/app/knowledge/")({
	component: KnowledgePage,
	head: () => ({ meta: [{ title: "Knowledge · The Weirdly ODD" }] }),
});

function KnowledgePage() {
	const { organization } = useWorkspace();
	const corpora = useQuery(corporaQueryOptions(organization.id));

	let content: ReactNode;
	if (corpora.isPending) {
		content = <KnowledgeGridSkeleton />;
	} else if (corpora.isError) {
		content = (
			<QueryError
				error={corpora.error}
				fallback="Could not load your knowledge collections"
				onRetry={corpora.refetch}
			/>
		);
	} else if (corpora.data.length === 0) {
		content = (
			<EmptyState
				action={
					<Button asChild size="sm" type="button">
						<Link to="/app/connections">Go to Connections</Link>
					</Button>
				}
				description="Connect GitHub, Notion or Slack so a member can make it searchable, or add a collection of your own."
				icon={BookOpen}
				title="Nothing for Krude to search yet"
			/>
		);
	} else {
		content = (
			<div className="knowledge-grid">
				{corpora.data.map((item) => (
					<KnowledgeCard corpus={item} key={item.id} />
				))}
			</div>
		);
	}

	return (
		<WorkspacePage
			description="What Krude can search when it answers you or acts on your connected apps."
			title="Knowledge"
		>
			{content}
		</WorkspacePage>
	);
}

function KnowledgeCard({ corpus }: { corpus: Corpus }) {
	return (
		<Link
			className="knowledge-card"
			params={{ corpusId: corpus.id }}
			to="/app/knowledge/$corpusId"
		>
			<Card className="knowledge-card-inner">
				<header className="knowledge-card-header">
					<strong>{corpus.name}</strong>
					<span className="knowledge-card-scope">
						{scopeLabel(corpus.scope)}
					</span>
				</header>
				<p className="knowledge-card-counts">
					{documentCountLabel(corpus.document_count)} ·{" "}
					{chunkCountLabel(corpus.chunk_count)}
				</p>
				{isFromConnectedApps(corpus) ? (
					<span className="knowledge-card-hint">
						<Plug aria-hidden="true" />
						From connected apps
					</span>
				) : null}
			</Card>
		</Link>
	);
}

function KnowledgeGridSkeleton() {
	return (
		<div aria-busy="true" className="knowledge-grid">
			{["a", "b", "c"].map((key) => (
				<Card className="knowledge-card-inner" key={key}>
					<Skeleton className="h-4 w-32" />
					<Skeleton className="h-3 w-24" />
				</Card>
			))}
		</div>
	);
}
