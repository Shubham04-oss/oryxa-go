import { useQuery } from "@tanstack/react-query";
import { Blocks } from "lucide-react";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { Checkbox } from "@/components/ui/checkbox";
import { providersQueryOptions } from "@/lib/connections/client";
import { describeTools, ToolGlyph } from "@/lib/tools/catalog";

/** A provider key, as connections names it. */
export type ToolProvider = string;

interface ToolEnablementProps {
	error: string;
	onFinish: () => void;
	onToggle: (provider: ToolProvider) => void;
	selected: ReadonlySet<ToolProvider>;
	submitting: boolean;
}

export function ToolEnablement({
	error,
	onFinish,
	onToggle,
	selected,
	submitting,
}: ToolEnablementProps) {
	const providers = useQuery(providersQueryOptions());
	const tools = providers.data ? describeTools(providers.data) : [];

	return (
		<section className="onboarding-organization" aria-labelledby="tools-title">
			<Card className="onboarding-form-card onboarding-tools-card">
				<header className="onboarding-tools-heading">
					<h2 className="auth-heading" id="tools-title">
						Choose your tools
					</h2>
					<p>Turn on what your team already works in.</p>
				</header>

				{error ? (
					<Alert
						className="auth-alert onboarding-tools-error"
						variant="destructive"
					>
						<AlertDescription>{error}</AlertDescription>
					</Alert>
				) : null}

				{providers.isPending ? (
					<p className="onboarding-tools-note">Loading the tools…</p>
				) : null}
				{providers.isError ? (
					<p className="onboarding-tools-note">
						The tool list did not load. You can choose tools in settings after
						setup.
					</p>
				) : null}
				<div className="onboarding-tool-grid">
					{tools.map((tool) => {
						const checked = selected.has(tool.provider);
						const checkboxId = `tool-${tool.provider}`;

						return (
							<label
								className={`onboarding-tool${checked ? " onboarding-tool-selected" : ""}`}
								htmlFor={checkboxId}
								key={tool.provider}
							>
								<span className="onboarding-tool-glyph" aria-hidden="true">
									<ToolGlyph provider={tool.provider} />
								</span>
								<span className="onboarding-tool-copy">
									<strong>{tool.name}</strong>
									<small>{tool.description}</small>
								</span>
								<Checkbox
									checked={checked}
									id={checkboxId}
									onCheckedChange={() => onToggle(tool.provider)}
								/>
							</label>
						);
					})}
				</div>

				<div className="onboarding-tools-guidance">
					<Blocks aria-hidden="true" />
					<span>Connect accounts after setup.</span>
					<small>{selected.size} selected</small>
				</div>

				<div className="onboarding-actions">
					<Button
						className="auth-button auth-button-primary"
						disabled={submitting}
						onClick={onFinish}
						size="lg"
						type="button"
					>
						{submitting ? "Finishing…" : "Finish setup"}
					</Button>
				</div>
			</Card>
		</section>
	);
}
