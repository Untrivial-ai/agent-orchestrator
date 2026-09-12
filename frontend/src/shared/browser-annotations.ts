export const MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH = 4096;

const MAX_INSTRUCTION_LENGTH = 1400;
const MAX_TEXT_FIELD_LENGTH = 700;
const MAX_SELECTOR_DEPTH = 6;

const CONSTRAINTS_LINE =
	"Constraints: smallest diff that satisfies the request; no watch-mode or long-running commands; use finite verification commands or the existing preview watcher.";

export type BrowserAnnotationSize = {
	width: number;
	height: number;
};

export type BrowserAnnotationComputedStyle = Partial<{
	display: string;
	position: string;
	color: string;
	backgroundColor: string;
	fontSize: string;
	fontWeight: string;
	padding: string;
	margin: string;
}>;

export type BrowserAnnotationContext = {
	url: string;
	title?: string;
	tag: string;
	id?: string;
	classes: string[];
	selector: string;
	size: BrowserAnnotationSize;
	visibleText?: string;
	selectedText?: string;
	ariaLabel?: string;
	computedStyle: BrowserAnnotationComputedStyle;
};

export type BrowserAnnotationModeInput = {
	viewId: string;
	enabled: boolean;
};

export type BrowserAnnotationSelection =
	| { kind: "element"; context: BrowserAnnotationContext }
	| { kind: "elements"; contexts: BrowserAnnotationContext[] };

export type BrowserAnnotationPageSubmitPayload = {
	instruction: string;
	selection: BrowserAnnotationSelection;
};

export type BrowserAnnotationSnapshot = {
	mimeType: string;
	data: string;
};

export type BrowserAnnotationDraft = BrowserAnnotationPageSubmitPayload;

export type BrowserAnnotationPageMode = {
	enabled: boolean;
	draft?: BrowserAnnotationDraft;
};

export type BrowserAnnotationSubmitPayload = BrowserAnnotationPageSubmitPayload & {
	viewId: string;
	snapshot?: BrowserAnnotationSnapshot;
};

export type BrowserAnnotationCancelReason = "escape" | "cancel" | "navigation" | "disabled";

export type BrowserAnnotationPageCancelPayload = {
	reason: BrowserAnnotationCancelReason;
};

export type BrowserAnnotationCancelPayload = BrowserAnnotationPageCancelPayload & {
	viewId: string;
};

export function createBrowserAnnotationContext(element: Element): BrowserAnnotationContext {
	const doc = element.ownerDocument;
	const view = doc.defaultView;
	const rect = element.getBoundingClientRect();
	const classList = Array.from(element.classList).slice(0, 8);
	const visibleText = elementText(element, MAX_TEXT_FIELD_LENGTH);
	const selectedText = compactText(view?.getSelection?.()?.toString() ?? "", MAX_TEXT_FIELD_LENGTH);
	const style = view?.getComputedStyle ? view.getComputedStyle(element) : null;

	return {
		url: view?.location.href ?? "",
		title: doc.title || undefined,
		tag: element.tagName.toLowerCase(),
		id: element.id || undefined,
		classes: classList,
		selector: selectorFor(element),
		size: {
			width: Math.round(rect.width),
			height: Math.round(rect.height),
		},
		visibleText: visibleText || undefined,
		selectedText: selectedText || undefined,
		ariaLabel: ariaName(element) || undefined,
		computedStyle: style
			? {
					display: style.display,
					position: style.position,
					color: style.color,
					backgroundColor: style.backgroundColor,
					fontSize: style.fontSize,
					fontWeight: style.fontWeight,
					padding: style.padding,
					margin: style.margin,
				}
			: {},
	};
}

export function formatBrowserAnnotationMessage(payload: BrowserAnnotationSubmitPayload): string {
	const selection = payload.selection;
	const lines =
		selection.kind === "element"
			? singleElementLines(payload.instruction, selection.context)
			: multiElementLines(payload.instruction, selection.contexts);
	return limitMessage(lines.join("\n"), MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH);
}

function singleElementLines(instruction: string, context: BrowserAnnotationContext): string[] {
	return [
		"Browser annotation — apply this change",
		"",
		`Request: ${compactText(instruction, MAX_INSTRUCTION_LENGTH) || "(empty)"}`,
		"",
		`Target: ${elementSummary(context)} (${context.selector})`,
		`Page: ${pageLine(context)}`,
		`Size: ${context.size.width}×${context.size.height}`,
		...textLine(context),
		...ariaLine(context),
		...styleLine(context),
		"",
		CONSTRAINTS_LINE,
	];
}

