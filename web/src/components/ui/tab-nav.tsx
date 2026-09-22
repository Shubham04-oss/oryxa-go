import type { LucideIcon } from "lucide-react";
import { useRef } from "react";

/**
 * The one control for switching between sections of a page.
 *
 * It is an underline rather than a pill: a row of labels with a mark under
 * the live one reads as "where am I" at any width, and it keeps working past
 * the point where enclosed pills run out of room and start scrolling. The
 * compact size is the same control for a two- or three-way switch sitting
 * inside a page, so there is one pattern to learn rather than two.
 */

export interface TabNavItem<Value extends string> {
	icon?: LucideIcon;
	label: string;
	value: Value;
}

/**
 * Where an arrow key lands, wrapping at both ends. Returns `current` for a
 * key that is not navigation, so the caller knows to leave the event alone.
 */
export function nextTabIndex(
	current: number,
	key: string,
	count: number,
): number {
	if (count === 0) {
		return current;
	}
	switch (key) {
		case "ArrowRight":
		case "ArrowDown":
			return (current + 1) % count;
		case "ArrowLeft":
		case "ArrowUp":
			return (current - 1 + count) % count;
		case "Home":
			return 0;
		case "End":
			return count - 1;
		default:
			return current;
	}
}

interface TabNavProps<Value extends string> {
	/** The id of the region these tabs govern, for screen readers. */
	controls?: string;
	items: readonly TabNavItem<Value>[];
	/** What the row of tabs is, for screen readers. */
	label: string;
	onChange: (value: Value) => void;
	size?: "compact" | "default";
	value: Value;
}

export function TabNav<Value extends string>({
	controls,
	items,
	label,
	onChange,
	size = "default",
	value,
}: TabNavProps<Value>) {
	const row = useRef<HTMLDivElement>(null);
	const current = items.findIndex((item) => item.value === value);

	// Arrows move and select in one go, which is how a tab row is expected to
	// behave when every panel is already loaded.
	function navigate(event: React.KeyboardEvent<HTMLDivElement>) {
		const next = nextTabIndex(current, event.key, items.length);
		if (next === current) {
			return;
		}
		event.preventDefault();
		const target = items[next];
		if (!target) {
			return;
		}
		onChange(target.value);
		row.current
			?.querySelectorAll<HTMLButtonElement>("[role='tab']")
			[next]?.focus();
	}

	return (
		<div
			aria-label={label}
			className="tab-nav"
			data-size={size}
			onKeyDown={navigate}
			ref={row}
			role="tablist"
		>
			{items.map((item, index) => {
				const Icon = item.icon;
				const selected = item.value === value;
				return (
					<button
						aria-controls={controls}
						aria-selected={selected}
						className="tab-nav-item"
						key={item.value}
						onClick={() => onChange(item.value)}
						role="tab"
						// Only the live tab is in the tab order; arrows reach the rest.
						tabIndex={index === current ? 0 : -1}
						type="button"
					>
						{Icon ? <Icon aria-hidden="true" /> : null}
						<span>{item.label}</span>
					</button>
				);
			})}
		</div>
	);
}
