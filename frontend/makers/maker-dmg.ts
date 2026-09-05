import path from "node:path";
import { existsSync } from "node:fs";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { MakerBase, type MakerOptions } from "@electron-forge/maker-base";
import type { ForgePlatform } from "@electron-forge/shared-types";

const run = promisify(execFile);

// macOS first-install distribution as a .dmg, so a user never runs an
// extraction step and the class of failure where a third-party unzip tool
// mishandles AppleDouble entries and breaks the code signature seal cannot
// happen at all. Full decision record: issue #3267, tracked as #3288
// workstream 6.
//
// This is ADDITIVE. The .zip must keep publishing forever:
// MacUpdater.doDownloadUpdate calls `findFile(files, "zip", ["pkg", "dmg"])`
// and throws ERR_UPDATER_ZIP_FILE_NOT_FOUND when it is absent, and findFile
// explicitly excludes .pkg and .dmg from candidates. There is no code path in
// electron-updater that can install an update from a .dmg, so the dmg is
// first-install only and never replaces the darwin maker-zip entry.
//
// Why bridge to app-builder-lib's `buildForge` instead of using Forge's own
// @electron-forge/maker-dmg: maker-dmg wraps electron-installer-dmg -> appdmg,
// neither of which have any notarize/staple code, and app-builder-lib's dmg
// target has the same limitation (dmg.sign defaults to false and its own docs
// warn against enabling it alongside notarization). So no maker solves signing
// for us and we need the same explicit postMake seal either way. Given that,
// consistency wins: app-builder-lib is already a direct dependency and already
// proven for a second-target bridge in maker-nsis.ts, so this reuses that
// pattern instead of pulling a second packaging engine into the tree for one
// platform. buildForge also needs no host tooling beyond hdiutil.

export type MakerDMGConfig = {
	// electron-builder appId; keep in sync with packagerConfig.appBundleId.
	appId?: string;
	// Volume/product name shown when the dmg is mounted. Defaults to appName.
	productName?: string;
	// Any extra electron-builder `dmg` options, merged over our defaults.
	dmg?: Record<string, unknown>;
};

export default class MakerDMG extends MakerBase<MakerDMGConfig> {
	name = "dmg";
	defaultPlatforms: ForgePlatform[] = ["darwin"];

	// hdiutil is macOS only, so unlike the NSIS bridge this cannot cross-build.
	isSupportedOnCurrentPlatform(): boolean {
		return process.platform === "darwin";
	}

	async make({ dir, makeDir, targetArch, appName }: MakerOptions): Promise<string[]> {
		const { buildForge } = await import("app-builder-lib");
		const cfg = this.config ?? {};
		// buildForge's `dir` becomes electron-builder's `prepackaged`, and on macOS
		// that value IS the app path: macPackager.packMacTargets does
		// `appPath = prepackaged ?? join(computeAppOutDir(...), "<productFilename>.app")`
		// and hands appPath straight to the dmg target, which copies it into the
		// image renamed to "<productFilename>.app" (dmg.ts computeDmgOptions'
		// default `contents` entry). Passing Forge's PACKAGE directory here would
		// therefore copy the whole package dir in under that name and produce
		// "Agent Orchestrator.app/Agent Orchestrator.app", an outer bundle with no
		// Contents that cannot launch.
		//
		// maker-nsis.ts passes the bare `dir` and is correct to: winPackager goes
		// through the generic platformPackager.pack, whose computeAppOutDir returns
		// `prepackaged` as the app OUT DIR, and the NSIS target wants that
		// directory. Windows has no .app wrapper, so there is nothing to descend
		// into. The asymmetry is in electron-builder, not in these makers.
		//
		// appName is Forge's filenamify'd packagerConfig.name, the same name
		// @electron/packager gives the bundle inside `dir`.
		const appPath = path.join(dir, `${appName}.app`);
		const artifacts = await buildForge(
			{ dir: appPath },
			{
				mac: [`dmg:${targetArch}`],
				config: {
					appId: cfg.appId,
					productName: cfg.productName ?? appName,
					// Forge already computed <out>/make and passes it as makeDir. Use it
					// rather than letting buildForge default to dirname(dir)/make, which
					// now that `dir` is the .app would land inside the package directory.
					directories: { output: makeDir },
					// Forge owns publishing (the workflow uploads via `gh release`).
					// `null` stops electron-builder from inferring a GitHub publish
					// target from package.json `repository` and trying to upload.
					publish: null,
					mac: {
						// The .app inside is ALREADY signed, notarized and stapled by
						// Forge's packagerConfig.osxSign/osxNotarize, which run before any
						// maker. `identity: null` guarantees electron-builder never
						// re-signs it and breaks that seal; we only want its dmg target.
						identity: null,
					},
					dmg: {
						// Left unsigned on purpose. electron-builder's own docs: "Signing
						// is not required and will lead to unwanted errors in combination
						// with notarization requirements." The dmg container is sealed
						// afterwards by sealDmg() in forge.config.ts's postMake hook.
						sign: false,
						// dmg-builder defaults writeUpdateInfo ON: DmgTarget.build does
						// `this.options.writeUpdateInfo === false ? null : createBlockmap(...)`.
						// Two reasons that is wrong for us. It contradicts the permanent
						// full-download-only baseline for macOS (#3151, #3267 decision 4),
						// and the blockmap would be computed BEFORE postMake's sealDmg
						// rewrites the dmg bytes, so the sidecar would describe a file that
						// no longer exists by the time anything published it.
						writeUpdateInfo: false,
						...cfg.dmg,
					},
				},
			},
		);
		// Enforcement, not just configuration: this array is what Forge hands its
		// publisher, so a stale or policy-violating sidecar is filtered at the one
		// boundary that decides what gets uploaded. Belt to writeUpdateInfo's
		// braces, and it also covers a caller overriding it through cfg.dmg.
		return artifacts.filter((artifact) => !artifact.endsWith(".dmg.blockmap"));
	}
}

