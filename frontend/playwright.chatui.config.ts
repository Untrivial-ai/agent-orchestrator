import { defineConfig } from "@playwright/test";
import path from "node:path";

const DEFAULT_PORT = 5198;

function chatUIRegressionPort(raw: string | undefined): number {
	if (!raw) return DEFAULT_PORT;
	const port = Number(raw);
	if (!Number.isInteger(port) || port < 1 || port > 65_535) {
		throw new Error(`AO_CHATUI_E2E_PORT must be an integer from 1 to 65535; received ${JSON.stringify(raw)}`);
	}
	return port;
}

const artifactRoot = process.env.AO_CHATUI_E2E_ARTIFACT_DIR;
if (!artifactRoot) {
	throw new Error(
		"AO_CHATUI_E2E_ARTIFACT_DIR is required. Run the suite through `npm run test:e2e:chatui`.",
	);
}

const artifactDir = path.resolve(artifactRoot);
const port = chatUIRegressionPort(process.env.AO_CHATUI_E2E_PORT);
const baseURL = `http://127.0.0.1:${port}`;

export default defineConfig({
	testDir: "chatui-regression/contracts",
	forbidOnly: true,
	fullyParallel: false,
	workers: 1,
	retries: 0,
	timeout: 60_000,
	expect: { timeout: 5_000 },
	outputDir: path.join(artifactDir, "test-results"),
	reporter: [
		["line"],
		["json", { outputFile: path.join(artifactDir, "results.json") }],
		["html", { outputFolder: path.join(artifactDir, "html"), open: "never" }],
	],
	use: {
		actionTimeout: 5_000,
		baseURL,
		// This suite is explicitly evidence-producing: successful runs retain the
		// same artifacts as failures, so a before/after fix can be compared later.
		trace: "on",
		video: "on",
		screenshot: "on",
	},
	webServer: {
		command: `npm run dev:web -- --port ${port} --host 127.0.0.1 --strictPort`,
		url: baseURL,
		reuseExistingServer: false,
		stdout: "pipe",
		stderr: "pipe",
	},
});
