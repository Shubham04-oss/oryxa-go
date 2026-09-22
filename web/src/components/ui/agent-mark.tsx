/**
 * An agent's mark: a form made of dots, its own for as long as the agent
 * exists.
 *
 * The shape is identity and the motion is state, which is why there is no
 * separate spinner anywhere in the product — an agent that is working is its
 * own avatar, turning. People never get one of these; they keep their
 * initials, and that difference is readable before any word is.
 *
 * Every mark is generated from geometry rather than stored as an asset, so a
 * new size is a number and there is nothing to keep in sync.
 */

/** What a mark is doing. Idle is the resting state and does not animate. */
export type MarkState = "idle" | "thinking" | "working" | "waiting";

export const SHAPES = [
	"sphere",
	"triangle",
	"diamond",
	"hexagon",
	"rings",
	"spiral",
	"cube",
	"star",
	"wave",
] as const;

export type MarkShape = (typeof SHAPES)[number];

/** One dot in the 40×40 field every mark is drawn in. */
interface Dot {
	/** Fixed for the life of the form, so React never re-keys the field. */
	id: string;
	o: number;
	r: number;
	x: number;
	y: number;
}

const FIELD = 40;
const CENTRE = FIELD / 2;
const RADIUS = FIELD / 2 - 3;
const TAU = Math.PI * 2;

/**
 * How long each form takes to turn once. Heavier shapes turn more slowly, so
 * a room of working agents does not read as a single blur.
 */
const REVOLUTION: Record<MarkShape, string> = {
	cube: "14s",
	diamond: "10s",
	hexagon: "12s",
	rings: "8s",
	sphere: "11s",
	spiral: "7s",
	star: "10s",
	triangle: "9s",
	wave: "4s",
};

function dot(x: number, y: number, r: number, o: number): Dot {
	const placed = { o, r, x: CENTRE + x, y: CENTRE + y };
	return {
		...placed,
		id: `${placed.x.toFixed(2)}:${placed.y.toFixed(2)}:${r.toFixed(2)}`,
	};
}

/** Points spread evenly over a sphere, brighter where they face you. */
function sphere(count = 170): Dot[] {
	const golden = Math.PI * (3 - Math.sqrt(5));
	const dots: Dot[] = [];
	for (let i = 0; i < count; i++) {
		const y = 1 - (i / (count - 1)) * 2;
		const ring = Math.sqrt(Math.max(0, 1 - y * y));
		const angle = golden * i;
		const depth = (Math.sin(angle) * ring + 1) / 2;
		dots.push(
			dot(
				Math.cos(angle) * ring * RADIUS,
				y * RADIUS,
				0.45 + depth * 0.45,
				0.16 + depth * 0.7,
			),
		);
	}
	return dots;
}

/** A polygon drawn as concentric outlines of dots. */
function polygon(sides: number, rotation = 0): Dot[] {
	const dots: Dot[] = [];
	const rings = [1, 0.62, 0.26];
	rings.forEach((scale, depth) => {
		const perEdge = Math.max(3, Math.round(13 * scale));
		for (let side = 0; side < sides; side++) {
			const from = rotation + (side * TAU) / sides;
			const to = rotation + ((side + 1) * TAU) / sides;
			// Walk the straight edge rather than the arc, so corners stay sharp.
			for (let step = 0; step < perEdge; step++) {
				const along = step / perEdge;
				const x1 = Math.cos(from) * RADIUS * scale;
				const y1 = Math.sin(from) * RADIUS * scale;
				const x2 = Math.cos(to) * RADIUS * scale;
				const y2 = Math.sin(to) * RADIUS * scale;
				dots.push(
					dot(
						x1 + (x2 - x1) * along,
						y1 + (y2 - y1) * along,
						0.75 - depth * 0.12,
						0.9 - depth * 0.26,
					),
				);
			}
		}
	});
	return dots;
}

function rings(): Dot[] {
	const dots: Dot[] = [];
	[
		[1, 30],
		[0.66, 20],
		[0.32, 10],
	].forEach(([scale, count], depth) => {
		for (let i = 0; i < count; i++) {
			const angle = (i / count) * TAU + depth * 0.3;
			dots.push(
				dot(
					Math.cos(angle) * RADIUS * scale,
					Math.sin(angle) * RADIUS * scale,
					0.8 - depth * 0.1,
					0.9 - depth * 0.22,
				),
			);
		}
	});
	return dots;
}

function spiral(count = 88, turns = 3.2): Dot[] {
	const dots: Dot[] = [];
	for (let i = 0; i < count; i++) {
		const along = i / (count - 1);
		const angle = along * TAU * turns;
		const reach = RADIUS * (0.12 + 0.88 * along);
		dots.push(
			dot(
				Math.cos(angle) * reach,
				Math.sin(angle) * reach,
				0.45 + along * 0.5,
				0.25 + along * 0.7,
			),
		);
	}
	return dots;
}

