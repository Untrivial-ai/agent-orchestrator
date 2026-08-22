import type { ForgeConfig } from "@electron-forge/shared-types";
import { VitePlugin } from "@electron-forge/plugin-vite";
import MakerNSIS from "./makers/maker-nsis";
import MakerDMG, { isSigningConfigured, sealDmg, verifyDmg, verifyMacArtifact } from "./makers/maker-dmg";
import MakerAppImage from "./makers/maker-appimage";
import { writeFileSync } from "node:fs";

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
const ACP_RUNTIME_NODE_PATH = "/Contents/Resources/acp-runtime/node/bin/node";
// V8 uses MAP_JIT on arm64 and mprotect on x64. electron-osx-sign's default
// allows only the former, so its re-signing makes the bundled Node crash on Intel.
const ACP_RUNTIME_NODE_ENTITLEMENTS = [
	"com.apple.security.cs.allow-jit",
	"com.apple.security.cs.allow-unsigned-executable-memory",
];

export function macSignOptionsForFile(
	filePath: string,
	arch: NodeJS.Architecture = process.arch,
): { entitlements?: string[] } {
	return arch === "x64" && filePath.endsWith(ACP_RUNTIME_NODE_PATH)
		? { entitlements: ACP_RUNTIME_NODE_ENTITLEMENTS }
		: {};
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
		appBundleId: "dev.agent-orchestrator.desktop",
		name: "Agent Orchestrator",
		executableName: EXECUTABLE_NAME,
		protocols: [AUTH_PROTOCOL],
		appCategoryType: "public.app-category.developer-tools",
		// App icon. electron-packager appends the per-platform extension
		// (.icns on macOS, .ico on Windows); Linux menu icons come from the
		// deb/rpm makers below, and the runtime window icon from src/main.ts.
		icon: "assets/icon",
		extraResource: [
			"daemon",
				"agent-browser",
				"resources/acp-runtime",
			"assets/icon.png",
			"assets/icon.ico",
			"assets/trayIconTemplate.png",
			"assets/trayIconTemplate@2x.png",
			"app-update.yml",
		],
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
		prePackage: async () => {
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
			},
		},
	],
	plugins: [
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
