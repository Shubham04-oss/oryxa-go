const dateFormat = new Intl.DateTimeFormat(undefined, {
	day: "numeric",
	month: "short",
	year: "numeric",
});

export function formatDate(value: string): string {
	return dateFormat.format(new Date(value));
}

const minuteMilliseconds = 60 * 1000;
const hourMilliseconds = 60 * minuteMilliseconds;
const dayMilliseconds = 24 * hourMilliseconds;

/** Whole days from `now` until `value`, rounded up; negative once it passed. */
export function daysUntil(value: string, now: number = Date.now()): number {
	return Math.ceil((Date.parse(value) - now) / dayMilliseconds);
}

/** When something that lasts `days` more whole days runs out, in words. */
export function expiryText(days: number): string {
	if (days <= 0) {
		return "Expires today";
	}
	return days === 1 ? "Expires tomorrow" : `Expires in ${days} days`;
}

const weekdayFormat = new Intl.DateTimeFormat(undefined, { weekday: "long" });

/** How long ago `value` was, in a short, human phrase: "3 min", "Yesterday", "Monday", or a date once it is further back. */
export function relativeTime(value: string, now: number = Date.now()): string {
	const elapsed = now - Date.parse(value);
	if (elapsed < minuteMilliseconds) {
		return "Just now";
	}
	if (elapsed < hourMilliseconds) {
		return `${Math.floor(elapsed / minuteMilliseconds)} min`;
	}
	if (elapsed < dayMilliseconds) {
		return `${Math.floor(elapsed / hourMilliseconds)} hr`;
	}
	if (elapsed < 2 * dayMilliseconds) {
		return "Yesterday";
	}
	if (elapsed < 7 * dayMilliseconds) {
		return weekdayFormat.format(new Date(value));
	}
	return formatDate(value);
}
