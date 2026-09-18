import { describe, expect, it, vi } from "vitest";
import {
	MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH,
	createBrowserAnnotationContext,
	createBrowserAnnotationSession,
	formatBrowserAnnotationMessage,
	parseBrowserAnnotationMessage,
	type BrowserAnnotationContext,
	type BrowserAnnotationSession,
	type BrowserAnnotationSubmitPayload,
} from "./browser-annotations";

function context(overrides: Partial<BrowserAnnotationContext> = {}): BrowserAnnotationContext {
	return {
		url: "http://localhost:5173/settings",
		title: "Settings",
		tag: "button",
		id: "save",
		classes: ["primary"],
		selector: "button#save",
		size: { width: 140, height: 36 },
		rect: { x: 16, y: 24, width: 140, height: 36 },
		visibleText: "Save changes",
		ariaLabel: "Save profile",
		computedStyle: {},
		...overrides,
	};
}

function submitPayload(session: BrowserAnnotationSession): BrowserAnnotationSubmitPayload {
	return {
		viewId: "42:sess-1",
		tabId: "t1",
		pageKey: session.page.url,
		sessionToken: "annotation-session-1",
		session,
	};
}

describe("createBrowserAnnotationContext", () => {
	it("captures bounded DOM context for the selected element", () => {
		document.body.innerHTML = `<button id="save" class="primary cta" aria-label="Save profile" style="font-size:18px;font-weight:700">Save changes</button>`;
		const button = document.querySelector<HTMLButtonElement>("#save")!;
		button.getBoundingClientRect = vi.fn(() => ({
			x: 16, y: 24, width: 140, height: 36, top: 24, right: 156, bottom: 60, left: 16,
			toJSON: () => ({}),
		}));

		const captured = createBrowserAnnotationContext(button);

		expect(captured.selector).toBe("button#save");
		expect(captured.classes).toEqual(["primary", "cta"]);
		expect(captured.size).toEqual({ width: 140, height: 36 });
		expect(captured.rect).toEqual({ x: 16, y: 24, width: 140, height: 36 });
		expect(captured.visibleText).toBe("Save changes");
		expect(captured.ariaLabel).toBe("Save profile");
		expect(captured.computedStyle.fontSize).toBe("18px");
	});
});

describe("formatBrowserAnnotationMessage", () => {
	it("builds a compact, structured handoff with precise visual changes", () => {
		const session = createBrowserAnnotationSession("https://www.google.com/", "Google");
		session.annotations.push({
			id: "annotation-1",
			number: 1,
			kind: "adjustment",
			body: "Try this treatment.",
			target: { context: context({ url: "https://www.google.com/", title: "Google" }) },
			adjustments: [
				{ property: "color", previousValue: "rgb(0, 0, 0)", value: "#e34b63" },
				{ property: "width", previousValue: "140px", value: "89px" },
			],
			createdAt: "2026-09-10T12:00:00.000Z",
			updatedAt: "2026-09-10T12:00:00.000Z",
		});

		const message = formatBrowserAnnotationMessage(submitPayload(session));

		expect(message).toMatch(/^<browser_annotations>\n/);
		expect(message).toContain("Browser feedback");
		expect(message).toContain('Comment: Try this treatment.');
		expect(message).toContain('- Text color: "rgb(0, 0, 0)" → "#e34b63"');
		expect(message).toContain('- Width: "140px" → "89px"');
		expect(message).toContain("Visual adjustments are already previewed in AO's shared browser");
		expect(message).toMatch(/\n<\/browser_annotations>$/);
		expect(message).not.toContain("Browser handoff:");
		expect(message).not.toContain("Do not");
	});

	it("keeps comments intent-sensitive and references staged screenshots", () => {
		const session = createBrowserAnnotationSession("http://localhost:5173/settings", "Settings");
		session.annotations.push({
			id: "annotation-1",
			number: 1,
			kind: "comment",
			body: "Why is this disabled?",
			target: { context: context() },
			adjustments: [],
			createdAt: "2026-09-10T12:00:00.000Z",
			updatedAt: "2026-09-10T12:00:00.000Z",
		});

		const message = formatBrowserAnnotationMessage(submitPayload(session), { screenshotPaths: [".ao/attachments/example.png"] });

		expect(message).toContain("Comment: Why is this disabled?");
		expect(message).toContain("Address the feedback below according to its wording");
		expect(message).toContain("Reference screenshots:");
		expect(message).toContain(".ao/attachments/example.png");
	});

	it("does not invent an empty note for an adjustment", () => {
		const session = createBrowserAnnotationSession("http://localhost:5173/settings", "Settings");
		session.annotations.push({
			id: "annotation-1",
			number: 1,
			kind: "adjustment",
			body: "",
			target: { context: context() },
			adjustments: [{ property: "height", previousValue: "36px", value: "48px" }],
			createdAt: "2026-09-10T12:00:00.000Z",
			updatedAt: "2026-09-10T12:00:00.000Z",
		});

		const message = formatBrowserAnnotationMessage(submitPayload(session));

		expect(message).not.toContain("Comment:");
		expect(message).not.toContain("(empty)");
	});

	it("parses the transport into transcript-safe display data", () => {
		const session = createBrowserAnnotationSession("http://localhost:5173/settings", "Settings");
		session.annotations.push({
			id: "annotation-1",
			number: 1,
			kind: "adjustment",
			body: "Make the primary action clearer.",
			target: { context: context() },
			adjustments: [
				{ property: "color", previousValue: "black", value: "white" },
				{ property: "backgroundColor", previousValue: "white", value: "blue" },
			],
			createdAt: "2026-09-10T12:00:00.000Z",
			updatedAt: "2026-09-10T12:00:00.000Z",
		});

		const parsed = parseBrowserAnnotationMessage(
			formatBrowserAnnotationMessage(submitPayload(session), { screenshotPaths: [".ao/attachments/example.png"] }),
		);

		expect(parsed).toEqual({
			pageTitle: "Settings",
			pageUrl: "http://localhost:5173/settings",
			items: [
				{
					number: 1,
					kind: "adjustment",
					target: "button#save.primary",
					comment: "Make the primary action clearer.",
					changes: ['Text color: "black" → "white"', 'Background: "white" → "blue"'],
				},
			],
			screenshotCount: 1,
		});
	});

	it("keeps the generated handoff below the daemon message limit", () => {
		const session = createBrowserAnnotationSession("http://localhost:5173/", "Preview");
		for (let index = 1; index <= 20; index += 1) {
			session.annotations.push({
				id: `annotation-${index}`,
				number: index,
				kind: "comment",
				body: "Change this. ".repeat(800),
				target: { context: context({ visibleText: "Long visible text ".repeat(500) }) },
				adjustments: [],
				createdAt: "2026-09-10T12:00:00.000Z",
				updatedAt: "2026-09-10T12:00:00.000Z",
			});
		}

		const message = formatBrowserAnnotationMessage(submitPayload(session));

		expect(message.length).toBeLessThanOrEqual(MAX_BROWSER_ANNOTATION_MESSAGE_LENGTH);
		expect(message).toContain("[truncated]");
		expect(message).toMatch(/\n<\/browser_annotations>$/);
		expect(parseBrowserAnnotationMessage(message)).not.toBeNull();
	});
});
