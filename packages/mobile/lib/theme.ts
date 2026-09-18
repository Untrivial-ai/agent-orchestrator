// Mission Control palette. Color = meaning; most states get none.
//
//   neutral (accent) = interactive emphasis: primary actions, selected controls
//   orange           = a working agent (alive, running)
//   amber            = needs your input / attention
//   red              = failing / stuck / crashed
//   green            = mergeable / passed / approved
//   neutral (muted)  = terminal states — merged, done, killed
//
// There is no blue and no purple in the product palette, on purpose. Every hue
// in the system already means a state, so the interactive accent carries
// emphasis with contrast — a filled pill against a hairline outline — instead
// of with a hue of its own. That keeps exactly one meaning per color, and stops
// a filled button from reading as a status.
//
// The terminal's ANSI ramp further down is the one exception: a terminal's 16
// colors are a compatibility surface owned by the agent TUIs, not part of this
// palette, and agents print in them by name.
//
// Two palettes of one shape. Light is derived from the desktop app's
// frontend/src/styles/tokens.css `:root[data-theme="light"]` block, with two
// deliberate departures:
//
//  1. Surfaces follow the iOS grouped-list convention — a grey base with white
//     cards — rather than desktop's near-white-on-white, which relies on borders
//     that are much harder to see on a phone.
//  2. Semantic colours are DARKENED for light mode, not reused. A hue tuned for
//     contrast against #0a0b0d is washed out on white; desktop darkens the same
//     way, and skipping this is what makes naive light themes look faded.
//
// There is deliberately no exported `theme` const. A default would let a screen
// keep rendering dark while compiling cleanly; without one, `tsc` names every
// call site that still needs the hook.

export type Theme = {
	bgBase: string;
	bgSide: string;
	bgColumn: string;
	bgSurface: string;
	bgElevated: string;
	bgElevatedHover: string;
	bgSubtle: string;

	textPrimary: string;
	textSecondary: string;
	textTertiary: string;
	textFaint: string;

	borderSubtle: string;
	borderDefault: string;
	borderStrong: string;

	/** Interactive emphasis. Deliberately hue-less — see the note at the top. */
	accent: string;
	accentTint: string;
	accentBorder: string;
	accentPressed: string;

	orange: string;
	amber: string;
	red: string;
	green: string;

	tintOrange: string;
	tintAmber: string;
	tintRed: string;
	tintGreen: string;

	/** Ink on a filled accent surface (primary button, send, mic). */
	onAccent: string;
	/** Scrim behind modals. */
	scrim: string;

	fontMono: string;
};

const FONT_MONO = "JetBrains Mono, Menlo, ui-monospace, monospace";

export const darkTheme: Theme = {
	// Surfaces (no box-in-box; the card is the only bordered surface)
	bgBase: "#0a0b0d",
	bgSide: "#08090b",
	bgColumn: "#0e0f12",
	bgSurface: "#121317", // headers, tab bar, input dock - flush chrome
	bgElevated: "#15171b", // cards & inputs
	bgElevatedHover: "#191b20",
	bgSubtle: "rgba(255,255,255,0.04)",

	// The two dimmest steps were below the contrast floor on the base surface
	// (tertiary measured 3.61:1, faint 2.17:1). They keep their hue and moved
	// only in lightness — the channel contrast responds to — so metadata reads
	// at 4.95:1 and the decorative step at 3.41:1.
	textPrimary: "#f4f5f7",
	textSecondary: "#9ba1aa",
	textTertiary: "#7a808a",
	textFaint: "#61666e",

	borderSubtle: "rgba(255,255,255,0.06)",
	borderDefault: "rgba(255,255,255,0.10)",
	borderStrong: "rgba(255,255,255,0.16)",

	accent: "#f4f5f7",
	accentTint: "rgba(244,245,247,0.14)",
	accentBorder: "rgba(244,245,247,0.28)",
	accentPressed: "rgba(244,245,247,0.22)",

	orange: "#f59f4c",
	amber: "#e8c14a",
	red: "#ef6b6b",
	green: "#74b98a",

	tintOrange: "rgba(245,159,76,0.14)",
	tintAmber: "rgba(232,193,74,0.14)",
	tintRed: "rgba(239,107,107,0.14)",
	tintGreen: "rgba(116,185,138,0.14)",

	// Near-black ink on the near-white accent.
	onAccent: "#0b0c0e",
	scrim: "rgba(0,0,0,0.6)",

	fontMono: FONT_MONO,
};

