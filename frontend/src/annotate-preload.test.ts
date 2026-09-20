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

const motionMocks = vi.hoisted(() => ({
	animate: vi.fn((
		subject: Element | number,
		keyframes: { height?: string } | number,
		options?: { onUpdate?: (latest: unknown) => void },
	) => {
		if (typeof subject === "number") {
			// Plain-value animations (the composer's panel height) report the frame
			// they are about to write; the caller applies it.
			options?.onUpdate?.(keyframes);
		} else {
			if (subject instanceof HTMLElement && typeof keyframes === "object" && keyframes?.height) {
				subject.style.height = keyframes.height;
			}
			options?.onUpdate?.(undefined);
		}
		const controls = { stop: vi.fn() };
		return Object.assign(Promise.resolve(), controls);
	}),
}));

vi.mock("motion", () => ({ animate: motionMocks.animate }));

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

const elementPrototypeSpies: Array<{ mockRestore: () => void }> = [];

/** jsdom measures no content, so give the adjustment panel a height to travel to. */
function stubPanelContentHeight(height: number): void {
	elementPrototypeSpies.push(vi.spyOn(Element.prototype, "scrollHeight", "get").mockReturnValue(height));
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
		for (const spy of elementPrototypeSpies.splice(0)) spy.mockRestore();
	});

	it("opens a compact adjust panel with native color controls", async () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "primary";
		button.textContent = "Continue";
		document.body.appendChild(button);

		const root = openAdjust(button);
		const form = root.querySelector<HTMLFormElement>(".composer--adjustment");
		const color = root.querySelector<HTMLInputElement>('[data-property="color"]');
		const styles = root.querySelector("style")?.textContent ?? "";

		expect(form?.style.width).toBe("320px");
		// The card takes the room the element leaves below it — a 768px viewport
		// less the gutter, the gap and the element's bottom — not a fixed ceiling.
		expect(form?.style.maxHeight).toBe(`${window.innerHeight - 14 - 66 - 10}px`);
		expect(color).toHaveAttribute("type", "color");
		expect(styles).toContain(".color-picker::-webkit-color-swatch");
		// Unified chrome: modest radius, equal padding, sans by default; mono only for CSS values.
		expect(styles).toContain("--radius:8px");
		expect(styles).toContain("--pad:8px");
		expect(styles).not.toContain("border-radius:999px");
		expect(styles).not.toContain('font:700 10px "Geist Mono Variable"');
		expect(styles).toContain('font:600 10px/1 "Geist Variable"');
		expect(styles).toMatch(/\.field input,\.field select,\.property-textarea\{[^}]*Geist Variable/);
		expect(styles).toMatch(/\.field input\[data-unit\]\{[^}]*Geist Mono Variable/);
		expect(styles).toContain("button:focus-visible{outline:none;box-shadow:inset");
		// The lock wears the same chrome as the composer's icon buttons.
		expect(styles).toMatch(/\.link-button:not\(:disabled\):hover,\.link-button--active\{background:var\(--muted\);color:var\(--fg\)\}/);
		expect(styles).toMatch(/\.link-button:not\(:disabled\):active\{transform:scale\(0\.98\)\}/);
		// One grid for every row: two field boxes and a trailing action cell, so
		// fields, values and link buttons all land on the same edges.
		expect(styles).toMatch(/\.panel-row\{[^}]*grid-template-columns:minmax\(0,1fr\) minmax\(0,1fr\) var\(--control\)/);
		expect(styles).toMatch(/\.field\{[^}]*grid-template-columns:40px minmax\(0,1fr\) auto/);
		expect(styles).toContain(".field--wide{grid-column:span 2}");
		// Narrow card (slim browser pane, 200% zoom) stacks the pair instead of
		// clipping the label, the value and the swatch into one row.
		expect(styles).toContain("container-type:inline-size");
		expect(styles).toContain("@container (max-width:296px)");
		expect(styles).toMatch(/@container \(max-width:296px\)\{\s*\.panel-row\{grid-template-columns:minmax\(0,1fr\) auto\}/);
		// The query measures the card's *content* box, so a threshold that only
		// just clears the 320 card (318 minus the borders) can never be met: the
		// paired layout would be dead code and every palette would stack tall.
		expect(Number(styles.match(/@container \(max-width:(\d+)px\)/)?.[1])).toBeLessThan(320 - 2);
		// The panel scrolls without a scrollbar, so the overflowed edge fades.
		expect(styles).toMatch(/\.adjustment-panel\[data-cue~="bottom"\]::after\{opacity:1\}/);
		expect(styles).toMatch(/\.adjustment-panel\[data-cue~="top"\]::before\{opacity:1\}/);
		expect(styles).toMatch(/\.composer--adjustment \.composer-note\{[^}]*padding:4px 0 0/);
		expect(styles).toMatch(/\.composer\{[^}]*border-radius:16px/);
		// The comment and adjustment composers are the same box: switching modes
		// must not resize the card out from under the row (that read as a jerk).
		expect(styles).toContain(".composer--adjustment{display:flex;flex-direction:column;padding:var(--pad) 0}");
		expect(styles).toMatch(/\.composer--comment\{padding:var\(--pad\) 10px\}/);
		expect(styles).not.toMatch(/\.composer--adjustment\{[^}]*border-radius:var\(--radius\)/);
		expect(styles).not.toMatch(/\.composer--adjustment \.composer-input-row\{[^}]*background:var\(--muted\)/);
		expect(styles).toContain(".adjust-button:not(:disabled):active{transform:scale(0.98)}");
		// A changed field swaps its label for its reset, in place.
		expect(styles).toMatch(/\.field-reset\{[^}]*position:absolute/);
		expect(styles).toMatch(/\.field:has\(\.field-reset--on\):hover \.field-reset/);
		// Labels are chrome: dragging across them must not start a selection.
		expect(styles).toContain(".element-header,.field-label,.panel-section summary{");
		expect(styles).toMatch(/\.element-header,\.field-label,\.panel-section summary\{[^}]*user-select:none/);
		expect(root.querySelector("[data-adjustment-panel]")).not.toBeNull();
		const widthInput = root.querySelector<HTMLInputElement>('[data-property="width"]');
		const field = widthInput?.closest(".field");
		const resetButton = field?.querySelector(".field-reset");
		expect(field).not.toBeNull();
		expect(resetButton).not.toBeNull();
		// Untouched fields keep their reset out of the tab order.
		expect(resetButton).toHaveAttribute("tabindex", "-1");
		// The panel unfurls from the row of controls: its height and the card's top
		// are driven from one animated value, so the edge the card shares with the
		// element cannot shear while it travels.
		expect(motionMocks.animate).toHaveBeenCalledWith(
			0,
			expect.any(Number),
			expect.objectContaining({ duration: 0.3, ease: [0.22, 1, 0.36, 1], type: "tween" }),
		);
		// The flipped layout belongs to the adjustment panel: the comment composer
		// keeps its own padding when it sits above the element too.
		expect(styles).toContain(".composer--adjustment.composer--above{flex-direction:column-reverse}");
		// No bare rule: an unscoped one would restyle the comment composer too.
		expect(styles).not.toMatch(/(?:^|[};])\.composer--above\{/);
		expect(styles).not.toMatch(/\.adjustment-scroll\{[^}]*max-height:/);

		motionMocks.animate.mockClear();
		root.querySelector<HTMLButtonElement>('[data-action="adjust"]')?.click();
		await vi.waitFor(() => {
			expect(root.querySelector(".composer--comment")).not.toBeNull();
		});
		expect(motionMocks.animate).toHaveBeenCalledWith(
			expect.any(Number),
			0,
			expect.objectContaining({ duration: 0.22, ease: [0.22, 1, 0.36, 1] }),
		);
		expect(root.querySelector(".composer--comment")).not.toBeNull();
		expect(root.querySelector("[data-adjustment-panel]")).toBeNull();
	});

	it("picks the side the panel opens on before it grows, and holds that edge", async () => {
		// Near the bottom of the viewport the panel cannot open downwards, so the
		// card has to flip above the element — decided from the panel's final size
		// rather than from whatever height it happens to be mid-animation.
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 640, width: 140, height: 36 });
		button.id = "low-anchor";
		button.textContent = "Continue";
		document.body.appendChild(button);
		stubPanelContentHeight(300);

		const root = openAdjust(button);
		const form = root.querySelector<HTMLFormElement>(".composer")!;
		const panel = root.querySelector<HTMLElement>("[data-adjustment-panel]")!;
		const [from, to] = motionMocks.animate.mock.calls.at(-1) as unknown as [number, number];

		expect(from).toBe(0);
		expect(to).toBe(300);
		expect(form.classList.contains("composer--above")).toBe(true);
		// jsdom reports zero-sized boxes, so the card is the panel's height alone and
		// its bottom lands one gap above the element's top edge (640 - 10).
		expect(Number.parseFloat(form.style.top) + 300).toBeCloseTo(630, 1);
		expect(panel.style.height).toBe("300px");

		// Replaying the collapse keeps the card on that edge: the top only travels
		// between the two ends, never jumping to the other side of the element.
		root.querySelector<HTMLButtonElement>('[data-action="adjust"]')?.click();
		const [closeFrom, closeTo, closeOptions] = motionMocks.animate.mock.calls.at(-1) as [
			number,
			number,
			{ onUpdate?: (height: number) => void },
		];
		expect(closeFrom).toBe(300);
		expect(closeTo).toBe(0);
		const topAt = (height: number): number => {
			closeOptions.onUpdate?.(height);
			return Number.parseFloat(form.style.top);
		};
		const start = topAt(300);
		const end = topAt(0);
		// Collapsing returns the composer below the element (bottom + gap)...
		expect(end).toBeCloseTo(686, 1);
		expect(start).not.toBeCloseTo(end, 1);
		// ...and travels there in step with the panel rather than re-deriving the
		// card's edge from each intermediate height.
		expect(topAt(150)).toBeCloseTo((start + end) / 2, 0);
		await vi.waitFor(() => expect(root.querySelector(".composer--comment")).not.toBeNull());
	});

	it("keeps the end of a capped panel inside the card's edge", async () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 640, width: 140, height: 36 });
		button.id = "capped-panel";
		button.textContent = "Continue";
		document.body.appendChild(button);
		stubPanelContentHeight(900);

		const root = openAdjust(button);
		const inner = root.querySelector<HTMLElement>(".adjustment-panel-inner")!;
		const scroll = root.querySelector<HTMLElement>(".adjustment-scroll")!;

		// The inner is offset from the panel by the divider gap, so a plain 100%
		// sizes it past the panel's edge and the end of the scroll clips the last
		// row instead of showing it with the panel's own padding.
		await vi.waitFor(() => {
			expect(inner.style.height).toBe("calc(100% - var(--panel-chrome))");
		});
		// Opened upwards, the rows nearest the input row are the ones the grow-in
		// animation left on screen, so a scrollable panel keeps that view.
		expect(scroll.scrollTop).toBe(900);
	});

	it("animates spacing accordions open and closed", async () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		document.body.appendChild(button);
		const root = openAdjust(button);
		const margin = root.querySelector<HTMLDetailsElement>('.panel-section:not([open])');
		const panel = root.querySelector<HTMLElement>("[data-adjustment-panel]")!;
		expect(margin).not.toBeNull();

		motionMocks.animate.mockClear();
		margin?.querySelector("summary")?.click();
		await vi.waitFor(() => expect(motionMocks.animate).toHaveBeenCalled());
		// The group and the panel travel in one motion, so the rows never slide
		// inside a card that has not resized yet.
		expect(motionMocks.animate).toHaveBeenCalledWith(
			0,
			expect.any(Number),
			expect.objectContaining({ duration: 0.22, ease: [0.22, 1, 0.36, 1], type: "tween" }),
		);
		await vi.waitFor(() => expect(panel.style.height).not.toBe("0px"));

		motionMocks.animate.mockClear();
		margin?.querySelector("summary")?.click();
		await vi.waitFor(() => expect(motionMocks.animate).toHaveBeenCalled());
		expect(motionMocks.animate).toHaveBeenCalledWith(
			expect.any(Number),
			0,
			expect.objectContaining({ duration: 0.22, ease: [0.22, 1, 0.36, 1] }),
		);
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

	it("keeps every field's visible label inside its accessible name", () => {
		// Compact in-field labels must stay part of the control's name (WCAG 2.5.3),
		// and each one has to be specific enough to tell sibling fields apart.
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "naming";
		button.textContent = "Continue";
		document.body.appendChild(button);

		const root = openAdjust(button);
		const fields = Array.from(root.querySelectorAll<HTMLElement>("[data-field]"));
		expect(fields.length).toBeGreaterThan(8);
		const visibleLabels = fields.map((field) => field.querySelector<HTMLElement>(".field-label")?.textContent?.trim() ?? "");
		for (const [index, field] of fields.entries()) {
			const name = field.querySelector<HTMLElement>("[data-property]")?.getAttribute("aria-label") ?? "";
			expect(visibleLabels[index].length).toBeGreaterThan(0);
			expect(name.toLowerCase()).toContain(visibleLabels[index].toLowerCase());
		}
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

	it("uses a concentric comment composer aligned on one row without a separate actions row", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "comment-row";
		button.textContent = "Continue";
		document.body.appendChild(button);

		clickPage(button);
		const root = overlayRoot();
		const form = root.querySelector<HTMLFormElement>(".composer--comment");
		const row = root.querySelector<HTMLElement>(".composer-input-row");
		const styles = root.querySelector("style")?.textContent ?? "";

		expect(form).not.toBeNull();
		expect(root.querySelector(".composer-actions")).toBeNull();
		expect(root.querySelector(".cancel-button")).toBeNull();
		expect(root.querySelector(".send-button")).toBeNull();
		expect(styles).toContain("border-radius:16px");
		expect(styles).toContain(".adjust-button{");
		expect(styles).toMatch(/\.adjust-button\{[^}]*border-radius:var\(--radius\)/);
		expect(styles).toContain("--radius:8px");
		expect(styles).toContain(".composer-input-row{display:flex;min-width:0;align-items:flex-start");
		expect(styles).toMatch(/\.adjust-button\{[^}]*align-self:flex-start/);
		expect(row?.querySelector(".adjust-button")).not.toBeNull();
		expect(row?.querySelector(".composer-note")).not.toBeNull();
		expect(row?.querySelector(".adjust-button svg")?.innerHTML).toContain("M12 22a1 1 0 0 1 0-20 10 9");
		const add = row?.querySelector<HTMLButtonElement>('[data-action="add"]');
		expect(add).not.toBeNull();
		expect(row?.lastElementChild).toBe(add);
		expect(add?.disabled).toBe(true);
		expect(add?.querySelector("svg")?.innerHTML).toContain("M12 19V5m-7 7 7-7 7 7");
		// Same 28px ghost box as the palette control, not a filled primary button.
		expect(styles).toMatch(/\.add-button\{[^}]*background:transparent/);
		expect(styles).toMatch(/\.add-button\{[^}]*align-self:flex-start/);
	});

	it("adds an annotation from the trailing add control instead of Enter", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "add-control";
		button.textContent = "Continue";
		document.body.appendChild(button);

		clickPage(button);
		const root = overlayRoot();
		const textarea = root.querySelector<HTMLTextAreaElement>(".composer-note")!;
		const add = root.querySelector<HTMLButtonElement>('[data-action="add"]')!;

		textarea.value = "Raise this above the fold";
		textarea.dispatchEvent(new Event("input", { bubbles: true }));
		expect(add.disabled).toBe(false);

		add.click();

		const session = latestSession();
		expect(session.annotations).toHaveLength(1);
		expect(session.annotations[0]?.body).toBe("Raise this above the fold");
		expect(session.draft).toBeUndefined();
		expect(overlayRoot().querySelector(".composer")).toBeNull();
	});

	it("keeps the add control live for an adjustment with no note", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "adjust-add-control";
		button.textContent = "Continue";
		document.body.appendChild(button);

		const root = openAdjust(button);
		const add = root.querySelector<HTMLButtonElement>('[data-action="add"]')!;
		expect(add.disabled).toBe(true);

		const background = root.querySelector<HTMLInputElement>('[data-property="backgroundColor"]')!;
		background.value = "#e34b63";
		background.dispatchEvent(new Event("input", { bubbles: true }));
		expect(add.disabled).toBe(false);

		add.click();

		const session = latestSession();
		expect(session.annotations).toHaveLength(1);
		expect(session.annotations[0]?.kind).toBe("adjustment");
		expect(session.draft).toBeUndefined();
	});

	it("discards an open comment when clicking outside the composer", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "outside-dismiss";
		button.textContent = "Continue";
		document.body.appendChild(button);

		clickPage(button);
		expect(latestSession().draft).toBeDefined();

		document.body.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
		expect(latestSession().draft).toBeUndefined();
		expect(overlayRoot().querySelector(".composer")).toBeNull();
	});

	it("keeps the selection highlight visible with a 6px outset while annotating", async () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "selected-box";
		button.textContent = "Continue";
		document.body.appendChild(button);

		const styles = overlayRoot().querySelector("style")?.textContent ?? "";
		expect(styles).toContain("transition:left 180ms ease,top 180ms ease,width 180ms ease,height 180ms ease");
		expect(styles).not.toContain("transform:scale(1.1)");

		clickPage(button);
		const highlight = overlayRoot().querySelector<HTMLElement>(".hover");
		expect(highlight?.hidden).toBe(false);
		await vi.waitFor(() => {
			expect(highlight?.classList.contains("hover--selected")).toBe(true);
			expect(highlight?.style.left).toBe("14px");
			expect(highlight?.style.top).toBe("24px");
			expect(highlight?.style.width).toBe("152px");
			expect(highlight?.style.height).toBe("48px");
		});
	});

	it("clears open selection when re-entering annotation mode but keeps batch markers", () => {
		const button = setElementBounds(document.createElement("button"), { left: 20, top: 30, width: 140, height: 36 });
		button.id = "persist-batch";
		button.textContent = "Continue";
		document.body.appendChild(button);

		clickPage(button);
		const note = overlayRoot().querySelector<HTMLTextAreaElement>(".composer-note")!;
		note.value = "keep this in the batch";
		note.dispatchEvent(new Event("input", { bubbles: true }));
		note.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true }));
		expect(latestSession().annotations).toHaveLength(1);
		expect(latestSession().draft).toBeUndefined();
		expect(overlayRoot().querySelectorAll(".marker")).toHaveLength(1);

		clickPage(button);
		expect(overlayRoot().querySelector(".composer")).not.toBeNull();
		expect(latestSession().draft).toBeDefined();

		// Main strips draft synchronously on disable, then re-sends the batch.
		const leaving = structuredClone(latestSession());
		delete leaving.draft;
		setMode(false, leaving);
		expect(document.querySelector("[data-ao-annotation-root]")).toBeNull();

		setMode(true, leaving);

		expect(overlayRoot().querySelector(".composer")).toBeNull();
		expect(latestSession().draft).toBeUndefined();
		expect(latestSession().annotations).toHaveLength(1);
		expect(overlayRoot().querySelectorAll(".marker")).toHaveLength(1);
	});

	it("copies a screenshot to the clipboard instead of adding it to the batch", async () => {
		electronMocks.invoke.mockResolvedValue(true);
		const listener = electronMocks.listeners.get("browser:annotation:action");
		if (!listener) throw new Error("annotation action listener was not registered");
		listener({}, "capture");

		await vi.waitFor(() => {
			expect(overlayRoot().querySelector<HTMLElement>(".screenshot-notice")?.hidden).toBe(false);
		});
		expect(electronMocks.invoke).toHaveBeenCalledWith("browser:annotation:capture");
		expect(overlayRoot().querySelector<HTMLElement>(".screenshot-notice")?.textContent).toBe(
			"Screenshot copied to clipboard",
		);
		// Nothing joins the annotation batch, so no session state is emitted.
		expect(electronMocks.send.mock.calls.some(([channel]) => channel === "browser:annotation:state")).toBe(false);
	});

	it("keeps the screenshot notice hidden when the clipboard copy fails", async () => {
		electronMocks.invoke.mockResolvedValue(false);
		const listener = electronMocks.listeners.get("browser:annotation:action");
		if (!listener) throw new Error("annotation action listener was not registered");
		listener({}, "capture");

		await vi.waitFor(() => {
			expect(electronMocks.invoke).toHaveBeenCalledWith("browser:annotation:capture");
		});
		await new Promise((resolve) => setTimeout(resolve, 0));
		expect(overlayRoot().querySelector<HTMLElement>(".screenshot-notice")?.hidden).toBe(true);
		expect(electronMocks.send.mock.calls.some(([channel]) => channel === "browser:annotation:state")).toBe(false);
	});
});
