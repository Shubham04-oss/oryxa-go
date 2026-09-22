import { useMutation, useQueryClient } from "@tanstack/react-query";
import {
	ArrowLeft,
	ArrowRight,
	BookOpen,
	Check,
	CircleDashed,
	Sparkles,
} from "lucide-react";
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
import { Textarea } from "@/components/ui/textarea";
import {
	agentsQueryKey,
	createAgent,
	draftSpecialist,
	listAgents,
	type SpecialistDraft,
	type SpecialistDraftContext,
} from "@/lib/agents/client";
import { apiErrorMessage } from "@/lib/api/client";
import { listConnections } from "@/lib/apps/client";
import { useWorkspace } from "@/lib/auth/session";
import { listSources } from "@/lib/knowledge/client";
import { getOrganizationContext } from "@/lib/teamspaces/client";
import {
	SpecialistAvatar,
	type SpecialistFocus,
	specialistFocuses,
} from "./specialist-identity";
import {
	compileSpecialistInstructions,
	inferSpecialistFocus,
	starterSpecialistDraft,
} from "./specialist-onboarding";

async function gatherContext(
	organizationId: string,
	memberName: string,
	organizationName: string,
): Promise<{ context: SpecialistDraftContext; unavailable: string[] }> {
	const [agents, sources, connections, synthesis] = await Promise.allSettled([
		listAgents(organizationId),
		listSources(organizationId),
		listConnections(organizationId),
		getOrganizationContext(organizationId),
	]);
	const unavailable: string[] = [];
	if (agents.status === "rejected") unavailable.push("primary agent");
	if (sources.status === "rejected") unavailable.push("knowledge index");
	if (connections.status === "rejected") unavailable.push("connected apps");
	if (synthesis.status === "rejected") unavailable.push("organization brief");
	return {
		context: {
			member_name: memberName,
			organization_name: organizationName,
			organization_brief:
				synthesis.status === "fulfilled"
					? synthesis.value.summary.slice(0, 4000)
					: undefined,
			primary_agent_name:
				agents.status === "fulfilled"
					? agents.value.find((agent) => agent.kind === "primary")?.name
					: undefined,
			knowledge_sources:
				sources.status === "fulfilled"
					? sources.value
							.filter((source) => source.status === "active")
							.slice(0, 20)
							.map((source) => {
								const target =
									source.target.name ||
									source.target.repository ||
									source.target.channel;
								return `${source.provider}${target ? ` · ${target.slice(0, 80)}` : ""}`;
							})
					: [],
			connected_apps:
				connections.status === "fulfilled"
					? connections.value
							.filter((connection) => connection.status === "active")
							.slice(0, 20)
							.map((connection) =>
								(connection.label || connection.toolkit).slice(0, 80),
							)
					: [],
		},
		unavailable,
	};
}