// resolveSigningIdentity mirrors packagerConfig.osxSign's credential shapes.
// APPLE_SIGNING_IDENTITY is the explicit identity used in CI and in the local
// runbook. With only CSC_LINK set, electron-osx-sign discovers the identity
// from the keychain it imported the .p12 into; `codesign` does the same given
// the common prefix, so that is the fallback. Undefined means "no signing
// material", which is a legitimate state (unsigned local and testing builds).
function resolveSigningIdentity(env: NodeJS.ProcessEnv): string | undefined {
	if (env.APPLE_SIGNING_IDENTITY) return env.APPLE_SIGNING_IDENTITY;
	if (env.CSC_LINK) return "Developer ID Application";
	return undefined;
}

// isSigningConfigured mirrors the same "was this build actually signed"
// question sealDmg answers for the dmg container, exposed for other artifacts
// (e.g. the zip maker's output) that need the identical gate: an unsigned
// local/testing build has nothing a Gatekeeper-facing verifier can check.
export function isSigningConfigured(env: NodeJS.ProcessEnv = process.env): boolean {
	return resolveSigningIdentity(env) !== undefined;
}

// resolveNotaryArgs mirrors packagerConfig.osxNotarize's two credential shapes:
// an AO_NOTARY_PROFILE notarytool keychain profile locally, or the App Store
// Connect API key trio in CI.
function resolveNotaryArgs(env: NodeJS.ProcessEnv): string[] | undefined {
	if (env.AO_NOTARY_PROFILE) return ["--keychain-profile", env.AO_NOTARY_PROFILE];
	if (env.APPLE_API_KEY && env.APPLE_API_KEY_ID && env.APPLE_API_ISSUER) {
		return ["--key", env.APPLE_API_KEY, "--key-id", env.APPLE_API_KEY_ID, "--issuer", env.APPLE_API_ISSUER];
	}
	return undefined;
}

function assertCompleteAppStoreConnectCredentials(env: NodeJS.ProcessEnv): void {
	const credentials = [env.APPLE_API_KEY, env.APPLE_API_KEY_ID, env.APPLE_API_ISSUER];
	const supplied = credentials.filter((credential) => credential !== undefined);
	if (supplied.length === 0) return;
	if (supplied.length === credentials.length && credentials.every((credential) => credential)) return;
	throw new Error(
		"[dmg] incomplete App Store Connect credentials; APPLE_API_KEY, APPLE_API_KEY_ID, and APPLE_API_ISSUER must " +
			"all be set and non-empty. Refusing to publish a dmg with partial notarization credentials.",
	);
}

/**
 * sealDmg signs, notarizes and staples one .dmg. Returns true when it actually
 * sealed the file, false for the deliberate unsigned no-op.
 *
 * Neither maker-dmg nor app-builder-lib's dmg target does any of this, and the
 * .app's own stapled ticket does not propagate through an unsealed dmg
 * container, so the container needs its own explicit seal (#3267 decision 3).
 * Credentials are the exact same env vars packagerConfig already consumes; no
 * new secrets are introduced.
 *
 * There are exactly two acceptable credential states, and everything in between
 * is a build failure:
 *
 *   - NEITHER a signing identity NOR notary credentials: a warned no-op. This is
 *     the legitimate local/unsigned case and keeps unsigned local builds and the
 *     unsigned desktop-testing pipeline producing a dmg.
 *   - BOTH: sign, notarize, staple, and let any failure throw.
 *
 * A partial set fails closed in both directions. Signing material without notary
 * credentials used to warn and return success, which let a signed-but-unnotarized
 * dmg reach publishing; notary credentials without an identity returned even
 * earlier and did nothing at all. Either way the release pipeline saw a green
 * build and an artifact Gatekeeper would reject, which is precisely the outcome
 * this work exists to prevent.
 */
