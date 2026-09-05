import { defineConfig } from "@playwright/test";

// Real-app harness: no webServer, no browser. Playwright launches the REAL
// packaged Electron app (via _electron.launch) inside the pod under xvfb.
export default defineConfig({
	testDir: ".",
	testMatch: /(real-app|restart-continuity)\.spec\.ts/,
	timeout: 90_000,
	// line = human-readable pod log; json = a structured result artifact the runner
	// tars up and uploads (see boot-real.sh + ao-e2e-pod-gate.mjs artifact capture).
	reporter: [["line"], ["json", { outputFile: "test-results/results.json" }]],
	workers: 1,
});
