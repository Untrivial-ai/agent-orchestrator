import { ipcRenderer } from "electron";
import { animate } from "motion";
import geistLatinWoff2 from "@fontsource-variable/geist/files/geist-latin-wght-normal.woff2?inline";
import geistMonoLatinWoff2 from "@fontsource-variable/geist-mono/files/geist-mono-latin-wght-normal.woff2?inline";
import {
	createBrowserAnnotationContext,
	createBrowserAnnotationSession,
	type BrowserAnnotationDraft,
	type BrowserAdjustmentProperty,
	type BrowserAnnotationCancelReason,
	type BrowserAnnotationKind,
	type BrowserAnnotationPageMode,
	type BrowserAnnotationTarget,
	type BrowserAnnotationTheme,
	type BrowserSavedAnnotation,
	type BrowserStyleAdjustment,
} from "./shared/browser-annotations";
import { promptPositionForRect, type AnnotationRectLike } from "./shared/browser-annotation-overlay";

let enabled = false;
let session = createBrowserAnnotationSession(window.location.href, document.title || undefined);
let hoveredElement: Element | null = null;
let selectedElement: Element | null = null;
let host: HTMLDivElement | null = null;
let shadow: ShadowRoot | null = null;
let theme: BrowserAnnotationTheme = {};
let showingOriginal = false;
let adjustmentBaseline = new WeakMap<Element, Map<BrowserAdjustmentProperty, string>>();
let editableTextTargets = new WeakMap<Element, Text>();
let composerAnchorRect: AnnotationRectLike | null = null;
type AdjustmentLockName = "ratio" | "paddingHorizontal" | "paddingVertical" | "marginHorizontal" | "marginVertical";
const emptyAdjustmentLocks = () => ({ ratio: false, paddingHorizontal: false, paddingVertical: false, marginHorizontal: false, marginVertical: false, ratioValue: 0 });
let adjustmentLocks = emptyAdjustmentLocks();
let nextLocalId = 0;
let screenshotNoticeTimer: ReturnType<typeof setTimeout> | null = null;

const ADJUST_MODE_ENABLED = true;

const PROMPT_GUTTER = 14;
const PROMPT_GAP = 10;
const COMMENT_WIDTH = 320;
const ADJUSTMENT_WIDTH = 320;
/** Smallest the card may shrink to before it starts scrolling its panel. */
const ADJUSTMENT_MIN_HEIGHT = 180;
/**
 * Composer expand/collapse curve. A decelerating ease keeps the edge the card
 * shares with the element glued in place while the panel settles; the previous
 * symmetric ease-in-out spent its first third barely moving and then lurched.
 */
const COMPOSER_TRANSITION_EASE: [number, number, number, number] = [0.22, 1, 0.36, 1];
const COMPOSER_OPEN_MS = 0.3;
const COMPOSER_CLOSE_MS = 0.22;
const SPACING_SECTION_MS = 0.22;
let adjustmentPanelAnimation: { stop: () => void } | null = null;
/** While true the panel animation owns the card's edge, so a scroll, resize or
 *  input event mid-flight must not re-anchor the composer underneath it. */
let composerPanelAnimating = false;
const spacingSectionAnimations = new WeakMap<HTMLElement, { stop: () => void }>();
const COMMENT_TEXTAREA_MIN_HEIGHT = 20;
const COMMENT_MAX_HEIGHT = 360;
/** Keep in sync with overlayStyles --pad / --gap / --control / --radius. */
const COMPOSER_PAD = 8;
const COMPOSER_GAP = 8;
/** Match browser tab bar icon buttons (--size-control-md / --radius-md). */
const COMPOSER_CONTROL = 28;
const COMPOSER_CONTROL_RADIUS = 8;
const COMPOSER_RADIUS = COMPOSER_CONTROL_RADIUS;
/** Concentric outer radius: inner control radius + container padding. */
const COMPOSER_OUTER_RADIUS = COMPOSER_CONTROL_RADIUS + COMPOSER_PAD;
const COMMENT_CHROME_VERTICAL = COMPOSER_PAD * 2;
/** Icon for the adjust/options control: palette | sliders | sliders-horizontal | sparkles | settings */
const ADJUST_ICON = "palette" as const;
/** Extra pixels outside the target on each side while a selection is active. */
const SELECTED_OUTSET_PX = 6;
const MARKDOWN_TARGETS =
	"h1, h2, h3, h4, h5, h6, p, ul, ol, li, blockquote, pre, table, th, td, figure, figcaption, img, hr, details, summary";

type AdjustmentDefinition = {
	property: BrowserAdjustmentProperty;
	/** Full name: the accessible name and the reset's title. */
	label: string;
	/** Compact name shown inside the field. Kept a substring of `label` so the
	 *  visible label stays part of the accessible name (WCAG 2.5.3). */
	short: string;
	group: "Content" | "Appearance" | "Size" | "Spacing" | "Layout";
	options?: string[];
};

const ADJUSTMENTS: AdjustmentDefinition[] = [
	{ property: "textContent", label: "Text content", short: "Text", group: "Content" },
	{ property: "color", label: "Text color", short: "Color", group: "Appearance" },
	{ property: "backgroundColor", label: "Fill", short: "Fill", group: "Appearance" },
	{ property: "opacity", label: "Opacity", short: "Opacity", group: "Appearance" },
	{ property: "fontFamily", label: "Font family", short: "Font", group: "Appearance", options: ["system-ui", "Arial", "Helvetica", "Georgia", "Times New Roman", "monospace"] },
	{ property: "fontSize", label: "Font size", short: "Size", group: "Appearance" },
	{ property: "fontWeight", label: "Font weight", short: "Weight", group: "Appearance", options: ["100", "200", "300", "400", "500", "600", "700", "800", "900"] },
	{ property: "borderRadius", label: "Border radius", short: "Radius", group: "Appearance" },
	{ property: "borderColor", label: "Border color", short: "Color", group: "Appearance" },
	{ property: "borderWidth", label: "Border width", short: "Width", group: "Appearance" },
	// The size pair reads W/H (the pattern every inspector uses), which also keeps
	// its visible label distinct from the border's "Width" field.
	{ property: "width", label: "Width", short: "W", group: "Size" },
	{ property: "height", label: "Height", short: "H", group: "Size" },
	{ property: "paddingTop", label: "Padding top", short: "Top", group: "Spacing" },
	{ property: "paddingBottom", label: "Padding bottom", short: "Bottom", group: "Spacing" },
	{ property: "paddingLeft", label: "Padding left", short: "Left", group: "Spacing" },
	{ property: "paddingRight", label: "Padding right", short: "Right", group: "Spacing" },
	{ property: "marginTop", label: "Margin top", short: "Top", group: "Spacing" },
	{ property: "marginBottom", label: "Margin bottom", short: "Bottom", group: "Spacing" },
	{ property: "marginLeft", label: "Margin left", short: "Left", group: "Spacing" },
	{ property: "marginRight", label: "Margin right", short: "Right", group: "Spacing" },
	{ property: "flexDirection", label: "Flex flow", short: "Flow", group: "Layout", options: ["row", "row-reverse", "column", "column-reverse"] },
	{ property: "alignItems", label: "Align items", short: "Align", group: "Layout", options: ["start", "center", "end", "stretch", "baseline"] },
	{ property: "justifyContent", label: "Justify content", short: "Justify", group: "Layout", options: ["start", "center", "end", "space-between", "space-around", "space-evenly"] },
	{ property: "columnGap", label: "Column gap", short: "Col", group: "Layout" },
	{ property: "rowGap", label: "Row gap", short: "Row", group: "Layout" },
];

const PIXEL_PROPERTIES = new Set<BrowserAdjustmentProperty>([
	"fontSize", "borderWidth", "borderRadius", "width", "height",
	"marginTop", "marginRight", "marginBottom", "marginLeft",
	"paddingTop", "paddingRight", "paddingBottom", "paddingLeft",
	"columnGap", "rowGap",
]);
const COLOR_PROPERTIES = new Set<BrowserAdjustmentProperty>(["color", "backgroundColor", "borderColor"]);
const TEXT_STYLE_PROPERTIES = new Set<BrowserAdjustmentProperty>(["color", "fontFamily", "fontSize", "fontWeight"]);
const SUPPORTED_ADJUSTMENT_PROPERTIES = new Set(ADJUSTMENTS.map((item) => item.property));

ipcRenderer.on("browser:annotation:setMode", (_event, input: BrowserAnnotationPageMode) => {
	if (input?.theme) theme = input.theme;
	const nextEnabled = Boolean(input?.enabled);
	// Clear any in-progress composer before applying the stored session when
	// leaving mode, so a stale draft from main cannot reopen the input.
	if (!nextEnabled) clearOpenDraft();
	if (input?.session && samePage(input.session.page.url, window.location.href)) session = input.session;
	else if (!samePage(session.page.url, window.location.href)) session = createBrowserAnnotationSession(window.location.href, document.title || undefined);
	if (!nextEnabled && session.draft) {
		delete session.draft;
		emitState();
	}
	sanitizeSessionAdjustments();
	applyAllAdjustments();
	setEnabled(nextEnabled, "disabled");
});

ipcRenderer.on("browser:annotation:action", (_event, action: string) => {
	if (!enabled) return;
	if (action === "capture") void captureScreenshot();
	else if (action === "preview-original") setOriginalPreview(true);
	else if (action === "restore-preview") setOriginalPreview(false);
	else if (action === "discard-all") discardAll();
	else if (action === "submit") void submitBatch();
});

window.addEventListener("beforeunload", cleanupOverlay);

function sanitizeSessionAdjustments(): void {
	for (const annotation of session.annotations)
		annotation.adjustments = annotation.adjustments.filter((item) => SUPPORTED_ADJUSTMENT_PROPERTIES.has(item.property));
	if (session.draft)
		session.draft.adjustments = session.draft.adjustments.filter((item) => SUPPORTED_ADJUSTMENT_PROPERTIES.has(item.property));
}

function setEnabled(next: boolean, reason: BrowserAnnotationCancelReason): void {
	if (enabled === next) {
		if (next) renderAll();
		return;
	}
	// Leaving mode drops any open composer/selection. Saved batch annotations
	// stay on the session. Main also strips draft on disable so re-entry does
	// not restore a selection; reload-while-enabled can still rehydrate draft.
	if (!next) clearOpenDraft();
	enabled = next;
	hoveredElement = null;
	selectedElement = null;
	composerAnchorRect = null;
	if (next) {
		if (session.draft) {
			selectedElement = resolveTarget(session.draft.target);
			composerAnchorRect = selectedElement ? copyRect(selectedElement.getBoundingClientRect()) : null;
		}
		ensureOverlay();
		installListeners();
		renderAll();
	} else {
		removeListeners();
		cleanupOverlay();
		if (reason !== "disabled") ipcRenderer.send("browser:annotation:cancel", { reason });
	}
}

