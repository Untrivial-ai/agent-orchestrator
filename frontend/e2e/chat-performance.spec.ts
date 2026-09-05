import { expect, test } from "@playwright/test";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";
import os from "node:os";
import { execFileSync } from "node:child_process";
import { installFakeBridge } from "./support/fake-bridge";
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
		"events",
		"streaming",
		"highlighting",
		"history",
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
});
