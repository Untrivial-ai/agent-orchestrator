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
import { resolveDevApiTarget } from "./src/shared/dev-api-target";

const devApiTarget = resolveDevApiTarget();

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

// Cloud terminals attach over a ticketed wss:// dialed directly from the
// renderer (the WorkOS token stays in the main process; the single-use ticket
// is the socket's whole authorization — see lib/cloud-terminal-mux.ts), so the
// packaged CSP must allow the control-plane origin or every cloud terminal
// stays on "Connecting…" forever in built apps. The runtime URL is a daemon
// setting the build cannot read; list the baked defaults and fold in a
// developer's AO_CLOUD_CONTROL_PLANE_URL override so a custom control plane
// keeps a working terminal in packaged builds too.
const CLOUD_CP_WS_ORIGINS = (() => {
	const origins = ["wss://staging-api.aoagents.dev", "wss://api.aoagents.dev"];
	const override = process.env.AO_CLOUD_CONTROL_PLANE_URL?.trim();
	if (!override) return origins;
	let url: URL;
	try {
		url = new URL(override);
	} catch {
		return origins;
	}
	const origin = `${url.protocol === "http:" ? "ws:" : "wss:"}//${url.host}`;
	if (!origins.includes(origin)) origins.push(origin);
	return origins;
})();

// CSP for the renderer. The daemon is loopback-only, so network access is
// pinned to 127.0.0.1 (REST + SSE over http, terminal mux over ws), plus the
// cloud control-plane websocket origins above. The policy is injected here
// rather than written into index.html so the serve variant can differ: the
// same directives apply in dev, relaxed only where the dev server itself
// needs it. Enforcing CSP in dev keeps dev/packaged parity — a connect-src
// gap then fails on the developer's screen, not weeks later in a packaged
// build (that skew is exactly how the cloud-terminal block in #4666 shipped).
function contentSecurityPolicy(mode: "build" | "serve"): string {
	return [
		"default-src 'self'",
		// react-refresh injects its inline preamble in serve mode; a hash is
		// impractical because the preamble changes with the plugin version.
		mode === "serve" ? "script-src 'self' 'unsafe-inline'" : "script-src 'self'",
		"style-src 'self' 'unsafe-inline'",
		// Repository avatars can come from self-hosted SCM instances whose origins
		// are only known at runtime. Keep the broad exception limited to images;
		// scripts, connections, frames, and other resource classes remain scoped.
		"img-src 'self' data: http://127.0.0.1:* https:",
		"font-src 'self' data:",
		[
			"connect-src",
			"'self'",
			"http://127.0.0.1:*",
			"ws://127.0.0.1:*",
			// Vite serves on localhost, which 'self' does not cover for the ws://
			// HMR socket.
			mode === "serve" ? "ws://localhost:*" : "",
			...POSTHOG_ORIGINS,
			...CLOUD_CP_WS_ORIGINS,
		]
			.filter(Boolean)
			.join(" "),
		"object-src 'none'",
		"base-uri 'self'",
		"frame-src 'none'",
	].join("; ");
}

const injectCspMeta: Plugin = {
	name: "inject-csp-meta",
	apply: "build",
	transformIndexHtml() {
		return [
			{
				tag: "meta",
				attrs: { "http-equiv": "Content-Security-Policy", content: contentSecurityPolicy("build") },
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

export default defineConfig({
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
	//
	// The target is resolved rather than fixed: `npm run dev` supervises a daemon
	// on a dev-only port, and pointing at the standalone default while that one is
	// running makes every /api/v1 request 502 out of this proxy (see
	// ./src/shared/dev-api-target.ts and issue #4324). Resolved once at config
	// load, so start the daemon before the dev server, or set AO_DEV_API_TARGET.
	server: {
		proxy: {
			"/api": {
				target: devApiTarget,
				changeOrigin: false,
			},
			"/mux": {
				target: devApiTarget,
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
});
