export const MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH = 4_096;

const MAX_ANNOTATION_BODY_LENGTH = 1_400;
const MAX_TEXT_FIELD_LENGTH = 700;
const MAX_SELECTOR_DEPTH = 6;

export type BrowserAnnotationSize = { width: number; height: number };
export type BrowserAnnotationRect = BrowserAnnotationSize & { x: number; y: number };

export type BrowserAnnotationComputedStyle = Partial<{
	display: string;
	position: string;
	color: string;
	backgroundColor: string;
	fontFamily: string;
	fontSize: string;
	fontWeight: string;
	opacity: string;
	borderColor: string;
	borderWidth: string;
	borderRadius: string;
	padding: string;
	margin: string;
	width: string;
	height: string;
	flexDirection: string;
	alignItems: string;
	justifyContent: string;
	columnGap: string;
	rowGap: string;
}>;

export type BrowserAnnotationContext = {
	url: string;
	title?: string;
	tag: string;
	id?: string;
	testId?: string;
	role?: string;
	classes: string[];
	selector: string;
	size: BrowserAnnotationSize;
	rect: BrowserAnnotationRect;
	visibleText?: string;
	selectedText?: string;
	ariaLabel?: string;
	computedStyle: BrowserAnnotationComputedStyle;
};

export type BrowserAnnotationTarget = { context: BrowserAnnotationContext };

export type BrowserAdjustmentProperty =
	| "textContent"
	| "color"
	| "fontFamily"
	| "fontSize"
	| "fontWeight"
	| "opacity"
	| "backgroundColor"
	| "borderColor"
	| "borderWidth"
	| "borderRadius"
	| "width"
	| "height"
	| "aspectRatio"
	| "margin"
	| "marginTop"
	| "marginRight"
	| "marginBottom"
	| "marginLeft"
	| "padding"
	| "paddingTop"
	| "paddingRight"
	| "paddingBottom"
	| "paddingLeft"
	| "flexDirection"
	| "alignItems"
	| "justifyContent"
	| "columnGap"
	| "rowGap";

export type BrowserStyleAdjustment = {
	property: BrowserAdjustmentProperty;
	previousValue: string;
	value: string;
};

export type BrowserAnnotationKind = "comment" | "adjustment";

export type BrowserSavedAnnotation = {
	id: string;
	number: number;
	kind: BrowserAnnotationKind;
	body: string;
	target: BrowserAnnotationTarget;
	adjustments: BrowserStyleAdjustment[];
	createdAt: string;
	updatedAt: string;
};

export type BrowserAnnotationDraft = {
	id?: string;
	kind: BrowserAnnotationKind;
	body: string;
	target: BrowserAnnotationTarget;
	adjustments: BrowserStyleAdjustment[];
};

export type BrowserAnnotationScreenshot = {
	id: string;
	mimeType: string;
	data: string;
	createdAt: string;
};

export type BrowserAnnotationSession = {
	version: 1;
	page: { url: string; title?: string };
	annotations: BrowserSavedAnnotation[];
	draft?: BrowserAnnotationDraft;
	screenshots: BrowserAnnotationScreenshot[];
};

export type BrowserAnnotationTheme = Partial<{
	background: string;
	foreground: string;
	muted: string;
	mutedForeground: string;
	border: string;
	accent: string;
	accentForeground: string;
	destructive: string;
}>;