/** Close an in-progress composer without removing saved batch annotations. */
function clearOpenDraft(): void {
	const draft = session.draft;
	if (!draft) return;
	const element = selectedElement ?? resolveTarget(draft.target);
	if (element) restoreAdjustments(element, draft.adjustments);
	if (draft.id) applyAllAdjustments();
	delete session.draft;
	adjustmentLocks = emptyAdjustmentLocks();
	composerAnchorRect = null;
	selectedElement = null;
	hoveredElement = null;
	emitState();
}

function installListeners(): void {
	document.addEventListener("pointermove", handlePointerMove, true);
	document.addEventListener("click", handleClick, true);
	document.addEventListener("keydown", handleKeyDown, true);
	window.addEventListener("scroll", refreshPositions, true);
	window.addEventListener("resize", refreshPositions, true);
}

function removeListeners(): void {
	document.removeEventListener("pointermove", handlePointerMove, true);
	document.removeEventListener("click", handleClick, true);
	document.removeEventListener("keydown", handleKeyDown, true);
	window.removeEventListener("scroll", refreshPositions, true);
	window.removeEventListener("resize", refreshPositions, true);
}

function handlePointerMove(event: PointerEvent): void {
	if (!enabled || isOverlayEvent(event) || session.draft) return;
	const target = annotationTarget(event.target);
	if (target === hoveredElement) return;
	hoveredElement = target;
	renderHover();
}

function handleClick(event: MouseEvent): void {
	if (!enabled || isOverlayEvent(event)) return;
	if (session.draft) {
		event.preventDefault();
		event.stopImmediatePropagation();
		closeComposer();
		return;
	}
	const target = annotationTarget(event.target);
	if (!target) return;
	event.preventDefault();
	event.stopImmediatePropagation();
	openComposer(target);
}

function handleKeyDown(event: KeyboardEvent): void {
	if (!enabled || isOverlayEvent(event)) return;
	if (event.key === "Escape") {
		event.preventDefault();
		if (session.draft) closeComposer();
		else setEnabled(false, "escape");
	} else if ((event.metaKey || event.ctrlKey) && event.key === "Enter") {
		event.preventDefault();
		void submitBatch();
	}
}

function annotationTarget(target: EventTarget | null): Element | null {
	if (!(target instanceof Element)) return null;
	const element =
		target.closest("button, a, input, textarea, select, [role]") ??
		(target.closest(".markdown-body") ? target.closest(MARKDOWN_TARGETS) : null) ??
		target.closest("[data-testid], [id], [class]") ??
		target;
	return element === document.documentElement || element === document.body ? null : element;
}

function openComposer(element: Element, annotation?: BrowserSavedAnnotation): void {
	selectedElement = element;
	hoveredElement = null;
	composerAnchorRect = copyRect(element.getBoundingClientRect());
	adjustmentLocks = emptyAdjustmentLocks();
	const target: BrowserAnnotationTarget = annotation?.target ?? { context: createBrowserAnnotationContext(element) };
	session.draft = annotation
		? { id: annotation.id, kind: annotation.kind, body: annotation.body, target, adjustments: annotation.adjustments.map((item) => ({ ...item })) }
		: { kind: "comment", body: "", target, adjustments: [] };
	emitState();
	renderAll();
	setTimeout(() => shadow?.querySelector<HTMLTextAreaElement>(".composer-note")?.focus(), 0);
}

function closeComposer(): void {
	clearOpenDraft();
	renderAll();
}

function saveDraft(): boolean {
	const draft = session.draft;
	if (!draft) return true;
	if (!draft.body.trim() && draft.adjustments.length === 0) {
		if (draft.id) discardSelected();
		else closeComposer();
		return true;
	}
	const now = new Date().toISOString();
	const existing = draft.id ? session.annotations.find((item) => item.id === draft.id) : undefined;
	if (existing) {
		existing.kind = draft.kind;
		existing.body = draft.body.trim();
		existing.target = draft.target;
		existing.adjustments = draft.adjustments.map((item) => ({ ...item }));
		existing.updatedAt = now;
	} else {
		session.annotations.push({
			id: localId("annotation"),
			number: nextAnnotationNumber(),
			kind: draft.kind,
			body: draft.body.trim(),
			target: draft.target,
			adjustments: draft.adjustments.map((item) => ({ ...item })),
			createdAt: now,
			updatedAt: now,
		});
	}
	delete session.draft;
	adjustmentLocks = emptyAdjustmentLocks();
	composerAnchorRect = null;
	selectedElement = null;
	hoveredElement = null;
	emitState();
	renderAll();
	return true;
}

async function submitBatch(): Promise<void> {
	if (!saveDraft() || session.annotations.length === 0) return;
	const root = ensureOverlay();
	root.querySelectorAll<HTMLElement>(".chrome").forEach((item) => { item.hidden = true; });
	await waitForPaint();
	await ipcRenderer.invoke("browser:annotation:submit", { session });
	setEnabled(false, "disabled");
}

async function captureScreenshot(): Promise<void> {
	const root = ensureOverlay();
	root.querySelectorAll<HTMLElement>(".chrome").forEach((item) => { item.hidden = true; });
	await waitForPaint();
	const copied = Boolean(await ipcRenderer.invoke("browser:annotation:capture"));
	renderAll();
	if (copied) showScreenshotNotice();
}

function discardSelected(): void {
	const draft = session.draft;
	if (!draft) return;
	if (selectedElement) restoreAdjustments(selectedElement, draft.adjustments);
	if (draft.id) session.annotations = session.annotations.filter((annotation) => annotation.id !== draft.id);
	delete session.draft;
	adjustmentLocks = emptyAdjustmentLocks();
	composerAnchorRect = null;
	selectedElement = null;
	hoveredElement = null;
	emitState();
	renderAll();
}

function discardAll(): void {
	for (const annotation of session.annotations) {
		const element = resolveTarget(annotation.target);
		if (element) restoreAdjustments(element, annotation.adjustments);
	}
	if (selectedElement && session.draft) restoreAdjustments(selectedElement, session.draft.adjustments);
	session = createBrowserAnnotationSession(window.location.href, document.title || undefined);
	selectedElement = null;
	hoveredElement = null;
	adjustmentBaseline = new WeakMap();
	editableTextTargets = new WeakMap();
	adjustmentLocks = emptyAdjustmentLocks();
	composerAnchorRect = null;
	ipcRenderer.send("browser:annotation:discard");
	emitState();
	renderAll();
}

function showScreenshotNotice(): void {
	const notice = ensureOverlay().querySelector<HTMLElement>(".screenshot-notice");
	if (!notice) return;
	notice.hidden = false;
	if (screenshotNoticeTimer) clearTimeout(screenshotNoticeTimer);
	screenshotNoticeTimer = setTimeout(() => {
		const current = shadow?.querySelector<HTMLElement>(".screenshot-notice");
		if (current) current.hidden = true;
		screenshotNoticeTimer = null;
	}, 1_600);
}

function emitState(): void {
	// Every draft mutation funnels through here, so the trailing add control
	// never drifts from what Enter would do with the same draft.
	syncComposerAddButton();
	ipcRenderer.send("browser:annotation:state", session);
}

/**
 * The add control is Enter's twin: it saves the draft, and stays inert while the
 * draft is empty so a stray click cannot dismiss the composer.
 */
function syncComposerAddButton(): void {
	const button = shadow?.querySelector<HTMLButtonElement>('[data-action="add"]');
	if (!button) return;
	const draft = session.draft;
	button.disabled = !draft || (!draft.body.trim() && draft.adjustments.length === 0);
}

function renderAll(): void {
	if (!enabled) return;
	ensureOverlay();
	renderHover();
	renderMarkers();
	renderComposer();
}

function renderHover(): void {
	const highlight = ensureOverlay().querySelector<HTMLElement>(".hover");
	if (!highlight) return;
	const target = session.draft
		? (selectedElement ?? resolveTarget(session.draft.target))
		: hoveredElement;
	if (!target) {
		highlight.hidden = true;
		highlight.classList.remove("hover--selected");
		return;
	}
	const selecting = Boolean(session.draft);
	const rect = target.getBoundingClientRect();
	const wasHidden = highlight.hidden;
	const alreadySelected = highlight.classList.contains("hover--selected");
	highlight.hidden = false;
	if (selecting) {
		// Ease from flush bounds to a constant outset once per selection.
		if (wasHidden || !alreadySelected) {
			highlight.classList.remove("hover--selected");
			positionBox(highlight, rect, 0);
			requestAnimationFrame(() => {
				if (!session.draft) return;
				highlight.classList.add("hover--selected");
				positionBox(highlight, target.getBoundingClientRect(), SELECTED_OUTSET_PX);
			});
		} else {
			positionBox(highlight, rect, SELECTED_OUTSET_PX);
		}
	} else {
		highlight.classList.remove("hover--selected");
		positionBox(highlight, rect, 0);
	}
}

function renderMarkers(): void {
	const root = ensureOverlay();
	const markers = root.querySelector<HTMLElement>(".markers")!;
	markers.innerHTML = "";
	const stacks = new Map<string, number>();
	for (const annotation of session.annotations) {
		const element = resolveTarget(annotation.target);
		if (!element) continue;
		const rect = element.getBoundingClientRect();
		const key = `${Math.round(rect.left)}:${Math.round(rect.top)}`;
		const offset = stacks.get(key) ?? 0;
		stacks.set(key, offset + 1);
		const marker = document.createElement("button");
		marker.className = "marker chrome";
		marker.type = "button";
		marker.textContent = String(annotation.number);
		marker.title = `Edit ${annotation.kind} ${annotation.number}`;
		marker.style.left = `${Math.max(4, rect.right - 9 + offset * 15)}px`;
		marker.style.top = `${Math.max(4, rect.top - 9)}px`;
		marker.addEventListener("click", (event) => {
			event.stopPropagation();
			if (!session.draft) openComposer(element, annotation);
		});
		markers.appendChild(marker);
	}
}

