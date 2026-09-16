import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { createBrowserAnnotationSession, type BrowserAnnotationSession } from "./shared/browser-annotations";

const electronMocks = vi.hoisted(() => {
	const listeners = new Map<string, (...args: unknown[]) => void>();
	return {
		listeners,
		on: vi.fn((channel: string, listener: (...args: unknown[]) => void) => listeners.set(channel, listener)),
		send: vi.fn(),
		invoke: vi.fn().mockResolvedValue(undefined),
	};
});

vi.mock("electron", () => ({
	ipcRenderer: {
		on: electronMocks.on,
		send: electronMocks.send,
		invoke: electronMocks.invoke,
	},
}));

const fontMocks = vi.hoisted(() => ({ add: vi.fn() }));
class MockFontFace {
	constructor(_family: string) {}
	load(): Promise<MockFontFace> { return Promise.resolve(this); }
}
vi.stubGlobal("FontFace", MockFontFace);
Object.defineProperty(document, "fonts", { configurable: true, value: { add: fontMocks.add } });

await import("./annotate-preload");

type Bounds = { left: number; top: number; width: number; height: number };

function setMode(enabled: boolean, session?: BrowserAnnotationSession): void {
	const listener = electronMocks.listeners.get("browser:annotation:setMode");
	if (!listener) throw new Error("annotation mode listener was not registered");
	listener({}, { enabled, ...(session ? { session } : {}) });
}

function setElementBounds<T extends Element>(element: T, bounds: Bounds): T {
	Object.defineProperty(element, "getBoundingClientRect", {
		configurable: true,
		value: () => ({
			x: bounds.left,
			y: bounds.top,
			left: bounds.left,
			top: bounds.top,
			right: bounds.left + bounds.width,
			bottom: bounds.top + bounds.height,
			width: bounds.width,
			height: bounds.height,
			toJSON: () => ({}),
		}) as DOMRect,
	});
	return element;
}

function clickPage(element: Element): void {
	element.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
}

function overlayRoot(): ShadowRoot {
	const host = document.querySelector<HTMLDivElement>("[data-ao-annotation-root]");
	if (!host?.shadowRoot) throw new Error("annotation overlay was not rendered");
	return host.shadowRoot;
}

function openAdjust(element: Element): ShadowRoot {
	clickPage(element);
	const root = overlayRoot();
	root.querySelector<HTMLButtonElement>('[data-action="adjust"]')?.click();
	return root;
}

function latestSession(): BrowserAnnotationSession {
	const call = electronMocks.send.mock.calls.findLast(([channel]) => channel === "browser:annotation:state");
	if (!call) throw new Error("annotation state was not emitted");
	return call[1] as BrowserAnnotationSession;
}

