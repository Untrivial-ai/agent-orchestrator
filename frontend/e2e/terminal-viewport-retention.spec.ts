import { expect, test, type Locator, type Page } from "@playwright/test";
import { installFakeAgent } from "./support/fake-bridge";
import {
	installFakeTerminalMux,
	type FakeTerminalMuxStats,
} from "./support/fake-terminal-mux";

const sessionA = {
	id: "viewport-a",
	title: "Viewport A",
	activity: "active" as const,
};
const sessionB = {
	id: "viewport-b",
	title: "Viewport B",
	activity: "active" as const,
};
const sessionC = {
	id: "viewport-c",
	title: "Viewport C",
	activity: "active" as const,
};
const sessionD = {
	id: "viewport-d",
	title: "Viewport D",
	activity: "active" as const,
};
const sessionE = {
	id: "viewport-e",
	title: "Viewport E",
	activity: "active" as const,
};
const sessionF = {
	id: "viewport-f",
	title: "Viewport F",
	activity: "active" as const,
};
const allSessions = [sessionA, sessionB, sessionC, sessionD, sessionE, sessionF];
const handleA = `${sessionA.id}/terminal_0`;
const handleB = `${sessionB.id}/terminal_0`;
const handleC = `${sessionC.id}/terminal_0`;
const handleD = `${sessionD.id}/terminal_0`;
const handleE = `${sessionE.id}/terminal_0`;
const handleF = `${sessionF.id}/terminal_0`;
const orchestratorHandle = "fake-proj-orchestrator/terminal_0";

const longReplay = [
	"\x1b[?25l",
	...Array.from({ length: 320 }, (_, index) => `A retained history ${String(index).padStart(3, "0")}`),
].join("\r\n");
const shortReplay = "\x1b[?25lB short replay\r\nB tail";

type TestXterm = {
	buffer: {
		active: {
			baseY: number;
			cursorY: number;
			getLine: (line: number) => { translateToString: (trimRight?: boolean) => string } | undefined;
			viewportY: number;
		};
	};
	getSelection: () => string;
	options: { fontSize: number };
	selectLines: (start: number, end: number) => void;
};

type RevealSample = {
	atBottom: boolean;
	domBottomDelta: number;
	fontSize: number;
	scrollTop: number;
	viewportY: number;
};

type RevealObserverWindow = Window & {
	__aoRevealFrame?: number;
	__aoRevealFrames?: string[];
	__aoRevealObserver?: MutationObserver;
	__aoRevealSamples?: RevealSample[];
};

function activeTerminal(page: Page): Locator {
	return page.getByTestId("session-terminal-slot").getByTestId("session-terminal");
}

function activeViewport(page: Page): Locator {
	return activeTerminal(page).locator(".xterm-viewport");
}

