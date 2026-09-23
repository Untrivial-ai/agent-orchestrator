/**
 * The app's measurement system. Every size, gap, radius, icon and duration in
 * the product comes from here, so a screen never invents a number.
 *
 * The sizes are Apple's Dynamic Type ramp (iOS 17 HIG) — a phone needs those
 * steps, and they are what `fontScaleCap` is calibrated against. Everything
 * else about the type follows the desktop app, which is the same product:
 *
 *   - **Geist**, the family the renderer uses (`--font-family-base`), and Geist
 *     Mono for code. iOS would otherwise draw SF Pro, and the two products
 *     would not look like each other.
 *   - **Weights of 400, 500 and 600 only.** The renderer's scale stops at
 *     `--font-weight-semibold`; bold was never part of the design, and a bold
 *     screen title on a phone read heavier than the same title on the desktop.
 *   - **Negative tracking**, from the renderer's scale: `-0.025em` for the
 *     largest steps, `-0.015em` for subheads, `-0.01em` for everything at
 *     reading size. SF Pro's optical tracking ran the other way at large sizes,
 *     which is why these used to be positive.
 *
 * Spacing is a 4pt grid with a 2pt half-step for dense rows. Radii, icon sizes
 * and motion follow the same idea: a short ladder of named steps chosen for a
 * job, not a number picked per screen.
 */

export type TypeToken = {
	fontFamily: string;
	fontSize: number;
	lineHeight: number;
	fontWeight: "400" | "500" | "600";
	letterSpacing: number;
};

/**
 * The desktop's family, one file per weight.
 *
 * A weight is a font file here, not a number: with a bundled family iOS draws
 * whichever face the name points at and ignores `fontWeight`, so a style that
 * sets a weight has to name the file that goes with it. `fontForWeight` is that
 * mapping in one place, and the type ramp below uses it.
 */
export const font = {
	regular: "Geist_400Regular",
	medium: "Geist_500Medium",
	semibold: "Geist_600SemiBold",
	mono: "GeistMono_400Regular",
} as const;

export function fontForWeight(weight: TypeToken["fontWeight"]): string {
	return weight === "600" ? font.semibold : weight === "500" ? font.medium : font.regular;
}

/** The renderer's tracking scale, in em. */
export const tracking = { tightXl: -0.025, tightLg: -0.015, tight: -0.01 } as const;

/**
 * Apple's ramp. Use the semantic name, not the number: a screen title is
 * `title2`, a row label is `body`, a metadata line is `footnote`.
 *
 *   largeTitle / title1 / title2  — screen and sheet titles
 *   title3                        — section titles inside a screen
 *   headline                      — emphasized body (row titles, primary copy)
 *   body                          — default reading size
 *   callout                       — slightly denser body
 *   subheadline                   — secondary copy under a row title
 *   footnote                      — metadata, timestamps, counts
 *   caption1                      — dense labels, tab bar
 *   caption2                      — the floor: badges and uppercase micro-labels
 */
export const type = {
	largeTitle: { fontFamily: font.semibold, fontSize: 34, lineHeight: 41, fontWeight: "600", letterSpacing: 34 * tracking.tightXl },
	title1: { fontFamily: font.semibold, fontSize: 28, lineHeight: 34, fontWeight: "600", letterSpacing: 28 * tracking.tightXl },
	title2: { fontFamily: font.semibold, fontSize: 22, lineHeight: 28, fontWeight: "600", letterSpacing: 22 * tracking.tightLg },
	title3: { fontFamily: font.semibold, fontSize: 20, lineHeight: 25, fontWeight: "600", letterSpacing: 20 * tracking.tightLg },
	headline: { fontFamily: font.semibold, fontSize: 17, lineHeight: 22, fontWeight: "600", letterSpacing: 17 * tracking.tight },
	body: { fontFamily: font.regular, fontSize: 17, lineHeight: 22, fontWeight: "400", letterSpacing: 17 * tracking.tight },
	callout: { fontFamily: font.regular, fontSize: 16, lineHeight: 21, fontWeight: "400", letterSpacing: 16 * tracking.tight },
	subheadline: { fontFamily: font.regular, fontSize: 15, lineHeight: 20, fontWeight: "400", letterSpacing: 15 * tracking.tight },
	footnote: { fontFamily: font.regular, fontSize: 13, lineHeight: 18, fontWeight: "400", letterSpacing: 13 * tracking.tight },
	caption1: { fontFamily: font.medium, fontSize: 12, lineHeight: 16, fontWeight: "500", letterSpacing: 12 * tracking.tight },
	caption2: { fontFamily: font.medium, fontSize: 11, lineHeight: 13, fontWeight: "500", letterSpacing: 0 },
} as const satisfies Record<string, TypeToken>;

