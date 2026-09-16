import type { ReactNode } from "react";
import { Alert, AlertAction, AlertDescription } from "@/components/ui/alert";
import { Button } from "@/components/ui/button";
import { apiErrorMessage } from "@/lib/api/client";

interface AppAlertProps {
	action?: ReactNode;
	children: ReactNode;
	tone?: "danger" | "info" | "success";
}

export function AppAlert({ action, children, tone = "danger" }: AppAlertProps) {
	return (
		<Alert
			className="app-alert"
			data-tone={tone}
			variant={tone === "danger" ? "destructive" : "default"}
		>
			<AlertDescription>{children}</AlertDescription>
			{action ? <AlertAction>{action}</AlertAction> : null}
		</Alert>
	);
}

interface QueryErrorProps {
	error: unknown;
	fallback: string;
	onRetry: () => unknown;
}

/** A load that failed, with a way to try it again. */
export function QueryError({ error, fallback, onRetry }: QueryErrorProps) {
	return (
		<AppAlert
			action={
				<Button
					onClick={() => onRetry()}
					size="sm"
					type="button"
					variant="ghost"
				>
					Try again
				</Button>
			}
		>
			{apiErrorMessage(error, fallback)}
		</AppAlert>
	);
}
