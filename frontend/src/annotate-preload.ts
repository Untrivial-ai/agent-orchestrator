import { ipcRenderer } from "electron";
import geistLatinWoff2 from "@fontsource-variable/geist/files/geist-latin-wght-normal.woff2?inline";
import geistMonoLatinWoff2 from "@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2?inline";
import {
	createBrowserAnnotationContext,
	type BrowserAnnotationCancelReason,
	type BrowserAnnotationContext,
	type BrowserAnnotationPageSubmitPayload,
} from "./shared/browser-annotations";
import { promptPositionForRect, type AnnotationRectLike } from "./shared/browser-annotation-overlay";

let enabled = false;
let selectedElement: Element | null = null;
let selectedContext: BrowserAnnotationContext | null = null;
let multiSelectActive = false;
let multiSelectElements: Element[] = [];
let multiSelectContexts: BrowserAnnotationContext[] | null = null;
let host: HTMLDivElement | null = null;
let shadow: ShadowRoot | null = null;
let viewportResizeObserver: ResizeObserver | null = null;
let hintFadeTimer: NodeJS.Timeout | null = null;

const PROMPT_GUTTER = 14;
const PROMPT_GAP = 10;
const PROMPT_MAX_HEIGHT = 360;
const PROMPT_COMPACT_CHROME_HEIGHT = 48;
const PROMPT_EXPANDED_CHROME_HEIGHT = 48;
const TEXTAREA_MIN_HEIGHT = 32;
const MARKDOWN_ANNOTATION_TARGETS =
	"h1, h2, h3, h4, h5, h6, p, ul, ol, li, blockquote, pre, table, th, td, figure, figcaption, img, hr, details, summary";

ipcRenderer.on("browser:annotation:setMode", (_event, input: { enabled?: boolean }) => {
	setEnabled(Boolean(input?.enabled), "disabled");
});

window.addEventListener("beforeunload", () => {
	if (enabled) sendCancel("navigation");
	cleanupOverlay();
	enabled = false;
});

function setEnabled(next: boolean, cancelReason: BrowserAnnotationCancelReason): void {
	if (enabled === next) return;
	enabled = next;
	selectedElement = null;
	selectedContext = null;
	multiSelectActive = false;
	multiSelectElements = [];
	multiSelectContexts = null;
	if (hintFadeTimer) clearTimeout(hintFadeTimer);
	if (enabled) {
		ensureOverlay();
		installListeners();
		renderHint();
	} else {
		removeListeners();
		cleanupOverlay();
		if (cancelReason !== "disabled") sendCancel(cancelReason);
	}
}

function installListeners(): void {
	document.addEventListener("pointerover", handlePointerMove, true);
	document.addEventListener("pointermove", handlePointerMove, true);
	document.addEventListener("click", handleClick, true);
	document.addEventListener("keydown", handleKeyDown, true);
	window.addEventListener("scroll", refreshHighlight, true);
	window.addEventListener("resize", refreshHighlight, true);
	window.visualViewport?.addEventListener("resize", refreshHighlight);
	window.visualViewport?.addEventListener("scroll", refreshHighlight);
	if (typeof ResizeObserver !== "undefined") {
		viewportResizeObserver = new ResizeObserver(refreshHighlight);
		viewportResizeObserver.observe(document.documentElement);
	}
}

function removeListeners(): void {
	document.removeEventListener("pointerover", handlePointerMove, true);
	document.removeEventListener("pointermove", handlePointerMove, true);
	document.removeEventListener("click", handleClick, true);
	document.removeEventListener("keydown", handleKeyDown, true);
	window.removeEventListener("scroll", refreshHighlight, true);
	window.removeEventListener("resize", refreshHighlight, true);
	window.visualViewport?.removeEventListener("resize", refreshHighlight);
	window.visualViewport?.removeEventListener("scroll", refreshHighlight);
	viewportResizeObserver?.disconnect();
	viewportResizeObserver = null;
}

function handlePointerMove(event: PointerEvent): void {
	if (!enabled || selectedContext || multiSelectContexts || isOverlayEvent(event)) return;
	const target = annotationTarget(event.target);
	if (!target || target === selectedElement) return;
	selectedElement = target;
	selectedContext = null;
	renderHighlight(target, false);
}

