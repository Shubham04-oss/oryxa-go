import type { ReactNode } from "react";
import { Card } from "@/components/ui/card";

interface AuthShellProps {
	children: ReactNode;
}

export function AuthShell({ children }: AuthShellProps) {
	return (
		<main className="auth-canvas">
			<div className="auth-layout">
				<div className="auth-quiet-space" aria-hidden="true" />
				<section className="auth-rail" aria-label="Account access">
					<Card className="auth-card">
						<div className="wordmark">
							<span className="wordmark-mark" aria-hidden="true">
								O
							</span>
							<span>
								The Weirdly <strong className="wordmark-odd">ODD</strong>
							</span>
						</div>
						{children}
					</Card>
				</section>
			</div>
		</main>
	);
}
