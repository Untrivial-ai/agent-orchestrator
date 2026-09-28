import { defineConfig } from "vite";

// better-sqlite3 is a native module rebuilt for Electron by Forge. Keep it out
// of Vite's bundle so the packaged app loads the rebuilt binary from
// node_modules (the auto-unpack plugin moves that binary outside app.asar).
//
// @cursor/sdk stays external too, but for a different reason: its runtime
// resolves platform bits and loads lazily from disk, so it must be read from
// node_modules rather than flattened into the bundle. Forge ships its dependency
// closure via PACKAGED_EXTERNAL_DEPENDENCIES in forge.config.ts.
export default defineConfig({
	build: {
		rollupOptions: {
			external: ["better-sqlite3", "@cursor/sdk"],
		},
	},
});
