import { useEffect, useState } from "react";
import { highlightToHtml, languageForPath } from "../lib/shiki";
import { useResolvedTheme } from "../stores/ui-store";

// A cache key, not a security boundary — a cheap 32-bit rolling hash is
// plenty and avoids paying for crypto.subtle.digest on every render.
function hashContent(content: string): number {
	let hash = 0;
	for (let i = 0; i < content.length; i++) {
		hash = (hash * 31 + content.charCodeAt(i)) | 0;
	}
	return hash;
}

const CACHE_LIMIT = 50;
const cache = new Map<string, string>();

function rememberInCache(key: string, html: string): void {
	cache.delete(key);
	cache.set(key, html);
	if (cache.size <= CACHE_LIMIT) return;
	const oldest = cache.keys().next().value;
	if (oldest !== undefined) cache.delete(oldest);
}

// Caches the *rendered HTML string* (not raw tokens), since that's what gets
// fed straight to dangerouslySetInnerHTML and it's the tokenization step
// that's expensive — remounting the same file (tab flip, unrelated
// re-render) is then a cache hit instead of a re-tokenize. Self-invalidating:
// edited content hashes differently, so there's no explicit invalidation to
// wire up.
export function useShikiHtml(path: string, content: string): string | null {
	const resolvedTheme = useResolvedTheme();
	const lang = languageForPath(path);
	const key = `${lang}:${resolvedTheme}:${content.length}:${hashContent(content)}`;
	const [html, setHtml] = useState<string | null>(() => cache.get(key) ?? null);

	useEffect(() => {
		const cached = cache.get(key);
		if (cached !== undefined) {
			setHtml(cached);
			return;
		}
		let cancelled = false;
		setHtml(null);
		void highlightToHtml(content, lang, resolvedTheme).then((result) => {
			if (cancelled) return;
			rememberInCache(key, result);
			setHtml(result);
		});
		return () => {
			cancelled = true;
		};
	}, [content, key, lang, resolvedTheme]);

	return html;
}
