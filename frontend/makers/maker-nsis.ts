import path from "node:path";
import { MakerBase, type MakerOptions } from "@electron-forge/maker-base";
import type { ForgePlatform } from "@electron-forge/shared-types";

// Electron Forge has no first-party NSIS maker, so we bridge to electron-builder's
// `buildForge`, the same engine recordly's working Windows installer uses. We drop
// Squirrel.Windows (per-user only, no custom install dir, fragile updates) for a
// real NSIS installer: per-user or per-machine, custom install directory, and a
// proper uninstaller. See https://github.com/aoagents/ReverbCode/issues/401.
//
// `buildForge` speaks Forge's legacy v5 function API, which Forge 7's class-based
// maker loader cannot resolve, so this thin MakerBase subclass adapts it.

export type MakerNSISConfig = {
	// electron-builder appId; required for a well-formed NSIS installer.
	appId?: string;
	// Display name for the installer + Start menu shortcut. Defaults to appName.
	productName?: string;
	// The packaged binary name WITHOUT ".exe" — must match Forge's
	// packagerConfig.executableName ("agent-orchestrator"). electron-builder
	// otherwise derives the exe name from productName and points the Start menu
	// shortcut at "Agent Orchestrator.exe", which does not exist, so the app
	// silently fails to launch and the shortcut shows a generic icon (#2414).
	executableName?: string;
	// Path to the Windows .ico used for the app and installer.
	icon?: string;
	// Any extra electron-builder `nsis` options, merged over our defaults.
	nsis?: Record<string, unknown>;
	// Any extra electron-builder `win` options (e.g. a hard-coded
	// signtoolOptions/azureSignOptions override), merged over our defaults.
	// Env-derived signing (see envSigningOptions below) still wins on the
	// keys it sets, so CI credentials cannot be silenced by a stale value.
	win?: Record<string, unknown>;
};

// envSigningOptions resolves Windows code-signing credentials from the
// environment, mirroring how forge.config.ts activates macOS signing only
// when the Apple env vars are present (#4502: without this, every Windows
// installer ships unsigned and Defender SmartScreen blocks first run with
// "unknown publisher"). Three credential paths, first match wins:
//
//  1. WIN_CSC_LINK (+ WIN_CSC_KEY_PASSWORD): a code-signing .pfx — local
//     path, URL, or base64, per electron-builder's certificateFile handling.
//     Deliberately NOT CSC_LINK: that secret is the macOS Apple Developer ID
//     .p12, and falling back to it would let a stray Apple credential on a
//     Windows build wedge signing (with forceCodeSigning below, a wrong-cert
//     misconfiguration becomes a hard failure instead of a clean unsigned
//     build). Per-platform signing must be opted into explicitly.
//  2. WIN_CERT_SUBJECT_NAME: a certificate already installed in the runner's
//     Windows certificate store — the standard path for non-exportable EV
//     tokens, where no .pfx can exist.
//  3. AZURE_PUBLISHER_NAME (+ AZURE_TENANT_ID / AZURE_CLIENT_ID /
//     AZURE_CLIENT_SECRET / AZURE_SUBSCRIPTION_ID / AZURE_RESOURCE_GROUP_NAME /
//     AZURE_ACCOUNT_NAME / AZURE_CODE_SIGNING_ACCOUNT_NAME): Azure Trusted
//     Signing. electron-builder itself consumes the AZURE_* credential env
//     vars; publisherName must be supplied explicitly, so its presence is
//     what marks this path as selected.
//
// Returns undefined when nothing is set — local and fork/dev builds stay
// unsigned and keep working exactly as before.
function envSigningOptions(): Record<string, unknown> | undefined {
	// WIN_CSC_LINK only — never the macOS CSC_LINK secret (the Apple Developer
	// ID .p12, the wrong cert for Windows). See the comment above.
	const certFile = process.env.WIN_CSC_LINK;
	const certPassword = process.env.WIN_CSC_KEY_PASSWORD;
	if (certFile) {
		const signtoolOptions: Record<string, unknown> = {
			certificateFile: certFile,
		};
		if (certPassword) {
			signtoolOptions.certificatePassword = certPassword;
		}
		if (process.env.WIN_SIGNING_HASH_ALGORITHMS) {
			// Trim each entry: "sha256, sha1" must not yield a " sha1" element
			// electron-builder would not recognize (PR review feedback).
			signtoolOptions.signingHashAlgorithms = process.env.WIN_SIGNING_HASH_ALGORITHMS.split(",")
				.map((algo) => algo.trim())
				.filter(Boolean);
		}
		return { signtoolOptions };
	}
	if (process.env.WIN_CERT_SUBJECT_NAME) {
		return { signtoolOptions: { certificateSubjectName: process.env.WIN_CERT_SUBJECT_NAME } };
	}
	if (process.env.AZURE_PUBLISHER_NAME) {
		const azureSignOptions: Record<string, unknown> = {
			publisherName: process.env.AZURE_PUBLISHER_NAME,
		};
		for (const [env, key] of [
			["AZURE_TENANT_ID", "tenantId"],
			["AZURE_CLIENT_ID", "clientId"],
			["AZURE_CLIENT_SECRET", "clientSecret"],
			["AZURE_SUBSCRIPTION_ID", "subscriptionId"],
			["AZURE_RESOURCE_GROUP_NAME", "resourceGroupName"],
			["AZURE_ACCOUNT_NAME", "accountName"],
			["AZURE_CODE_SIGNING_ACCOUNT_NAME", "codeSigningAccountName"],
		] as const) {
			if (process.env[env]) azureSignOptions[key] = process.env[env];
		}
		return { azureSignOptions };
	}
	return undefined;
}