function renderComposer(): void {
	const mount = ensureOverlay().querySelector<HTMLElement>(".composer-mount")!;
	const draft = session.draft;
	if (!draft) { mount.innerHTML = ""; return; }
	const target = selectedElement ?? resolveTarget(draft.target);
	// Always built in its comment shape, with the input row first: the adjustment
	// panel is added and removed in place so the row (and the caret) survives the
	// transition instead of being rebuilt from scratch behind the animation.
	mount.innerHTML = `
		<form class="composer chrome composer--comment" aria-label="Annotate selection">
			<div class="composer-input-row">
				${ADJUST_MODE_ENABLED ? `<button type="button" data-action="adjust" class="adjust-button" aria-label="Adjust element" title="Adjust element">${adjustIcon()}</button>` : ""}
				<textarea class="composer-note" rows="1" aria-label="Comment" placeholder="Add a comment..."></textarea>
				<button type="button" data-action="add" class="add-button" aria-label="Add annotation" title="Add annotation">${icon("arrow")}</button>
			</div>
		</form>`;
	const form = mount.querySelector<HTMLFormElement>("form")!;
	const textarea = form.querySelector<HTMLTextAreaElement>(".composer-note")!;
	const addButton = form.querySelector<HTMLButtonElement>('[data-action="add"]');
	textarea.value = draft.body;
	if (!composerAnchorRect && target) composerAnchorRect = copyRect(target.getBoundingClientRect());
	const resize = () => composerAnchorRect && resizeAndPositionComposer(form, textarea, composerAnchorRect);
	textarea.addEventListener("input", () => {
		draft.body = textarea.value;
		resize();
		emitState();
	});
	textarea.addEventListener("keydown", (event) => {
		event.stopPropagation();
		if (event.key === "Enter" && !event.shiftKey && !event.metaKey && !event.ctrlKey) { event.preventDefault(); saveDraft(); }
		else if (event.key === "Escape") { event.preventDefault(); closeComposer(); }
		else if (event.key === "Enter" && (event.metaKey || event.ctrlKey)) { event.preventDefault(); void submitBatch(); }
	});
	form.addEventListener("submit", (event) => { event.preventDefault(); saveDraft(); });
	form.addEventListener("keydown", (event) => {
		if (event.target === textarea) return;
		event.stopPropagation();
		if (event.key === "Escape") {
			event.preventDefault();
			closeComposer();
		}
	});
	form.querySelector<HTMLElement>('[data-action="adjust"]')?.addEventListener("click", () => {
		void toggleAdjustmentMode(form, draft, target, resize);
	});
	addButton?.addEventListener("click", () => saveDraft());
	syncComposerAddButton();
	resize();
	// A re-opened saved adjustment arrives already expanded; the composer itself
	// appeared instantly, so there is nothing to open *from*.
	if (ADJUST_MODE_ENABLED && draft.kind === "adjustment" && target) {
		promoteAdjustmentPanel(form, target, resize, { instant: true });
	}
}

function applyComposerMode(form: HTMLFormElement, kind: BrowserAnnotationKind): void {
	const adjusting = kind === "adjustment";
	const textarea = form.querySelector<HTMLTextAreaElement>(".composer-note");
	if (textarea) {
		textarea.placeholder = adjusting ? "Describe these changes…" : "Add a comment...";
		textarea.setAttribute("aria-label", adjusting ? "Adjustment note" : "Comment");
	}
	const button = form.querySelector<HTMLButtonElement>('[data-action="adjust"]');
	if (!button) return;
	const label = adjusting ? "Return to comment" : "Adjust element";
	button.classList.toggle("adjust-button--active", adjusting);
	button.setAttribute("aria-label", label);
	button.title = label;
}

function bindAdjustmentPanel(form: HTMLFormElement, target: Element, resize: () => void): void {
	const panel = form.querySelector<HTMLElement>("[data-adjustment-panel]");
	if (!panel) return;
	// The panel scrolls without a scrollbar: keep the cue on its overflowed edge.
	panel.querySelector<HTMLElement>(".adjustment-scroll")
		?.addEventListener("scroll", () => syncPanelScrollCue(panel), { passive: true });
	form.querySelectorAll<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>("[data-property]").forEach((input) => {
		input.addEventListener("input", () => {
			const property = input.dataset.property as BrowserAdjustmentProperty;
			const value = adjustmentInputValue(input, property);
			updateAdjustment(target, property, value);
			syncAdjustmentFieldState(property);
			if (COLOR_PROPERTIES.has(property)) {
				const valueLabel = input.closest(".field")?.querySelector<HTMLElement>(".color-value");
				if (valueLabel) valueLabel.textContent = value.toUpperCase();
			}
		});
		input.addEventListener("change", () => {
			const property = input.dataset.property as BrowserAdjustmentProperty;
			if (PIXEL_PROPERTIES.has(property)) input.value = pixelControlValue(adjustmentInputValue(input, property));
		});
	});
	form.querySelectorAll<HTMLButtonElement>("[data-reset]").forEach((button) => button.addEventListener("click", () => {
		resetAdjustment(target, button.dataset.reset as BrowserAdjustmentProperty);
	}));
	form.querySelectorAll<HTMLButtonElement>("[data-lock]").forEach((button) => button.addEventListener("click", () => {
		const lock = button.dataset.lock as AdjustmentLockName;
		adjustmentLocks[lock] = !adjustmentLocks[lock];
		button.classList.toggle("link-button--active", adjustmentLocks[lock]);
		button.setAttribute("aria-pressed", String(adjustmentLocks[lock]));
		if (adjustmentLocks[lock]) synchronizeLockedValues(target, lock);
		else if (lock === "ratio") adjustmentLocks.ratioValue = 0;
	}));
	bindSpacingSections(form, panel, resize);
}

function promoteAdjustmentPanel(
	form: HTMLFormElement,
	target: Element,
	resize: () => void,
	transition: { startRect?: AnnotationRectLike; instant?: boolean } = {},
): void {
	if (form.querySelector("[data-adjustment-panel]")) return;
	form.classList.replace("composer--comment", "composer--adjustment");
	form.insertAdjacentHTML("beforeend", adjustmentPanel(target));
	applyComposerMode(form, "adjustment");
	const panel = form.querySelector<HTMLElement>("[data-adjustment-panel]");
	if (!panel) return;
	bindAdjustmentPanel(form, target, resize);
	resize();
	void animateAdjustmentPanel(panel, form, "open", resize, transition);
}

/** Put the composer back to its single-row shape without rebuilding it. */
function demoteComposer(form: HTMLFormElement): void {
	form.querySelector("[data-adjustment-panel]")?.remove();
	form.classList.remove("composer--above");
	form.classList.replace("composer--adjustment", "composer--comment");
	applyComposerMode(form, "comment");
}

async function toggleAdjustmentMode(
	form: HTMLFormElement,
	draft: BrowserAnnotationDraft,
	target: Element | null,
	resize: () => void,
): Promise<void> {
	const adjustButton = form.querySelector<HTMLButtonElement>('[data-action="adjust"]');
	if (adjustButton) adjustButton.disabled = true;
	try {
		if (draft.kind === "adjustment") {
			const panel = form.querySelector<HTMLElement>("[data-adjustment-panel]");
			if (panel) await animateAdjustmentPanel(panel, form, "close");
			if (target) restoreAdjustments(target, draft.adjustments);
			draft.adjustments = [];
			draft.kind = "comment";
			adjustmentLocks = emptyAdjustmentLocks();
			demoteComposer(form);
			resize();
		} else {
			if (!target) return;
			draft.kind = "adjustment";
			promoteAdjustmentPanel(form, target, resize, { startRect: copyRect(form.getBoundingClientRect()) });
		}
		emitState();
	} finally {
		if (adjustButton) adjustButton.disabled = false;
	}
}

function icon(name: "camera" | "eye" | "trash" | "close" | "arrow" | "check" | "sliders" | "sliders-horizontal" | "palette" | "sparkles" | "settings" | "reset" | "link" | "plus"): string {
	const paths = {
		camera: '<path d="M14.5 5 13 3h-4L7.5 5H4a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2V7a2 2 0 0 0-2-2z"/><circle cx="11" cy="11" r="3.5"/>',
		eye: '<path d="M2 12s3.5-6 10-6 10 6 10 6-3.5 6-10 6S2 12 2 12z"/><circle cx="12" cy="12" r="2.5"/>',
		trash: '<path d="M3 6h18M8 6V4h8v2m-9 0 1 14h8l1-14M10 10v6m4-6v6"/>',
		close: '<path d="m6 6 12 12M18 6 6 18"/>',
		arrow: '<path d="M12 19V5m-7 7 7-7 7 7"/>',
		check: '<path d="m5 12 4 4 10-10"/>',
		sliders: '<path d="M4 21v-7m0-4V3m8 18v-9m0-4V3m8 18v-5m0-4V3M1 14h6m2-6h6m2 8h6"/>',
		"sliders-horizontal": '<path d="M21 4H14M10 4H3M21 12h-9M8 12H3M21 20h-5M12 20H3M14 2v4M8 10v4M16 18v4"/>',
		palette: '<path d="M12 22a1 1 0 0 1 0-20 10 9 0 0 1 10 9 5 5 0 0 1-5 5h-2.25a1.75 1.75 0 0 0-1.4 2.8l.3.4a1.75 1.75 0 0 1-1.4 2.8z"/><circle cx="13.5" cy="6.5" r=".5" fill="currentColor" stroke="none"/><circle cx="17.5" cy="10.5" r=".5" fill="currentColor" stroke="none"/><circle cx="6.5" cy="12.5" r=".5" fill="currentColor" stroke="none"/><circle cx="8.5" cy="7.5" r=".5" fill="currentColor" stroke="none"/>',
		sparkles: '<path d="M15 4 16 8l4 1-3 3 1 4-4-2-4 2 1-4-3-3 4-1 1-4m-9 3 1 2 2 1-2 1-1 2-1-2-2-1 2-1 1-2m18 12 1 2 2 1-2 1-1 2-1-2-2-1 2-1 1-2"/>',
		settings: '<path d="M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8z"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M4.93 19.07l1.41-1.41M17.66 6.34l1.41-1.41"/>',
		reset: '<path d="M3 12a9 9 0 1 0 3-6.7L3 8m0-5v5h5"/>',
		link: '<path d="M10 13a5 5 0 0 0 7.54.54l2-2a5 5 0 0 0-7.07-7.07l-1.15 1.15M14 11a5 5 0 0 0-7.54-.54l-2 2a5 5 0 0 0 7.07 7.07l1.14-1.14"/>',
		plus: '<path d="M12 5v14M5 12h14"/>',
	};
	return `<svg viewBox="0 0 24 24" aria-hidden="true">${paths[name]}</svg>`;
}

function adjustIcon(): string {
	return icon(ADJUST_ICON);
}

