import { ArrowUp, Paperclip, SlidersHorizontal } from "lucide-react";
import type { FormEvent, RefObject } from "react";
import { Button } from "@/components/ui/button";
import { Textarea } from "@/components/ui/textarea";

interface ThreadComposerProps {
	inputRef?: RefObject<HTMLTextAreaElement | null>;
	onChange: (value: string) => void;
	onSubmit?: (value: string) => void;
	value: string;
}

export function ThreadComposer({
	inputRef,
	onChange,
	onSubmit,
	value,
}: ThreadComposerProps) {
	const hasInput = value.trim().length > 0;

	function submit(event: FormEvent<HTMLFormElement>) {
		event.preventDefault();
		if (!hasInput) return;
		onSubmit?.(value.trim());
	}

	return (
		<form
			className="workspace-pill-composer"
			data-has-input={hasInput}
			onSubmit={submit}
		>
			<Button
				aria-label="Add files"
				className="composer-file-button"
				size="icon"
				title="Add files"
				type="button"
				variant="ghost"
			>
				<Paperclip />
			</Button>

			<Textarea
				aria-label="Message The Weirdly ODD"
				onChange={(event) => onChange(event.target.value)}
				placeholder="Ask, plan, research, or build…"
				ref={inputRef}
				rows={1}
				value={value}
			/>

			<div className="composer-pill-actions">
				<Button
					aria-label="Choose tools"
					size="icon"
					title="Choose tools"
					type="button"
					variant="ghost"
				>
					<SlidersHorizontal />
				</Button>
				<Button
					aria-label="Send message"
					className="composer-send-button"
					disabled={!hasInput}
					size="icon"
					type="submit"
				>
					<ArrowUp />
				</Button>
			</div>
		</form>
	);
}