/**
 * Long-form copy inside a message. The HIG line heights above are tuned for
 * labels and rows; prose wants the looser 1.5 leading that keeps a paragraph
 * readable, so it gets its own step rather than borrowing the callout's.
 */
export const prose: TypeToken = {
	fontFamily: font.regular,
	fontSize: 16,
	lineHeight: 24,
	fontWeight: "400",
	letterSpacing: 16 * tracking.tight,
};

export type TypeName = keyof typeof type;

/**
 * An uppercase micro-label: the one place small caps are worth the tracking.
 * Pair with the color the section actually means — never with the accent.
 */
export const microLabel: TypeToken = {
	fontFamily: font.semibold,
	fontSize: type.caption2.fontSize,
	lineHeight: type.caption2.lineHeight,
	fontWeight: "600",
	letterSpacing: 0.9,
};

/** 4pt grid, with a 2pt half-step for hairline gaps inside a row. */
export const space = {
	none: 0,
	hair: 2,
	xxs: 4,
	xs: 6,
	sm: 8,
	md: 12,
	lg: 16,
	xl: 20,
	xxl: 24,
	xxxl: 32,
	huge: 40,
} as const;

/** Horizontal padding for screen content and list rows. */
export const gutter = space.lg;

/**
 * Radii. Nesting reads as one surface only when the inner radius is the outer
 * radius minus the padding between them — `concentric()` does that arithmetic
 * so a card and its inset children never disagree by a pixel.
 */
export const radius = {
	xs: 4,
	sm: 8,
	md: 12,
	lg: 16,
	xl: 20,
	xxl: 28,
	pill: 999,
} as const;

/** Icon ladder. Match the stroke to the weight of the text beside it. */
export const iconSize = {
	xs: 12,
	sm: 15,
	md: 17,
	lg: 20,
	xl: 24,
	xxl: 28,
} as const;

/** The minimum tappable square, from the HIG. */
export const touchTarget = 44;

/**
 * The inset every sheet body shares. The native sheet draws its own top
 * clearance, so this is the one place the JS side pads — keeping it here is what
 * stops two pickers from disagreeing about their top edge.
 */
export const sheetInset = {
	horizontal: space.xl,
	top: space.xl,
	bottom: space.xxl,
} as const;

/**
 * Motion. Fast and short for anything the user triggers repeatedly; longer
 * only when something enters the screen for the first time.
 *
 * The standard curve is a decelerating ease — quick off the mark, settled at
 * the end — which is what makes a transition feel like the system's own.
 */
export const duration = {
	instant: 0,
	fast: 100,
	base: 150,
	slow: 220,
	slower: 320,
} as const;

export const easingCurve = {
	/** cubic-bezier control points — quick off the mark, settled at the end. */
	standard: [0.2, 0, 0, 1] as const,
	decelerate: [0.2, 0, 0, 1] as const,
	accelerate: [0.4, 0, 1, 1] as const,
} as const;

/** Spring configs for RN's Animated. */
export const spring = {
	/** Settles without overshoot — sheets, sidebars, anything structural. */
	gentle: { damping: 24, stiffness: 220, mass: 0.9 },
	/** A little faster, still no bounce — press feedback, toggles. */
	snappy: { damping: 20, stiffness: 320, mass: 0.8 },
} as const;

/** Press feedback: scale down, fade a touch, and come back on the same curve. */
export const press = {
	scale: 0.96,
	opacity: 0.7,
	in: duration.fast,
	out: duration.base,
} as const;

/**
 * How far each kind of text may grow under Dynamic Type.
 *
 * Uncapped text breaks this app specifically: the worker dock is a fixed 52pt,
 * rows are fixed-height controls, and chips and eyebrows are laid out expecting
 * one line. At the largest accessibility sizes those clip or draw under a
 * neighbouring control rather than reflowing.
 *
 * This is the accessibility exception, not a policy: content keeps the user's
 * full setting, and a cap tight enough to defeat it is worse than a layout that
 * bends — hence the 1.3 floor.
 */
export const fontScaleCap = {
	/** Chips, badges, pills, eyebrows, row values — dense and single-line. */
	chrome: 1.3,
	/** Screen and sheet titles. */
	title: 1.4,
	/** Body copy, buttons, settings labels, empty states. */
	body: 1.6,
} as const;