function activeBufferAtBottom(page: Page): Promise<boolean> {
	return activeTerminal(page)
		.locator("[aria-label='Session terminal']")
		.evaluate((element) => {
			const terminal = (element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest;
			return Boolean(terminal && terminal.buffer.active.viewportY === terminal.buffer.active.baseY);
		});
}

async function muxStats(page: Page): Promise<FakeTerminalMuxStats> {
	return page.evaluate(() => window.__aoFakeTerminalMux!.stats());
}

async function observeNextReveal(container: Locator): Promise<void> {
	await container.evaluate((element) => {
		const state = window as RevealObserverWindow;
		state.__aoRevealObserver?.disconnect();
		if (state.__aoRevealFrame !== undefined) {
			cancelAnimationFrame(state.__aoRevealFrame);
		}
		const samples: RevealSample[] = [];
		const framePhases: string[] = [];
		state.__aoRevealSamples = samples;
		state.__aoRevealFrames = framePhases;
		const captureVisibleSample = () => {
			const host = element.querySelector<HTMLElement>("[aria-label='Session terminal']");
			const viewport = host?.querySelector<HTMLElement>(".xterm-viewport");
			const terminal = (host as (HTMLElement & { __aoXtermForTest?: TestXterm }) | null)
				?.__aoXtermForTest;
			if (
				(element as HTMLElement).style.visibility === "hidden" ||
				(element as HTMLElement).dataset.terminalActivationPhase !== "visible" ||
				!viewport ||
				!terminal
			) {
				return;
			}
			samples.push({
				atBottom: terminal.buffer.active.viewportY === terminal.buffer.active.baseY,
				domBottomDelta: viewport.scrollHeight - viewport.clientHeight - viewport.scrollTop,
				fontSize: terminal.options.fontSize,
				scrollTop: viewport.scrollTop,
				viewportY: terminal.buffer.active.viewportY,
			});
		};
		const sampleFrame = () => {
			const phase =
				(element as HTMLElement).dataset.terminalActivationPhase ?? "missing";
			framePhases.push(phase);
			if (phase === "visible") {
				captureVisibleSample();
				state.__aoRevealFrame = undefined;
				return;
			}
			state.__aoRevealFrame = requestAnimationFrame(sampleFrame);
		};
		state.__aoRevealFrame = requestAnimationFrame(sampleFrame);
		const observer = new MutationObserver(() => {
			captureVisibleSample();
		});
		observer.observe(element, {
			attributes: true,
			attributeFilter: ["aria-hidden", "data-terminal-activation-phase", "style"],
		});
		state.__aoRevealObserver = observer;
	});
}

async function revealSamples(page: Page): Promise<RevealSample[]> {
	return page.evaluate(() => (window as RevealObserverWindow).__aoRevealSamples ?? []);
}

async function revealFramePhases(page: Page): Promise<string[]> {
	return page.evaluate(() => (window as RevealObserverWindow).__aoRevealFrames ?? []);
}

async function settledRevealSamples(page: Page): Promise<RevealSample[]> {
	await expect.poll(async () => (await revealSamples(page)).length).toBeGreaterThan(0);
	return revealSamples(page);
}

async function openSession(page: Page, title: string): Promise<void> {
	await page.getByRole("button", { name: `Open ${title}`, exact: true }).click();
	await expect(activeTerminal(page)).toBeVisible();
	await expect(
		page
			.getByTestId("session-terminal-slot")
			.locator("[data-terminal-activation-phase='visible']"),
	).toHaveCount(1);
}

async function installHarness(page: Page): Promise<void> {
	await page.setViewportSize({ width: 1280, height: 800 });
	await installFakeAgent(page, { workers: allSessions });
	await installFakeTerminalMux(page, {
		[handleA]: longReplay,
		[handleB]: shortReplay,
		// C deliberately has no replay bytes.
		[handleC]: "",
		[handleD]: "D short replay",
		[handleE]: "E short replay",
		[handleF]: "F short replay",
		[orchestratorHandle]: "Orchestrator ready",
	});
	await page.goto(`/#/projects/fake-proj/sessions/${sessionA.id}`);
	await expect(activeTerminal(page)).toBeVisible();
	await expect(page.getByTestId("terminal-replay-cover")).toHaveCount(0);
}

test.describe("retained terminal viewport", () => {
	test("retains the first of six live sessions with zero reopen and reveals its latest output", async ({
		page,
	}) => {
		await installHarness(page);
		const viewport = activeViewport(page);
		await expect
			.poll(() => viewport.evaluate((element) => element.scrollHeight - element.clientHeight))
			.toBeGreaterThan(500);

		await activeTerminal(page)
			.locator("[aria-label='Session terminal']")
			.evaluate((element) => element.setAttribute("data-six-session-instance", "a"));

		for (const session of [sessionB, sessionC, sessionD, sessionE, sessionF]) {
			await openSession(page, session.title);
		}
		await expect(page.locator("[data-terminal-cache-key]")).toHaveCount(6);
		const parkedA = page.locator(`[data-terminal-cache-key^="session:${sessionA.id}:worker|"]`);
		await expect(parkedA).toHaveAttribute("data-terminal-activation-phase", "parked");
		await page.evaluate(
			(handleId) =>
				window.__aoFakeTerminalMux!.emit(
					handleId,
					"\r\n" + Array.from({ length: 20 }, (_, index) => `A six-session hidden ${index}`).join("\r\n"),
				),
			handleA,
		);

		await observeNextReveal(parkedA);

		await openSession(page, sessionA.title);
		await expect(activeTerminal(page).locator("[aria-label='Session terminal']")).toHaveAttribute(
			"data-six-session-instance",
			"a",
		);
		await expect.poll(() => activeBufferAtBottom(page)).toBe(true);
		const firstRevealSamples = await settledRevealSamples(page);
		for (const sample of firstRevealSamples) {
			expect(sample.atBottom).toBe(true);
			expect(Math.round(sample.domBottomDelta)).toBeLessThanOrEqual(1);
		}
		const firstRevealFrames = await revealFramePhases(page);
		expect(firstRevealFrames).toContain("revealed");
		expect(
			firstRevealFrames.filter((phase) => phase === "revealed").length,
		).toBeGreaterThanOrEqual(2);
		expect(firstRevealFrames.indexOf("revealed")).toBeLessThan(
			firstRevealFrames.indexOf("visible"),
		);
		const sixStats = await muxStats(page);
		for (const handle of [handleA, handleB, handleC, handleD, handleE, handleF]) {
			expect(sixStats.opens[handle]).toBe(1);
			expect(sixStats.closes[handle] ?? 0).toBe(0);
		}
		expect(sixStats.sockets).toBe(1);
		expect(sixStats.writers).toBe(6);

		// A real width and font-size change reflows xterm's buffer. Preparation
		// must finish that reflow at the latest output before revealing the pane.
		await openSession(page, sessionB.title);
		const beforeParkedGridChange = await muxStats(page);
		const aResizesBeforeReturn = beforeParkedGridChange.resizes[handleA]?.length ?? 0;
		const bResizesBeforeGridChange = beforeParkedGridChange.resizes[handleB]?.length ?? 0;
		await page.setViewportSize({ width: 1040, height: 680 });
		await expect
			.poll(async () => (await muxStats(page)).resizes[handleB]?.length ?? 0)
			.toBeGreaterThan(bResizesBeforeGridChange);
		const fontSizeBefore = await activeTerminal(page)
			.locator("[aria-label='Session terminal']")
			.evaluate((element) =>
				(element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest!.options.fontSize,
			);
		await activeTerminal(page).locator(".xterm-helper-textarea").focus();
		await page.keyboard.press("ControlOrMeta+=");
		const fontSizeAfter = fontSizeBefore + 1;
		expect((await muxStats(page)).resizes[handleA]?.length ?? 0).toBe(aResizesBeforeReturn);
		await observeNextReveal(parkedA);
		await openSession(page, sessionA.title);
		await expect
			.poll(async () => (await muxStats(page)).resizes[handleA]?.length ?? 0)
			.toBeGreaterThan(aResizesBeforeReturn);
		const activationResizePhases = (await muxStats(page)).resizePhases[handleA]?.slice(
			aResizesBeforeReturn,
		);
		expect(activationResizePhases).not.toContain("parked");
		expect(activationResizePhases).not.toContain("preparing");
		expect(activationResizePhases).not.toContain("ready");
		expect(activationResizePhases).toContain("visible");
		const resizedRevealSamples = await settledRevealSamples(page);
		for (const sample of resizedRevealSamples) {
			expect(sample.fontSize).toBe(fontSizeAfter);
			expect(sample.atBottom).toBe(true);
			expect(Math.round(sample.domBottomDelta)).toBeLessThanOrEqual(1);
		}
		expect((await muxStats(page)).opens[handleA]).toBe(1);
		await page.setViewportSize({ width: 1280, height: 800 });

		// Frame-sensitive bottom-follow case: hidden output leaves xterm's
		// offscreen DOM scrollTop stale even though its canonical buffer is at
		// bottom. The reveal observer must never see that stale frame.
		await activeTerminal(page).locator(".xterm-screen").hover();
		await page.mouse.wheel(0, 100_000);
		await expect.poll(() => activeBufferAtBottom(page)).toBe(true);
		await openSession(page, sessionB.title);
		await page.evaluate(
			(handleId) => window.__aoFakeTerminalMux!.emit(handleId, "\r\nA frame-sensitive bottom output"),
			handleA,
		);
		await observeNextReveal(parkedA);
		await openSession(page, sessionA.title);
		const bottomRevealSamples = await settledRevealSamples(page);
		for (const sample of bottomRevealSamples) {
			expect(sample.atBottom).toBe(true);
			expect(Math.round(sample.domBottomDelta)).toBeLessThanOrEqual(1);
		}
		expect((await muxStats(page)).opens[handleA]).toBe(1);
	});

	test("suppresses parked resize and scroll storms during rapid A-B-A switching", async ({
		page,
	}) => {
		await installHarness(page);
		const viewport = activeViewport(page);
		await expect
			.poll(() => viewport.evaluate((element) => element.scrollHeight - element.clientHeight))
			.toBeGreaterThan(500);

		await activeTerminal(page).locator(".xterm-screen").hover();
		await page.mouse.wheel(0, -1_400);
		await expect
			.poll(async () => {
				const metrics = await viewport.evaluate((element) => ({
					max: element.scrollHeight - element.clientHeight,
					top: element.scrollTop,
				}));
				return metrics.top < metrics.max - 100 ? metrics.top : -1;
			})
			.toBeGreaterThanOrEqual(0);
		await viewport.evaluate((element) => {
			(element as HTMLElement & { __aoScrollEvents?: number }).__aoScrollEvents = 0;
			element.addEventListener("scroll", () => {
				const tracked = element as HTMLElement & { __aoScrollEvents?: number };
				tracked.__aoScrollEvents = (tracked.__aoScrollEvents ?? 0) + 1;
			});
		});

		await openSession(page, sessionB.title);
		const parkedA = page.locator(`[data-terminal-cache-key^="session:${sessionA.id}:worker|"]`);
		await expect(parkedA).toHaveAttribute("aria-hidden", "true");
		expect(await parkedA.evaluate((element) => (element as HTMLElement).inert)).toBe(true);

		const beforeResize = await muxStats(page);
		await page.setViewportSize({ width: 1040, height: 680 });
		await expect
			.poll(async () => (await muxStats(page)).resizes[handleB]?.length ?? 0)
			.toBeGreaterThan(beforeResize.resizes[handleB]?.length ?? 0);
		expect((await muxStats(page)).resizes[handleA]?.length ?? 0).toBe(
			beforeResize.resizes[handleA]?.length ?? 0,
		);
		await page.setViewportSize({ width: 1280, height: 800 });

		await page.evaluate((handleId) => {
			window.__aoFakeTerminalMux!.emit(
				handleId,
				"\r\n" + Array.from({ length: 30 }, (_, index) => `A hidden output ${index}`).join("\r\n"),
			);
		}, handleA);
		const aResizesBeforeReturn = (await muxStats(page)).resizes[handleA]?.length ?? 0;

		// Exercise the same rapid return seen in 04.30.04, including output in
		// the return commit. The retained terminal returns to its already-published
		// grid, so activation must not manufacture another PTY/SIGWINCH repaint.
		await openSession(page, sessionA.title);
		await expect
			.poll(async () => (await muxStats(page)).resizes[handleA]?.length ?? 0)
			.toBe(aResizesBeforeReturn);
		const afterReturn = await muxStats(page);
		expect(afterReturn.resizePhases[handleA]?.slice(aResizesBeforeReturn) ?? []).toEqual([]);
		await page.evaluate((handleId) => {
			window.__aoFakeTerminalMux!.emit(handleId, "\r\nA output during return");
		}, handleA);
		await expect.poll(() => activeBufferAtBottom(page)).toBe(true);
		await expect
			.poll(() => activeViewport(page).evaluate((element) => {
				const max = Math.max(0, element.scrollHeight - element.clientHeight);
				return Math.round(max - element.scrollTop);
			}))
			.toBeLessThanOrEqual(1);
		expect(
			await activeViewport(page).evaluate(
				(element) => (element as HTMLElement & { __aoScrollEvents?: number }).__aoScrollEvents ?? 0,
			),
		).toBeLessThanOrEqual(3);

		const stats = await muxStats(page);
		expect(stats.opens[handleA]).toBe(1);
		expect(stats.closes[handleA] ?? 0).toBe(0);
		await expect(page.locator("[data-terminal-cache-key]:not([aria-hidden='true'])")).toHaveCount(1);
	});

	test("does not republish stable grids across warmed orchestrator-worker switches", async ({
		page,
	}) => {
		await installHarness(page);
		await openSession(page, "Project orchestrator");
		await openSession(page, sessionA.title);

		const before = await muxStats(page);
		const workerResizes = before.resizes[handleA]?.length ?? 0;
		const orchestratorResizes = before.resizes[orchestratorHandle]?.length ?? 0;

		for (let index = 0; index < 3; index += 1) {
			await openSession(page, "Project orchestrator");
			await openSession(page, sessionA.title);
		}

		const after = await muxStats(page);
		expect(after.resizes[handleA]?.length ?? 0).toBe(workerResizes);
		expect(after.resizes[orchestratorHandle]?.length ?? 0).toBe(orchestratorResizes);
		expect(after.opens[handleA]).toBe(1);
		expect(after.opens[orchestratorHandle]).toBe(1);
	});

	test("handles empty and short replay, blocks hidden input, and retains xterm through reconnect", async ({ page }) => {
		await installHarness(page);
		const originalViewport = activeViewport(page);
		await originalViewport.evaluate((element) => element.setAttribute("data-reconnect-instance", "a"));
		await activeTerminal(page).locator(".xterm-screen").hover();
		await page.mouse.wheel(0, -1_400);
		await expect.poll(() => activeBufferAtBottom(page)).toBe(false);
		const retainedSelection = await activeTerminal(page)
			.locator("[aria-label='Session terminal']")
			.evaluate((element) => {
				const terminal = (element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest!;
				terminal.selectLines(125, 125);
				return terminal.getSelection();
			});

		// A hidden terminal stays connected for output but cannot accept
		// keyboard/paste input; only the active B pane receives it.
		await openSession(page, sessionB.title);
		await expect(page.getByTestId("terminal-replay-cover")).toHaveCount(0);
		const inputsBefore = (await muxStats(page)).inputs[handleA]?.length ?? 0;
		await activeTerminal(page).locator(".xterm-helper-textarea").focus();
		await page.keyboard.type("b-input");
		await expect
			.poll(async () => (await muxStats(page)).inputs[handleB]?.join("") ?? "")
			.toContain("b-input");
		expect((await muxStats(page)).inputs[handleA]?.length ?? 0).toBe(inputsBefore);

		await openSession(page, sessionC.title);
		await expect(page.getByTestId("terminal-replay-cover")).toHaveCount(0);
		expect((await muxStats(page)).opens[handleC]).toBe(1);

		const beforeReconnect = await muxStats(page);
		await page.evaluate((handleId) => window.__aoFakeTerminalMux!.disconnect(handleId), handleA);
		await expect(activeTerminal(page).getByText("Terminal disconnected — reattaching…")).toBeVisible();
		await expect.poll(async () => (await muxStats(page)).opens[handleA] ?? 0).toBe(2);
		await expect.poll(async () => (await muxStats(page)).opens[handleB] ?? 0).toBe(2);
		await expect.poll(async () => (await muxStats(page)).opens[handleC] ?? 0).toBe(2);
		const hiddenReconnect = await muxStats(page);
		for (const parkedHandle of [handleA, handleB]) {
			expect(hiddenReconnect.openFrames[parkedHandle]?.[1]).toMatchObject({
				cols: 0,
				phase: "parked",
				rows: 0,
			});
			expect(hiddenReconnect.resizes[parkedHandle]?.length ?? 0).toBe(
				beforeReconnect.resizes[parkedHandle]?.length ?? 0,
			);
		}
		expect(hiddenReconnect.openFrames[handleC]?.[1]?.phase).toBe("visible");
		expect(hiddenReconnect.openFrames[handleC]?.[1]?.cols).toBeGreaterThan(0);
		expect(hiddenReconnect.openFrames[handleC]?.[1]?.rows).toBeGreaterThan(0);

		const aResizesBeforeReturn = hiddenReconnect.resizes[handleA]?.length ?? 0;
		await openSession(page, sessionA.title);
		await expect
			.poll(async () => (await muxStats(page)).resizes[handleA]?.length ?? 0)
			.toBeGreaterThan(aResizesBeforeReturn);
		const returnStats = await muxStats(page);
		const returnResizeIndex = returnStats.resizes[handleA].length - 1;
		expect(returnStats.resizes[handleA][returnResizeIndex]).toMatchObject({
			cols: expect.any(Number),
			rows: expect.any(Number),
		});
		expect(returnStats.resizes[handleA][returnResizeIndex].cols).toBeGreaterThan(0);
		expect(returnStats.resizes[handleA][returnResizeIndex].rows).toBeGreaterThan(0);
		expect(returnStats.resizePhases[handleA][returnResizeIndex]).toBe("visible");
		await expect(activeViewport(page)).toHaveAttribute("data-reconnect-instance", "a");
		await expect(page.getByTestId("terminal-replay-cover")).toHaveCount(0);
		await expect(activeViewport(page)).toHaveAttribute("data-reconnect-instance", "a");
		const afterReconnectSelection = await activeTerminal(page)
			.locator("[aria-label='Session terminal']")
			.evaluate((element) => {
				const terminal = (element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest!;
				return terminal.getSelection();
			});
		await expect.poll(() => activeBufferAtBottom(page)).toBe(true);
		expect(afterReconnectSelection).toBe(retainedSelection);
		expect((await muxStats(page)).opens[handleA]).toBe(2);
		const reconnectStats = await muxStats(page);
		expect(reconnectStats.writers).toBe(3); // Exactly one writer for each retained A/B/C attachment.
		expect(reconnectStats.sockets).toBe(1);

		// Authoritative session removal is a hard lifecycle boundary, not an LRU
		// event: every retained renderer and mux must be disposed.
		await page.evaluate((ids) => {
			for (const id of ids) window.__aoFakeAgent!.removeWorker(id);
		}, [sessionA.id, sessionB.id, sessionC.id]);
		await expect.poll(async () => (await muxStats(page)).writers).toBe(0);
		await expect.poll(async () => (await muxStats(page)).sockets).toBe(0);
	});

	test("reveals the latest output after hidden output evicts old scrollback", async ({
		page,
	}) => {
		await installHarness(page);
		await activeTerminal(page).locator(".xterm-screen").hover();
		await page.mouse.wheel(0, -100_000);
		await expect.poll(() => activeBufferAtBottom(page)).toBe(false);

		await openSession(page, sessionB.title);
		const parkedA = page.locator(`[data-terminal-cache-key^="session:${sessionA.id}:worker|"]`);
		await page.evaluate((handleId) => {
			window.__aoFakeTerminalMux!.emit(
				handleId,
				"\r\n" +
					Array.from({ length: 5_300 }, (_, index) => `A eviction output ${String(index).padStart(4, "0")}`).join(
						"\r\n",
					),
			);
		}, handleA);
		await expect
			.poll(() =>
				parkedA.locator("[aria-label='Session terminal']").evaluate((element) => {
					const terminal = (element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest!;
					return terminal.buffer.active.baseY;
				}),
			)
			.toBeGreaterThanOrEqual(5_000);

		await observeNextReveal(parkedA);
		await openSession(page, sessionA.title);
		const restored = await activeTerminal(page)
			.locator("[aria-label='Session terminal']")
			.evaluate((element) => {
				const terminal = (element as HTMLElement & { __aoXtermForTest?: TestXterm }).__aoXtermForTest!;
				return {
					baseY: terminal.buffer.active.baseY,
					latestLine: terminal.buffer.active
						.getLine(terminal.buffer.active.baseY + terminal.buffer.active.cursorY)
						?.translateToString(true),
					viewportY: terminal.buffer.active.viewportY,
				};
			});
		expect(restored.viewportY).toBe(restored.baseY);
		expect(restored.latestLine).toContain("A eviction output 5299");
		const samples = await revealSamples(page);
		expect(samples.length).toBeGreaterThan(0);
		for (const sample of samples) {
			expect(sample.atBottom).toBe(true);
			expect(Math.round(sample.domBottomDelta)).toBeLessThanOrEqual(1);
		}
	});
});
