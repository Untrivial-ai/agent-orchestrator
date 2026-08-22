// defineConfig comes from vitest/config (a superset of vite's) so the `test`
// block typechecks; vitest itself must be pointed at this file explicitly
// (package.json test script) because it only auto-discovers vite.config.*.
import { defineConfig } from "vitest/config";
import type { Plugin } from "vite";
import { fileURLToPath, URL } from "node:url";
import { TanStackRouterVite } from "@tanstack/router-plugin/vite";
import react from "@vitejs/plugin-react";
import tailwindcss from "@tailwindcss/vite";
import { DEFAULT_POSTHOG_HOST } from "./src/shared/posthog-config";

const POSTHOG_ORIGINS = (() => {
	const configured = process.env.VITE_AO_POSTHOG_HOST?.trim() || DEFAULT_POSTHOG_HOST;
	if (!configured) return [];
	let url: URL;
	try {
		url = new URL(configured);
	} catch {
		return [];
	}
	// posthog-js serves capture from api_host but fetches remote config from a
	// sibling "-assets" host it derives from the same name, so a CSP built only
	// from api_host blocks that request and logs a console error on every launch
	// of a packaged build. Capture is unaffected (it uses api_host), and AO
	// ignores what remote config offers, since replay, flags, and surveys are all
	// disabled in the client. Allowing the origin only silences the error; the
	// client settings still win over anything the server would say.
	//
	// The asset_host option deliberately does not cover this: per its own docs it
	// "only applies to /static/* asset paths; dynamic assets like remote config
	// continue to use the regular asset host derived from api_host".
	// Scoped to PostHog Cloud, matching what posthog-js itself does: it only
	// rewrites to an "-assets" sibling for *.posthog.com. A self-hosted instance
	// or a loopback capture endpoint serves everything from one origin, and
	// deriving there would emit a nonsense entry (127.0.0.1 would become
	// "127-assets.0.0.1").
	const origins = [url.origin];
	if (/\.posthog\.com$/i.test(url.hostname)) {
		const assetsHost = url.hostname.replace(/^([^.]+)\./, "$1-assets.");
		if (assetsHost !== url.hostname) origins.push(`${url.protocol}//${assetsHost}`);
	}
	return origins;
})();

// CSP for the built renderer. The daemon is loopback-only, so network access is
// pinned to 127.0.0.1 (REST + SSE over http, terminal mux over ws). Injected at
// build time rather than written into index.html because the dev server needs
// inline scripts (react-refresh preamble) that a static meta tag would block.
const CONTENT_SECURITY_POLICY = [
	"default-src 'self'",
	"script-src 'self'",
	"style-src 'self' 'unsafe-inline'",
	"img-src 'self' data: http://127.0.0.1:*",
	"font-src 'self' data:",
	["connect-src", "'self'", "http://127.0.0.1:*", "ws://127.0.0.1:*", ...POSTHOG_ORIGINS].filter(Boolean).join(" "),
	"object-src 'none'",
	"base-uri 'self'",
	"frame-src 'none'",
].join("; ");

const injectCspMeta: Plugin = {
	name: "inject-csp-meta",
	apply: "build",
	transformIndexHtml() {
		return [
			{
				tag: "meta",
				attrs: { "http-equiv": "Content-Security-Policy", content: CONTENT_SECURITY_POLICY },
				injectTo: "head-prepend",
			},
		];
	},
};

const productUiReactBoundary: Plugin = {
	name: "product-ui-react-boundary",
	enforce: "pre",
	async resolveId(source, importer) {
		if (!importer?.includes("/packages/product-ui/")) {
			return null;
		}
		const remap =
			source === "react" ||
			source.startsWith("react/") ||
			source === "react-dom" ||
			source.startsWith("react-dom/") ||
			source === "motion" ||
			source.startsWith("motion/");
		if (!remap) {
			return null;
		}
		return this.resolve(
			source,
			fileURLToPath(new URL("./src/renderer/main.tsx", import.meta.url)),
			{ skipSelf: true },
		);
	},
};

export default defineConfig(({ mode }) => ({
	// `dev:web` asks for the Electron-free browser preview with `--mode web`, and
	// the flag is injected here rather than as a `VITE_NO_ELECTRON=1` shell
	// prefix in package.json: npm runs scripts through cmd.exe on Windows, which
	// has no env-prefix syntax, so the prefix form fails before vite ever starts.
	// An .env file would be the other option, but .gitignore excludes .env* so it
	// could not be tracked. Only "web" defines the key; every other mode
	// (electron-forge dev/package, vitest) leaves it undefined exactly as before.
	define: mode === "web" ? { "import.meta.env.VITE_NO_ELECTRON": '"1"' } : {},
	// "@/" → the renderer root (src/renderer), the shadcn/ui import convention.
	resolve: {
		alias: {
			"@": fileURLToPath(new URL("./src/renderer", import.meta.url)),
			"@aoagents/product-ui": fileURLToPath(
				new URL("../packages/product-ui/src/index.ts", import.meta.url),
			),
			// The alias above resolves product-ui to its source, so that package's
			// own imports resolve from packages/product-ui/ — which only has a
			// node_modules if `npm ci` was run there too. CI does that; a
			// frontend-only install does not, and the failure mode is quiet: every
			// test importing product-ui dies at transform time with "failed to
			// resolve clsx", which reads as pre-existing breakage rather than a
			// missing install. Point both runtime deps at the frontend copies so
			// one install is enough.
			clsx: fileURLToPath(new URL("./node_modules/clsx", import.meta.url)),
			"tailwind-merge": fileURLToPath(
				new URL("./node_modules/tailwind-merge", import.meta.url),
			),
		},
	},
	// Dev proxy for VITE_NO_ELECTRON=1 browser preview — forwards /api and /mux
	// to the daemon so the renderer can be tested against a running daemon from
	// a plain browser without an Electron shell.
	server: {
		proxy: {
			"/api": {
				target: process.env.AO_DEV_API_TARGET ?? "http://127.0.0.1:3001",
				changeOrigin: false,
			},
			"/mux": {
				target: process.env.AO_DEV_API_TARGET ?? "http://127.0.0.1:3001",
				changeOrigin: false,
				ws: true,
			},
		},
	},
	plugins: [
		TanStackRouterVite({
			routesDirectory: "./src/renderer/routes",
			generatedRouteTree: "./src/renderer/routeTree.gen.ts",
			target: "react",
			autoCodeSplitting: true,
		}),
		productUiReactBoundary,
		react(),
		tailwindcss(),
		injectCspMeta,
	],
	test: {
		environment: "jsdom",
		testTimeout: 20_000,
		// Anchor node_modules at any depth: a bare "node_modules/**" replaces
		// vitest's default "**/node_modules/**" and only matches the root, so the
		// tracked src/landing preview app's nested node_modules would otherwise
		// have its vendored third-party test suites collected and run.
		exclude: ["**/node_modules/**", "dist/**", "dist-electron/**", "e2e/**"],
		globals: true,
		setupFiles: "./src/renderer/test/setup.ts",
	},
}));