function handleClick(event: MouseEvent): void {
	if (!enabled || isOverlayEvent(event)) return;
	if (selectedContext || multiSelectContexts) {
		event.preventDefault();
		event.stopPropagation();
		event.stopImmediatePropagation();
		return;
	}
	const target = annotationTarget(event.target);
	if (!target) return;
	event.preventDefault();
	event.stopPropagation();
	event.stopImmediatePropagation();
	if (multiSelectActive) {
		toggleMultiSelectElement(target);
		return;
	}
	selectedElement = target;
	selectedContext = createBrowserAnnotationContext(target);
	renderPrompt(target, selectedContext);
}

function handleKeyDown(event: KeyboardEvent): void {
	if (!enabled) return;
	if (event.key === "Escape") {
		event.preventDefault();
		event.stopPropagation();
		event.stopImmediatePropagation();
		setEnabled(false, "escape");
		return;
	}
	if (event.key !== "Shift" || event.repeat || selectedContext || multiSelectContexts) return;
	event.preventDefault();
	event.stopPropagation();
	event.stopImmediatePropagation();
	if (multiSelectActive) {
		finishMultiSelect();
	} else {
		startMultiSelect();
	}
}

function startMultiSelect(): void {
	multiSelectActive = true;
	multiSelectElements = [];
	hideHoverHighlight();
	renderMultiSelections();
	renderHint();
}

function finishMultiSelect(): void {
	multiSelectActive = false;
	hideHoverHighlight();
	if (multiSelectElements.length === 0) {
		renderHint();
		return;
	}
	multiSelectContexts = multiSelectElements.map(createBrowserAnnotationContext);
	renderMultiPrompt(multiSelectElements, multiSelectContexts);
}

function toggleMultiSelectElement(target: Element): void {
	const index = multiSelectElements.indexOf(target);
	if (index === -1) {
		multiSelectElements.push(target);
	} else {
		multiSelectElements.splice(index, 1);
	}
	renderMultiSelections();
	renderHint();
}

function hideHoverHighlight(): void {
	selectedElement = null;
	const highlight = ensureOverlay().querySelector<HTMLDivElement>(".highlight");
	if (highlight) highlight.hidden = true;
}

function currentPromptRect(): AnnotationRectLike | null {
	if (multiSelectContexts && multiSelectElements.length > 0) return unionRect(multiSelectElements);
	if (selectedContext && selectedElement) return selectedElement.getBoundingClientRect();
	return null;
}

function refreshHighlight(): void {
	if (!enabled) return;
	if (selectedElement) renderHighlight(selectedElement, Boolean(selectedContext));
	if (multiSelectElements.length > 0) renderMultiSelections();
	const rect = currentPromptRect();
	if (rect) repositionPrompt(rect);
}

function annotationTarget(target: EventTarget | null): Element | null {
	if (!(target instanceof Element)) return null;
	const element =
		target.closest("button, a, input, textarea, select, [role]") ??
		markdownAnnotationTarget(target) ??
		target.closest("[data-testid], [id], [class]") ??
		target;
	if (element === document.documentElement || element === document.body) return null;
	return element;
}

function markdownAnnotationTarget(target: Element): Element | null {
	// Goldmark emits semantic nodes without classes inside one classed layout
	// wrapper. Prefer the content block before the generic component heuristic
	// promotes every Markdown annotation to the full document wrapper.
	if (!target.closest(".markdown-body")) return null;
	return target.closest(MARKDOWN_ANNOTATION_TARGETS);
}

// Registered as FontFace objects from decoded bytes rather than an @font-face
// `src: url(data:...)` rule: the annotated page's own CSP (font-src) can block
// that url() fetch, silently falling the overlay back to a system font. A
// FontFace built from an in-memory buffer does no fetch, so it is not subject
// to the page's CSP and always loads.
function decodeBase64Font(dataUri: string): ArrayBuffer {
	const base64 = dataUri.slice(dataUri.indexOf(",") + 1);
	const binary = atob(base64);
	const buffer = new ArrayBuffer(binary.length);
	const bytes = new Uint8Array(buffer);
	for (let i = 0; i < binary.length; i += 1) bytes[i] = binary.charCodeAt(i);
	return buffer;
}

