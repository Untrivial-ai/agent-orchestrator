import type { ForgeConfig } from "@electron-forge/shared-types";
import { AutoUnpackNativesPlugin } from "@electron-forge/plugin-auto-unpack-natives";
import { VitePlugin } from "@electron-forge/plugin-vite";
import { rebuild } from "@electron/rebuild";
import electronPackage from "electron/package.json";
import MakerNSIS from "./makers/maker-nsis";
import MakerDMG, { isSigningConfigured, sealDmg, verifyDmg, verifyMacArtifact } from "./makers/maker-dmg";
import { machoHasX86_64Slice } from "./makers/macho-archs";
import MakerAppImage from "./makers/maker-appimage";
import { existsSync, readdirSync, writeFileSync } from "node:fs";
import { spawnSync } from "node:child_process";
import path from "node:path";

// Default GitHub release target (production). Releases land on Untrivial-ai
// (the org the repo was transferred to in July 2026; AgentWrapper and aoagents
// are prior homes). Builds cut by CI must NOT rely on this fallback: the
// workflows set AO_RELEASE_REPO to the repo they run in, and build-artifacts.yml
// asserts the baked app-update.yml matches it, so a future org/repo rename
// fails the build instead of stranding the fleet on a redirect (#3523).
const DEFAULT_RELEASE_REPO = "Untrivial-ai/agent-orchestrator";

// The packaged binary name (no extension). Single source of truth: the packager
// names the exe/ELF from this, and the NSIS + deb makers must point their
// shortcut/launcher at the SAME name. Drift here means a broken Start menu
// shortcut on Windows (#2414) or "could not find the Electron app binary" on deb.
const EXECUTABLE_NAME = "agent-orchestrator";
const AUTH_PROTOCOL = {
	name: "Agent Orchestrator authentication callback",
	schemes: ["ao-app"],
};
const AUTH_PROTOCOL_MIME_TYPE = "x-scheme-handler/ao-app";
const PACKAGED_EXTERNAL_DEPENDENCIES = [
	"/node_modules/better-sqlite3",
	"/node_modules/bindings",
	"/node_modules/file-uri-to-path",
];

function ignoreFromVitePackage(file: string): boolean {
	if (!file) return false;
	if (file.startsWith("/.vite")) return false;
	if (file === "/node_modules") return false;
	return !PACKAGED_EXTERNAL_DEPENDENCIES.some(
		(dependency) => file === dependency || file.startsWith(`${dependency}/`),
	);
}

async function prepareNativeDependencies(platform: NodeJS.Platform, arch: string): Promise<void> {
	// Rebuild in the source tree, where prebuild-install and its helper packages
	// are available. The Vite package intentionally carries only the resulting
	// native runtime, not the install-time download toolchain.
	await rebuild({
		buildPath: process.cwd(),
		electronVersion: electronPackage.version,
		platform,
		arch,
		onlyModules: ["better-sqlite3"],
		force: true,
	});
}

export function extraResourcesForPlatform(platform: NodeJS.Platform): string[] {
	return [
		"daemon",
		"agent-browser",
		"resources/acp-runtime",
		...(platform === "darwin" ? ["update-helper"] : []),
		...(platform === "darwin" || platform === "linux" ? ["tmux"] : []),
		"assets/icon.png",
		"assets/icon.ico",
		"assets/trayIconTemplate.png",
		"assets/trayIconTemplate@2x.png",
		"app-update.yml",
	];
}

const ACP_RUNTIME_NODE_PATH = "/Contents/Resources/acp-runtime/node/bin/node";
// V8 uses MAP_JIT on arm64 and mprotect on x64. electron-osx-sign's default
// allows only the former, so its re-signing makes the bundled Node crash on
// Intel (#3879). Which entitlements a file needs is a property of its bytes,
// not of the signing machine: @electron/osx-sign hands optionsForFile only the
// path, and one signing host may sign both the arm64 and the x64 artifact —
// the canonical release signer does exactly that. So the selector below keys
// off the Mach-O header of the file being signed (thin x64, or a universal
// carrying an x64 slice), never off process.arch. The public CI build stays
// unsigned, so this hook runs for locally signed builds and stands as the
// reference implementation the canonical signer mirrors; see
// frontend/docs/desktop-release.md.
const ACP_RUNTIME_NODE_ENTITLEMENTS = [
	"com.apple.security.cs.allow-jit",
	"com.apple.security.cs.allow-unsigned-executable-memory",
];