function adjustmentPanel(element: Element): string {
	const draft = session.draft;
	if (!draft) return "";
	const field = (property: BrowserAdjustmentProperty, options: { wide?: boolean } = {}) => {
		const definition = ADJUSTMENTS.find((item) => item.property === property);
		return definition ? adjustmentField(element, definition, draft.adjustments, options) : "";
	};
	const linkButton = (lock: AdjustmentLockName, label: string) => `<button type="button" class="link-button${adjustmentLocks[lock] ? " link-button--active" : ""}" data-lock="${lock}" aria-label="${label}" title="${label}" aria-pressed="${adjustmentLocks[lock]}">${icon("link")}</button>`;
	// Every row is the same three-cell grid: two field boxes and one trailing
	// action cell, so fields, values and link buttons share the same edges.
	const row = (fields: string[], options: { link?: string } = {}) =>
		`<div class="panel-row">${fields.join("")}${options.link ?? ""}</div>`;
	const display = getComputedStyle(element).display;
	const hasEditableText = Boolean(editableTextNode(element));
	// Sections are ordered by what people reach for first: the text itself, then
	// its type, then the box it sits in, then geometry, then space around it.
	const content = hasEditableText
		? `<div class="adjustment-group">${row([field("textContent", { wide: true })])}</div>`
		: "";
	const type = hasEditableText
		? `<div class="adjustment-group">${row([field("fontFamily", { wide: true })])}${row([field("fontSize"), field("fontWeight")])}${row([field("color", { wide: true })])}</div>`
		: "";
	const fill = `<div class="adjustment-group">${row([field("backgroundColor"), field("opacity")])}</div>`;
	const border = `<div class="adjustment-group">${row([field("borderRadius"), field("borderWidth")])}${row([field("borderColor", { wide: true })])}</div>`;
	const size = `<div class="adjustment-group">${row([field("width"), field("height")], { link: linkButton("ratio", "Lock width and height ratio") })}</div>`;
	const section = (
		title: string,
		fields: string,
		options: { open?: boolean } = {},
	) => `<details class="panel-section"${options.open ? " open" : ""}><summary>${title}</summary><div class="section-body">${fields}</div></details>`;
	const padding = section("Padding", [
		row([field("paddingTop"), field("paddingBottom")], { link: linkButton("paddingVertical", "Link top and bottom padding") }),
		row([field("paddingLeft"), field("paddingRight")], { link: linkButton("paddingHorizontal", "Link left and right padding") }),
	].join(""), { open: true });
	const margin = section("Margin", [
		row([field("marginTop"), field("marginBottom")], { link: linkButton("marginVertical", "Link top and bottom margins") }),
		row([field("marginLeft"), field("marginRight")], { link: linkButton("marginHorizontal", "Link left and right margins") }),
	].join(""));
	const layout = display === "flex" || display === "inline-flex"
		? section("Layout", [
			row([field("flexDirection", { wide: true })]),
			row([field("alignItems", { wide: true })]),
			row([field("justifyContent", { wide: true })]),
			row([field("columnGap"), field("rowGap")]),
		].join(""))
		: "";
	return `<div class="adjustment-panel" data-adjustment-panel><div class="adjustment-panel-inner"><div class="element-header"><strong>${escapeAttribute(element.tagName.toLowerCase())}</strong></div>
		<div class="adjustment-scroll">
			${content}${type}${fill}${border}${size}${padding}${margin}${layout}
	</div></div></div>`;
}

/**
 * Tallest the card may get: the room the element leaves on the side the card
 * opens towards. There is no fixed ceiling on top of that — a panel only grows
 * this far when its property list actually needs the space, so a long inspector
 * is readable in one go instead of hiding its last rows behind a scroll, and a
 * short one stays the compact card it always was.
 */
function composerMaxHeight(): number {
	const viewport = viewportSize();
	const rect = composerAnchorRect;
	const room = viewport.height - PROMPT_GUTTER * 2;
	if (!rect) return room;
	const above = rect.top - PROMPT_GAP - PROMPT_GUTTER;
	const below = viewport.height - PROMPT_GUTTER - (rect.bottom + PROMPT_GAP);
	return Math.max(ADJUSTMENT_MIN_HEIGHT, Math.min(room, Math.max(above, below)));
}

function adjustmentPanelHeightCap(form: HTMLFormElement): number {
	const formMax = composerMaxHeight();
	// Everything the card spends on itself — the input row, the padding and the
	// hairline — so a capped panel can fill the card without its edge clipping.
	const panel = form.querySelector<HTMLElement>("[data-adjustment-panel]");
	const overhead = panel
		? Math.max(0, form.getBoundingClientRect().height - panel.getBoundingClientRect().height)
		: (form.querySelector(".composer-input-row")?.getBoundingClientRect().height ?? 36) + COMPOSER_PAD;
	return Math.max(ADJUSTMENT_MIN_HEIGHT - 20, formMax - overhead);
}

function measureAdjustmentPanelHeight(inner: HTMLElement, form: HTMLFormElement): number {
	// How tall the panel has to be for the inner to sit in it whole: its content,
	// plus the divider gap and the hairline the inner is offset by. Measuring the
	// content alone left that much of the first or last row outside the panel.
	// The header and the scroll area are measured separately so this still holds
	// when the inner is already size-constrained by a capped panel.
	const header = inner.querySelector<HTMLElement>(".element-header");
	const scroll = inner.querySelector<HTMLElement>(".adjustment-scroll");
	const content = header && scroll ? header.offsetHeight + scroll.scrollHeight : inner.scrollHeight;
	const innerStyle = getComputedStyle(inner);
	const inset = (Number.parseFloat(innerStyle.marginTop) || 0)
		+ (Number.parseFloat(innerStyle.marginBottom) || 0)
		+ Math.max(0, inner.offsetHeight - inner.clientHeight);
	return Math.min(Math.max(content + inset, 1), adjustmentPanelHeightCap(form));
}

function panelInlineHeight(panel: HTMLElement): number {
	const inline = panel.style.height;
	if (inline.endsWith("px")) {
		const parsed = Number.parseFloat(inline);
		if (Number.isFinite(parsed)) return parsed;
	}
	return panel.getBoundingClientRect().height;
}

function prefersReducedMotion(): boolean {
	return window.matchMedia("(prefers-reduced-motion: reduce)").matches;
}

function viewportSize(): { width: number; height: number } {
	return {
		width: Math.min(window.innerWidth, document.documentElement.clientWidth || window.innerWidth),
		height: Math.min(window.innerHeight, document.documentElement.clientHeight || window.innerHeight),
	};
}

function composerWidth(adjusting: boolean): number {
	return Math.max(0, Math.min(adjusting ? ADJUSTMENT_WIDTH : COMMENT_WIDTH, viewportSize().width - PROMPT_GUTTER * 2));
}

/**
 * Where the composer card lands for a given height.
 *
 * Callers pass the height the card will *end* at, so which side of the element
 * it opens on is decided once: re-deriving the flip from a mid-animation height
 * is what used to make the card jump to the other side of the element partway
 * through, and what made its contents slide under a fixed edge.
 *
 * The edge the card is glued to does not depend on that height — the top when it
 * opens downwards, the bottom when it opens upwards — so the row of controls
 * keeps its place while the panel grows or shrinks away from it.
 */
function composerPlacement(
	rect: AnnotationRectLike,
	width: number,
	height: number,
): { left: number; top: number; bottom: number; above: boolean } {
	const viewport = viewportSize();
	const position = promptPositionForRect(rect, {
		width: viewport.width,
		height: viewport.height,
		promptWidth: width,
		promptHeight: height,
		gutter: PROMPT_GUTTER,
		gap: PROMPT_GAP,
	});
	return {
		left: position.left,
		top: position.top,
		bottom: position.top + height,
		above: position.top < rect.top,
	};
}

function bindSpacingSections(form: HTMLFormElement, panel: HTMLElement, resize: () => void): void {
	form.querySelectorAll<HTMLDetailsElement>(".panel-section").forEach((details) => {
		const summary = details.querySelector("summary");
		const body = details.querySelector<HTMLElement>(".section-body");
		if (!summary || !body) return;
		summary.addEventListener("click", (event) => {
			event.preventDefault();
			void toggleSpacingSection(details, body, panel, form, resize);
		});
	});
}

/**
 * Expand or collapse a spacing group.
 *
 * The panel's height travels with the group in the same motion. Growing the
 * panel *after* the group has closed leaves the rows sliding down inside a card
 * that has not shrunk yet — an empty band at the far edge, then a snap — which
 * is the accordion version of the jerk the composer itself used to have.
 */
async function toggleSpacingSection(
	details: HTMLDetailsElement,
	body: HTMLElement,
	panel: HTMLElement,
	form: HTMLFormElement,
	resize: () => void,
): Promise<void> {
	const inner = panel.querySelector<HTMLElement>(".adjustment-panel-inner");
	if (!inner) return;
	spacingSectionAnimations.get(body)?.stop();
	spacingSectionAnimations.delete(body);
	const opening = !details.open;
	const startBodyHeight = body.getBoundingClientRect().height;
	const startPanelHeight = panelInlineHeight(panel);
	body.style.overflow = "hidden";
	// Measure the panel with the group already in its new size, then animate the
	// group to it. Both ends of the panel's travel are therefore known exactly and
	// the card lands on the measured height instead of being reconciled afterwards
	// (a reconcile is what made the card look like it changed size on its own).
	let endBodyHeight: number;
	let endPanelHeight: number;
	if (opening) {
		// Let the group take its natural height again: a previous collapse left it
		// pinned at 0 and the measurement below has to see the real content.
		body.style.height = "";
		body.style.overflow = "";
		details.open = true;
		endBodyHeight = body.getBoundingClientRect().height;
		endPanelHeight = measureAdjustmentPanelHeight(inner, form);
		body.style.overflow = "hidden";
		body.style.height = "0px";
	} else {
		endBodyHeight = 0;
		body.style.height = "0px";
		endPanelHeight = measureAdjustmentPanelHeight(inner, form);
		body.style.height = `${startBodyHeight}px`;
	}
	// Size the inner for the panel's *current* height first: a capped panel needs
	// its scroll range to exist while the group animates, otherwise the rows the
	// group is adding sit below the clip until the motion is over.
	applyAdjustmentPanelLayout(inner, form, startPanelHeight);
	const progressOf = (bodyHeight: number): number => {
		const span = endBodyHeight - startBodyHeight;
		return Math.abs(span) < 0.5 ? 1 : Math.min(1, Math.max(0, (bodyHeight - startBodyHeight) / span));
	};
	const panelHeightFor = (bodyHeight: number): number =>
		startPanelHeight + (endPanelHeight - startPanelHeight) * progressOf(bodyHeight);
	if (prefersReducedMotion()) {
		body.style.height = opening ? "auto" : "0px";
		body.style.overflow = opening ? "" : "hidden";
		if (!opening) details.open = false;
		panel.style.height = `${endPanelHeight}px`;
		applyAdjustmentPanelLayout(inner, form, endPanelHeight);
		resize();
		return;
	}
	const controls = animate(startBodyHeight, endBodyHeight, {
		duration: SPACING_SECTION_MS,
		ease: COMPOSER_TRANSITION_EASE,
		type: "tween",
		onUpdate: (bodyHeight: number) => {
			body.style.height = `${bodyHeight}px`;
			panel.style.height = `${panelHeightFor(bodyHeight)}px`;
			// A capped panel cannot grow with the group, so keep the rows nearest
			// the input row in view instead of pushing them out of the panel.
			const scroll = form.querySelector<HTMLElement>(".adjustment-scroll");
			if (scroll && form.classList.contains("composer--above")
				&& panelHeightFor(bodyHeight) >= adjustmentPanelHeightCap(form) - 1) {
				scroll.scrollTop = scroll.scrollHeight;
			}
			resize();
		},
	});
	spacingSectionAnimations.set(body, controls);
	await controls;
	spacingSectionAnimations.delete(body);
	body.style.height = opening ? "auto" : "0px";
	body.style.overflow = opening ? "" : "hidden";
	if (!opening) details.open = false;
	panel.style.height = `${endPanelHeight}px`;
	applyAdjustmentPanelLayout(inner, form, endPanelHeight);
	resize();
}

