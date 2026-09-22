/**
 * A state, drawn as a small orb of dots.
 *
 * The product already says "an agent" in dots — see `agent-mark` — so a
 * connection's state is said the same way, one size down. Colour lives here
 * and nowhere else: the word beside an orb stays in plain ink, which is what
 * keeps a page of connections calm while still reading green at a glance.
 *
 * Nothing about an orb moves. A connection that is still settling is a state,
 * not an activity, and a page of pulsing dots is harder to read than a page
 * of still ones.
 */

/** How a state reads. `active` is the only green in the product. */
export type OrbTone = "active" | "danger" | "muted" | "neutral" | "warning";

/** One dot in the field an orb is drawn in. */
interface OrbDot {
	/** Fixed for the life of the field, so React never re-keys it. */
	id: string;
	o: number;
	r: number;
	x: number;
	y: number;
}

const FIELD = 24;
const CENTRE = FIELD / 2;
const RADIUS = 10.4;
/** Centre-to-centre spacing of the dot grid, and each dot's share of it. */
const STEP = 4.2;
const DOT = STEP * 0.4;
/** Rows of a hex grid sit √3/2 of a step apart. */
const ROW = 0.866;

/**
 * A halftone disc: dots on a hex grid, clipped to a circle, lit from the
 * upper left so the ring of them reads as a ball rather than a badge. Small
 * enough to stay one shape at 13px, open enough that the dots are still dots.
 */
export function orbDots(): OrbDot[] {
	const dots: OrbDot[] = [];
	const reach = Math.ceil((RADIUS * 2) / (STEP * ROW)) + 2;
	for (let row = -reach; row <= reach; row++) {
		const y = row * STEP * ROW;
		for (let column = -reach; column <= reach; column++) {
			// Every other row is offset half a step — that is what makes it hex.
			const x = column * STEP + (row % 2 ? STEP / 2 : 0);
			if (Math.hypot(x, y) > RADIUS - DOT * 0.35) {
				continue;
			}
			const lit = Math.max(
				0,
				1 - Math.hypot(x + RADIUS * 0.3, y + RADIUS * 0.32) / (RADIUS * 1.75),
			);
			dots.push({
				id: `${x.toFixed(2)}:${y.toFixed(2)}`,
				o: 0.48 + lit * 0.52,
				r: DOT * (0.74 + lit * 0.46),
				x: CENTRE + x,
				y: CENTRE + y,
			});
		}
	}
	return dots;
}

// The geometry never changes and the viewBox makes it size-independent, so
// the field is drawn once for the life of the tab.
const field = orbDots();

interface StatusOrbProps {
	/**
	 * What a screen reader is told. Leave it off — the default — whenever the
	 * state is already spelled out beside the orb, which is the usual case.
	 */
	label?: string;
	size?: number;
	tone: OrbTone;
}

export function StatusOrb({ label, size = 14, tone }: StatusOrbProps) {
	const dots = field.map((point) => (
		<circle
			cx={point.x}
			cy={point.y}
			fill="currentColor"
			key={point.id}
			opacity={point.o}
			r={point.r}
		/>
	));
	const shared = {
		className: "status-orb",
		"data-tone": tone,
		height: size,
		viewBox: `0 0 ${FIELD} ${FIELD}`,
		width: size,
	};

	if (label === undefined) {
		return (
			<svg aria-hidden="true" {...shared}>
				{dots}
			</svg>
		);
	}
	return (
		<svg role="img" {...shared}>
			<title>{label}</title>
			{dots}
		</svg>
	);
}
