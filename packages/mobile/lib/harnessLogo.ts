// Per-harness brand marks, ported from the desktop's
// frontend/src/renderer/assets/agents/. The desktop keeps 7 of these as .svg,
// which React Native cannot render, so those were rasterised to PNG at 128px
// during the port; the rest were copied as-is (crush re-encoded — it was a JPEG
// with a .png name — and aider downscaled from 384px).
//
// Pure data + rules, no React, so the registry can be unit-tested.

export type BackdropPolarity = "neutral" | "needs-dark" | "needs-light";

// Which harnesses ship a mark. The `require` calls themselves live in
// harnessLogoAssets.ts so this module stays importable by a plain unit test —
// Node cannot require a PNG. `harnessLogo.test.ts` checks the two against the
// real asset directory, so they cannot drift apart silently.
export const LOGO_KEYS: ReadonlySet<string> = new Set([
	"agy", "aider", "amp", "auggie", "autohand", "claude-code", "cline", "codex",
	"continue", "copilot", "crush", "cursor", "devin", "droid", "goose", "grok",
	"kilocode", "kimi", "kiro", "muse", "opencode", "pi", "qwen", "vibe",
	"kimchi",
]);

/** Normalised lookup key, or "" when there is no usable harness. */
export function logoKey(harness?: string | null): string {
	return harness?.trim().toLowerCase() ?? "";
}

/** Whether a mark exists for this harness. */
export function hasLogo(harness?: string | null): boolean {
	return LOGO_KEYS.has(logoKey(harness));
}

// Which marks disappear against which background.
//
// These two sets are MEASURED, not guessed: for every asset, the share of its
// visible pixels whose WCAG contrast against the target background falls below
// 1.5:1. Anything over 50% is listed here.
//
// The problem is symmetric, which is easy to miss. `opencode` and `cursor` are
// pure white and `cline` is #e6e6e6, so they vanish on a light card — that much
// is visible in the desktop's SVG source. But `goose` and `kilocode` are pure
// black and `devin`/`droid`/`pi`/`kimi` are near-black, so they vanish just as
// completely on the dark card. Desktop has both bugs and renders every mark
// bare on every theme.
const NEEDS_DARK_BACKDROP = new Set(["opencode", "cursor", "cline", "continue", "grok", "copilot"]);
const NEEDS_LIGHT_BACKDROP = new Set(["kilocode", "goose", "devin", "droid", "pi", "kimi"]);

/**
 * What the mark needs behind it to stay visible.
 *
 * Keyed to the mark rather than the theme, so a chip is drawn only where one is
 * actually needed and its colour is fixed rather than following the palette —
 * a white mark needs something dark behind it in *both* themes.
 */
export function backdropFor(harness?: string | null): BackdropPolarity {
	const key = logoKey(harness);
	if (!key) return "neutral";
	if (NEEDS_DARK_BACKDROP.has(key)) return "needs-dark";
	if (NEEDS_LIGHT_BACKDROP.has(key)) return "needs-light";
	return "neutral";
}

/**
 * Fallback for a harness with no mark.
 *
 * Desktop's `toAgentProvider()` funnels every unrecognised harness into
 * `default: return "codex"`, so an unknown agent renders as the Codex logo —
 * confidently wrong. An initial says "some agent I don't have a mark for",
 * which is true. The fake harness intentionally has no brand asset and lands
 * here.
 */
export function harnessInitial(harness?: string | null): string {
	const key = harness?.trim();
	if (!key) return "?";
	return key.charAt(0).toUpperCase();
}