/**
 * The panel scrolls without a scrollbar, so the edge the content runs past gets
 * a fade. Which edge depends on which way the card grows: a card that opens
 * downwards overflows below, one that opens upwards overflows above it.
 */
function syncPanelScrollCue(panel: HTMLElement): void {
	const scroll = panel.querySelector<HTMLElement>(".adjustment-scroll");
	if (!scroll) return;
	const overflowing = scroll.scrollHeight - scroll.clientHeight > 1;
	const above = Boolean(panel.closest(".composer")?.classList.contains("composer--above"));
	const cue = !overflowing
		? ""
		: above
			? scroll.scrollTop > 1 ? "top" : ""
			: scroll.scrollTop < scroll.scrollHeight - scroll.clientHeight - 1 ? "bottom" : "";
	if (cue) {
		if (panel.dataset.cue !== cue) panel.dataset.cue = cue;
		return;
	}
	if (panel.dataset.cue) delete panel.dataset.cue;
}

function applyAdjustmentPanelLayout(inner: HTMLElement, form: HTMLFormElement, panelHeight: number): void {
	const capped = panelHeight >= adjustmentPanelHeightCap(form) - 1;
	// A plain 100% ignores the divider gap the inner is offset by, which pushes its
	// last row past the panel's edge — the scroll end then clips it instead of
	// showing it with the panel's own padding.
	inner.style.height = capped ? "calc(100% - var(--panel-chrome))" : "";
	const panel = inner.parentElement;
	if (panel) syncPanelScrollCue(panel);
	if (!capped) return;
	// Opened upwards, the rows nearest the input row are the ones the grow-in
	// animation leaves on screen, so a scrollable panel keeps that view rather
	// than jumping to the top of the list the moment it fills.
	const scroll = form.querySelector<HTMLElement>(".adjustment-scroll");
	if (scroll && form.classList.contains("composer--above")) scroll.scrollTop = scroll.scrollHeight;
}

async function animateAdjustmentPanel(
	panel: HTMLElement,
	form: HTMLFormElement,
	direction: "open" | "close",
	onComplete?: () => void,
	transition: { startRect?: AnnotationRectLike; instant?: boolean } = {},
): Promise<void> {
	adjustmentPanelAnimation?.stop();
	adjustmentPanelAnimation = null;
	const inner = panel.querySelector<HTMLElement>(".adjustment-panel-inner");
	if (!inner) return;
	const open = direction === "open";
	const targetPanelHeight = measureAdjustmentPanelHeight(inner, form);
	const startPanelHeight = open ? 0 : panelInlineHeight(panel);
	const endPanelHeight = open ? targetPanelHeight : 0;
	const formRect = form.getBoundingClientRect();
	// Where the card sat before the panel was inserted, so opening continues from
	// the row the user is already looking at instead of re-deriving it.
	const startRect = transition.startRect ?? formRect;
	const chromeHeight = Math.max(0, formRect.height - panel.getBoundingClientRect().height);
	const width = formRect.width > 0 ? formRect.width : composerWidth(true);
	const endPlacement = composerAnchorRect
		? composerPlacement(composerAnchorRect, width, chromeHeight + endPanelHeight)
		: null;
	// Collapsing stays on the side the card is already on; opening picks the side
	// it will end on. Either way the decision is frozen for the whole transition.
	const above = endPlacement ? (open ? endPlacement.above : form.classList.contains("composer--above")) : false;
	const startEdge = above ? startRect.bottom : startRect.top;
	const endEdge = endPlacement ? (above ? endPlacement.bottom : endPlacement.top) : startEdge;
	const startTop = above ? startEdge - (chromeHeight + startPanelHeight) : startEdge;
	const endTop = above ? endEdge - (chromeHeight + endPanelHeight) : endEdge;
	const applyTop = (panelHeight: number): void => {
		const span = endPanelHeight - startPanelHeight;
		const progress = Math.abs(span) < 0.5 ? 1 : Math.min(1, Math.max(0, (panelHeight - startPanelHeight) / span));
		form.style.top = `${startTop + (endTop - startTop) * progress}px`;
	};
	const settle = (): void => {
		panel.style.height = `${endPanelHeight}px`;
		form.style.top = `${endTop}px`;
		if (open) applyAdjustmentPanelLayout(inner, form, endPanelHeight);
		onComplete?.();
	};
	panel.style.overflow = "hidden";
	form.classList.toggle("composer--above", above);
	// The card only ever moves when it has to change sides: the row stays put and
	// the far edge sweeps open, so nothing shifts under the element mid-flight.
	if (transition.instant || prefersReducedMotion() || Math.abs(endPanelHeight - startPanelHeight) < 1) {
		composerPanelAnimating = false;
		settle();
		return;
	}
	if (open) panel.style.height = "0px";
	applyTop(startPanelHeight);
	composerPanelAnimating = true;
	// The panel's height and the card's top come from the same frame value, so the
	// anchored edge cannot shear: driving the top from the element's own height
	// (which motion writes after it reports the frame) lagged it by a frame.
	const controls = animate(startPanelHeight, endPanelHeight, {
		duration: open ? COMPOSER_OPEN_MS : COMPOSER_CLOSE_MS,
		ease: COMPOSER_TRANSITION_EASE,
		type: "tween",
		onUpdate: (panelHeight: number) => {
			panel.style.height = `${panelHeight}px`;
			applyTop(panelHeight);
			onComplete?.();
		},
	});
	adjustmentPanelAnimation = controls;
	try {
		await controls;
	} finally {
		composerPanelAnimating = false;
		adjustmentPanelAnimation = null;
	}
	settle();
}

/**
 * One field: a filled box holding a compact label, the control and — trailing —
 * its unit or colour swatch. The box, not the input, carries the surface, so all
 * three line up on the same grid.
 *
 * A modified field offers its reset in the label's slot: the label fades out and
 * the reset fades in, in place, so nothing else in the row moves.
 */
function adjustmentField(
	element: Element,
	definition: AdjustmentDefinition,
	adjustments: BrowserStyleAdjustment[],
	options: { wide?: boolean } = {},
): string {
	const property = definition.property;
	const adjustment = adjustments.find((item) => item.property === property);
	const value = adjustment?.value ?? baselineValue(element, property);
	const escapedName = escapeAttribute(definition.label);
	let control: string;
	if (property === "textContent") {
		control = `<textarea class="property-textarea" data-property="textContent" rows="2" spellcheck="true" aria-label="${escapedName}">${escapeHtml(value)}</textarea>`;
	} else if (COLOR_PROPERTIES.has(property)) {
		const color = colorInputValue(value);
		control = `<span class="color-value">${color.toUpperCase()}</span><input class="color-picker" type="color" data-property="${property}" value="${color}" aria-label="${escapedName}">`;
	} else if (definition.options) {
		const choices = Array.from(new Set([value, ...definition.options])).filter(Boolean);
		control = `<select data-property="${property}" aria-label="${escapedName}">${choices.map((option) => `<option value="${escapeAttribute(option)}" ${option === value ? "selected" : ""}>${escapeHtml(option)}</option>`).join("")}</select>`;
	} else if (PIXEL_PROPERTIES.has(property)) {
		// The unit lives in the field's label context rather than in the box: a
		// trailing "px" costs the value column ~20px, which long measurements need.
		control = `<input data-property="${property}" data-unit="px" placeholder="px" value="${escapeAttribute(pixelControlValue(value))}" inputmode="decimal" spellcheck="false" aria-label="${escapedName}">`;
	} else {
		control = `<input data-property="${property}" value="${escapeAttribute(value)}" spellcheck="false" aria-label="${escapedName}">`;
	}
	const classes = [
		"field",
		COLOR_PROPERTIES.has(property) ? "field--color" : "",
		property === "textContent" ? "field--content" : "",
		options.wide ? "field--wide" : "",
	].filter(Boolean).join(" ");
	// Only a changed property can be reset, so the rest stay out of the tab order.
	const reset = `<button type="button" class="field-reset${adjustment ? " field-reset--on" : ""}" data-reset="${property}" title="Reset ${escapedName}" aria-label="Reset ${escapedName}"${adjustment ? "" : " tabindex=\"-1\" aria-hidden=\"true\""}>${icon("reset")}</button>`;
	return `<div class="${classes}" data-field="${property}">
		<span class="field-leading"><span class="field-label" title="${escapedName}">${escapeHtml(definition.short)}</span>${reset}</span>
		${control}
	</div>`;
}

function adjustmentInputValue(input: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement, property: BrowserAdjustmentProperty): string {
	const value = input.value.trim();
	if (!PIXEL_PROPERTIES.has(property) || !value) return value;
	return /^-?(?:\d+\.?\d*|\.\d+)$/.test(value) ? `${String(Number(value))}px` : value;
}

function pixelControlValue(value: string): string {
	const match = value.trim().match(/^(-?(?:\d+\.?\d*|\.\d+))px$/i);
	return match?.[1] ?? value;
}