export type BrowserAnnotationModeInput = { viewId: string; enabled: boolean; theme?: BrowserAnnotationTheme };
export type BrowserAnnotationPageMode = {
	enabled: boolean;
	session?: BrowserAnnotationSession;
	theme?: BrowserAnnotationTheme;
};
export type BrowserAnnotationPageSubmitPayload = { session: BrowserAnnotationSession };
export type BrowserAnnotationSnapshot = { mimeType: string; data: string };
export type BrowserAnnotationSubmitPayload = BrowserAnnotationPageSubmitPayload & {
	viewId: string;
	tabId: string;
	pageKey: string;
	sessionToken: string;
	snapshot?: BrowserAnnotationSnapshot;
};
export type BrowserAnnotationStatePayload = {
	viewId: string;
	count: number;
	screenshotCount: number;
	hasDraft: boolean;
};
export type BrowserAnnotationCompleteInput = {
	viewId: string;
	tabId: string;
	pageKey: string;
	sessionToken: string;
	success: boolean;
};
export type BrowserAnnotationDiscardInput = { viewId: string };
export type BrowserAnnotationActionInput = {
	viewId: string;
	action: "capture" | "preview-original" | "restore-preview" | "discard-all" | "submit";
};
export type BrowserAnnotationCancelReason = "escape" | "cancel" | "navigation" | "disabled";
export type BrowserAnnotationPageCancelPayload = { reason: BrowserAnnotationCancelReason };
export type BrowserAnnotationCancelPayload = BrowserAnnotationPageCancelPayload & { viewId: string };

export type ParsedBrowserAnnotationItem = {
	number: number;
	kind: BrowserAnnotationKind;
	target: string;
	comment: string;
	changes: string[];
};

export type ParsedBrowserAnnotationMessage = {
	pageTitle: string;
	pageUrl: string;
	items: ParsedBrowserAnnotationItem[];
	screenshotCount: number;
};

const BROWSER_ANNOTATIONS_BLOCK_PATTERN = /^<browser_annotations>\n([\s\S]*?)\n<\/browser_annotations>$/;

const ADJUSTMENT_LABELS: Record<BrowserAdjustmentProperty, string> = {
	textContent: "Text",
	color: "Text color",
	fontFamily: "Font family",
	fontSize: "Font size",
	fontWeight: "Font weight",
	opacity: "Opacity",
	backgroundColor: "Background",
	borderColor: "Border color",
	borderWidth: "Border width",
	borderRadius: "Border radius",
	width: "Width",
	height: "Height",
	aspectRatio: "Aspect ratio",
	margin: "Margin",
	marginTop: "Top margin",
	marginRight: "Right margin",
	marginBottom: "Bottom margin",
	marginLeft: "Left margin",
	padding: "Padding",
	paddingTop: "Top padding",
	paddingRight: "Right padding",
	paddingBottom: "Bottom padding",
	paddingLeft: "Left padding",
	flexDirection: "Layout direction",
	alignItems: "Alignment",
	justifyContent: "Distribution",
	columnGap: "Horizontal gap",
	rowGap: "Vertical gap",
};

export function createBrowserAnnotationContext(element: Element): BrowserAnnotationContext {
	const doc = element.ownerDocument;
	const view = doc.defaultView;
	const rect = element.getBoundingClientRect();
	const visibleText = elementText(element, MAX_TEXT_FIELD_LENGTH);
	const selectedText = compactText(view?.getSelection?.()?.toString() ?? "", MAX_TEXT_FIELD_LENGTH);
	const style = view?.getComputedStyle ? view.getComputedStyle(element) : null;
	return {
		url: view?.location.href ?? "",
		title: doc.title || undefined,
		tag: element.tagName.toLowerCase(),
		id: element.id || undefined,
		testId: element.getAttribute("data-testid") || undefined,
		role: element.getAttribute("role") || undefined,
		classes: Array.from(element.classList).slice(0, 8),
		selector: selectorFor(element),
		size: { width: Math.round(rect.width), height: Math.round(rect.height) },
		rect: { x: Math.round(rect.x), y: Math.round(rect.y), width: Math.round(rect.width), height: Math.round(rect.height) },
		visibleText: visibleText || undefined,
		selectedText: selectedText || undefined,
		ariaLabel: ariaName(element) || undefined,
		computedStyle: style
			? {
				display: style.display,
				position: style.position,
				color: style.color,
				backgroundColor: style.backgroundColor,
				fontFamily: style.fontFamily,
				fontSize: style.fontSize,
				fontWeight: style.fontWeight,
				opacity: style.opacity,
				borderColor: style.borderColor,
				borderWidth: style.borderWidth,
				borderRadius: style.borderRadius,
				padding: style.padding,
				margin: style.margin,
				width: style.width,
				height: style.height,
				flexDirection: style.flexDirection,
				alignItems: style.alignItems,
				justifyContent: style.justifyContent,
				columnGap: style.columnGap,
				rowGap: style.rowGap,
			}
			: {},
	};
}