function registerFonts(root: ShadowRoot): void {
	if (typeof FontFace === "undefined") return;
	const fontSet = (root as ShadowRoot & { fonts?: FontFaceSet }).fonts ?? document.fonts;
	const sans = new FontFace("Geist Variable", decodeBase64Font(geistLatinWoff2), {
		weight: "100 900",
		style: "normal",
	});
	const mono = new FontFace("Geist Mono Variable", decodeBase64Font(geistMonoLatinWoff2), {
		weight: "100 900",
		style: "normal",
	});
	fontSet.add(sans);
	fontSet.add(mono);
	void sans.load();
	void mono.load();
}

function ensureOverlay(): ShadowRoot {
	if (shadow && host?.isConnected) return shadow;
	host = document.createElement("div");
	host.setAttribute("data-ao-annotation-root", "");
	host.style.position = "fixed";
	host.style.inset = "0";
	host.style.zIndex = "2147483647";
	host.style.pointerEvents = "none";
	(document.documentElement ?? document.body).appendChild(host);
	shadow = host.attachShadow({ mode: "open" });
	registerFonts(shadow);
	shadow.innerHTML = `
		<style>
			:host {
				all: initial;
				--ao-background: oklch(0.185 0.006 285.885);
				--ao-foreground: oklch(0.985 0 0);
				--ao-surface: oklch(0.24 0.008 285.885);
				--ao-muted: oklch(0.274 0.006 286.033);
				--ao-border: oklch(1 0 0 / 7%);
				--ao-input: oklch(1 0 0 / 4%);
				--ao-ring: oklch(0.552 0.016 285.938);
				--ao-passive: oklch(0.442 0.017 285.786);
				--ao-primary: oklch(0.92 0.004 286.32);
				--ao-primary-foreground: oklch(0.21 0.006 285.885);
				--ao-font-sans: "Geist Variable", "Geist", ui-sans-serif, system-ui, sans-serif;
				--ao-font-mono: "Geist Mono Variable", "Geist Mono", "JetBrainsMono Nerd Font Mono", "JetBrainsMono Nerd Font", "FiraCode Nerd Font Mono", "FiraCode Nerd Font", "MesloLGS NF", ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
				--ao-text-xs: 12px;
				--ao-control-md: 28px;
				--ao-icon-sm: 13px;
				--ao-radius-md: 8px;
			}
			.highlight {
				position: fixed;
				box-sizing: border-box;
				border: 2px solid #4d8dff;
				border-radius: 8px;
				background: rgba(77, 141, 255, 0.11);
				box-shadow:
					0 0 0 9999px rgba(0, 0, 0, 0.10),
					0 0 0 1px rgba(255, 255, 255, 0.20),
					0 12px 36px rgba(0, 0, 0, 0.24);
				pointer-events: none;
				transition:
					left 120ms ease,
					top 120ms ease,
					width 120ms ease,
					height 120ms ease,
					border-color 120ms ease,
					background 120ms ease;
			}
			.highlight--selected {
				border-color: #74b98a;
				background: rgba(116, 185, 138, 0.14);
				box-shadow:
					0 0 0 1px rgba(255, 255, 255, 0.20),
					0 12px 36px rgba(0, 0, 0, 0.24);
			}
			.prompt {
				position: fixed;
				width: min(360px, calc(100vw - 28px));
				box-sizing: border-box;
				border: 1px solid color-mix(in oklch, var(--ao-border) 85%, transparent);
				border-radius: 12px;
				background: var(--ao-surface);
				color: var(--ao-foreground);
				box-shadow: 0 10px 30px rgba(0, 0, 0, 0.38);
				padding: 5px 5px 43px;
				font: 13px/1.5 var(--ao-font-sans);
				font-weight: 400;
				pointer-events: auto;
				animation: prompt-in 140ms ease-out;
				transition:
					border-color 120ms ease,
					box-shadow 120ms ease;
			}
			.prompt:focus-within {
				border-color: color-mix(in oklch, var(--ao-ring) 80%, transparent);
				box-shadow:
					0 0 0 3px color-mix(in oklch, var(--ao-ring) 24%, transparent),
					0 10px 30px rgba(0, 0, 0, 0.38);
			}
			.prompt textarea {
				display: block;
				width: 100%;
				height: 32px;
				min-height: 32px;
				max-height: var(--ao-prompt-textarea-max-height, 350px);
				box-sizing: border-box;
				resize: none;
				border: 0;
				border-radius: 8px;
				background: transparent;
				color: var(--ao-foreground);
				caret-color: var(--ao-foreground);
				padding: 6px 9px;
				font-family: var(--ao-font-sans);
				font-size: var(--ao-text-xs);
				line-height: 20px;
				font-weight: 400;
				outline: none;
				overflow-y: hidden;
				scrollbar-width: none;
				-ms-overflow-style: none;
			}
			.prompt::after {
				content: "";
				position: absolute;
				left: 0;
				right: 0;
				bottom: 42px;
				height: 1px;
				background: color-mix(in oklch, var(--ao-border) 85%, transparent);
				pointer-events: none;
			}
			.prompt textarea::-webkit-scrollbar {
				display: none;
			}
			.prompt textarea::placeholder {
				color: var(--ao-passive);
			}
			.prompt button[type="submit"] {
				position: absolute;
				right: 8px;
				bottom: 7px;
				display: inline-flex;
				width: 28px;
				height: 28px;
				align-items: center;
				justify-content: center;
				border-radius: 999px;
				border: 1px solid transparent;
				background: var(--ao-primary);
				color: var(--ao-primary-foreground);
				padding: 0;
				transition:
					background 120ms ease,
					opacity 120ms ease,
					transform 120ms ease;
			}
			.prompt button[type="submit"]:hover {
				background: color-mix(in oklch, var(--ao-primary) 82%, transparent);
			}
			.prompt button[type="submit"]:active {
				transform: translateY(1px);
			}
			.prompt button[type="submit"]:focus-visible {
				outline: none;
				border-color: var(--ao-ring);
				box-shadow: 0 0 0 3px color-mix(in oklch, var(--ao-ring) 30%, transparent);
			}
			.prompt button[type="submit"]:disabled {
				opacity: 0.32;
				pointer-events: none;
			}
			.prompt button[type="submit"] svg {
				width: var(--ao-icon-sm);
				height: var(--ao-icon-sm);
				stroke: currentColor;
				stroke-width: 2;
				stroke-linecap: round;
				stroke-linejoin: round;
				fill: none;
			}
			.hint {
				position: fixed;
				left: 12px;
				bottom: 12px;
				max-width: calc(100vw - 36px);
				border-radius: 6px;
				background: #15171b;
				color: #c9d1d9;
				padding: 7px 9px;
				font: 12px/1.3 var(--ao-font-sans);
				box-shadow: 0 10px 30px rgba(0, 0, 0, 0.35);
				pointer-events: none;
				animation: prompt-in 140ms ease-out;
				word-wrap: break-word;
				overflow-wrap: break-word;
				white-space: normal;
			}
			.hint-fade-out {
				animation: hint-fade-out 300ms ease-in forwards !important;
			}
			@keyframes prompt-in {
				from { opacity: 0; transform: translateY(4px) scale(0.985); }
				to { opacity: 1; transform: translateY(0) scale(1); }
			}
			@keyframes hint-fade-out {
				from { opacity: 1; }
				to { opacity: 0; }
			}
			@media (prefers-reduced-motion: reduce) {
				.highlight,
				.prompt,
				.prompt button[type="submit"] {
					transition: none;
				}
				.prompt,
				.hint {
					animation: none;
				}
			}
			</style>
		<div class="highlight" hidden></div>
		<div class="selections"></div>
		<div class="mount"></div>
	`;
	return shadow;
}

