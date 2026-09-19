import {
	Ban,
	CircleAlert,
	CircleCheck,
	Loader2,
	ShieldQuestion,
} from "lucide-react";
import {
	humanizeToolName,
	type TimelineEntry,
	type ToolEntry,
} from "../../lib/threads/timeline";
import { WordRotate } from "../ui/word-rotate";

export interface ToolActivityEntry {
	id: string;
	kind: "tool_activity";
	tools: ToolEntry[];
}

export type DisplayTimelineEntry =
	| Exclude<TimelineEntry, ToolEntry>
	| ToolActivityEntry;

/**
 * Consecutive calls are one quiet activity row in the conversation. Messages,
 * artifacts, and confirmations remain hard visual boundaries between groups.
 */
export function groupToolActivity(
	entries: TimelineEntry[],
): DisplayTimelineEntry[] {
	const grouped: DisplayTimelineEntry[] = [];
	let current: ToolActivityEntry | undefined;
	for (const entry of entries) {
		if (entry.kind === "tool") {
			if (current) {
				current.tools.push(entry);
			} else {
				current = {
					id: `tool-activity-${entry.callId}`,
					kind: "tool_activity",
					tools: [entry],
				};
				grouped.push(current);
			}
			continue;
		}
		current = undefined;
		grouped.push(entry);
	}
	return grouped;
}

function toolStateLabel(state: ToolEntry["state"]): string {
	switch (state) {
		case "running":
			return "Running…";
		case "waiting_approval":
			return "Waiting for approval";
		case "declined":
			return "Declined";
		case "failed":
			return "Failed";
		default:
			return "Done";
	}
}

function activityState(tools: ToolEntry[]): ToolEntry["state"] {
	for (const state of ["waiting_approval", "running", "failed"] as const) {
		if (tools.some((tool) => tool.state === state)) {
			return state;
		}
	}
	return tools.every((tool) => tool.state === "declined") ? "declined" : "done";
}

function ToolStateIcon({ state }: { state: ToolEntry["state"] }) {
	switch (state) {
		case "running":
			return (
				<Loader2 aria-hidden="true" className="thread-tool-icon animate-spin" />
			);
		case "waiting_approval":
			return <ShieldQuestion aria-hidden="true" className="thread-tool-icon" />;
		case "failed":
			return <CircleAlert aria-hidden="true" className="thread-tool-icon" />;
		case "declined":
			return <Ban aria-hidden="true" className="thread-tool-icon" />;
		default:
			return <CircleCheck aria-hidden="true" className="thread-tool-icon" />;
	}
}

export function ToolActivity({ tools }: { tools: ToolEntry[] }) {
	const state = activityState(tools);
	const labels = tools.map((tool) => humanizeToolName(tool.tool));
	const status = toolStateLabel(state);
	const focusIndex = Math.max(
		0,
		tools.findIndex((tool) => tool.state === state),
	);
	return (
		<div
			aria-live={state === "running" ? "polite" : undefined}
			className="thread-tool-activity"
			data-state={state}
		>
			<span className="sr-only">
				Tool activity: {labels.join(", then ")}. {status}
			</span>
			<ToolStateIcon state={state} />
			<span className="thread-tool-reel">
				<WordRotate
					className="thread-tool-step"
					getWordClassName={(index) =>
						tools[index]?.state === "running" ? "shimmer" : undefined
					}
					startIndex={focusIndex}
					words={labels}
				/>
			</span>
			<small className="thread-tool-status">
				{tools.length > 1 ? `${tools.length} calls · ${status}` : status}
			</small>
		</div>
	);
}