export const lightTheme: Theme = {
	// iOS grouped-list convention: grey base, white cards. Desktop uses
	// #fcfcfc/#ffffff, which on a phone leaves cards nearly invisible.
	bgBase: "#f2f2f7",
	bgSide: "#eceef2",
	bgColumn: "#f7f7fa",
	bgSurface: "#ffffff",
	bgElevated: "#ffffff",
	bgElevatedHover: "#ececf0",
	bgSubtle: "rgba(0,0,0,0.04)",

	// Same correction on white: secondary also deepened slightly to keep a
	// visible step between it and the new tertiary (5.97:1 vs 4.83:1).
	textPrimary: "#1a1a1a",
	textSecondary: "#5c5c60",
	textTertiary: "#6a6a6e",
	textFaint: "#87878b",

	// Alpha rather than solid hex, mirroring the dark ramp's structure so a
	// hairline behaves the same over a white card and over the grey base.
	borderSubtle: "rgba(0,0,0,0.06)",
	borderDefault: "rgba(0,0,0,0.12)",
	borderStrong: "rgba(0,0,0,0.20)",

	accent: "#141519",
	accentTint: "rgba(20,21,25,0.08)",
	accentBorder: "rgba(20,21,25,0.24)",
	accentPressed: "rgba(20,21,25,0.16)",

	// Hue kept, darkened for contrast on white — each clears 4.5:1 both on the
	// base surface and on its own 12% tint, which is where these actually render.
	orange: "#a04a08",
	amber: "#875900",
	red: "#b13428",
	green: "#2a702d",

	tintOrange: "rgba(180,83,9,0.12)",
	tintAmber: "rgba(148,98,0,0.12)",
	tintRed: "rgba(192,57,43,0.12)",
	tintGreen: "rgba(47,125,50,0.12)",

	onAccent: "#ffffff",
	scrim: "rgba(0,0,0,0.45)",

	fontMono: FONT_MONO,
};

export type ColorScheme = "light" | "dark";

export function themeFor(scheme: ColorScheme): Theme {
	return scheme === "light" ? lightTheme : darkTheme;
}

// ---- Terminal --------------------------------------------------------------

// The terminal's own ramp, kept apart from the product palette: agent TUIs own
// the semantic meaning of the ANSI slots, so a red from a test runner must not
// be redefined by our "failing" red. Values ported from the desktop's
// frontend/src/renderer/lib/terminal-themes.ts + tokens.css.
export type TerminalTheme = {
	background: string;
	foreground: string;
	cursor: string;
	black: string;
	red: string;
	green: string;
	yellow: string;
	blue: string;
	magenta: string;
	cyan: string;
	white: string;
	brightBlack: string;
	brightRed: string;
	brightGreen: string;
	brightYellow: string;
	brightBlue: string;
	brightMagenta: string;
	brightCyan: string;
	brightWhite: string;
};

const darkTerminal: TerminalTheme = {
	background: "#0c0d10",
	foreground: "#f4f5f7",
	cursor: "#f59f4c",
	// Collapsed into the background on purpose, so a TUI that fills a row with
	// "black" draws an invisible band instead of a bar. Agent TUIs use this slot
	// only as a fill — as a foreground it would be unreadable on their own dark
	// canvas — so nothing legitimate is lost, and the user's submitted prompt sits
	// on the terminal background in both themes rather than in a black stripe.
	black: "#0c0d10",
	red: "#f05d5e",
	green: "#44c97a",
	yellow: "#e5c34b",
	blue: "#5b9cff",
	magenta: "#c678dd",
	cyan: "#56b6c2",
	white: "#d7dae0",
	brightBlack: "#7f8792",
	brightRed: "#ff7b7c",
	brightGreen: "#62df91",
	brightYellow: "#f2d66d",
	brightBlue: "#79b1ff",
	brightMagenta: "#d99aee",
	brightCyan: "#79d4df",
	brightWhite: "#f4f5f7",
};