describe("annotation adjustment preload", () => {
	beforeEach(() => {
		document.body.innerHTML = "";
		electronMocks.send.mockClear();
		electronMocks.invoke.mockClear();
		setMode(true, createBrowserAnnotationSession(window.location.href));
	});

	afterEach(() => {
		setMode(false);
		document.body.innerHTML = "";
	});

	it("opens a compact adjust panel with native color controls", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "primary";
		button.textContent = "Continue";
		document.body.appendChild(button);

		const root = openAdjust(button);
		const form = root.querySelector<HTMLFormElement>(".composer--adjustment");
		const color = root.querySelector<HTMLInputElement>('[data-property="color"]');

		expect(form?.style.width).toBe("316px");
		expect(form?.style.maxHeight).toBe("400px");
		expect(color).toHaveAttribute("type", "color");
		expect(root.querySelector("style")?.textContent).toContain(".color-picker::-webkit-color-swatch");
	});

	it("applies a picked text color live to the element that paints nested text", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "nested-label";
		const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
		const label = document.createElement("span");
		label.textContent = "Continue";
		button.append(icon, label);
		document.body.appendChild(button);

		const root = openAdjust(button);
		const color = root.querySelector<HTMLInputElement>('[data-property="color"]')!;
		color.value = "#e34b63";
		color.dispatchEvent(new Event("input", { bubbles: true }));

		expect(label.style.getPropertyValue("color")).toBe("rgb(227, 75, 99)");
		expect(label.style.getPropertyPriority("color")).toBe("important");
		expect(root.querySelector(".color-value")?.textContent).toBe("#E34B63");
		expect(latestSession().draft?.adjustments).toContainEqual(expect.objectContaining({
			property: "color",
			value: "#e34b63",
		}));
	});

	it("edits only the direct text node and preserves nested markup", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "safe-text";
		const icon = document.createElementNS("http://www.w3.org/2000/svg", "svg");
		const path = document.createElementNS("http://www.w3.org/2000/svg", "path");
		icon.appendChild(path);
		const label = document.createElement("span");
		label.textContent = "Continue";
		button.append(icon, label);
		document.body.appendChild(button);

		const root = openAdjust(button);
		const text = root.querySelector<HTMLTextAreaElement>('[data-property="textContent"]')!;
		text.value = "Launch";
		text.dispatchEvent(new Event("input", { bubbles: true }));

		expect(label.textContent).toBe("Launch");
		expect(button.querySelector("svg path")).toBe(path);

		text.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true, cancelable: true }));
		expect(label.textContent).toBe("Continue");
		expect(button.querySelector("svg path")).toBe(path);
	});

	it("does not offer text editing for logos and non-text elements", () => {
		const image = setElementBounds(document.createElement("img"), { left: 20, top: 30, width: 80, height: 80 });
		image.id = "logo";
		document.body.appendChild(image);

		const root = openAdjust(image);

		expect(root.querySelector('[data-property="textContent"]')).toBeNull();
		expect(root.querySelector('[data-property="color"]')).toBeNull();
		expect(root.querySelector<HTMLInputElement>('[data-property="backgroundColor"]')).toHaveAttribute("type", "color");
	});

	it("keeps the inspector anchored while target dimensions change", () => {
		const bounds = { left: 620, top: 180, width: 160, height: 44 };
		const button = document.createElement("button");
		button.id = "resizable";
		button.textContent = "Resize me";
		Object.defineProperty(button, "getBoundingClientRect", {
			configurable: true,
			value: () => ({
				x: bounds.left,
				y: bounds.top,
				left: bounds.left,
				top: bounds.top,
				right: bounds.left + bounds.width,
				bottom: bounds.top + bounds.height,
				width: bounds.width,
				height: bounds.height,
				toJSON: () => ({}),
			}) as DOMRect,
		});
		document.body.appendChild(button);

		const root = openAdjust(button);
		const form = root.querySelector<HTMLFormElement>(".composer--adjustment")!;
		const width = root.querySelector<HTMLInputElement>('[data-property="width"]')!;
		const initialPosition = { left: form.style.left, top: form.style.top };

		bounds.left = 80;
		bounds.top = 500;
		width.value = "10";
		width.dispatchEvent(new Event("input", { bubbles: true }));
		width.value = "70";
		width.dispatchEvent(new Event("input", { bubbles: true }));

		expect(form.style.left).toBe(initialPosition.left);
		expect(form.style.top).toBe(initialPosition.top);
		expect(button.style.getPropertyValue("width")).toBe("70px");
	});

	it("normalizes leading zeroes before storing pixel adjustments", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "normalized-size";
		button.textContent = "Resize me";
		document.body.appendChild(button);

		const root = openAdjust(button);
		const width = root.querySelector<HTMLInputElement>('[data-property="width"]')!;
		width.value = "089";
		width.dispatchEvent(new Event("input", { bubbles: true }));
		width.dispatchEvent(new Event("change", { bubbles: true }));

		expect(width.value).toBe("89");
		expect(button.style.getPropertyValue("width")).toBe("89px");
		expect(latestSession().draft?.adjustments).toContainEqual(expect.objectContaining({
			property: "width",
			value: "89px",
		}));
	});
});
