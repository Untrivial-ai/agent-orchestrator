/**
 * A ```mermaid fence rendered as a diagram instead of source text.
 *
 * Renders the same chrome as a code block (language label, copy control) so a
 * diagram never looks like a different kind of object in the timeline, plus an
 * always-visible Diagram/Code switch for when the layout engine misread the
 * author's intent or the reader wants the source. The switch stays visible
 * rather than hover-revealed like the copy control: a diagram whose source
 * is undiscoverable reads as a picture the reader cannot audit.
 *
 * Loading shows the source as plain text and swaps in the diagram when the
 * engine chunk arrives — the same plain-first pattern as syntax highlighting,
 * so there is no spinner flash and no layout shift on first paint. A fence
 * that is still streaming never starts a render at all: its text changes per
 * delta, and each attempt would join the cache under a key nothing rereads.
 *
 * On any failure (syntax error, oversized block, engine chunk that will not
 * load) the block stays source text with a quiet caption. A missing diagram
 * is a readable outcome; a blank box or an error box that reads as content
 * is not.
 */

import { memo, useCallback, useEffect, useState, type MouseEvent } from "react";
import { cn } from "../../lib/utils";
import { isWebLink, openLinkInSystemBrowser } from "../../lib/external-link-policy";
import { isRenderableDiagram, renderMermaidDiagram, type DiagramTheme } from "../../lib/mermaid-diagram";
import { CopyButton } from "./CopyButton";

function readTheme(): DiagramTheme {
	if (typeof document !== "undefined") {
		const stored = document.documentElement.dataset.theme;
		if (stored === "light" || stored === "dark") return stored;
	}
	if (typeof window !== "undefined" && typeof window.matchMedia === "function") {
		return window.matchMedia("(prefers-color-scheme: light)").matches ? "light" : "dark";
	}
	return "dark";
}

/** Re-renders the diagram when AO's theme flips, like the code theme does. */
function useDiagramTheme(): DiagramTheme {
	const [theme, setTheme] = useState<DiagramTheme>(readTheme);

	useEffect(() => {
		const refresh = () => setTheme(readTheme());
		const observer = new MutationObserver(refresh);
		observer.observe(document.documentElement, { attributes: true, attributeFilter: ["data-theme"] });
		const media = window.matchMedia("(prefers-color-scheme: light)");
		media.addEventListener("change", refresh);
		return () => {
			observer.disconnect();
			media.removeEventListener("change", refresh);
		};
	}, []);

	return theme;
}