export async function sealDmg(dmgPath: string, env: NodeJS.ProcessEnv = process.env): Promise<boolean> {
	const identity = resolveSigningIdentity(env);
	// Validate this before the zero-credential no-op: an incomplete trio cannot
	// be treated as equivalent to no notarization credentials.
	assertCompleteAppStoreConnectCredentials(env);
	const notaryArgs = resolveNotaryArgs(env);

	if (!identity && !notaryArgs) {
		console.warn(`[dmg] no signing material (APPLE_SIGNING_IDENTITY / CSC_LINK); leaving ${dmgPath} unsigned`);
		return false;
	}
	if (!identity) {
		throw new Error(
			`[dmg] notarization credentials are set but no signing identity is (APPLE_SIGNING_IDENTITY / CSC_LINK); ` +
				`refusing to publish an unsigned ${dmgPath}. Set both, or neither for an unsigned local build.`,
		);
	}
	if (!notaryArgs) {
		throw new Error(
			`[dmg] a signing identity is set but no notarization credentials are (AO_NOTARY_PROFILE, or the ` +
				`APPLE_API_KEY / APPLE_API_KEY_ID / APPLE_API_ISSUER trio); refusing to publish an unnotarized ` +
				`${dmgPath}. Set both, or neither for an unsigned local build.`,
		);
	}

	console.log(`[dmg] signing ${dmgPath}`);
	await run("codesign", ["--sign", identity, "--timestamp", "--force", dmgPath]);

	console.log(`[dmg] notarizing ${dmgPath} (this waits on Apple)`);
	await run("xcrun", ["notarytool", "submit", dmgPath, ...notaryArgs, "--wait"], {
		// Notarization routinely takes several minutes.
		maxBuffer: 10 * 1024 * 1024,
	});

	console.log(`[dmg] stapling ${dmgPath}`);
	await run("xcrun", ["stapler", "staple", dmgPath]);
	return true;
}

// The canonical verifier, resolved from the working directory. Forge is always
// invoked from `frontend/` (its npm scripts), the same assumption the prePackage
// hook already makes when it writes app-update.yml to a relative path.
const VERIFY_SCRIPT = "scripts/verify-mac-artifact.sh";

/**
 * verifyMacArtifact runs the canonical post-seal gate on any signed macOS
 * artifact (.dmg, .zip, or .app) — verify-mac-artifact.sh dispatches on the
 * extension itself (rule 4/5 in that script's header).
 *
 * Producing and proving an artifact are different things: sealing (or the
 * packager's own osxSign/osxNotarize) only proves the relevant commands exited
 * 0 on this machine, not that the published bytes are something Gatekeeper
 * accepts with a stapled ticket, or that a bundled nested executable (the
 * Intel ACP Node, #3879) actually runs. verify-mac-artifact.sh is the
 * canonical local check (#3288 workstreams 1 and 2) and exists specifically so
 * nobody hand-rolls a variant of it and draws a wrong conclusion; the
 * pre-publication gate for canonical releases is the release conductor's own
 * verifier (frontend/docs/desktop-release.md).
 *
 * Only meaningful on a signed artifact, so callers gate accordingly (sealDmg's
 * return value for the dmg container; isSigningConfigured for the zip, whose
 * inner .app was already signed/notarized/stapled by packagerConfig before any
 * maker ran).
 */
export async function verifyMacArtifact(
	artifactPath: string,
	scriptPath: string = path.resolve(VERIFY_SCRIPT),
): Promise<void> {
	if (!existsSync(scriptPath)) {
		throw new Error(
			`[dmg] cannot verify ${artifactPath}: ${scriptPath} not found (run electron-forge from frontend/)`,
		);
	}
	console.log(`[dmg] verifying ${artifactPath}`);
	const { stdout } = await run("bash", [scriptPath, artifactPath], { maxBuffer: 10 * 1024 * 1024 });
	if (stdout) console.log(stdout.trim());
}

// Kept as a thin, dmg-specific alias: existing callers and the dmg-oriented
// name/log-message stay unchanged. verifyMacArtifact is the generic entry
// point new callers (e.g. the zip artifact) should use directly.
export const verifyDmg = verifyMacArtifact;

export { MakerDMG };