export function createBrowserAnnotationSession(url: string, title?: string): BrowserAnnotationSession {
	return { version: 1, page: { url, title }, annotations: [], screenshots: [] };
}

export function formatBrowserAnnotationMessage(
	payload: BrowserAnnotationSubmitPayload,
	options?: { screenshotPaths?: string[] },
): string {
	const { session } = payload;
	const total = session.annotations.length;
	const lines = [
		"<browser_annotations>",
		"Browser feedback",
		`Page: ${compactText(session.page.title ?? "", 160) || "Untitled page"}`,
		`URL: ${session.page.url || "(unknown)"}`,
		`Annotations: ${total}`,
		"Task: Address the feedback below according to its wording. Visual adjustments are already previewed in AO's shared browser and describe the intended result; implement that result in workspace source when available, otherwise use it as visual context.",
	];
	for (const annotation of session.annotations) {
		const context = annotation.target.context;
		lines.push("", `Annotation ${annotation.number} (${annotation.kind}):`);
		lines.push(`Target: ${elementSummary(context)}`);
		lines.push(`Selector: ${context.selector}`);
		lines.push(`Dimensions: ${context.size.width}×${context.size.height}`);
		if (context.visibleText || context.selectedText)
			lines.push(`Element text: ${JSON.stringify(compactText(context.visibleText || context.selectedText || "", MAX_TEXT_FIELD_LENGTH))}`);
		if (context.ariaLabel) lines.push(`Accessible name: ${JSON.stringify(compactText(context.ariaLabel, 180))}`);
		const comment = compactText(annotation.body, MAX_ANNOTATION_BODY_LENGTH);
		if (comment) lines.push(`Comment: ${comment}`);
		if (annotation.adjustments.length > 0) lines.push("Requested visual changes:");
		for (const adjustment of annotation.adjustments) {
			lines.push(
				`- ${ADJUSTMENT_LABELS[adjustment.property]}: ${JSON.stringify(adjustment.previousValue || "(unset)")} → ${JSON.stringify(adjustment.value)}`,
			);
		}
	}
	const screenshotPaths = options?.screenshotPaths ?? [];
	if (screenshotPaths.length > 0)
		lines.push("", "Reference screenshots:", ...screenshotPaths.map((path) => `- ${path}`));
	lines.push("", "</browser_annotations>");
	return limitMessage(lines.join("\n"), MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH);
}

/**
 * Parse AO's annotation transport so the transcript can render a concise card
 * instead of exposing selectors and agent-facing handoff instructions.
 */
export function parseBrowserAnnotationMessage(message: string): ParsedBrowserAnnotationMessage | null {
	const match = BROWSER_ANNOTATIONS_BLOCK_PATTERN.exec(message.trim());
	if (!match) return null;

	const lines = (match[1] ?? "").split("\n");
	const pageTitle = valueAfterPrefix(lines, "Page: ") || "Untitled page";
	const pageUrl = valueAfterPrefix(lines, "URL: ");
	const items: ParsedBrowserAnnotationItem[] = [];
	let current: ParsedBrowserAnnotationItem | null = null;
	let readingChanges = false;
	let readingScreenshots = false;
	let screenshotCount = 0;

	for (const line of lines) {
		const heading = /^Annotation (\d+) \((comment|adjustment)\):$/.exec(line);
		if (heading) {
			current = {
				number: Number(heading[1]),
				kind: heading[2] as BrowserAnnotationKind,
				target: "",
				comment: "",
				changes: [],
			};
			items.push(current);
			readingChanges = false;
			readingScreenshots = false;
			continue;
		}
		if (line === "Requested visual changes:") {
			readingChanges = true;
			continue;
		}
		if (line === "Reference screenshots:") {
			current = null;
			readingChanges = false;
			readingScreenshots = true;
			continue;
		}
		if (readingScreenshots && line.startsWith("- ")) {
			screenshotCount += 1;
			continue;
		}
		if (!current) continue;
		if (line.startsWith("Target: ")) current.target = line.slice("Target: ".length).trim();
		else if (line.startsWith("Comment: ")) current.comment = line.slice("Comment: ".length).trim();
		else if (readingChanges && line.startsWith("- ")) current.changes.push(line.slice(2).trim());
	}

	return { pageTitle, pageUrl, items, screenshotCount };
}