function renderHint(): void {
	const root = ensureOverlay();
	const mount = root.querySelector<HTMLDivElement>(".mount");
	if (!mount) return;
	mount.innerHTML = `<div class="hint">${hintText()}</div>`;

	if (hintFadeTimer) clearTimeout(hintFadeTimer);
	hintFadeTimer = setTimeout(() => {
		const hintElement = root.querySelector<HTMLDivElement>(".hint");
		if (hintElement) {
			hintElement.classList.add("hint-fade-out");
		}
		hintFadeTimer = null;
	}, 3000);
}

function hintText(): string {
	if (!multiSelectActive) {
		return "Click an element to annotate, or press Shift to select multiple. Press Esc to cancel.";
	}
	if (multiSelectElements.length === 0) {
		return "Click elements to select them. Press Shift again to finish.";
	}
	const count = multiSelectElements.length;
	return `${count} element${count === 1 ? "" : "s"} selected. Click more, or press Shift to finish.`;
}

function renderHighlight(element: Element, locked: boolean): void {
	const root = ensureOverlay();
	const highlight = root.querySelector<HTMLDivElement>(".highlight");
	if (!highlight) return;
	const rect = element.getBoundingClientRect();
	highlight.hidden = false;
	highlight.style.left = `${Math.max(0, rect.left)}px`;
	highlight.style.top = `${Math.max(0, rect.top)}px`;
	highlight.style.width = `${Math.max(0, rect.width)}px`;
	highlight.style.height = `${Math.max(0, rect.height)}px`;
	highlight.style.borderColor = locked ? "#74b98a" : "#4d8dff";
}