export default class MakerNSIS extends MakerBase<MakerNSISConfig> {
	name = "nsis";
	defaultPlatforms: ForgePlatform[] = ["win32"];

	isSupportedOnCurrentPlatform(): boolean {
		return true;
	}

	async make({ dir, targetArch, appName }: MakerOptions): Promise<string[]> {
		const { buildForge } = await import("app-builder-lib");
		const cfg = this.config ?? {};
		// Mirror buildForge's own output layout (<dir>/../make) so artifacts land
		// where Forge's publisher expects them.
		const output = path.join(path.dirname(path.resolve(dir)), "make");
		// electron-builder derives the Windows exe name — and thus the Start menu
		// shortcut's target path and icon — from `win.executableName`, falling back
		// to productName when it is unset. Forge's packager already named the binary
		// "agent-orchestrator.exe" (packagerConfig.executableName), so we forward the
		// same name here; otherwise the shortcut targets a nonexistent
		// "Agent Orchestrator.exe" and the app never launches (#2414).
		const win: Record<string, unknown> = { ...(cfg.win ?? {}) };
		if (cfg.icon) win.icon = cfg.icon;
		if (cfg.executableName) win.executableName = cfg.executableName;
		// Windows code signing (#4502): when signing credentials are present in
		// the environment, electron-builder signs the packaged
		// agent-orchestrator.exe AND the NSIS installer itself. The signing
		// block is deep-merged over any explicit cfg.win override. With
		// credentials set we also flip forceCodeSigning so a silently-unsigned
		// artifact fails the build instead of shipping (same "prove the seal"
		// stance as the macOS dmg verification in forge.config.ts).
		const signing = envSigningOptions();
		if (signing) {
			for (const [key, value] of Object.entries(signing)) {
				win[key] =
					value !== null && typeof value === "object" && !Array.isArray(value)
						? { ...((win[key] as Record<string, unknown> | undefined) ?? {}), ...value }
						: value;
			}
			win.forceCodeSigning = true;
		}
		return buildForge(
			{ dir },
			{
				win: [`nsis:${targetArch}`],
				config: {
					appId: cfg.appId,
					productName: cfg.productName ?? appName,
					directories: { output },
					// Forge owns publishing (the workflow uploads via `gh release`).
					// `null` stops electron-builder from inferring a GitHub publish
					// target from package.json `repository` and trying to upload,
					// which fails in CI with no GH_TOKEN set.
					publish: null,
					...(Object.keys(win).length ? { win } : {}),
					nsis: {
						// A real installer, not Squirrel's silent per-user drop.
						oneClick: false,
						perMachine: false,
						allowToChangeInstallationDirectory: true,
						createDesktopShortcut: true,
						createStartMenuShortcut: true,
						...cfg.nsis,
					},
				},
			},
		);
	}
}

export { MakerNSIS };