const lightTerminal: TerminalTheme = {
	background: "#f5f5f4",
	foreground: "#24292f",
	cursor: "#b45309",
	// See the dark palette: same reasoning, and the reason the prompt band was a
	// stark black bar on a light canvas.
	black: "#f5f5f4",
	red: "#a13c37",
	green: "#2e6b3e",
	yellow: "#87660f",
	blue: "#3b5aa6",
	magenta: "#7b5799",
	cyan: "#3d7a7a",
	white: "#666d75",
	brightBlack: "#4c535b",
	brightRed: "#7e3330",
	brightGreen: "#265231",
	brightYellow: "#6b5108",
	brightBlue: "#31487f",
	brightMagenta: "#5f4476",
	brightCyan: "#316061",
	brightWhite: "#24292f",
};

export function terminalTheme(scheme: ColorScheme): TerminalTheme {
	return scheme === "light" ? lightTerminal : darkTerminal;
}

// ---- Status vocabulary -----------------------------------------------------

// AO's attention levels, in urgency order. Drives the board sections.
export type AttentionLevel = "merge" | "action" | "respond" | "review" | "pending" | "working" | "done";

export type AttentionMeta = { label: string; color: string; tint: string; order: number };

export function attentionMetaFor(t: Theme): Record<string, AttentionMeta> {
	return {
		merge: { label: "Ready to merge", color: t.green, tint: t.tintGreen, order: 0 },
		action: { label: "Needs you", color: t.amber, tint: t.tintAmber, order: 1 },
		respond: { label: "Needs you", color: t.amber, tint: t.tintAmber, order: 1 },
		review: { label: "Review", color: t.red, tint: t.tintRed, order: 2 },
		pending: { label: "In review", color: t.textTertiary, tint: t.bgSubtle, order: 3 },
		working: { label: "Working", color: t.orange, tint: t.tintOrange, order: 4 },
		done: { label: "Done", color: t.textTertiary, tint: t.bgSubtle, order: 5 },
	};
}

export type StatusVisual = { color: string; label: string; breathing?: boolean };

// One status maps to one dot color and short label. Mirrors AO's getStatusSpec so
// the phone speaks the same visual language as the dashboard.
export function statusVisual(t: Theme, status?: string | null): StatusVisual {
	switch (status) {
		case "spawning":
			return { color: t.textSecondary, label: "Starting" };
		case "working":
			return { color: t.orange, label: "Working", breathing: true };
		case "detecting":
			return { color: t.orange, label: "Detecting", breathing: true };
		case "needs_input":
			return { color: t.amber, label: "Needs input" };
		case "changes_requested":
			return { color: t.amber, label: "Changes req." };
		case "stuck":
			return { color: t.red, label: "Stuck" };
		case "errored":
			return { color: t.red, label: "Crashed" };
		case "ci_failed":
			return { color: t.red, label: "CI failed" };
		case "pr_open":
			return { color: t.textSecondary, label: "PR open" };
		case "review_pending":
			return { color: t.textSecondary, label: "In review" };
		case "approved":
			return { color: t.green, label: "Approved" };
		case "mergeable":
			return { color: t.green, label: "Mergeable" };
		// Merged is terminal and no longer actionable, so it drops out of the
		// success hue and settles into the neutral the other finished states use.
		case "merged":
			return { color: t.textSecondary, label: "Merged" };
		case "done":
			return { color: t.green, label: "Done" };
		case "idle":
			return { color: t.textTertiary, label: "Idle" };
		// Without these four the wire value leaks into the UI verbatim — the board
		// was rendering a literal "no_signal". Labels from desktop's
		// sessionStatusViews so the two apps say the same words.
		case "no_signal":
			return { color: t.textTertiary, label: "No signal" };
		case "exited":
			return { color: t.red, label: "Exited" };
		case "draft":
			return { color: t.textSecondary, label: "Draft PR" };
		case "unknown":
			return { color: t.textTertiary, label: "Unknown" };
		case "cleanup":
			return { color: t.textTertiary, label: "Cleanup" };
		case "killed":
		case "terminated":
			return { color: t.textFaint, label: "Terminated" };
		default:
			return { color: t.textTertiary, label: status ?? "unknown" };
	}
}
