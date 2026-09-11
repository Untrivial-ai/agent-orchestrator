import { defineConfig } from "@playwright/test";

// Overridable because 5173 is not ours alone: another worktree's
// `electron-forge start` also listens there, and with reuseExistingServer the
// suite silently runs against ITS renderer — which is built for Electron and
// fails in confusing, unrelated ways. Set AO_E2E_PORT to run alongside one.
const port = Number(process.env.AO_E2E_PORT ?? 5173);

export default defineConfig({
	testDir: "e2e",
	use: {
		baseURL: `http://127.0.0.1:${port}`,
	},
	webServer: {
		// dev:web serves the renderer alone (VITE_NO_ELECTRON=1) — no Electron child to
		// launch, which is all the browser-based e2e suite needs.
		command: `npm run dev:web -- --port ${port} --host 127.0.0.1`,
		port,
		reuseExistingServer: !process.env.CI,
	},
});