export const MermaidBlock = memo(function MermaidBlock({
	code,
	streaming = false,
	onLinkOpen,
}: {
	code: string;
	/** Text still arriving. A block that changes per delta is not worth laying out. */
	streaming?: boolean;
	/**
	 * Where a web link inside the diagram goes. Chat passes its AO Browser
	 * handler; surfaces without one (file preview) leave it undefined and
	 * links open in the system browser instead. Either way a click never
	 * navigates the renderer itself.
	 */
	onLinkOpen?: (url: string) => void;
}) {
	const theme = useDiagramTheme();
	const [svg, setSvg] = useState<string | undefined>(undefined);
	const [failed, setFailed] = useState(false);
	const [showSource, setShowSource] = useState(false);

	useEffect(() => {
		if (streaming || !isRenderableDiagram(code)) {
			setSvg(undefined);
			setFailed(!streaming && !isRenderableDiagram(code) && code.trim().length > 0);
			return;
		}
		let live = true;
		setSvg(undefined);
		setFailed(false);
		void renderMermaidDiagram(code, theme).then(
			(diagram) => {
				if (live) setSvg(diagram);
			},
			() => {
				if (live) setFailed(true);
			},
		);
		return () => {
			live = false;
		};
	}, [code, theme, streaming]);

	// A `click A https://…` directive in the source becomes a live anchor in
	// the SVG. Letting it navigate would drive the whole renderer to that
	// URL, so it goes through the same policy as chat links instead.
	// Non-web schemes end at `openLinkInSystemBrowser` too, but that IPC is
	// allowlisted main-side (`main/external-open.ts`: http/https/mailto
	// only), so `file://` and custom schemes from a `click` directive are
	// rejected there and never reach `shell.openExternal`.
	const onDiagramClick = useCallback(
		(event: MouseEvent<HTMLDivElement>) => {
			const anchor = (event.target as Element).closest?.("a[href]");
			if (!anchor) return;
			const href = anchor.getAttribute("href");
			if (!href) return;
			event.preventDefault();
			const toSystemBrowser =
				event.metaKey || event.ctrlKey || event.altKey || !onLinkOpen || !isWebLink(href);
			if (toSystemBrowser) {
				void openLinkInSystemBrowser(href);
				return;
			}
			onLinkOpen(href);
		},
		[onLinkOpen],
	);

	// Middle-click fires `auxclick`, not `click`: without this a middle-click
	// on a diagram anchor would skip the policy above and fall through to the
	// app-level window-open guard. Right-click (button 2) is ignored — this
	// surface builds no native context menu, so there is no "open link" path
	// to intercept there.
	const onDiagramAuxClick = useCallback(
		(event: MouseEvent<HTMLDivElement>) => {
			if (event.button !== 1) return;
			onDiagramClick(event);
		},
		[onDiagramClick],
	);

	const showingDiagram = svg !== undefined && !showSource;

	return (
		<div className="chat-code group/code my-2.5 overflow-hidden rounded-lg border border-border bg-surface">
			<div className="flex items-center gap-2 border-b border-border bg-raised/40 px-2.5 py-1">
				<span className="font-mono text-[10px] uppercase tracking-wide text-muted-foreground">
					mermaid
				</span>
				<div className="ml-auto flex items-center gap-1.5">
					{svg !== undefined ? (
						<div
							role="group"
							aria-label="Diagram view"
							className="flex items-center rounded-md border border-border bg-background p-0.5"
						>
							<button
								type="button"
								onClick={() => setShowSource(false)}
								aria-pressed={!showSource}
								className={cn(
									"rounded px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide transition-colors",
									showSource
										? "text-muted-foreground hover:text-foreground"
										: "bg-raised text-foreground",
								)}
							>
								Diagram
							</button>
							<button
								type="button"
								onClick={() => setShowSource(true)}
								aria-pressed={showSource}
								className={cn(
									"rounded px-2 py-0.5 font-mono text-[10px] uppercase tracking-wide transition-colors",
									showSource
										? "bg-raised text-foreground"
										: "text-muted-foreground hover:text-foreground",
								)}
							>
								Code
							</button>
						</div>
					) : null}
					<div className="flex items-center gap-0.5 opacity-0 transition-opacity duration-150 focus-within:opacity-100 group-hover/code:opacity-100">
						<CopyButton text={code} label="Copy diagram source" />
					</div>
				</div>
			</div>
			{showingDiagram ? (
				<div className="overflow-x-auto bg-background px-3 py-3">
					{/* The SVG string is sanitized in `lib/mermaid-diagram.ts`
					    (mermaid strict mode plus DOMPurify; scripts and
					    active-content tags forbidden, `foreignObject` labels
					    scrubbed of handlers). This div is the one sanctioned
					    HTML sink in the chat surface, and link clicks inside
					    it are intercepted, never navigated. */}
					<div
						data-testid="mermaid-diagram"
						role="img"
						aria-label="Mermaid diagram"
						onClick={onDiagramClick}
						onAuxClick={onDiagramAuxClick}
						// The theme already matches AO's: the diagram re-renders with
						// mermaid's dark theme when AO is dark (see
						// useDiagramTheme), so no CSS inversion is applied here.
						className="mermaid-diagram mx-auto max-w-full [&>svg]:mx-auto [&>svg]:h-auto [&>svg]:max-w-full"
						dangerouslySetInnerHTML={{ __html: svg }}
					/>
				</div>
			) : (
				<pre className="scrollbar-none overflow-x-auto px-3 py-2.5">
					<code className="font-mono text-[12px] leading-[1.6] text-foreground">{code}</code>
				</pre>
			)}
			{failed && !showSource ? (
				<div className="border-t border-border px-3 py-1.5 text-[11px] text-muted-foreground">
					Couldn&apos;t render this diagram — showing its source.
				</div>
			) : null}
		</div>
	);
});