/** Three rhombus faces sharing a centre — a cube seen from the corner. */
function cube(perEdge = 11): Dot[] {
	const dots: Dot[] = [];
	const h = RADIUS * 0.92;
	const faces = [
		[
			[0, -h],
			[h * 0.87, -h * 0.5],
			[0, 0],
			[-h * 0.87, -h * 0.5],
		],
		[
			[-h * 0.87, -h * 0.5],
			[0, 0],
			[0, h],
			[-h * 0.87, h * 0.5],
		],
		[
			[h * 0.87, -h * 0.5],
			[0, 0],
			[0, h],
			[h * 0.87, h * 0.5],
		],
	];
	faces.forEach((face, depth) => {
		for (let corner = 0; corner < face.length; corner++) {
			const [x1, y1] = face[corner] as [number, number];
			const [x2, y2] = face[(corner + 1) % face.length] as [number, number];
			for (let step = 0; step < perEdge; step++) {
				const along = step / perEdge;
				dots.push(
					dot(
						x1 + (x2 - x1) * along,
						y1 + (y2 - y1) * along,
						0.7,
						0.92 - depth * 0.2,
					),
				);
			}
		}
	});
	return dots;
}

function star(spokes = 7): Dot[] {
	const dots: Dot[] = [];
	for (let spoke = 0; spoke < spokes; spoke++) {
		const angle = (spoke * TAU) / spokes;
		for (let step = 0; step < 9; step++) {
			const along = 0.18 + (step / 9) * 0.82;
			dots.push(
				dot(
					Math.cos(angle) * RADIUS * along,
					Math.sin(angle) * RADIUS * along,
					0.4 + along * 0.5,
					0.25 + along * 0.7,
				),
			);
		}
	}
	return dots;
}

function wave(rows = 7, columns = 13): Dot[] {
	const dots: Dot[] = [];
	for (let row = 0; row < rows; row++) {
		for (let column = 0; column < columns; column++) {
			const x = -RADIUS + (column / (columns - 1)) * 2 * RADIUS;
			const y =
				-RADIUS * 0.72 +
				(row / (rows - 1)) * RADIUS * 1.44 +
				Math.sin((column / columns) * TAU + row * 0.5) * 2.2;
			if (x * x + y * y > (RADIUS * 1.02) ** 2) continue;
			dots.push(dot(x, y, 0.65, 0.3 + 0.6 * (row / rows)));
		}
	}
	return dots;
}

function draw(shape: MarkShape): Dot[] {
	switch (shape) {
		case "sphere":
			return sphere();
		case "triangle":
			return polygon(3, -Math.PI / 2);
		case "diamond":
			return polygon(4, Math.PI / 4);
		case "hexagon":
			return polygon(6);
		case "rings":
			return rings();
		case "spiral":
			return spiral();
		case "cube":
			return cube();
		case "star":
			return star();
		case "wave":
			return wave();
	}
}

// The geometry never changes, and the viewBox makes it size-independent, so
// each form is drawn once for the life of the tab.
const drawn = new Map<MarkShape, Dot[]>();

export function dotsOf(shape: MarkShape): Dot[] {
	const already = drawn.get(shape);
	if (already) return already;
	const dots = draw(shape);
	drawn.set(shape, dots);
	return dots;
}

/**
 * Which form an agent gets. A stable hash of its id, so the mark survives
 * renaming, reinstalling and every other thing that changes about an agent —
 * it is how you recognise Ledger across rooms without reading.
 */
export function shapeFor(agentId: string): MarkShape {
	let hash = 2166136261;
	for (let i = 0; i < agentId.length; i++) {
		hash ^= agentId.charCodeAt(i);
		hash = Math.imul(hash, 16777619);
	}
	return SHAPES[Math.abs(hash) % SHAPES.length] as MarkShape;
}

interface AgentMarkProps {
	/** The agent this mark belongs to; it picks the form. */
	agentId: string;
	/**
	 * What a screen reader is told. Give it whenever the mark carries state —
	 * "Ledger, reading the April invoice" — and leave it off when the agent's
	 * name is already beside it.
	 */
	label?: string;
	/** Overrides the form, for a mark that has no agent behind it yet. */
	shape?: MarkShape;
	size?: number;
	state?: MarkState;
}

export function AgentMark({
	agentId,
	label,
	shape,
	size = 28,
	state = "idle",
}: AgentMarkProps) {
	const form = shape ?? shapeFor(agentId);
	const field = dotsOf(form).map((point) => (
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
		className: `agent-mark agent-mark-${state}`,
		height: size,
		style: { "--mark-revolution": REVOLUTION[form] } as React.CSSProperties,
		viewBox: `0 0 ${FIELD} ${FIELD}`,
		width: size,
	};

	// A mark beside its agent's name says nothing a reader does not already
	// have; a mark carrying state on its own has to say it.
	if (label === undefined) {
		return (
			<svg aria-hidden="true" {...shared}>
				{field}
			</svg>
		);
	}
	return (
		<svg role="img" {...shared}>
			<title>{label}</title>
			{field}
		</svg>
	);
}