function renderMultiSelections(): void {
	const root = ensureOverlay();
	const container = root.querySelector<HTMLDivElement>(".selections");
	if (!container) return;
	container.innerHTML = "";
	for (const element of multiSelectElements) {
		const rect = element.getBoundingClientRect();
		const box = document.createElement("div");
		box.className = "highlight highlight--selected";
		box.style.left = `${Math.max(0, rect.left)}px`;
		box.style.top = `${Math.max(0, rect.top)}px`;
		box.style.width = `${Math.max(0, rect.width)}px`;
		box.style.height = `${Math.max(0, rect.height)}px`;
		container.appendChild(box);
	}
}

function renderPrompt(element: Element, context: BrowserAnnotationContext): void {
	renderHighlight(element, true);
	openPrompt(element.getBoundingClientRect(), (instruction) => ({
		instruction,
		selection: { kind: "element", context },
	}));
}

function renderMultiPrompt(elements: Element[], contexts: BrowserAnnotationContext[]): void {
	renderMultiSelections();
	openPrompt(unionRect(elements), (instruction) => ({
		instruction,
		selection: { kind: "elements", contexts },
	}));
}

function openPrompt(
	rect: AnnotationRectLike,
	buildPayload: (instruction: string) => BrowserAnnotationPageSubmitPayload,
): void {
	const root = ensureOverlay();
	const mount = root.querySelector<HTMLDivElement>(".mount");
	if (!mount) return;
	mount.innerHTML = `
		<form class="prompt" aria-label="Annotate selection">
			<textarea rows="1" aria-label="Annotation request" placeholder="Describe the change…"></textarea>
			<button disabled type="submit" aria-label="Send annotation" title="Send (⌘/Ctrl + Enter)">
				<svg viewBox="0 0 24 24" aria-hidden="true">
					<path d="M12 19V5"></path>
					<path d="m5 12 7-7 7 7"></path>
				</svg>
			</button>
		</form>
	`;
	const form = mount.querySelector<HTMLFormElement>("form")!;
	repositionPrompt(rect);
	const textarea = form.querySelector<HTMLTextAreaElement>("textarea")!;
	const submitButton = form.querySelector<HTMLButtonElement>('button[type="submit"]')!;
	const updateSubmitState = (): void => {
		submitButton.disabled = textarea.value.trim().length === 0;
		const current = currentPromptRect();
		if (current) repositionPrompt(current);
	};
	let submitting = false;
	const submitAnnotation = (): void => {
		if (submitting) return;
		const instruction = textarea.value.trim();
		if (!instruction) {
			textarea.focus();
			return;
		}
		submitting = true;
		// Hide the input chrome but leave the highlight/selection boxes up, then
		// wait a paint before invoking so the main process captures a clean
		// snapshot of the picked element(s) — not the annotation popup itself.
		// invoke (not send) so the overlay teardown below cannot race the
		// snapshot capture in the main process.
		mount.innerHTML = "";
		void waitForPaint()
			.then(() => ipcRenderer.invoke("browser:annotation:submit", buildPayload(instruction)))
			.then(() => setEnabled(false, "disabled"));
	};
	form.addEventListener("submit", (event) => {
		event.preventDefault();
		submitAnnotation();
	});
	textarea.addEventListener("input", updateSubmitState);
	textarea.addEventListener("keydown", (event) => {
		event.stopPropagation();
		if (event.key === "Escape") {
			event.preventDefault();
			setEnabled(false, "escape");
		} else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) {
			event.preventDefault();
			submitAnnotation();
		}
	});
	setTimeout(() => textarea.focus(), 0);
}