export function SpecialistOnboardingDialog({
	organizationId,
	trigger,
}: {
	organizationId: string;
	trigger: ReactElement;
}) {
	const { organization, user } = useWorkspace();
	const queryClient = useQueryClient();
	const nameId = useId();
	const purposeId = useId();
	const instructionsId = useId();
	const [open, setOpen] = useState(false);
	const [stage, setStage] = useState<"identity" | "review">("identity");
	const [name, setName] = useState("");
	const [purpose, setPurpose] = useState("");
	const [focus, setFocus] = useState<SpecialistFocus | null>(null);
	const [plan, setPlan] = useState<SpecialistDraft | null>(null);
	const [context, setContext] = useState<SpecialistDraftContext | null>(null);
	const [notice, setNotice] = useState("");
	const [error, setError] = useState("");

	const prepare = useMutation({
		mutationFn: async () => {
			const gathered = await gatherContext(
				organizationId,
				user.display_name?.trim() || "the member",
				organization.name,
			);
			const input = {
				name: name.trim(),
				purpose: purpose.trim(),
				focus: focus || inferSpecialistFocus(name),
				context: gathered.context,
			};
			try {
				return {
					context: gathered.context,
					plan: await draftSpecialist(organizationId, input),
					unavailable: gathered.unavailable,
					usedStarter: false,
				};
			} catch {
				return {
					context: gathered.context,
					plan: starterSpecialistDraft(input),
					unavailable: gathered.unavailable,
					usedStarter: true,
				};
			}
		},
		onSuccess: ({
			context: gathered,
			plan: drafted,
			unavailable,
			usedStarter,
		}) => {
			setContext(gathered);
			setPlan(drafted);
			setPurpose(drafted.purpose);
			setNotice(
				[
					usedStarter
						? "The live draft was unavailable, so this is a basic starter plan you can edit."
						: "",
					unavailable.length
						? `Some context could not be loaded: ${unavailable.join(", ")}.`
						: "",
				]
					.filter(Boolean)
					.join(" "),
			);
			setStage("review");
		},
	});
	const create = useMutation({
		mutationFn: () => {
			if (!plan) throw new Error("Prepare a plan first.");
			return createAgent(organizationId, {
				name: name.trim(),
				description: purpose.trim(),
				instructions: compileSpecialistInstructions(plan),
			});
		},
		onSuccess: async () => {
			await queryClient.invalidateQueries({
				queryKey: agentsQueryKey(organizationId),
			});
			setOpen(false);
		},
	});

	function changeOpen(next: boolean) {
		setOpen(next);
		if (next) {
			setStage("identity");
			setName("");
			setPurpose("");
			setFocus(null);
			setPlan(null);
			setContext(null);
			setNotice("");
			setError("");
			prepare.reset();
			create.reset();
		}
	}

	function startPreparing() {
		if (!name.trim()) {
			setError("Name the specialist to prepare its onboarding plan.");
			return;
		}
		setError("");
		prepare.mutate();
	}

	return (
		<Dialog onOpenChange={changeOpen} open={open}>
			<DialogTrigger asChild>{trigger}</DialogTrigger>
			<DialogContent className="specialist-onboarding-dialog">
				<DialogHeader>
					<DialogTitle>
						{stage === "identity"
							? "Meet your next specialist"
							: `Onboard ${name}`}
					</DialogTitle>
					<DialogDescription>
						{stage === "identity"
							? "Give them a name. We’ll draft their role, working cycle, and instructions from your workspace context."
							: "Review the proposal before it becomes this specialist’s operating guidance. Nothing is connected automatically."}
					</DialogDescription>
				</DialogHeader>
				{error ? <AppAlert>{error}</AppAlert> : null}
				{create.isError ? (
					<AppAlert>
						{apiErrorMessage(create.error, "Could not create specialist")}
					</AppAlert>
				) : null}
				{stage === "identity" ? (
					<div className="specialist-onboarding-layout">
						<div className="specialist-onboarding-main">
							<div className="specialist-onboarding-field">
								<Label htmlFor={nameId}>What will you call them?</Label>
								<Input
									autoComplete="off"
									id={nameId}
									maxLength={60}
									onChange={(event) => setName(event.target.value)}
									onKeyDown={(event) => {
										if (event.key === "Enter") startPreparing();
									}}
									placeholder="e.g. Market Researcher"
									value={name}
								/>
							</div>
							<div className="specialist-onboarding-field">
								<Label htmlFor={purposeId}>
									Anything they should own? <span>Optional</span>
								</Label>
								<Input
									id={purposeId}
									maxLength={500}
									onChange={(event) => setPurpose(event.target.value)}
									placeholder="A specific responsibility helps shape the proposal"
									value={purpose}
								/>
							</div>
							<fieldset className="specialist-onboarding-focus">
								<legend>
									Refine the focus <span>Optional</span>
								</legend>
								<div>
									{specialistFocuses.map((option) => (
										<button
											aria-pressed={focus === option.key}
											key={option.key}
											onClick={() => setFocus(option.key)}
											type="button"
										>
											{option.label}
										</button>
									))}
								</div>
							</fieldset>
						</div>
						<aside className="specialist-onboarding-aside">
							<SpecialistAvatar
								name={name || "New specialist"}
								seed={name || "new-specialist"}
								size="large"
							/>
							<strong>{name.trim() || "A role with its own identity"}</strong>
							<p>
								We’ll look at your primary agent, organization brief, indexed
								sources, and connected-app names. You decide what guidance to
								keep.
							</p>
							<div>
								<CircleDashed aria-hidden /> Draft only · no new access
							</div>
						</aside>
					</div>
				) : plan && context ? (
					<div className="specialist-onboarding-review">
						{notice ? <AppAlert>{notice}</AppAlert> : null}
						<div className="specialist-onboarding-review-grid">
							<div className="specialist-onboarding-main">
								<div className="specialist-onboarding-field">
									<Label htmlFor={purposeId}>Purpose</Label>
									<Input
										id={purposeId}
										maxLength={500}
										onChange={(event) => setPurpose(event.target.value)}
										value={purpose}
									/>
								</div>
								<section
									className="specialist-onboarding-cycle"
									aria-label="Proposed working cycle"
								>
									<header>
										<Sparkles aria-hidden />
										<h3>How they’ll work</h3>
									</header>
									<ol>
										{plan.cycle.map((step, index) => (
											<li key={`${step.title}-${step.detail}`}>
												<span>{index + 1}</span>
												<div>
													<strong>{step.title}</strong>
													<p>{step.detail}</p>
												</div>
											</li>
										))}
									</ol>
								</section>
								<div className="specialist-onboarding-field">
									<Label htmlFor={instructionsId}>Operating instructions</Label>
									<Textarea
										id={instructionsId}
										onChange={(event) =>
											setPlan({ ...plan, instructions: event.target.value })
										}
										rows={8}
										value={plan.instructions}
									/>
									<small>
										The working cycle and opening question are included when
										saved. Runtime permission checks still apply.
									</small>
								</div>
							</div>
							<aside className="specialist-onboarding-context">
								<div className="specialist-onboarding-identity">
									<SpecialistAvatar name={name} seed={name} />
									<div>
										<strong>{name}</strong>
										<span>Proposal for {context.organization_name}</span>
									</div>
								</div>
								<h3>Context considered</h3>
								<p>
									{context.member_name} · {context.organization_name}
									{context.primary_agent_name
										? ` · ${context.primary_agent_name}`
										: ""}
								</p>
								{context.organization_brief ? (
									<p className="specialist-onboarding-brief">
										{context.organization_brief}
									</p>
								) : null}
								<div className="specialist-onboarding-context-group">
									<BookOpen aria-hidden />
									<div>
										<strong>Knowledge</strong>
										<span>
											{context.knowledge_sources.length
												? context.knowledge_sources.join(", ")
												: "No active indexed source found"}
										</span>
									</div>
								</div>
								<div className="specialist-onboarding-context-group">
									<Check aria-hidden />
									<div>
										<strong>Apps</strong>
										<span>
											{context.connected_apps.length
												? context.connected_apps.join(", ")
												: "No active connection found"}
										</span>
									</div>
								</div>
								<h3>Suggested next integrations</h3>
								<ul>
									{plan.integrations.map((item) => (
										<li key={item}>{item}</li>
									))}
								</ul>
								<h3>First conversation</h3>
								<p>{plan.first_message}</p>
							</aside>
						</div>
					</div>
				) : null}
				<DialogFooter>
					{stage === "review" ? (
						<Button
							onClick={() => setStage("identity")}
							type="button"
							variant="ghost"
						>
							<ArrowLeft data-icon="inline-start" /> Back
						</Button>
					) : (
						<DialogClose asChild>
							<Button type="button" variant="ghost">
								Cancel
							</Button>
						</DialogClose>
					)}
					<Button
						disabled={
							prepare.isPending ||
							create.isPending ||
							(stage === "review" &&
								(!purpose.trim() || !plan?.instructions.trim()))
						}
						onClick={() =>
							stage === "identity" ? startPreparing() : create.mutate()
						}
						type="button"
					>
						{stage === "identity" ? (
							prepare.isPending ? (
								"Gathering context…"
							) : (
								<>
									Prepare onboarding <ArrowRight data-icon="inline-end" />
								</>
							)
						) : create.isPending ? (
							"Creating…"
						) : (
							"Create with this plan"
						)}
					</Button>
				</DialogFooter>
			</DialogContent>
		</Dialog>
	);
}
