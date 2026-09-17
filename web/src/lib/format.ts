const dateFormat = new Intl.DateTimeFormat(undefined, {
	day: "numeric",
	month: "short",
	year: "numeric",
});

export function formatDate(value: string): string {
	return dateFormat.format(new Date(value));
}

const dayMilliseconds = 24 * 60 * 60 * 1000;

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