function currentViewport(): { width: number; height: number } {
	const documentWidth = document.documentElement.clientWidth;
	const documentHeight = document.documentElement.clientHeight;
	const layoutWidth = Math.min(window.innerWidth, documentWidth > 0 ? documentWidth : window.innerWidth);
	const layoutHeight = Math.min(window.innerHeight, documentHeight > 0 ? documentHeight : window.innerHeight);
	return {
		width: Math.min(layoutWidth, window.visualViewport?.width ?? layoutWidth),
		height: Math.min(layoutHeight, window.visualViewport?.height ?? layoutHeight),
	};
}

function setPromptHeightLimit(form: HTMLFormElement, rect: AnnotationRectLike, chromeHeight: number): number {
	const viewportHeight = currentViewport().height;
	const spaceAbove = rect.top - PROMPT_GAP - PROMPT_GUTTER;
	const spaceBelow = viewportHeight - PROMPT_GUTTER - rect.bottom - PROMPT_GAP;
	const availablePromptHeight = Math.max(spaceAbove, spaceBelow);
	const promptMaxHeight = Math.max(
		TEXTAREA_MIN_HEIGHT + chromeHeight,
		Math.min(PROMPT_MAX_HEIGHT, availablePromptHeight),
	);
	const textareaMaxHeight = promptMaxHeight - chromeHeight;
	form.style.setProperty("--ao-prompt-textarea-max-height", `${textareaMaxHeight}px`);
	return textareaMaxHeight;
}

function repositionPrompt(rect: AnnotationRectLike): void {
	const form = shadow?.querySelector<HTMLFormElement>(".prompt");
	if (!form) return;
	const { width: viewportWidth, height: viewportHeight } = currentViewport();
	const promptWidth = Math.max(0, Math.min(360, viewportWidth - 28));
	form.style.width = `${promptWidth}px`;
	const textarea = form.querySelector<HTMLTextAreaElement>("textarea");
	if (textarea) {
		textarea.style.height = "0px";
		const expanded = textarea.scrollHeight > TEXTAREA_MIN_HEIGHT;
		form.classList.toggle("prompt--expanded", expanded);
		const chromeHeight = expanded ? PROMPT_EXPANDED_CHROME_HEIGHT : PROMPT_COMPACT_CHROME_HEIGHT;
		const textareaMaxHeight = setPromptHeightLimit(form, rect, chromeHeight);
		const height = Math.min(textareaMaxHeight, Math.max(TEXTAREA_MIN_HEIGHT, textarea.scrollHeight));
		textarea.style.height = `${height}px`;
		textarea.style.overflowY = textarea.scrollHeight > textareaMaxHeight ? "auto" : "hidden";
	}
	const measuredHeight = form.getBoundingClientRect().height;
	const { left, top } = promptPosition(rect, promptWidth, measuredHeight || 44, {
		width: viewportWidth,
		height: viewportHeight,
	});
	form.style.left = `${left}px`;
	form.style.top = `${top}px`;
}

function promptPosition(
	rect: AnnotationRectLike,
	promptWidth: number,
	promptHeight: number,
	viewport = { width: window.innerWidth, height: window.innerHeight },
): { left: number; top: number } {
	return promptPositionForRect(rect, {
		width: viewport.width,
		height: viewport.height,
		promptWidth,
		promptHeight,
		gutter: PROMPT_GUTTER,
		gap: PROMPT_GAP,
	});
}

// Resolves after the DOM mutation made just before calling this has been
// painted, so a capture taken right after is guaranteed to reflect it.
function waitForPaint(): Promise<void> {
	return new Promise((resolve) => {
		requestAnimationFrame(() => requestAnimationFrame(() => resolve()));
	});
}

function unionRect(elements: Element[]): AnnotationRectLike {
	const rects = elements.map((element) => element.getBoundingClientRect());
	return {
		left: Math.min(...rects.map((rect) => rect.left)),
		top: Math.min(...rects.map((rect) => rect.top)),
		bottom: Math.max(...rects.map((rect) => rect.bottom)),
	};
}

function cleanupOverlay(): void {
	host?.remove();
	host = null;
	shadow = null;
}

function sendCancel(reason: BrowserAnnotationCancelReason): void {
	ipcRenderer.send("browser:annotation:cancel", { reason });
}

function isOverlayEvent(event: Event): boolean {
	return Boolean(host && event.composedPath().includes(host));
}
