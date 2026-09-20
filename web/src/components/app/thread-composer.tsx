import { ArrowUp, Square } from "lucide-react";
import type { FormEvent, KeyboardEvent, ReactNode, RefObject } from "react";
import { AutosizeTextarea } from "@/components/ui/autosize-textarea";
import { Button } from "@/components/ui/button";

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

	function submitFromKeyboard(event: KeyboardEvent<HTMLTextAreaElement>) {
		if (
			event.key !== "Enter" ||
			event.shiftKey ||
			event.nativeEvent.isComposing ||
			streaming
		) {
			return;
		}
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
				<AutosizeTextarea
					aria-label="Message The Weirdly ODD"
					disabled={disabled}
					maxRows={7}
					minRows={1}
					onChange={(event) => onChange(event.target.value)}
					onKeyDown={submitFromKeyboard}
					placeholder={placeholder}
					ref={inputRef}
					value={value}
				/>

				<div className="composer-pill-actions">
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
