import { StatusOrb } from "@/components/ui/status-orb";
import {
	type ConnectionStatus,
	connectionStatusLabel,
	connectionStatusTone,
} from "@/lib/apps/client";

interface ConnectionStateProps {
	status: ConnectionStatus;
}

/** A connection's state: the orb carries the colour, the word carries the meaning. */
export function ConnectionState({ status }: ConnectionStateProps) {
	return (
		<span className="connection-state">
			<StatusOrb tone={connectionStatusTone(status)} />
			{connectionStatusLabel(status)}
		</span>
	);
}
