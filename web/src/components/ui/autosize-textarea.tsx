import { cn } from "cn";
import type * as React from "react";
import TextareaAutosize from "react-textarea-autosize";

function AutosizeTextarea({
	className,
	...props
}: React.ComponentProps<typeof TextareaAutosize>) {
	return (
		<TextareaAutosize
			className={cn(
				"w-full resize-none bg-transparent text-sm leading-6 outline-none placeholder:text-muted-foreground disabled:cursor-not-allowed disabled:opacity-50",
				className,
			)}
			data-slot="autosize-textarea"
			{...props}
		/>
	);
}

export { AutosizeTextarea };
