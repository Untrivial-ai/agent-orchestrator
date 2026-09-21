// Shared spacing, radius and type scales.
//
// Deliberately NOT a design-system migration. The ~50 existing `makeStyles`
// blocks keep their own literals: two source tests pin exact pixel numbers
// (settings-density.source.test.ts, android-compatibility.source.test.ts), CI
// runs no lint and no build step, and `tsc` cannot tell `space.md === 16` from
// `space.md === 12` — so a mechanical restyle would ship unverifiable visual
// drift. This module is a *vocabulary for new and rewritten code* only.
//
// Every value below is lifted from a literal already in the codebase, so
// adopting a token in a rewritten file is provably a no-op. The comment on each
// entry names where it came from.
//
// Free of React Native imports so it can be unit-tested directly, the same split
// the codebase already uses for worker-dock-layout.ts and pushStatus.ts.

/**
 * Spacing steps. Gaps, padding and margins.
 *
 * Sources: `4` (ui.tsx borderRadius/gap), `8` (ui.tsx emptyMsg marginTop),
 * `12` (cardShell marginHorizontal), `16` (ui.tsx paddingHorizontal),
 * `20` (SHEET_SCROLL_CONTENT paddingHorizontal), `24` (SHEET_SCROLL_CONTENT
 * paddingBottom).
 */
export const space = {
	xs: 4,
	sm: 8,
	md: 12,
	lg: 16,
	xl: 20,
	xxl: 24,
} as const;

/**
 * Corner radii.
 *
 * Sources: `7` (the former project row's workerRow), `12` (cardShell),
 * `16` (app/settings.tsx card, pinned by settings-density.source.test.ts),
 * `20` (ui.tsx Pill).
 */
export const radius = {
	sm: 7,
	md: 12,
	lg: 16,
	xl: 20,
} as const;

/**
 * Type roles, by the job the text does rather than by size.
 *
 * `as const` keeps the string literals assignable to RN's `fontWeight` without
 * importing `TextStyle` — which would put a React Native import in a module that
 * must stay runnable under Node.
 *
 * Sources, in order: ui.tsx `screenTitle`, ui.tsx `sheetTitle`, ui.tsx
 * `emptyTitle`, worker-list-row.tsx `title`, ui.tsx `rowLabel`, worker-list-row
 * `project`/`details` + ui.tsx `listSectionLabel`, ui.tsx `chipText`, ui.tsx
 * `sectionLabel`.
 */
export const type = {
	/** Screen headers. */
	title: { fontSize: 26, fontWeight: "800", letterSpacing: -0.5 },
	/** Sheet headers. */
	sheetTitle: { fontSize: 19, fontWeight: "800", letterSpacing: -0.3 },
	/** Empty-state headings. */
	emptyTitle: { fontSize: 17, fontWeight: "700" },
	/** The subject of a list row — the name you scan for. */
	rowTitle: { fontSize: 16, lineHeight: 21, fontWeight: "600", letterSpacing: -0.15 },
	/** Body copy and settings-row labels. */
	body: { fontSize: 15, fontWeight: "500" },
	/** Supporting detail under a row title; timestamps, branches, counts. */
	meta: { fontSize: 12, lineHeight: 16, fontWeight: "500" },
	/** Chips and badges. */
	micro: { fontSize: 11, fontWeight: "600" },
	/** Uppercase section labels. The tracking is what makes them read as labels. */
	eyebrow: { fontSize: 11, fontWeight: "700", letterSpacing: 1.2 },
} as const;

/**
 * How far each kind of text may grow under Dynamic Type / font scale.
 *
 * Uncapped text breaks this app specifically: the worker dock is a fixed 52pt,
 * list rows are fixed heights, and chips and eyebrows are laid out expecting one
 * line. At the largest accessibility sizes those clip rather than reflow.
 *
 * Capping is per-component on purpose. `Text.defaultProps` is the usual global
 * escape hatch, but React 19 removed it for function components, so a global
 * default would be a silent no-op.
 *
 * The ceiling rises as the text's job gets more important: chrome must stay on
 * its line, a screen title can afford some growth, and body copy — the text
 * someone enabled large type in order to *read* — grows the most. Nothing is
 * capped below 1.3, because a cap tight enough to defeat the setting is worse
 * than a layout that bends.
 */
export const fontScaleCap = {
	/** Chips, badges, pills, eyebrows, row values — dense, single-line. */
	chrome: 1.3,
	/** Screen and sheet titles. */
	title: 1.4,
	/** Body copy, buttons, settings labels, empty states. */
	body: 1.6,
} as const;

export type SpaceToken = keyof typeof space;
export type RadiusToken = keyof typeof radius;
export type TypeToken = keyof typeof type;
