/**
 * Mermaid diagrams for ```mermaid fences in agent-authored markdown.
 *
 * Shared by `chat/ChatMarkdown.tsx` and `markdown/MarkdownFileView.tsx`, for
 * the same reason `markdown-fence.ts` is shared: one place for the render
 * policy to drift from.
 *
 * Three constraints shape this:
 *
 *   - The bundle stays lean. Mermaid is ~2MB, so it is dynamically imported on
 *     first use, the same lazy-chunk pattern as `code-highlight-engine.ts`.
 *     A conversation without a diagram never downloads or parses it.
 *   - The renderer's CSP is `script-src 'self'` with no `wasm-unsafe-eval`.
 *     Mermaid is plain JS (no WASM), and its output is inline SVG — no
 *     external fetch, no worker — so it renders inside the policy.
 *   - Agent output is only as trustworthy as the files the agent just read,
 *     so the SVG string is sanitized before injection. Mermaid runs with
 *     `securityLevel: 'strict'` (no JS callbacks, no `javascript:` links),
 *     and the result additionally passes through DOMPurify. Flowchart and
 *     state labels render as HTML inside `foreignObject`, which DOMPurify
 *     strips by default (its HTML integration points are `annotation-xml`
 *     only) — so the tag and its integration point are explicitly added back,
 *     while everything executable or exfiltrating stays forbidden: scripts,
 *     event-handler attributes and `javascript:` URLs are scrubbed, and
 *     `img`/`image`/media/form elements are forbidden outright (a diagram
 *     never needs them; CSP would block most loads anyway). Link clicks never
 *     navigate the renderer: `MermaidBlock` intercepts them and routes them
 *     through the same external-link policy as chat links. This container is
 *     the one `dangerouslySetInnerHTML` sink in the chat surface, scoped to a
 *     single div per diagram.
 */

export type DiagramTheme = "light" | "dark";

/**
 * Above this, the fence stays source text. Mermaid lays out synchronously on
 * the render thread, and a pasted 100KB "diagram" is really a log file with
 * the wrong label — rendering it would hang the timeline.
 */
export const MAX_DIAGRAM_CHARS = 20_000;

/** Empty fences have nothing to lay out; callers keep them as code blocks. */
export function isRenderableDiagram(code: string): boolean {
	return code.trim().length > 0 && code.length <= MAX_DIAGRAM_CHARS;
}

/* -------------------------------------------------------------------------- */
/* cache                                                                      */
/* -------------------------------------------------------------------------- */

/**
 * Bounded for the same reason as the highlight cache: a long session can
 * scroll past many diagrams and this holds SVG strings, which run larger than
 * their source. Eviction is insertion-ordered (LRU-ish: layout output is
 * deterministic per code+theme, so a re-rendered evictee just re-renders).
 */
const MAX_ENTRIES = 50;
const MAX_CHARS = 500_000;
const cache = new Map<string, string>();
let cachedChars = 0;

function cacheKey(code: string, theme: DiagramTheme): string {
	let hash = 0x811c9dc5;
	for (let i = 0; i < code.length; i += 1) {
		hash ^= code.charCodeAt(i);
		hash = Math.imul(hash, 0x01000193);
	}
	return `${theme}:${code.length}:${(hash >>> 0).toString(36)}`;
}

function store(key: string, svg: string): void {
	if (svg.length > MAX_CHARS) return;
	while (cache.size >= MAX_ENTRIES || cachedChars + svg.length > MAX_CHARS) {
		const oldest = cache.keys().next().value;
		if (oldest === undefined) break;
		cachedChars -= cache.get(oldest)?.length ?? 0;
		cache.delete(oldest);
	}
	cache.set(key, svg);
	cachedChars += svg.length;
}

/** Test-only reset; production code never needs to drop rendered diagrams. */
export function clearDiagramCache(): void {
	cache.clear();
	cachedChars = 0;
}

/* -------------------------------------------------------------------------- */
/* render                                                                     */
/* -------------------------------------------------------------------------- */

type MermaidModule = typeof import("mermaid").default;

let mermaidPromise: Promise<MermaidModule> | undefined;
let initializedTheme: DiagramTheme | undefined;
let renderCounter = 0;

function loadMermaid(): Promise<MermaidModule> {
	mermaidPromise ??= import("mermaid").then((module) => module.default as unknown as MermaidModule);
	return mermaidPromise;
}

/**
 * The diagram SVG for mermaid source, sanitized and safe to inject.
 *
 * Throws when the source is empty, oversized, unparseable, or the engine
 * cannot load — every one of those means the caller shows the source as a
 * code block instead. Never returns a partial or error-placeholder SVG:
 * with `suppressErrorRendering` mermaid would otherwise hand back a rendered
 * error box, which reads as content rather than as a failure.
 */
export async function renderMermaidDiagram(code: string, theme: DiagramTheme): Promise<string> {
	if (!isRenderableDiagram(code)) {
		throw new Error(code.trim().length === 0 ? "empty mermaid block" : "mermaid block too large");
	}
	const key = cacheKey(code, theme);
	const hit = cache.get(key);
	if (hit) return hit;

	const mermaid = await loadMermaid();
	if (initializedTheme !== theme) {
		mermaid.initialize({
			startOnLoad: false,
			theme: theme === "dark" ? "dark" : "default",
			securityLevel: "strict",
			suppressErrorRendering: true,
		});
		initializedTheme = theme;
	}

	// Parse first so a syntax error throws here, before render can produce an
	// error-placeholder SVG that would look like a real diagram.
	await mermaid.parse(code);

	// The id must be unique per render: mermaid requires it, and reusing one
	// across diagrams in the same document collides.
	renderCounter += 1;
	const id = `ao-mermaid-${renderCounter.toString(36)}-${Date.now().toString(36)}`;
	const { svg } = await mermaid.render(id, code);

	const { default: DOMPurify } = await import("dompurify");
	// `<style>` stays allowed: mermaid themes entirely through one style
	// element per SVG, and DOMPurify still scrubs its contents (no
	// `javascript:` URLs, no `expression()`). The page's own CSP
	// (`style-src 'self' 'unsafe-inline'`) already permits inline styles, so
	// this grants nothing the app does not grant itself.
	//
	// `foreignObject` and its integration point are added back deliberately:
	// flowchart/state labels render as HTML inside it, and DOMPurify's
	// default integration points (`annotation-xml` only) would otherwise
	// empty every label. Everything inside is still scrubbed — handlers,
	// scripts, `javascript:` URLs — and elements a diagram never needs
	// (`img`/`image`, media, forms) are forbidden outright rather than left
	// to CSP. Link clicks are intercepted by `MermaidBlock`, never navigated
	// directly. Verified against the real engine in headless Chromium (labels
	// kept; `onerror`, `<script>`, `javascript:` links and label `<img>`
	// all gone); see issue #4886.
	const clean = DOMPurify.sanitize(svg, {
		USE_PROFILES: { svg: true, html: true },
		ADD_TAGS: ["foreignObject"],
		HTML_INTEGRATION_POINTS: { foreignobject: true },
		FORBID_TAGS: [
			"script",
			"iframe",
			"object",
			"embed",
			"link",
			"meta",
			"img",
			"image",
			"video",
			"audio",
			"source",
			"track",
			"input",
			"button",
			"form",
			"textarea",
			"select",
		],
	});
	if (!clean || !clean.includes("<svg")) {
		throw new Error("mermaid produced no diagram");
	}
	store(key, clean);
	return clean;
}