function multiElementLines(instruction: string, contexts: BrowserAnnotationContext[]): string[] {
	const url = contexts.find((context) => context.url)?.url || "(unknown)";
	const items = contexts.map(
		(context, index) =>
			`${index + 1}. ${elementSummary(context)} (selector: ${context.selector}) — ${context.size.width}×${context.size.height}`,
	);
	return [
		`Browser annotation — apply this change to ${contexts.length} selected element${contexts.length === 1 ? "" : "s"}`,
		"",
		`Request: ${compactText(instruction, MAX_INSTRUCTION_LENGTH) || "(empty)"}`,
		"",
		`Targets @ ${url}:`,
		...items,
		"",
		CONSTRAINTS_LINE,
	];
}

function pageLine(context: BrowserAnnotationContext): string {
	const url = context.url || "(unknown)";
	return context.title ? `${url} — "${compactText(context.title, 160)}"` : url;
}

function textLine(context: BrowserAnnotationContext): string[] {
	const text = context.visibleText || context.selectedText;
	return text ? [`Text: "${compactText(text, MAX_TEXT_FIELD_LENGTH)}"`] : [];
}

function ariaLine(context: BrowserAnnotationContext): string[] {
	return context.ariaLabel ? [`Aria-label: "${compactText(context.ariaLabel, 180)}"`] : [];
}

function styleLine(context: BrowserAnnotationContext): string[] {
	const style = context.computedStyle;
	const parts: string[] = [];
	if (style.display) parts.push(`display:${style.display}`);
	if (style.position) parts.push(`position:${style.position}`);
	if (style.color) parts.push(`color:${compactCss(style.color)}`);
	if (style.backgroundColor) parts.push(`background:${compactCss(style.backgroundColor)}`);
	const font = [style.fontWeight, style.fontSize].filter(Boolean).join(" ");
	if (font) parts.push(`font:${font}`);
	if (style.padding) parts.push(`padding:${style.padding}`);
	if (style.margin) parts.push(`margin:${style.margin}`);
	return parts.length > 0 ? [`Style: ${parts.join("; ")}`] : [];
}

function compactCss(value: string): string {
	return value.replace(/,\s+/g, ",");
}

function elementSummary(context: BrowserAnnotationContext): string {
	const id = context.id ? `#${context.id}` : "";
	const classes = context.classes.length > 0 ? `.${context.classes.join(".")}` : "";
	return `${context.tag}${id}${classes}`;
}

function selectorFor(element: Element): string {
	if (element.id) return `${element.tagName.toLowerCase()}#${cssEscape(element.id)}`;
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
	while (sibling) {
		if (sibling.tagName === element.tagName) index += 1;
		sibling = sibling.previousElementSibling;
	}
	return index;
}

function hasSameTagSibling(element: Element): boolean {
	let sibling = element.previousElementSibling;
	while (sibling) {
		if (sibling.tagName === element.tagName) return true;
		sibling = sibling.previousElementSibling;
	}
	sibling = element.nextElementSibling;
	while (sibling) {
		if (sibling.tagName === element.tagName) return true;
		sibling = sibling.nextElementSibling;
	}
	return false;
}

function ariaName(element: Element): string {
	const label = compactText(element.getAttribute("aria-label") ?? "", 180);
	if (label) return label;
	const labelledBy = element.getAttribute("aria-labelledby");
	if (!labelledBy) return "";
	const doc = element.ownerDocument;
	return compactText(
		labelledBy
			.split(/\s+/)
			.map((id) => doc.getElementById(id)?.textContent ?? "")
			.join(" "),
		180,
	);
}

function elementText(element: Element, maxLength: number): string {
	const htmlElement = element as HTMLElement;
	return compactText(htmlElement.innerText ?? element.textContent ?? "", maxLength);
}

function compactText(value: string, maxLength: number): string {
	const compact = value.replace(/\s+/g, " ").trim();
	if (compact.length <= maxLength) return compact;
	const suffix = " [truncated]";
	return `${compact.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}

function limitMessage(message: string, maxLength: number): string {
	if (message.length <= maxLength) return message;
	const suffix = "\n[truncated]";
	return `${message.slice(0, Math.max(0, maxLength - suffix.length)).trimEnd()}${suffix}`;
}

function cssEscape(value: string): string {
	return globalThis.CSS?.escape ? globalThis.CSS.escape(value) : value.replace(/[^a-zA-Z0-9_-]/g, "\\$&");
}
