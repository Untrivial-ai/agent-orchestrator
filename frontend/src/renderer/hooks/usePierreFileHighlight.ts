import { useEffect, useState } from "react";
import {
	getFiletypeFromFileName,
	getHighlighterIfLoaded,
	preloadHighlighter,
	type SupportedLanguages,
} from "@pierre/diffs";

const PIERRE_FILE_THEMES = { dark: "github-dark", light: "github-light" } as const;
const PIERRE_FILE_THEME_NAMES = [PIERRE_FILE_THEMES.dark, PIERRE_FILE_THEMES.light] as const;

/**
 * Starts resolving the selected file's Shiki grammar while its contents are
 * still loading. Pierre can render plain text before its lazy highlighter is
 * ready; waiting for this signal prevents that first unhighlighted paint.
 * A failed grammar load still settles so unsupported files remain readable.
 */
export function usePierreFileHighlightReady(path: string | null): boolean {
	const language: SupportedLanguages | null = path ? getFiletypeFromFileName(path) : null;
	const [settledLanguage, setSettledLanguage] = useState<SupportedLanguages | null>(null);
	const loaded = language == null || getHighlighterIfLoaded({ lang: language, theme: PIERRE_FILE_THEMES }) != null;
	const ready = loaded || settledLanguage === language;

	useEffect(() => {
		if (language == null || ready) return;
		let active = true;
		void preloadHighlighter({
			langs: [language],
			preferredHighlighter: "shiki-js",
			themes: [...PIERRE_FILE_THEME_NAMES],
		})
			.catch((error: unknown) => {
				console.warn(`files: could not preload syntax grammar ${language}`, error);
			})
			.finally(() => {
				if (active) setSettledLanguage(language);
			});
		return () => {
			active = false;
		};
	}, [language, ready]);

	return ready;
}