function colorInputValue(value: string): string {
	const normalized = value.trim().toLowerCase();
	if (/^#[\da-f]{6}$/.test(normalized)) return normalized;
	if (/^#[\da-f]{3}$/.test(normalized))
		return `#${normalized.slice(1).split("").map((part) => `${part}${part}`).join("")}`;
	const rgb = normalized.match(/^rgba?\(\s*([\d.]+)(?:\s*,\s*|\s+)([\d.]+)(?:\s*,\s*|\s+)([\d.]+)/);
	if (!rgb) return "#000000";
	return `#${rgb.slice(1, 4).map((part) => Math.max(0, Math.min(255, Math.round(Number(part)))).toString(16).padStart(2, "0")).join("")}`;
}

const NON_EDITABLE_TEXT_TAGS = new Set(["IMG", "SVG", "PICTURE", "VIDEO", "CANVAS", "INPUT", "TEXTAREA", "SELECT", "OPTION"]);

function editableTextNode(element: Element): Text | null {
	if (NON_EDITABLE_TEXT_TAGS.has(element.tagName)) return null;
	const known = editableTextTargets.get(element);
	if (known?.isConnected) return known;
	const direct = Array.from(element.childNodes).filter(
		(node): node is Text => node.nodeType === Node.TEXT_NODE && Boolean(node.nodeValue?.trim()),
	);
	if (direct.length === 1) {
		editableTextTargets.set(element, direct[0]);
		return direct[0];
	}
	if (direct.length > 1) return null;
	const elementText = compactVisibleText(element.textContent ?? "");
	if (!elementText) return null;
	const candidates = Array.from(element.querySelectorAll("*")).flatMap((candidate) => {
		if (NON_EDITABLE_TEXT_TAGS.has(candidate.tagName)) return [];
		const nodes = Array.from(candidate.childNodes).filter(
			(node): node is Text => node.nodeType === Node.TEXT_NODE && Boolean(node.nodeValue?.trim()),
		);
		return nodes.length === 1 && compactVisibleText(candidate.textContent ?? "") === elementText ? nodes : [];
	});
	if (candidates.length !== 1) return null;
	editableTextTargets.set(element, candidates[0]);
	return candidates[0];
}

function compactVisibleText(value: string): string {
	return value.replace(/\s+/g, " ").trim();
}

function adjustmentStyleTarget(element: Element, property: BrowserAdjustmentProperty): Element {
	if (!TEXT_STYLE_PROPERTIES.has(property)) return element;
	return editableTextNode(element)?.parentElement ?? element;
}

function updateAdjustment(element: Element, property: BrowserAdjustmentProperty, value: string, link = true): void {
	const draft = session.draft;
	if (!draft) return;
	if (property !== "textContent" && value && globalThis.CSS?.supports && !globalThis.CSS.supports(kebabCase(property), value)) return;
	const previousValue = baselineValue(element, property);
	const index = draft.adjustments.findIndex((item) => item.property === property);
	if ((property !== "textContent" && !value) || value === previousValue) {
		if (index >= 0) draft.adjustments.splice(index, 1);
		setElementValue(element, property, previousValue);
	} else if (index >= 0) draft.adjustments[index] = { property, previousValue, value };
	else draft.adjustments.push({ property, previousValue, value });
	if ((property === "textContent" || value) && value !== previousValue) setElementValue(element, property, value);
	if (link) applyLocks(element, property, value);
	emitState();
	renderMarkers();
}

function resetAdjustment(element: Element, property: BrowserAdjustmentProperty): void {
	const draft = session.draft;
	if (!draft) return;
	const existing = draft.adjustments.find((item) => item.property === property);
	if (existing) setElementValue(element, property, existing.previousValue);
	draft.adjustments = draft.adjustments.filter((item) => item.property !== property);
	// Reflect the revert in the field itself. Rebuilding the composer would also
	// throw away the panel's scroll position and which sections were open.
	const control = shadow?.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[data-property="${property}"]`);
	if (control) setControlValue(control, property, baselineValue(element, property));
	syncAdjustmentFieldState(property);
	emitState();
	renderMarkers();
}

/** Write a raw CSS value back into its control, in that field's own notation. */
function setControlValue(
	control: HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement,
	property: BrowserAdjustmentProperty,
	value: string,
): void {
	if (property === "textContent") {
		control.value = value;
		return;
	}
	if (COLOR_PROPERTIES.has(property)) {
		const color = colorInputValue(value);
		control.value = color;
		const valueLabel = control.closest(".field")?.querySelector<HTMLElement>(".color-value");
		if (valueLabel) valueLabel.textContent = color.toUpperCase();
		return;
	}
	control.value = PIXEL_PROPERTIES.has(property) ? pixelControlValue(value) : value;
}

/** A field offers its reset only while the draft carries an adjustment for it. */
function syncAdjustmentFieldState(property: BrowserAdjustmentProperty): void {
	const reset = shadow?.querySelector<HTMLButtonElement>(`[data-field="${property}"] .field-reset`);
	if (!reset) return;
	const changed = Boolean(session.draft?.adjustments.some((item) => item.property === property));
	reset.classList.toggle("field-reset--on", changed);
	if (changed) {
		reset.removeAttribute("tabindex");
		reset.removeAttribute("aria-hidden");
		return;
	}
	reset.setAttribute("tabindex", "-1");
	reset.setAttribute("aria-hidden", "true");
}

function applyLocks(element: Element, property: BrowserAdjustmentProperty, value: string): void {
	if (!value) return;
	if (adjustmentLocks.marginHorizontal) {
		if (property === "marginLeft") updateLinkedAdjustment(element, "marginRight", value);
		if (property === "marginRight") updateLinkedAdjustment(element, "marginLeft", value);
	}
	if (adjustmentLocks.paddingHorizontal) {
		if (property === "paddingLeft") updateLinkedAdjustment(element, "paddingRight", value);
		if (property === "paddingRight") updateLinkedAdjustment(element, "paddingLeft", value);
	}
	if (adjustmentLocks.marginVertical) {
		if (property === "marginTop") updateLinkedAdjustment(element, "marginBottom", value);
		if (property === "marginBottom") updateLinkedAdjustment(element, "marginTop", value);
	}
	if (adjustmentLocks.paddingVertical) {
		if (property === "paddingTop") updateLinkedAdjustment(element, "paddingBottom", value);
		if (property === "paddingBottom") updateLinkedAdjustment(element, "paddingTop", value);
	}
	if (adjustmentLocks.ratio && adjustmentLocks.ratioValue > 0 && (property === "width" || property === "height")) {
		const dimension = parseCssDimension(value);
		if (!dimension) return;
		const linkedProperty = property === "width" ? "height" : "width";
		const linkedAmount = property === "width"
			? dimension.amount / adjustmentLocks.ratioValue
			: dimension.amount * adjustmentLocks.ratioValue;
		updateLinkedAdjustment(element, linkedProperty, `${formatCssNumber(linkedAmount)}${dimension.unit}`);
	}
}

function synchronizeLockedValues(element: Element, lock: AdjustmentLockName): void {
	if (lock === "ratio") {
		const rect = element.getBoundingClientRect();
		adjustmentLocks.ratioValue = rect.width && rect.height ? rect.width / rect.height : 0;
	} else if (lock === "marginHorizontal") {
		const style = getComputedStyle(element);
		updateLinkedAdjustment(element, "marginRight", style.marginLeft);
	} else if (lock === "paddingHorizontal") {
		const style = getComputedStyle(element);
		updateLinkedAdjustment(element, "paddingRight", style.paddingLeft);
	} else if (lock === "marginVertical") {
		const style = getComputedStyle(element);
		updateLinkedAdjustment(element, "marginBottom", style.marginTop);
	} else if (lock === "paddingVertical") {
		const style = getComputedStyle(element);
		updateLinkedAdjustment(element, "paddingBottom", style.paddingTop);
	}
}

function updateLinkedAdjustment(element: Element, property: BrowserAdjustmentProperty, value: string): void {
	updateAdjustment(element, property, value, false);
	const control = shadow?.querySelector<HTMLInputElement | HTMLSelectElement | HTMLTextAreaElement>(`[data-property="${property}"]`);
	if (control) setControlValue(control, property, value);
	syncAdjustmentFieldState(property);
}

function parseCssDimension(value: string): { amount: number; unit: string } | null {
	const match = value.trim().match(/^(-?(?:\d+\.?\d*|\.\d+))\s*(px|rem|em|%|vw|vh|vmin|vmax|ch|ex|cm|mm|in|pt|pc)?$/i);
	if (!match) return null;
	const amount = Number(match[1]);
	return Number.isFinite(amount) ? { amount, unit: match[2] ?? "px" } : null;
}

function formatCssNumber(value: number): string {
	return String(Math.round(value * 100) / 100);
}

function baselineValue(element: Element, property: BrowserAdjustmentProperty): string {
	let values = adjustmentBaseline.get(element);
	if (!values) { values = new Map(); adjustmentBaseline.set(element, values); }
	const known = values.get(property);
	if (known !== undefined) return known;
	const styleTarget = adjustmentStyleTarget(element, property);
	const rawValue = property === "textContent"
		? editableTextNode(element)?.nodeValue ?? ""
		: (getComputedStyle(styleTarget) as unknown as Record<string, string>)[property] ?? "";
	let value = rawValue;
	if (property === "alignItems" || property === "justifyContent") value = rawValue.replace(/^flex-/, "");
	if (property === "alignItems" && value === "normal") value = "stretch";
	if (property === "justifyContent" && value === "normal") value = "start";
	values.set(property, value);
	return value;
}

function setElementValue(element: Element, property: BrowserAdjustmentProperty, value: string): void {
	if (property === "textContent") {
		const textNode = editableTextNode(element);
		if (textNode) textNode.nodeValue = value;
		return;
	}
	const styleTarget = adjustmentStyleTarget(element, property) as HTMLElement;
	styleTarget.style.setProperty(kebabCase(property), value, "important");
}

function applyAllAdjustments(): void {
	if (showingOriginal) return;
	for (const annotation of session.annotations) {
		const element = resolveTarget(annotation.target);
		if (!element) continue;
		for (const adjustment of annotation.adjustments) {
			baselineValue(element, adjustment.property);
			setElementValue(element, adjustment.property, adjustment.value);
		}
	}
}

function restoreAdjustments(element: Element, adjustments: BrowserStyleAdjustment[]): void {
	for (const adjustment of adjustments) setElementValue(element, adjustment.property, adjustment.previousValue);
}

function setOriginalPreview(next: boolean): void {
	if (showingOriginal === next) return;
	showingOriginal = next;
	if (next) {
		for (const annotation of session.annotations) {
			const element = resolveTarget(annotation.target);
			if (element) restoreAdjustments(element, annotation.adjustments);
		}
		if (selectedElement && session.draft) restoreAdjustments(selectedElement, session.draft.adjustments);
		host?.setAttribute("data-original", "true");
		return;
	}
	host?.removeAttribute("data-original");
	applyAllAdjustments();
	if (selectedElement && session.draft)
		for (const adjustment of session.draft.adjustments)
			setElementValue(selectedElement, adjustment.property, adjustment.value);
}

function resolveTarget(target: BrowserAnnotationTarget): Element | null {
	const context = target.context;
	const exact = uniqueQuery(context.selector);
	if (exact) return exact;
	if (context.id) {
		const byId = document.getElementById(context.id);
		if (byId?.tagName.toLowerCase() === context.tag) return byId;
	}
	if (context.testId) {
		const byTestId = uniqueQuery(`[data-testid="${cssEscape(context.testId)}"]`);
		if (byTestId?.tagName.toLowerCase() === context.tag) return byTestId;
	}
	const candidates = Array.from(document.querySelectorAll(context.tag)).filter((candidate) => {
		if (context.role && candidate.getAttribute("role") !== context.role) return false;
		if (context.ariaLabel && createBrowserAnnotationContext(candidate).ariaLabel !== context.ariaLabel) return false;
		if (context.visibleText && (candidate.textContent ?? "").replace(/\s+/g, " ").trim() !== context.visibleText) return false;
		return true;
	});
	return candidates.length === 1 ? candidates[0] : null;
}

function uniqueQuery(selector: string): Element | null {
	try { const matches = document.querySelectorAll(selector); return matches.length === 1 ? matches[0] : null; } catch { return null; }
}

function refreshPositions(): void {
	if (!enabled) return;
	renderHover();
	renderMarkers();
	if (!session.draft) return;
	const form = shadow?.querySelector<HTMLFormElement>(".composer");
	const textarea = form?.querySelector<HTMLTextAreaElement>(".composer-note");
	const target = selectedElement ?? resolveTarget(session.draft.target);
	if (!composerAnchorRect && target) composerAnchorRect = copyRect(target.getBoundingClientRect());
	if (form && textarea && composerAnchorRect) resizeAndPositionComposer(form, textarea, composerAnchorRect);
}

function copyRect(rect: AnnotationRectLike): AnnotationRectLike {
	return { left: rect.left, top: rect.top, bottom: rect.bottom };
}

function positionBox(box: HTMLElement, rect: DOMRect, outset = 0): void {
	box.style.left = `${Math.max(0, rect.left - outset)}px`;
	box.style.top = `${Math.max(0, rect.top - outset)}px`;
	box.style.width = `${Math.max(0, rect.width + outset * 2)}px`;
	box.style.height = `${Math.max(0, rect.height + outset * 2)}px`;
}

function resizeAndPositionComposer(form: HTMLFormElement, textarea: HTMLTextAreaElement, rect: AnnotationRectLike): void {
	const viewport = viewportSize();
	const adjusting = form.classList.contains("composer--adjustment");
	const width = composerWidth(adjusting);
	form.style.width = `${width}px`;
	if (adjusting) {
		form.style.maxHeight = `${composerMaxHeight()}px`;
		textarea.style.height = "0px";
		textarea.style.height = `${Math.min(72, Math.max(COMMENT_TEXTAREA_MIN_HEIGHT, textarea.scrollHeight))}px`;
		textarea.style.overflowY = "hidden";
	} else {
		form.style.maxHeight = "none";
		textarea.style.height = "0px";
		const spaceAbove = rect.top - PROMPT_GAP - PROMPT_GUTTER;
		const spaceBelow = viewport.height - PROMPT_GUTTER - rect.bottom - PROMPT_GAP;
		const availableHeight = Math.max(spaceAbove, spaceBelow);
		const maxComposerHeight = Math.max(44, Math.min(COMMENT_MAX_HEIGHT, availableHeight));
		const maxTextareaHeight = Math.max(COMMENT_TEXTAREA_MIN_HEIGHT, maxComposerHeight - COMMENT_CHROME_VERTICAL);
		const naturalHeight = Math.max(COMMENT_TEXTAREA_MIN_HEIGHT, textarea.scrollHeight);
		textarea.style.height = `${Math.min(maxTextareaHeight, naturalHeight)}px`;
		textarea.style.overflowY = naturalHeight > maxTextareaHeight ? "auto" : "hidden";
	}
	// The card's own height decides where it lands, so the placement matches what
	// the panel animation settled on instead of drifting from a rounded-up guess.
	const measured = form.getBoundingClientRect().height;
	const height = Math.min(viewport.height - PROMPT_GUTTER * 2, measured > 1 ? measured : 44);
	const placement = composerPlacement(rect, width, height);
	if (!composerPanelAnimating) {
		// Which side the card opens on is decided when it grows, not from every
		// intermediate height — otherwise collapsing a section could hop the card
		// to the other side of the element. A closed composer decides for itself.
		if (!adjusting) form.classList.toggle("composer--above", placement.above);
		const above = form.classList.contains("composer--above");
		const top = above
			? Math.max(PROMPT_GUTTER, rect.top - PROMPT_GAP - height)
			: rect.bottom + PROMPT_GAP;
		form.style.top = `${top}px`;
	}
	form.style.left = `${placement.left}px`;
}

function decodeBase64Font(dataUri: string): ArrayBuffer {
	const binary = atob(dataUri.slice(dataUri.indexOf(",") + 1));
	const bytes = new Uint8Array(binary.length);
	for (let index = 0; index < binary.length; index += 1) bytes[index] = binary.charCodeAt(index);
	return bytes.buffer;
}

function registerFonts(root: ShadowRoot): void {
	if (typeof FontFace === "undefined") return;
	const set = (root as ShadowRoot & { fonts?: FontFaceSet }).fonts ?? document.fonts;
	const sans = new FontFace("Geist Variable", decodeBase64Font(geistLatinWoff2), { weight: "100 900" });
	const mono = new FontFace("Geist Mono Variable", decodeBase64Font(geistMonoLatinWoff2), { weight: "100 900" });
	set.add(sans); set.add(mono); void sans.load(); void mono.load();
}

function ensureOverlay(): ShadowRoot {
	if (shadow && host?.isConnected) return shadow;
	host = document.createElement("div");
	host.setAttribute("data-ao-annotation-root", "");
	host.style.cssText = "position:fixed;inset:0;z-index:2147483647;pointer-events:none";
	(document.documentElement ?? document.body).appendChild(host);
	shadow = host.attachShadow({ mode: "open" });
	registerFonts(shadow);
	shadow.innerHTML = `<style>${overlayStyles()}</style><div class="hover" hidden></div><div class="markers"></div><div class="composer-mount"></div><div class="screenshot-notice chrome" hidden>Screenshot copied to clipboard</div>`;
	return shadow;
}

function overlayStyles(): string {
	const vars = {
		background: theme.background ?? "oklch(0.185 0.006 285.885)",
		foreground: theme.foreground ?? "oklch(0.985 0 0)",
		muted: theme.muted ?? "oklch(0.274 0.006 286.033)",
		mutedForeground: theme.mutedForeground ?? "oklch(0.705 0.015 286.067)",
		border: theme.border ?? "oklch(1 0 0 / 10%)",
		accent: theme.accent ?? "oklch(0.92 0.004 286.32)",
		accentForeground: theme.accentForeground ?? "oklch(0.21 0.006 285.885)",
		destructive: theme.destructive ?? "oklch(0.704 0.191 22.216)",
	};
	// Mirror renderer Button/Input chrome: 8px radius, 28px controls, equal 8px padding,
	// Geist sans everywhere except code-like CSS values (px / hex).
	return `
		:host{
			all:initial;
			--bg:${vars.background};
			--fg:${vars.foreground};
			--muted:${vars.muted};
			--muted-fg:${vars.mutedForeground};
			--border:${vars.border};
			--accent:${vars.accent};
			--accent-fg:${vars.accentForeground};
			--danger:${vars.destructive};
		--radius:${COMPOSER_RADIUS}px;
		--control:${COMPOSER_CONTROL}px;
		--pad:${COMPOSER_PAD}px;
		--gap:${COMPOSER_GAP}px;
		/* The adjustment panel and its rules are offset from their box by the
		   divider gap plus its hairline; sizing maths has to subtract both. */
		--panel-chrome:calc(var(--gap) + 1px);
		font-family:"Geist Variable",system-ui,sans-serif;
		color:var(--fg);
	}
		.hover{
			position:fixed;box-sizing:border-box;border:2px solid #4d8dff;border-radius:var(--radius);
			background:rgba(77,141,255,.10);pointer-events:none;
			transition:left 180ms ease,top 180ms ease,width 180ms ease,height 180ms ease;
		}
		.marker{
			position:fixed;width:20px;height:20px;border:2px solid var(--bg);border-radius:50%;
			background:#74b98a;color:#101512;padding:0;
			font:600 10px/1 "Geist Variable",system-ui,sans-serif;font-variant-numeric:tabular-nums;
			pointer-events:auto;box-shadow:0 2px 8px rgba(0,0,0,.28);cursor:pointer;
		}
		button{
			display:inline-flex;height:var(--control);align-items:center;justify-content:center;
			border:1px solid transparent;border-radius:var(--radius);background:var(--muted);color:var(--fg);
			padding:0 10px;font:400 12px/1 "Geist Variable",system-ui,sans-serif;cursor:pointer;
			transition:background-color 120ms ease,border-color 120ms ease,color 120ms ease,opacity 120ms ease;
		}
		button:hover{background:color-mix(in oklch,var(--muted) 88%,var(--fg))}
		button:disabled{opacity:.5;cursor:default}
		button:focus-visible{outline:none;box-shadow:inset 0 0 0 2px var(--accent)}
		.primary{background:var(--accent);color:var(--accent-fg)}
		.primary:hover{background:color-mix(in oklch,var(--accent) 88%,var(--fg))}
		.danger{color:var(--danger)}
		button svg{width:14px;height:14px;fill:none;stroke:currentColor;stroke-width:1.8;stroke-linecap:round;stroke-linejoin:round}
		.composer{
			position:fixed;box-sizing:border-box;border:1px solid var(--border);
			border-radius:${COMPOSER_OUTER_RADIUS}px;
			background:var(--bg);color:var(--fg);
			box-shadow:0 8px 28px rgba(0,0,0,.32);pointer-events:auto;font-size:12px;overflow:hidden;
			container-type:inline-size;
		}
		/* Equal insets: the trailing control sits as close to the edge as the
		   palette control does at the leading edge. */
		.composer--comment{padding:var(--pad) 10px}
		.composer-input-row{display:flex;min-width:0;align-items:flex-start;gap:var(--gap)}
		.adjust-button{
			transform-origin:center;
			transition:background-color 120ms ease,color 120ms ease,opacity 120ms ease,transform 120ms ease;
		}
		.adjust-button:not(:disabled):active{transform:scale(0.98)}
		.adjust-button{
			width:var(--control);height:var(--control);flex:0 0 var(--control);align-self:flex-start;
			border:0;border-radius:var(--radius);background:transparent;color:var(--muted-fg);padding:0;
		}
		.adjust-button:hover,.adjust-button--active{background:var(--muted);color:var(--fg)}
		.adjust-button svg{width:16px;height:16px}
		/* Trailing twin of the palette control: the same 28px ghost box and hover,
		   because the two are the composer's only two controls. */
		.add-button{
			width:var(--control);height:var(--control);flex:0 0 var(--control);align-self:flex-start;
			border:0;border-radius:var(--radius);background:transparent;color:var(--muted-fg);padding:0;
			transform-origin:center;
			transition:background-color 120ms ease,color 120ms ease,opacity 120ms ease,transform 120ms ease;
		}
		.add-button:not(:disabled):hover{background:var(--muted);color:var(--fg)}
		.add-button:not(:disabled):active{transform:scale(0.98)}
		.add-button:disabled{background:transparent}
		.add-button svg{width:16px;height:16px}
		.composer-note{
			display:block;box-sizing:border-box;min-width:0;flex:1;resize:none;
			border:0;background:transparent;color:var(--fg);caret-color:var(--fg);
			padding:0;margin:0;font:400 13px/20px "Geist Variable",system-ui,sans-serif;outline:none;
			overflow-y:hidden;scrollbar-width:none;
		}
		/* Labels are chrome, not content: dragging across the panel must not start
		   a text selection. Field values and the note stay selectable. */
		.element-header,.field-label,.panel-section summary{
			user-select:none;-webkit-user-select:none;
		}
		.composer--comment .composer-note{height:20px;min-height:20px;padding-top:4px}
		.composer-note::-webkit-scrollbar,.property-textarea::-webkit-scrollbar,.adjustment-scroll::-webkit-scrollbar{display:none}
		.composer-note::placeholder{color:var(--muted-fg)}
		textarea:focus,input:focus,select:focus{outline:none}
		/* Same box as the comment composer: the card keeps its padding on both
		   sides, so switching modes never resizes it out from under the row. */
		.composer--adjustment{display:flex;flex-direction:column;padding:var(--pad) 0}
		.composer--adjustment .composer-input-row{flex:0 0 auto;align-items:flex-start;padding:0 10px}
		.composer--adjustment .composer-note{max-height:52px;padding:4px 0 0}
		.adjustment-panel{position:relative;overflow:hidden;height:0;flex:0 0 auto}
		.adjustment-panel-inner{display:flex;flex-direction:column;min-height:0;border-top:1px solid var(--border);margin-top:var(--gap)}
		/* Fades the edge the content runs past, where the scrollbar would be. */
		.adjustment-panel::before,.adjustment-panel::after{
			content:"";position:absolute;inset-inline:0;height:16px;pointer-events:none;
			opacity:0;transition:opacity 150ms ease-out;
		}
		.adjustment-panel::before{top:0;background:linear-gradient(to bottom,var(--bg),transparent)}
		.adjustment-panel::after{bottom:0;background:linear-gradient(to top,var(--bg),transparent)}
		.adjustment-panel[data-cue~="top"]::before{opacity:1}
		.adjustment-panel[data-cue~="bottom"]::after{opacity:1}
		/* Opened upwards: the input row stays glued to the element's top edge and
		   the panel grows away from it, so the row never travels across the page. */
		.composer--adjustment.composer--above{flex-direction:column-reverse}
		.composer--adjustment.composer--above .adjustment-panel{display:flex;flex-direction:column;justify-content:flex-end}
		.composer--adjustment.composer--above .adjustment-panel-inner{
			flex:0 0 auto;border-top:0;border-bottom:1px solid var(--border);
			margin-top:0;margin-bottom:var(--gap);
		}
		.adjustment-scroll{min-height:0;flex:1 1 auto;overflow-y:auto;scrollbar-width:none}
		.element-header{
			display:flex;flex:0 0 auto;align-items:center;justify-content:space-between;
			border-bottom:1px solid var(--border);
			padding:var(--pad);font-size:12px;
		}
		.element-header strong{font-weight:600}
		.adjustment-group{
			display:flex;flex-direction:column;gap:var(--gap);
			border-bottom:1px solid var(--border);padding:var(--pad);
		}
		/* One grid for every row: two field boxes and a trailing action cell, so
		   fields, values and link buttons all land on the same edges. */
		.panel-row{
			display:grid;grid-template-columns:minmax(0,1fr) minmax(0,1fr) var(--control);
			align-items:center;gap:var(--gap);min-height:var(--control);
		}
		/* Only a row that actually carries a lock reserves the trailing cell.
		   Without this, every other row left the control column empty and the
		   fields stopped short of the card's right edge. */
		.panel-row:not(:has(.link-button)){grid-template-columns:minmax(0,1fr) minmax(0,1fr)}
		.field{
			display:grid;grid-template-columns:40px minmax(0,1fr) auto;
			align-items:center;gap:4px;box-sizing:border-box;
			min-width:0;height:var(--control);padding-inline:10px;
			border-radius:var(--radius);background:color-mix(in oklch,var(--muted) 70%,transparent);
			transition:background-color 120ms ease,box-shadow 120ms ease;
		}
		.field--wide{grid-column:span 2}
		.field:hover{background:color-mix(in oklch,var(--muted) 88%,transparent)}
		.field:focus-within{box-shadow:inset 0 0 0 1px var(--accent)}
		.field-leading{position:relative;display:flex;align-items:center;min-width:0;gap:6px}
		.field-label{
			min-width:0;overflow:hidden;color:var(--muted-fg);
			font:500 10px/1 "Geist Variable",system-ui,sans-serif;
			text-overflow:ellipsis;white-space:nowrap;user-select:none;
			transition:opacity 120ms ease;
		}
		.field input,.field select,.property-textarea{
			box-sizing:border-box;min-width:0;width:100%;height:var(--control);
			border:0;border-radius:0;background:transparent;color:var(--fg);
			padding:0;font:400 12px/17px "Geist Variable",system-ui,sans-serif;
		}
		.field input[data-unit]{font:400 12px/17px "Geist Mono Variable",ui-monospace,monospace;font-variant-numeric:tabular-nums}
		.field select{padding-right:2px;font-size:12px}
		.unit{
			color:var(--muted-fg);user-select:none;
			font:400 10px/1 "Geist Mono Variable",ui-monospace,monospace;font-variant-numeric:tabular-nums;
		}
		.field--content{align-items:start;height:auto;padding-block:6px}
		.field--content .field-leading{padding-top:4px}
		.property-textarea{
			height:46px;resize:none;padding:0;scrollbar-width:none;
		}
		.field input.color-picker{
			width:24px;height:24px;border:0;border-radius:5px;
			background:transparent;padding:3px;overflow:hidden;cursor:pointer;
		}
		.color-picker::-webkit-color-swatch-wrapper{padding:0}
		.color-picker::-webkit-color-swatch{border:1px solid var(--border);border-radius:4px}
		.color-value{
			min-width:0;overflow:hidden;color:var(--fg);
			font:400 10px/1 "Geist Mono Variable",ui-monospace,monospace;font-variant-numeric:tabular-nums;
			text-overflow:ellipsis;white-space:nowrap;
		}
		/* A changed field swaps its label for its reset, in place. */
		.field-reset{
			position:absolute;inset-inline-start:0;top:50%;
			display:inline-flex;width:24px;height:24px;align-items:center;justify-content:center;
			border:0;border-radius:6px;background:transparent;color:var(--fg);padding:0;
			opacity:0;pointer-events:none;transform:translateY(-50%) scale(0.25);
			transition:opacity 120ms ease,transform 120ms ease,background-color 120ms ease;
		}
		.field-reset svg{width:12px;height:12px}
		.field-reset--on{pointer-events:auto}
		.field:has(.field-reset--on) .field-label{color:var(--fg)}
		/* Swap only when the reset is the thing under the pointer or the keyboard
		   focus, so the property's name stays on screen while the value is typed. */
		.field:has(.field-reset--on):hover .field-label,
		.field:has(.field-reset--on:focus-visible) .field-label{opacity:0}
		.field:has(.field-reset--on):hover .field-reset,
		.field-reset--on:focus-visible{opacity:1;transform:translateY(-50%) scale(1)}
		.field-reset--on:active{opacity:1;transform:translateY(-50%) scale(0.96)}
		.field-reset--on:hover{background:var(--muted)}
		/* The lock is the same ghost control as the composer's icon buttons: no
		   border, muted glyph, muted fill on hover or while locked, and the same
		   press scale and transition. */
		.link-button{
			width:var(--control);height:var(--control);
			border:0;border-radius:var(--radius);
			background:transparent;color:var(--muted-fg);padding:0;
			transform-origin:center;
			transition:background-color 120ms ease,color 120ms ease,opacity 120ms ease,transform 120ms ease;
		}
		.link-button:not(:disabled):hover,.link-button--active{background:var(--muted);color:var(--fg)}
		.link-button:not(:disabled):active{transform:scale(0.98)}
		.link-button svg{width:16px;height:16px}
		/* Collapsible section: the same pattern for Padding, Margin and Layout. */
		/* Sections take the same inset as the groups, so every row in the panel
		   shares one gutter and the heading sits on its edge. */
		.panel-section{border-bottom:1px solid var(--border);padding:0 var(--pad)}
		.panel-section summary{
			display:flex;align-items:center;justify-content:space-between;gap:var(--gap);
			cursor:pointer;color:var(--muted-fg);font-size:12px;font-weight:500;
			padding:var(--pad) 0;list-style:none;user-select:none;
			transition:color 120ms ease;
		}
		.panel-section summary:hover{color:var(--fg)}
		.panel-section summary::-webkit-details-marker{display:none}
		.panel-section summary::after{
			content:"›";flex:0 0 auto;font-size:16px;line-height:13px;
			transform:rotate(90deg);transition:transform 120ms ease;
		}
		.panel-section[open] summary::after{transform:rotate(-90deg)}
		/* The animated box carries no padding or margin of its own: anything outside
		   the animated height stays put while the group opens, which reads as a
		   snap. The bottom gap lives on the last row instead, so it animates too. */
		.section-body{display:flex;flex-direction:column;gap:var(--gap);overflow:hidden}
		.section-body>.panel-row:last-child{margin-bottom:var(--pad)}
		/* The card fits two fields side by side down to ~300px; anything narrower
		   (a slim browser pane, 200% zoom) stacks the pair so the label, the value
		   and the swatch each keep their room instead of clipping.
		   The query measures the card's *content* box, so it has to clear the 320
		   card minus its 1px borders with room to spare — at 318 the default card
		   matched and the paired layout never applied at all. */
		@container (max-width:296px){
			.panel-row{grid-template-columns:minmax(0,1fr) auto}
			/* Stacked rows keep the field at its own width, lock or no lock. */
			.panel-row:not(:has(.link-button)){grid-template-columns:minmax(0,1fr) auto}
			.field{grid-column:1}
			.field--wide{grid-column:1}
		}
		.screenshot-notice{
			position:fixed;left:50%;top:55px;transform:translateX(-50%);
			border:1px solid var(--border);border-radius:var(--radius);background:var(--bg);color:var(--fg);
			padding:var(--pad) 12px;box-shadow:0 8px 22px rgba(0,0,0,.28);
			font:400 12px/1 "Geist Variable",system-ui,sans-serif;pointer-events:none;
		}
		:host([data-original]) .hover,:host([data-original]) .marker,:host([data-original]) .composer{visibility:hidden}
		@media(prefers-reduced-motion:reduce){*{transition:none!important}}
	`;
}

function cleanupOverlay(): void { host?.remove(); host = null; shadow = null; }
function isOverlayEvent(event: Event): boolean { return Boolean(host && event.composedPath().includes(host)); }
function waitForPaint(): Promise<void> { return new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))); }
function nextAnnotationNumber(): number { return session.annotations.reduce((max, item) => Math.max(max, item.number), 0) + 1; }
function localId(prefix: string): string { nextLocalId += 1; return `${prefix}-${Date.now().toString(36)}-${nextLocalId.toString(36)}`; }
function samePage(left: string, right: string): boolean { try { const a = new URL(left); const b = new URL(right); a.hash = ""; b.hash = ""; return a.href === b.href; } catch { return left.split("#")[0] === right.split("#")[0]; } }
function kebabCase(value: string): string { return value.replace(/[A-Z]/g, (letter) => `-${letter.toLowerCase()}`); }
function cssEscape(value: string): string { return globalThis.CSS?.escape ? globalThis.CSS.escape(value) : value.replace(/[^a-zA-Z0-9_-]/g, "\\$&"); }
function escapeAttribute(value: string): string { return value.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
function escapeHtml(value: string): string { return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;"); }