export function macSignOptionsForFile(filePath: string): { entitlements?: string[] } {
	// Cheap gate first: optionsForFile is invoked for every Mach-O in the
	// bundle, and only the nested Node path can need an override.
	if (!filePath.endsWith(ACP_RUNTIME_NODE_PATH)) return {};
	// Fail closed: an unreadable or unparseable binary throws out of
	// optionsForFile and aborts the signing pass. Never fall back to
	// process.arch or to "no entitlements" — silently signing the Intel Node
	// without allow-unsigned-executable-memory is exactly the #3879 crash.
	return machoHasX86_64Slice(filePath) ? { entitlements: ACP_RUNTIME_NODE_ENTITLEMENTS } : {};
}

// parseReleaseRepo turns an "owner/repo" string (from AO_RELEASE_REPO) into the
// publisher-github { owner, name } shape, falling back to the production default
// when unset or malformed.
function parseReleaseRepo(value: string | undefined): { owner: string; name: string } {
	const [owner, name] = (value || DEFAULT_RELEASE_REPO).split("/");
	if (!owner || !name) {
		const [defOwner, defName] = DEFAULT_RELEASE_REPO.split("/");
		return { owner: defOwner, name: defName };
	}
	return { owner, name };
}

const config: ForgeConfig = {
	packagerConfig: {
		asar: true,
		// The Vite plugin normally packages only .vite. better-sqlite3 must stay
		// external so Electron can load its native binary, so include its minimal
		// runtime dependency tree explicitly; AutoUnpackNativesPlugin then places
		// the .node binary outside app.asar.
		ignore: ignoreFromVitePackage,
		appBundleId: "dev.agent-orchestrator.desktop",
		name: "Agent Orchestrator",
		executableName: EXECUTABLE_NAME,
		protocols: [AUTH_PROTOCOL],
		appCategoryType: "public.app-category.developer-tools",
		// App icon. electron-packager appends the per-platform extension
		// (.icns on macOS, .ico on Windows); Linux menu icons come from the
		// deb/rpm makers below, and the runtime window icon from src/main.ts.
		icon: "assets/icon",
		extraResource: extraResourcesForPlatform(process.platform),
		// Notarization. Two paths:
		//  - CI: an App Store Connect API key. APPLE_API_KEY is a PATH to the .p8
		//    (the workflow decodes APPLE_API_KEY_BASE64 to a temp file), plus the
		//    key id + issuer uuid. Matches the proven local runbook creds.
		//  - Local: AO_NOTARY_PROFILE, a notarytool keychain profile created with
		//    `notarytool store-credentials`. See ao-macos-signed-release runbook.
		// Both are valid NotaryToolCredentials, so no cast is needed.
		osxSign: process.env.APPLE_SIGNING_IDENTITY
			? {
					identity: process.env.APPLE_SIGNING_IDENTITY,
					optionsForFile: macSignOptionsForFile,
				}
			: process.env.CSC_LINK
				? { optionsForFile: macSignOptionsForFile }
				: undefined,
		osxNotarize: process.env.AO_NOTARY_PROFILE
			? { keychainProfile: process.env.AO_NOTARY_PROFILE }
			: process.env.APPLE_API_KEY
				? {
						appleApiKey: process.env.APPLE_API_KEY,
						appleApiKeyId: process.env.APPLE_API_KEY_ID!,
						appleApiIssuer: process.env.APPLE_API_ISSUER!,
					}
				: undefined,
	},
	hooks: {
		// electron-forge does not generate app-update.yml (electron-builder does);
		// electron-updater reads it from the app's Resources dir at runtime to know
		// which GitHub repo to pull from, else it throws ENOENT during download.
		// Generate it in prePackage (BEFORE osxSign) and ship it via extraResource
		// above, so it is copied into the bundle and SIGNED as part of the seal.
		// Writing it after signing (a postPackage hook) adds an unsealed resource
		// and macOS reports the app as "damaged". owner/repo are baked from
		// AO_RELEASE_REPO at build time.
		prePackage: async (_forgeConfig, platform, arch) => {
			await prepareNativeDependencies(platform as NodeJS.Platform, arch);
			if (platform === "darwin") {
				const helperBuild = spawnSync(process.execPath, [path.resolve("scripts/build-update-helper.mjs"), "--arch", arch], { stdio: "inherit" });
				if (helperBuild.error) throw helperBuild.error;
				if (helperBuild.status !== 0) throw new Error("macOS update helper build failed");
			}
			const { owner, name } = parseReleaseRepo(process.env.AO_RELEASE_REPO);
			const yml = [
				"provider: github",
				`owner: ${owner}`,
				`repo: ${name}`,
				"updaterCacheDirName: agent-orchestrator-updater",
				"",
			].join("\n");
			writeFileSync("app-update.yml", yml);
		},
		packageAfterPrune: async (_forgeConfig, buildPath) => {
			const nativeModule = path.join(
				buildPath,
				"node_modules",
				"better-sqlite3",
				"build",
				"Release",
				"better_sqlite3.node",
			);
			if (!existsSync(nativeModule)) {
				throw new Error("Packaged app is missing the better-sqlite3 native runtime");
			}
		},
		// Assert the native resource survived Electron Packager's copy/asar/sign
		// pipeline. A source build succeeding is not enough: a missing extraResource
		// would otherwise publish an app that silently fell back to machine tmux.
		postPackage: async (_forgeConfig, packageResult) => {
			if (packageResult.platform !== "darwin" && packageResult.platform !== "linux") return;
			for (const outputPath of packageResult.outputPaths) {
				let resourcesPath = path.join(outputPath, "resources");
				if (packageResult.platform === "darwin") {
					const appBundle = readdirSync(outputPath).find((entry) => entry.endsWith(".app"));
					if (!appBundle) throw new Error(`packaged macOS app bundle missing from ${outputPath}`);
					resourcesPath = path.join(outputPath, appBundle, "Contents", "Resources");
					const helper = path.join(resourcesPath, "update-helper", "ao-update-progress");
					if (!existsSync(helper)) throw new Error(`packaged macOS update helper missing from ${helper}`);
				}
				const binary = path.join(resourcesPath, "tmux", "bin", "tmux");
				if (!existsSync(binary)) throw new Error(`packaged tmux missing from ${binary}`);
				const version = spawnSync(binary, ["-V"], { encoding: "utf8" });
				if (version.status !== 0 || version.stdout.trim() !== "tmux 3.5a") {
					throw new Error(`packaged tmux failed verification at ${binary}: ${version.stderr || version.stdout}`);
				}
			}
		},
		// The dmg container is NOT signed, notarized or stapled by any maker
		// (neither Forge's maker-dmg nor app-builder-lib's dmg target does it), and
		// the .app's own stapled ticket does not propagate through an unsealed
		// container. So seal it here, after the maker has produced it, reusing the
		// same credentials packagerConfig already consumes (#3267 decision 3).
		// The .app inside was already signed + notarized + stapled by
		// packagerConfig above, before any maker ran; nothing here touches it.
		//
		// Then PROVE the seal. sealDmg exiting 0 only says three commands ran on
		// this machine; it does not say Gatekeeper accepts the published bytes with
		// a stapled ticket, or that a bundled nested executable (the Intel ACP
		// Node, #3879) actually runs. verify-mac-artifact.sh is the canonical gate
		// for both (#3288 workstreams 1 and 2; #3879 for the nested-Node check),
		// and #3267 decision 3 step 4 asks for exactly this check on the dmg. Run
		// only when sealDmg actually sealed: an unsigned local or desktop-testing
		// build has nothing to verify and must keep producing its dmg.
		//
		// The zip needs the SAME nested-Node check but no separate sealing step:
		// its inner .app was already signed/notarized/stapled by packagerConfig
		// above, so verifying it only needs the same "was this build actually
		// signed" gate sealDmg uses (isSigningConfigured), not a seal call of its
		// own. Without this, the dmg-only check left the maker-zip artifact — the
		// one electron-updater actually installs auto-updates from, per
		// makers/maker-dmg.ts's ERR_UPDATER_ZIP_FILE_NOT_FOUND note, and per-arch
		// the artifact CI ships for x64 — completely unverified: a regression of
		// exactly the crash #3879 fixed could ship without any automated gate
		// catching it.
		postMake: async (_forgeConfig, makeResults) => {
			for (const result of makeResults) {
				if (result.platform !== "darwin") continue;
				for (const artifact of result.artifacts) {
					if (artifact.endsWith(".dmg")) {
						if (await sealDmg(artifact)) await verifyDmg(artifact);
					} else if (artifact.endsWith(".zip") && isSigningConfigured()) {
						await verifyMacArtifact(artifact);
					}
				}
			}
			return makeResults;
		},
	},
	rebuildConfig: {},
	makers: [
		// Windows installer: NSIS via electron-builder (see makers/maker-nsis.ts).
		// Replaces Squirrel.Windows, which only does per-user installs with no
		// custom install dir or proper uninstaller (issue #401).
		new MakerNSIS(
			{
				appId: "dev.agent-orchestrator.desktop",
				productName: "Agent Orchestrator",
				// Match the packaged binary name so the Start menu shortcut targets
				// the real "agent-orchestrator.exe" (not "Agent Orchestrator.exe").
				executableName: EXECUTABLE_NAME,
				icon: "assets/icon.ico",
			},
			["win32"],
		),
		// macOS auto-update artifact. This entry can NEVER be removed:
		// MacUpdater.doDownloadUpdate looks for a "zip" and explicitly excludes
		// .pkg/.dmg, throwing ERR_UPDATER_ZIP_FILE_NOT_FOUND otherwise, so the zip
		// and latest-mac.yml must keep publishing forever (#3267 decision 2).
		{ name: "@electron-forge/maker-zip", platforms: ["darwin"], config: {} },
		// macOS FIRST-INSTALL artifact, additive to the zip above: a dmg has no
		// user-driven extraction step, so a third-party unzip tool can no longer
		// break the signature seal on the way in (see makers/maker-dmg.ts, #3267).
		new MakerDMG(
			{
				appId: "dev.agent-orchestrator.desktop",
				productName: "Agent Orchestrator",
			},
			["darwin"],
		),
		// Linux fetch-and-run artifact for `ao start`: a single self-contained
		// AppImage the Go bootstrapper downloads and runs directly (see
		// makers/maker-appimage.ts). The deb/rpm makers below stay for users who
		// prefer a system package.
		new MakerAppImage(
			{
				appId: "dev.agent-orchestrator.desktop",
				productName: "Agent Orchestrator",
				icon: "assets/icon.png",
				protocols: [AUTH_PROTOCOL],
			},
			["linux"],
		),
		{
			name: "@electron-forge/maker-deb",
			config: {
				options: {
					// Must match packagerConfig.executableName, or the deb maker
					// looks for the package name and fails with "could not find
					// the Electron app binary". (Both are "agent-orchestrator".)
					bin: EXECUTABLE_NAME,
					icon: "assets/icon.png",
					maintainer: "Agent Orchestrator",
					homepage: "https://github.com/aoagents/agent-orchestrator",
					mimeType: [AUTH_PROTOCOL_MIME_TYPE],
				},
			},
		},
		{
			name: "@electron-forge/maker-rpm",
			config: {
				options: {
					icon: "assets/icon.png",
					// rpmbuild rejects a spec with an empty License field.
					license: "MIT",
					homepage: "https://github.com/aoagents/agent-orchestrator",
					mimeType: [AUTH_PROTOCOL_MIME_TYPE],
				},
			},
		},
	],
	publishers: [
		{
			name: "@electron-forge/publisher-github",
			// Release target is build-time overridable so a fork run publishes to the
			// fork without a source edit. AO_RELEASE_REPO is "owner/repo"; it defaults
			// to the production target. The dev/test loop sets
			// AO_RELEASE_REPO=harshitsinghbhandari/agent-orchestrator (spec §1.1, §8).
			// Note: aoagents/agent-orchestrator and AgentWrapper/agent-orchestrator
			// are prior homes and intentionally NOT the default; releases land on
			// Untrivial-ai.
			config: {
				repository: parseReleaseRepo(process.env.AO_RELEASE_REPO),
				prerelease: process.env.AO_RELEASE_PRERELEASE === "true",
				draft: false,
				// Ask GitHub to compose the body from the PRs merged since the last
				// release. Without it the publisher creates the release with an empty
				// body, and the app's new "what's new" section has nothing to show:
				// electron-updater reads release notes from the release body, so an
				// empty body means users get told nothing about what changed.
				generateReleaseNotes: true,
			},
		},
	],
	plugins: [
		new AutoUnpackNativesPlugin({}),
		new VitePlugin({
			build: [
				{ entry: "src/main.ts", config: "vite.main.config.ts", target: "main" },
				{ entry: "src/preload.ts", config: "vite.preload.config.ts", target: "preload" },
				{ entry: "src/annotate-preload.ts", config: "vite.preload.config.ts", target: "preload" },
			],
			renderer: [{ name: "main_window", config: "vite.renderer.config.ts" }],
		}),
	],
};

export default config;
