import { constants } from "node:fs";
import { open } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { OmarchyPalette } from "../shared/omarchy-theme";

export const MAX_PALETTE_BYTES = 16 * 1024;
const ansiNames = ["black", "red", "green", "yellow", "blue", "magenta", "cyan", "white"];
const keys = new Set([
	"background", "foreground", "accent", "cursor", "selection", "selection_background",
	"dark_background", "lighter_background", "dark_foreground", "bright_foreground", "muted", "purple", "bright_purple",
	"bg", "fg", "dark_fg", "bright_fg",
	...ansiNames, ...ansiNames.map((name) => `bright_${name}`),
	...Array.from({ length: 16 }, (_, i) => `color${i}`),
]);

/** Deliberately a flat color subset, not a TOML/CSS interpreter. */
export function parseOmarchyPalette(source: string): OmarchyPalette | null {
	if (Buffer.byteLength(source) > MAX_PALETTE_BYTES) return null;
	const colors: Record<string, string> = Object.create(null);
	for (const line of source.split(/\r?\n/)) {
		const key = /^\s*([a-z_0-9]+)\s*=/.exec(line)?.[1];
		if (!key || !keys.has(key)) continue;
		const match = /^\s*[a-z_0-9]+\s*=\s*(["'])(#[0-9a-fA-F]{6})\1\s*(?:#.*)?$/.exec(line);
		if (!match || colors[key]) return null;
		colors[key] = match[2].toLowerCase();
	}
	const background = colors.background ?? colors.bg ?? colors.color0;
	const foreground = colors.foreground ?? colors.fg ?? colors.color7;
	const accent = colors.accent;
	if (!background || !foreground || !accent) return null;
	const brightForeground = colors.bright_foreground ?? colors.bright_fg ?? colors.color15 ?? foreground;
	const ansi = Array.from({ length: 16 }, (_, i) => {
		if (i === 0) return background;
		if (i === 7) return foreground;
		if (i === 8) return colors.color8 ?? colors.muted ?? colors.dark_foreground ?? colors.dark_fg ?? foreground;
		if (i === 15) return colors.color15 ?? brightForeground;
		const name = ansiNames[i % 8];
		const base = colors[name] ?? (name === "magenta" ? colors.purple : undefined) ?? colors[`color${i % 8}`];
		if (!base) return "";
		return colors[`color${i}`] ?? (i < 8 ? base : colors[`bright_${name}`]
			?? (name === "magenta" ? colors.bright_purple : undefined) ?? brighten(base));
	});
	if (ansi.some((color) => !color)) return null;
	return { background, foreground, accent, ansi, surface: colors.lighter_background, sidebar: colors.dark_background, cursor: colors.cursor ?? brightForeground,
		selection: colors.selection_background ?? colors.selection ?? colors.color8 ?? background };
}

function brighten(color: string): string {
	return "#" + [1, 3, 5].map((offset) => Math.round(Number.parseInt(color.slice(offset, offset + 2), 16) * 0.8 + 255 * 0.2).toString(16).padStart(2, "0")).join("");
}

export function omarchyPalettePaths(home = os.homedir()): string[] {
	return [path.join(home, ".local/state/omarchy/current/theme/colors.toml"),
		path.join(home, ".config/omarchy/current/theme/colors.toml")];
}

export async function readOmarchyPalette(paths = omarchyPalettePaths(), platform = process.platform): Promise<OmarchyPalette | null> {
	if (platform !== "linux") return null;
	for (const filename of paths) {
		let file;
		try {
			// NONBLOCK prevents a substituted FIFO from hanging Electron. Symlinks
			// are intentional: Omarchy atomically replaces its active theme link.
			file = await open(filename, constants.O_RDONLY | constants.O_NONBLOCK);
			const stat = await file.stat();
			if (!stat.isFile() || stat.size > MAX_PALETTE_BYTES) return null;
			const buffer = Buffer.alloc(MAX_PALETTE_BYTES + 1);
			const { bytesRead } = await file.read(buffer, 0, buffer.length, 0);
			return parseOmarchyPalette(buffer.subarray(0, bytesRead).toString("utf8"));
		} catch (error) {
			if ((error as NodeJS.ErrnoException).code !== "ENOENT") return null;
		} finally {
			await file?.close();
		}
	}
	return null;
}
