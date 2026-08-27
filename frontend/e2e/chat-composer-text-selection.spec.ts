import { expect, test } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";

const projectId = "chat-composer-selection";
const sessionId = "chat-composer-selection-worker";
const draft =
	"Select this existing chat draft across its wrapped text so an earlier instruction can be replaced precisely without retyping the entire message.";
const themeStyles = [
	"orchestrate",
	"github",
	"catppuccin",
	"dracula",
	"tokyo-night",
	"rose-pine",
	"nord",
	"gruvbox",
	"solarized",
] as const;

test("typed chat composer text is visibly selected with a pointer drag @T0", async ({ page }) => {
	await page.emulateMedia({ reducedMotion: "reduce" });
	await installFakeAgent(page, {
		projectId,
		projectName: projectId,
		workers: [{ id: sessionId, provider: "codex", title: "Composer selection", mode: "chat" }],
	});
	await page.route("http://127.0.0.1:8080/api/v1/**", async (route) => {
		const pathname = new URL(route.request().url()).pathname;
		if (pathname === `/api/v1/projects/${projectId}`) {
			await route.fulfill({
				json: {
					status: "ok",
					project: { id: projectId, agent: "codex", config: { worker: { agent: "codex" } } },
				},
			});
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation`) {
			const completedAt = "2026-08-26T06:00:00Z";
			await route.fulfill({
				json: {
					conversationId: "conversation-chat-composer-selection",
					sessionId,
					harness: "codex",
					mode: "chat",
					controller: "ready",
					latestSequence: 1,
					oldestSequence: 1,
					hasMoreBefore: false,
					turns: [
						{
							id: "turn-1",
							state: "completed",
							requestedAt: completedAt,
							startedAt: completedAt,
							completedAt,
						},
					],
					messages: [
						{
							kind: "message",
							id: "message-1",
							turnId: "turn-1",
							sequence: 1,
							revision: 0,
							role: "user",
							origin: "human",
							text: "An existing conversation",
							streaming: false,
							createdAt: completedAt,
						},
					],
					activities: [],
					settings: {},
				},
			});
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation/models`) {
			await route.fulfill({ json: { models: [], selected: {} } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/conversation/skills`) {
			await route.fulfill({ json: { skills: [] } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/workspace/files`) {
			await route.fulfill({ json: { files: [], truncated: false } });
			return;
		}
		if (pathname === `/api/v1/sessions/${sessionId}/interface-transition`) {
			await route.fulfill({ json: { supported: true, targetMode: "tui" } });
			return;
		}
		await route.fulfill({ json: { status: "ok" } });
	});

	await page.goto(`/#/projects/${projectId}/sessions/${sessionId}`);
	const composer = page.getByRole("combobox", { name: "Message the agent" });
	await expect(composer).toBeVisible();
	await composer.fill(draft);
	await expect(composer).toHaveText(draft);

	const textBounds = await composer.evaluate((element) => {
		const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
		const textNodes: Text[] = [];
		for (let node = walker.nextNode(); node; node = walker.nextNode()) {
			if (node instanceof Text && node.data.length > 0) textNodes.push(node);
		}
		const first = textNodes.at(0);
		const last = textNodes.at(-1);
		if (!first || !last) throw new Error("composer text nodes not found");
		const firstRange = document.createRange();
		firstRange.setStart(first, 0);
		firstRange.setEnd(first, 1);
		const lastRange = document.createRange();
		lastRange.setStart(last, last.data.length - 1);
		lastRange.setEnd(last, last.data.length);
		const firstRect = firstRange.getBoundingClientRect();
		const lastRect = lastRange.getBoundingClientRect();
		return {
			startX: lastRect.right - 1,
			startY: lastRect.top + lastRect.height / 2,
			endX: firstRect.left + 1,
			endY: firstRect.top + firstRect.height / 2,
		};
	});
	await page.mouse.move(textBounds.startX, textBounds.startY);
	await page.mouse.down();
	await page.mouse.move(textBounds.endX, textBounds.endY, { steps: 12 });
	await page.mouse.up();

	const selection = await page.evaluate(() => ({
		text: window.getSelection()?.toString() ?? "",
		collapsed: window.getSelection()?.isCollapsed ?? true,
	}));
	expect(selection.collapsed).toBe(false);
	expect(draft).toContain(selection.text.trim());
	expect(selection.text.trim().length).toBeGreaterThan(draft.length / 2);

	const selectionContrasts = await composer.evaluate((element, styles) => {
		function rgba(color: string): [number, number, number, number] {
			const canvas = document.createElement("canvas");
			canvas.width = 1;
			canvas.height = 1;
			const context = canvas.getContext("2d");
			if (!context) throw new Error("2D canvas unavailable");
			context.clearRect(0, 0, 1, 1);
			context.fillStyle = color;
			context.fillRect(0, 0, 1, 1);
			return [...context.getImageData(0, 0, 1, 1).data].map((channel) => channel / 255) as [
				number,
				number,
				number,
				number,
			];
		}
		function luminance([red, green, blue]: number[]): number {
			const linear = [red, green, blue].map((channel) =>
				channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
			);
			return 0.2126 * linear[0]! + 0.7152 * linear[1]! + 0.0722 * linear[2]!;
		}

		const surface = element.closest("form") ?? element;
		if (surface instanceof HTMLElement) surface.style.transition = "none";
		return styles.flatMap((style) =>
			(["dark", "light"] as const).map((appearance) => {
				document.documentElement.dataset.theme = appearance;
				if (style === "orchestrate") delete document.documentElement.dataset.styleTheme;
				else document.documentElement.dataset.styleTheme = style;

				const selectionBackground = getComputedStyle(element, "::selection").backgroundColor;
				const selectionForeground = getComputedStyle(element, "::selection").color;
				const surfaceBackground = getComputedStyle(surface).backgroundColor;
				const selectionColor = rgba(selectionBackground);
				const textColor = rgba(selectionForeground);
				const surfaceColor = rgba(surfaceBackground);
				const compositedBackground = selectionColor.slice(0, 3).map(
					(channel, index) => channel * selectionColor[3] + surfaceColor[index]! * (1 - selectionColor[3]),
				);
				const compositedText = textColor
					.slice(0, 3)
					.map(
						(channel, index) =>
							channel * textColor[3] + compositedBackground[index]! * (1 - textColor[3]),
					);
				const highlightLuminance = luminance(compositedBackground);
				const surfaceLuminance = luminance(surfaceColor);
				const textLuminance = luminance(compositedText);
				return {
					style,
					appearance,
					selectionBackground,
					selectionForeground,
					surfaceBackground,
					contrast:
						(Math.max(highlightLuminance, surfaceLuminance) + 0.05) /
						(Math.min(highlightLuminance, surfaceLuminance) + 0.05),
					textContrast:
						(Math.max(textLuminance, highlightLuminance) + 0.05) /
						(Math.min(textLuminance, highlightLuminance) + 0.05),
				};
			}),
		);
	}, themeStyles);
	expect(selectionContrasts).toHaveLength(themeStyles.length * 2);
	const lowestHighlightContrast = selectionContrasts.reduce((lowest, current) =>
		current.contrast < lowest.contrast ? current : lowest,
	);
	expect(
		lowestHighlightContrast.contrast,
		`${lowestHighlightContrast.style} ${lowestHighlightContrast.appearance} selection contrast (${lowestHighlightContrast.selectionBackground} on ${lowestHighlightContrast.surfaceBackground})`,
	).toBeGreaterThanOrEqual(1.5);
	const lowestTextContrast = selectionContrasts.reduce((lowest, current) =>
		current.textContrast < lowest.textContrast ? current : lowest,
	);
	expect(
		lowestTextContrast.textContrast,
		`${lowestTextContrast.style} ${lowestTextContrast.appearance} selected text contrast (${lowestTextContrast.selectionForeground} on ${lowestTextContrast.selectionBackground})`,
	).toBeGreaterThanOrEqual(4.5);
});