function valueAfterPrefix(lines: string[], prefix: string): string {
	return lines.find((line) => line.startsWith(prefix))?.slice(prefix.length).trim() ?? "";
}

function elementSummary(context: BrowserAnnotationContext): string {
	return `${context.tag}${context.id ? `#${context.id}` : ""}${context.classes.length > 0 ? `.${context.classes.join(".")}` : ""}`;
}

function selectorFor(element: Element): string {
	if (element.id) return `${element.tagName.toLowerCase()}#${cssEscape(element.id)}`;
	const testId = element.getAttribute("data-testid");
	if (testId) return `${element.tagName.toLowerCase()}[data-testid="${cssEscape(testId)}"]`;
	const parts: string[] = [];
	let current: Element | null = element;
	while (current && current.nodeType === Node.ELEMENT_NODE && parts.length < MAX_SELECTOR_DEPTH) {
		const tag = current.tagName.toLowerCase();
		if (tag === "html") break;
		let part = tag;
		const classes = Array.from(current.classList).slice(0, 2);
		if (classes.length > 0) part += `.${classes.map(cssEscape).join(".")}`;
		const index = nthOfType(current);
		if (index > 1 || hasSameTagSibling(current)) part += `:nth-of-type(${index})`;
		parts.unshift(part);
		current = current.parentElement;
	}
	return parts.join(" > ") || element.tagName.toLowerCase();
}

function nthOfType(element: Element): number {
	let index = 1;
	let sibling = element.previousElementSibling;
	while (sibling) { if (sibling.tagName === element.tagName) index += 1; sibling = sibling.previousElementSibling; }
	return index;
}

function hasSameTagSibling(element: Element): boolean {
	let sibling = element.previousElementSibling;
	while (sibling) { if (sibling.tagName === element.tagName) return true; sibling = sibling.previousElementSibling; }
	sibling = element.nextElementSibling;
	while (sibling) { if (sibling.tagName === element.tagName) return true; sibling = sibling.nextElementSibling; }
	return false;
}

function ariaName(element: Element): string {
	const label = compactText(element.getAttribute("aria-label") ?? "", 180);
	if (label) return label;
	const labelledBy = element.getAttribute("aria-labelledby");
	if (!labelledBy) return "";
	return compactText(labelledBy.split(/\s+/).map((id) => element.ownerDocument.getElementById(id)?.textContent ?? "").join(" "), 180);
}

function elementText(element: Element, maxLength: number): string {
	return compactText((element as HTMLElement).innerText ?? element.textContent ?? "", maxLength);
}

function compactText(value: string, maxLength: number): string {
	const compact = value.replace(/\s+/g, " ").trim();
	if (compact.length <= maxLength) return compact;
	const suffix = " [truncated]";
	return `${compact.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}

function limitMessage(message: string, maxLength: number): string {
	if (message.length <= maxLength) return message;
	const closingTag = "\n</browser_annotations>";
	const suffix = `\n[truncated]${message.endsWith(closingTag) ? closingTag : ""}`;
	const body = message.endsWith(closingTag) ? message.slice(0, -closingTag.length) : message;
	return `${body.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}

function cssEscape(value: string): string {
	return globalThis.CSS?.escape ? globalThis.CSS.escape(value) : value.replace(/[^a-zA-Z0-9_-]/g, "\\$&");
}
