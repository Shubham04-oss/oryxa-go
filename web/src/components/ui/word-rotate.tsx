import { useEffect, useState } from "react";

import { cn } from "../../lib/utils";

interface WordRotateProps {
	words: string[];
	duration?: number;
	className?: string;
	getWordClassName?: (index: number) => string | undefined;
	startIndex?: number;
}

function normalizeIndex(index: number, length: number) {
	return length === 0 ? 0 : ((index % length) + length) % length;
}

function useReducedMotion() {
	const [shouldReduceMotion, setShouldReduceMotion] = useState(false);
	useEffect(() => {
		const query = window.matchMedia("(prefers-reduced-motion: reduce)");
		const updatePreference = () => setShouldReduceMotion(query.matches);
		updatePreference();
		query.addEventListener("change", updatePreference);
		return () => query.removeEventListener("change", updatePreference);
	}, []);
	return shouldReduceMotion;
}

export function WordRotate({
	words,
	duration = 1800,
	className,
	getWordClassName,
	startIndex = 0,
}: WordRotateProps) {
	const [index, setIndex] = useState(() =>
		normalizeIndex(startIndex, words.length),
	);
	const shouldReduceMotion = useReducedMotion();
	useEffect(() => {
		setIndex(normalizeIndex(startIndex, words.length));
	}, [startIndex, words.length]);

	useEffect(() => {
		if (words.length <= 1 || shouldReduceMotion) {
			return;
		}
		const interval = window.setInterval(() => {
			setIndex((previousIndex) => (previousIndex + 1) % words.length);
		}, duration);

		return () => window.clearInterval(interval);
	}, [duration, shouldReduceMotion, words.length]);

	if (words.length === 0) {
		return null;
	}
	const visibleIndex = normalizeIndex(
		shouldReduceMotion ? startIndex : index,
		words.length,
	);
	const visibleWord = words[visibleIndex];

	return (
		<span
			aria-hidden="true"
			className="relative inline-grid max-w-full overflow-hidden align-middle"
			data-slot="word-rotate"
		>
			{words.map((word, wordIndex) => (
				<span
					className={cn(
						"invisible col-start-1 row-start-1 overflow-hidden text-ellipsis whitespace-nowrap",
						className,
					)}
					// Sizing clones have no state and tool names may repeat in one run.
					// biome-ignore lint/suspicious/noArrayIndexKey: order is the identity here
					key={`${word}-${wordIndex}`}
				>
					{word}
				</span>
			))}
			<span
				className={cn(
					"word-rotate-tumble absolute inset-0 overflow-hidden text-ellipsis whitespace-nowrap",
					className,
					getWordClassName?.(visibleIndex),
				)}
				data-slot="word-rotate-active"
				key={`${visibleWord}-${visibleIndex}`}
			>
				{visibleWord}
			</span>
		</span>
	);
}
