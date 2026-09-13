import { defineConfig } from "vitest/config";

// Local-only unit tests for the opt-in ChatUI regression runner. The ordinary
// Vitest config excludes this file so CI cannot discover it accidentally.
export default defineConfig({
	test: {
		environment: "node",
		include: ["scripts/run-chatui-regressions.test.mjs"],
	},
});
