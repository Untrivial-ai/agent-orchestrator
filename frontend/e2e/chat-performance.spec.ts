import { expect, test } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { execFileSync } from "node:child_process";
import { installFakeBridge } from "./support/fake-bridge";
import { installFakeAgent } from "./support/fake-bridge";
import type { performanceHarness } from "./performance/harness";

declare global {
	interface Window {
		performanceHarness: typeof performanceHarness;
	}
}

test.describe("live renderer performance workloads", () => {
	test.skip(
		process.env.AO_PERF_BENCH !== "1",
		"Opt-in benchmark; timings are evidence, not hardware-dependent CI assertions",
	);
	test.use({ viewport: { width: 1440, height: 1000 } });
	for (const workload of [
		"commandPalette",
		"settingsDialog",
		"events",
		"streaming",
		"highlighting",
		"history",
		"projectDragChurn",
	] as const) {
		test(workload, async ({ page, browser }, testInfo) => {
			test.setTimeout(120_000);
			await installFakeBridge(page);
			await page.emulateMedia({ reducedMotion: "no-preference" });
			await page.goto("/e2e/performance/harness.html");
			await page.waitForFunction(() => Boolean(window.performanceHarness));
			const result = await page.evaluate(
				async (name): Promise<Record<string, unknown>> =>
					await window.performanceHarness[name](),
				workload,
			);
			if ("exact" in result) expect(result.exact).toBe(true);
			if ("textPreserved" in result) expect(result.textPreserved).toBe(true);
			if ("mountedTurns" in result) expect(result.mountedTurns).toBe(250);
			const label = process.env.AO_PERF_LABEL ?? "sample";
			const record = {
				label,
				commit: execFileSync("git", ["rev-parse", "HEAD"], {
					encoding: "utf8",
				}).trim(),
				workload,
				repeat: testInfo.repeatEachIndex,
				browser: browser.version(),
				node: process.version,
				platform: os.platform(),
				arch: os.arch(),
				cpus: os.cpus().length,
				cpuModel: os.cpus()[0]?.model,
				totalMemoryBytes: os.totalmem(),
				viewport: page.viewportSize(),
				timestamp: new Date().toISOString(),
				result,
			};
			await testInfo.attach("measurements", {
				body: JSON.stringify(record, null, 2),
				contentType: "application/json",
			});
			const output = process.env.AO_PERF_DIR;
			if (output) {
				await mkdir(output, { recursive: true });
				await writeFile(
					path.join(
						output,
						`${label}-${workload}-${testInfo.repeatEachIndex}.json`,
					),
					JSON.stringify(record, null, 2) + "\n",
				);
				if (workload === "history" && testInfo.repeatEachIndex === 0)
					await page.screenshot({
						path: path.join(output, `${label}-history.png`),
					});
			}
		});
	}

	test("file completion", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await installFakeBridge(page);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/e2e/performance/harness.html");
		await page.waitForFunction(() => Boolean(window.performanceHarness));
		const prepared = await page.evaluate(() => window.performanceHarness.prepareFileCompletion());
		await page.getByLabel("Message the agent").pressSequentially("@chatcomposer");
		const result = await page.evaluate(() => window.performanceHarness.finishFileCompletion());
		expect(result.visibleSuggestions).toBe(50);
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "file-completion",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			cpus: os.cpus().length,
			cpuModel: os.cpus()[0]?.model,
			totalMemoryBytes: os.totalmem(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			prepared,
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-file-completion-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("typing beside a long mounted history", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await installFakeBridge(page);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/e2e/performance/harness.html");
		await page.waitForFunction(() => Boolean(window.performanceHarness));
		const prepared = await page.evaluate(() => window.performanceHarness.prepareLongHistoryTyping());
		expect(prepared.mountedTurns).toBe(250);
		await page.getByLabel("Message the agent").pressSequentially("typing beside a long conversation");
		const result = await page.evaluate(() => window.performanceHarness.finishLongHistoryTyping());
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "long-history-typing",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			prepared,
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-long-history-typing-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("command palette interaction", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		const projectId = "perf-palette";
		await installFakeAgent(page, {
			projectId,
			projectName: "Performance palette",
			workers: Array.from({ length: 400 }, (_, index) => ({
				id: `perf-worker-${index}`,
				title: `Performance worker ${index}`,
				status: "working",
			})),
		});
		await page.route("http://127.0.0.1:8080/api/v1/**", (route) => {
			const pathname = new URL(route.request().url()).pathname;
			if (pathname === "/api/v1/agents/readiness") return route.fulfill({ json: { agents: [] } });
			if (pathname === "/api/v1/settings") {
				return route.fulfill({
					json: {
						defaultSessionMode: "tui",
						chatHarnesses: [],
						localEnabled: true,
						cloudEnabled: false,
					},
				});
			}
			return route.fulfill({ json: {} });
		});
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto(`/#/projects/${projectId}`);
		await page.getByRole("button", { name: "New task" }).first().waitFor();

		const cdp = await page.context().newCDPSession(page);
		const complete = new Promise<{ stream?: string }>((resolve) =>
			cdp.once("Tracing.tracingComplete", resolve),
		);
		await cdp.send("Tracing.start", {
			categories: "devtools.timeline,v8.execute,blink",
			transferMode: "ReturnAsStream",
		});
		const startedAt = await page.evaluate(() => performance.now());
		await page.keyboard.press("ControlOrMeta+k");
		await page.getByRole("dialog", { name: "Command palette" }).waitFor();
		const dialogFirstPaintMs = await page.evaluate((start) => new Promise<number>((resolve) => {
			requestAnimationFrame(() => resolve(performance.now() - start));
		}), startedAt);
		await cdp.send("Tracing.end");
		const { stream } = await complete;
		if (!stream) throw new Error("Chrome did not return a trace stream");
		let trace = "";
		for (;;) {
			const chunk = await cdp.send("IO.read", { handle: stream });
			trace += chunk.data;
			if (chunk.eof) break;
		}
		await cdp.send("IO.close", { handle: stream });
		const events = (JSON.parse(trace) as { traceEvents: Array<{
			name?: string;
			ph?: string;
			dur?: number;
			args?: { data?: { type?: string }; elementCount?: number };
		}> }).traceEvents;
		const keydownTasks = events.filter((event) =>
			event.name === "EventDispatch" && event.ph === "X" && event.args?.data?.type === "keydown",
		);
		const layoutUpdates = events.filter((event) => event.name === "UpdateLayoutTree" && event.ph === "X");
		const result = {
			workerCount: 400,
			domElementCount: await page.locator("*").count(),
			dialogFirstPaintMs,
			keydownDurationsMs: keydownTasks.map((event) => (event.dur ?? 0) / 1_000),
			layoutUpdateDurationsMs: layoutUpdates.map((event) => (event.dur ?? 0) / 1_000),
			maxLayoutElementCount: Math.max(...layoutUpdates.map((event) => event.args?.elementCount ?? 0), 0),
		};
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "command-palette-interaction",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-command-palette-interaction-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("inspector interaction", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/#/projects/ao-demo/sessions/demo-working");
		const inspector = page.locator("#inspector");
		await inspector.waitFor();

		const cdp = await page.context().newCDPSession(page);
		const complete = new Promise<{ stream?: string }>((resolve) =>
			cdp.once("Tracing.tracingComplete", resolve),
		);
		await cdp.send("Tracing.start", {
			categories: "devtools.timeline,v8.execute,blink",
			transferMode: "ReturnAsStream",
		});
		const startedAt = await page.evaluate(() => performance.now());
		await page.getByRole("button", { name: "Close inspector panel" }).click();
		await inspector.waitFor({ state: "hidden" });
		const settledMs = await page.evaluate((start) => performance.now() - start, startedAt);
		await cdp.send("Tracing.end");
		const { stream } = await complete;
		if (!stream) throw new Error("Chrome did not return a trace stream");
		let trace = "";
		for (;;) {
			const chunk = await cdp.send("IO.read", { handle: stream });
			trace += chunk.data;
			if (chunk.eof) break;
		}
		await cdp.send("IO.close", { handle: stream });
		const events = (JSON.parse(trace) as { traceEvents: Array<{
			name?: string;
			ph?: string;
			dur?: number;
			args?: { data?: { type?: string }; elementCount?: number };
		}> }).traceEvents;
		const clickTasks = events.filter((event) =>
			event.name === "EventDispatch" && event.ph === "X" && event.args?.data?.type === "click",
		);
		const layoutUpdates = events.filter((event) => event.name === "UpdateLayoutTree" && event.ph === "X");
		const result = {
			domElementCount: await page.locator("*").count(),
			settledMs,
			clickDurationsMs: clickTasks.map((event) => (event.dur ?? 0) / 1_000),
			layoutUpdateDurationsMs: layoutUpdates.map((event) => (event.dur ?? 0) / 1_000),
			maxLayoutElementCount: Math.max(...layoutUpdates.map((event) => event.args?.elementCount ?? 0), 0),
		};
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "inspector-interaction",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-inspector-interaction-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	for (const resize of [
		{
			name: "inspector",
			path: "/#/projects/ao-demo/sessions/demo-working",
			handleTestId: "inspector-resize-handle",
			widthSelector: '[data-slot="inspector-gap"]',
			cssVar: "--ao-inspector-w",
			workload: "inspector-direct-resize",
		},
		{
			name: "left sidebar",
			path: "/",
			handleTestId: "resize-handle",
			widthSelector: '[data-slot="sidebar-gap"]',
			cssVar: "--ao-sidebar-w",
			workload: "sidebar-direct-resize",
		},
	] as const) test(`${resize.name} direct-resize interaction`, async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto(resize.path);
		const handle = page.getByTestId(resize.handleTestId);
		await handle.waitFor();
		const initialWidth = await page.locator(resize.widthSelector).evaluate(
			(element, cssVar) => (element as HTMLElement).style.getPropertyValue(cssVar),
			resize.cssVar,
		);
		const box = await handle.boundingBox();
		if (!box) throw new Error(`${resize.name} resize handle has no bounds`);

		const cdp = await page.context().newCDPSession(page);
		const complete = new Promise<{ stream?: string }>((resolve) =>
			cdp.once("Tracing.tracingComplete", resolve),
		);
		await cdp.send("Tracing.start", {
			categories: "devtools.timeline,v8.execute,blink",
			transferMode: "ReturnAsStream",
		});
		await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
		await page.mouse.down();
		// Mirror a human drag: one small change per display frame, not a burst of
		// synthetic pointer events in one task.
		for (let step = 1; step <= 30; step += 1) {
			await page.mouse.move(box.x + box.width / 2 + step * 3, box.y + box.height / 2);
			await page.waitForTimeout(16);
		}
		await page.mouse.up();
		const finalWidth = await page.locator(resize.widthSelector).evaluate(
			(element, cssVar) => (element as HTMLElement).style.getPropertyValue(cssVar),
			resize.cssVar,
		);
		await cdp.send("Tracing.end");
		const { stream } = await complete;
		if (!stream) throw new Error("Chrome did not return a trace stream");
		let trace = "";
		for (;;) {
			const chunk = await cdp.send("IO.read", { handle: stream });
			trace += chunk.data;
			if (chunk.eof) break;
		}
		await cdp.send("IO.close", { handle: stream });
		const events = (JSON.parse(trace) as { traceEvents: Array<{
			name?: string;
			ph?: string;
			dur?: number;
			args?: { data?: { type?: string }; elementCount?: number };
		}> }).traceEvents;
		const pointerMoves = events.filter((event) =>
			event.name === "EventDispatch" && event.ph === "X" && event.args?.data?.type === "pointermove",
		);
		const layouts = events.filter((event) => event.name === "UpdateLayoutTree" && event.ph === "X");
		expect(pointerMoves.length).toBeGreaterThan(0);
		expect(finalWidth).not.toBe(initialWidth);
		const result = {
			initialWidth,
			finalWidth,
			pointerMoveDurationsMs: pointerMoves.map((event) => (event.dur ?? 0) / 1_000),
			layoutUpdateDurationsMs: layouts.map((event) => (event.dur ?? 0) / 1_000),
			maxLayoutElementCount: Math.max(...layouts.map((event) => event.args?.elementCount ?? 0), 0),
		};
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: resize.workload,
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-${resize.workload}-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("inspector interaction with long timeline", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await installFakeBridge(page);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/e2e/performance/harness.html");
		await page.waitForFunction(() => Boolean(window.performanceHarness));
		const prepared = await page.evaluate(() => window.performanceHarness.prepareInspectorTimeline());
		expect(prepared.mountedTurns).toBe(250);

		const cdp = await page.context().newCDPSession(page);
		const complete = new Promise<{ stream?: string }>((resolve) =>
			cdp.once("Tracing.tracingComplete", resolve),
		);
		await cdp.send("Tracing.start", {
			categories: "devtools.timeline,v8.execute,blink",
			transferMode: "ReturnAsStream",
		});
		const startedAt = await page.evaluate(() => performance.now());
		await page.getByRole("button", { name: "Open inspector for long timeline" }).click();
		await expect(page.getByTestId("chat-conversation-minimap")).toHaveAttribute("aria-hidden", "true");
		const firstPaintMs = await page.evaluate((start) => new Promise<number>((resolve) => {
			requestAnimationFrame(() => resolve(performance.now() - start));
		}), startedAt);
		await cdp.send("Tracing.end");
		const { stream } = await complete;
		if (!stream) throw new Error("Chrome did not return a trace stream");
		let trace = "";
		for (;;) {
			const chunk = await cdp.send("IO.read", { handle: stream });
			trace += chunk.data;
			if (chunk.eof) break;
		}
		await cdp.send("IO.close", { handle: stream });
		const events = (JSON.parse(trace) as { traceEvents: Array<{
			name?: string;
			ph?: string;
			dur?: number;
			args?: { data?: { type?: string }; elementCount?: number };
		}> }).traceEvents;
		const clickTasks = events.filter((event) =>
			event.name === "EventDispatch" && event.ph === "X" && event.args?.data?.type === "click",
		);
		const layoutUpdates = events.filter((event) => event.name === "UpdateLayoutTree" && event.ph === "X");
		const result = {
			...prepared,
			firstPaintMs,
			clickDurationsMs: clickTasks.map((event) => (event.dur ?? 0) / 1_000),
			layoutUpdateDurationsMs: layoutUpdates.map((event) => (event.dur ?? 0) / 1_000),
			maxLayoutElementCount: Math.max(...layoutUpdates.map((event) => event.args?.elementCount ?? 0), 0),
		};
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "inspector-interaction-long-timeline",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-inspector-interaction-long-timeline-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("skill completion", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await installFakeBridge(page);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/e2e/performance/harness.html");
		await page.waitForFunction(() => Boolean(window.performanceHarness));
		const prepared = await page.evaluate(() => window.performanceHarness.prepareSkillCompletion());
		await page.getByLabel("Message the agent").pressSequentially("/chatcomposer");
		const result = await page.evaluate(() => window.performanceHarness.finishSkillCompletion());
		expect(result.visibleSuggestions).toBe(50);
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "skill-completion",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			cpus: os.cpus().length,
			cpuModel: os.cpus()[0]?.model,
			totalMemoryBytes: os.totalmem(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			prepared,
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-skill-completion-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});

	test("local echo", async ({ page, browser }, testInfo) => {
		test.setTimeout(120_000);
		await installFakeBridge(page);
		await page.emulateMedia({ reducedMotion: "no-preference" });
		await page.goto("/e2e/performance/harness.html");
		await page.waitForFunction(() => Boolean(window.performanceHarness));
		const prepared = await page.evaluate(() => window.performanceHarness.prepareLocalEcho());
		await page.getByRole("button", { name: "Benchmark local echo" }).click();
		const result = await page.evaluate(() => window.performanceHarness.finishLocalEcho());
		expect(result.localRowFirstPaintMs, JSON.stringify(result)).not.toBeNull();
		expect(result.httpWasPendingAtFirstPaint).toBe(true);
		const label = process.env.AO_PERF_LABEL ?? "sample";
		const record = {
			label,
			commit: execFileSync("git", ["rev-parse", "HEAD"], { encoding: "utf8" }).trim(),
			workload: "local-echo",
			repeat: testInfo.repeatEachIndex,
			browser: browser.version(),
			node: process.version,
			platform: os.platform(),
			arch: os.arch(),
			cpus: os.cpus().length,
			cpuModel: os.cpus()[0]?.model,
			totalMemoryBytes: os.totalmem(),
			viewport: page.viewportSize(),
			timestamp: new Date().toISOString(),
			prepared,
			result,
		};
		await testInfo.attach("measurements", {
			body: JSON.stringify(record, null, 2),
			contentType: "application/json",
		});
		const output = process.env.AO_PERF_DIR;
		if (output) {
			await mkdir(output, { recursive: true });
			await writeFile(
				path.join(output, `${label}-local-echo-${testInfo.repeatEachIndex}.json`),
				JSON.stringify(record, null, 2) + "\n",
			);
		}
	});
});
