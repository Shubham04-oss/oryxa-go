import { ArrowUp, Paperclip, SlidersHorizontal, Square } from "lucide-react";
import type { FormEvent, ReactNode, RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

interface ThreadComposerProps {
	/** Disables input while a run streams or an answer is required first. */
	disabled?: boolean;
	/** Why the composer is disabled right now, shown under it. */
	hint?: ReactNode;
	inputRef?: RefObject<HTMLTextAreaElement | null>;
	onChange: (value: string) => void;
	onSubmit?: (value: string) => void;
	/** Called when the stop button is pressed while `streaming`. */
	onStop?: () => void;
	placeholder?: string;
	/** Swaps the send button for a stop button that is never disabled. */
	streaming?: boolean;
	value: string;
}

export function ThreadComposer({
	disabled = false,
	hint,
	inputRef,
	onChange,
	onStop,
	onSubmit,
	placeholder = "Ask, plan, research, or build…",
	streaming = false,
	value,
}: ThreadComposerProps) {
	const hasInput = value.trim().length > 0;

	function submit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!hasInput || disabled) return;
		onSubmit?.(value.trim());
	}

	return (
		<>
			<form
				className="workspace-pill-composer"
				data-has-input={hasInput}
				onSubmit={submit}
			>
				<Button
					aria-label="Add files"
					className="composer-file-button"
					disabled={disabled}
					size="icon"
					title="Add files"
					type="button"
					variant="ghost"
				>
					<Paperclip />
				</Button>

				<Textarea
					aria-label="Message The Weirdly ODD"
					disabled={disabled}
					onChange={(event) => onChange(event.target.value)}
					placeholder={placeholder}
					ref={inputRef}
					rows={1}
					value={value}
				/>

				<div className="composer-pill-actions">
					<Button
						aria-label="Choose tools"
						disabled={disabled}
						size="icon"
						title="Choose tools"
						type="button"
						variant="ghost"
					>
						<SlidersHorizontal />
					</Button>
					{streaming ? (
						<Button
							aria-label="Stop"
							className="composer-send-button composer-stop-button"
							onClick={onStop}
							size="icon"
							title="Stop"
							type="button"
						>
							<Square />
						</Button>
					) : (
						<Button
							aria-label="Send message"
							className="composer-send-button"
							disabled={!hasInput || disabled}
							size="icon"
							type="submit"
						>
							<ArrowUp />
						</Button>
					)}
				</div>
			</form>
			{hint ? <p className="thread-composer-hint">{hint}</p> : null}
		</>
	);
}
