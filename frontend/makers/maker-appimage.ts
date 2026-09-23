import path from "node:path";
import { MakerBase, type MakerOptions } from "@electron-forge/maker-base";
import type { ForgePlatform } from "@electron-forge/shared-types";

// Electron Forge has no first-party AppImage maker, so we bridge to
// electron-builder's `buildForge`, exactly as makers/maker-nsis.ts does for the
// Windows NSIS installer. AppImage is the Linux fetch-and-run artifact for the
// `ao start` bootstrapper: a single self-contained executable the Go agent can
// download from releases/latest/download and run directly, with no system
// package manager. The deb/rpm makers stay for users who want a system package.
//
// `buildForge` speaks Forge's legacy v5 function API, which Forge 7's class-based
// maker loader cannot resolve, so this thin MakerBase subclass adapts it.

export type MakerAppImageConfig = {
	// electron-builder appId; required for a well-formed AppImage.
	appId?: string;
	// Display name for the app. Defaults to appName.
	productName?: string;
	// Path to the PNG icon used for the app and desktop entry.
	icon?: string;
	// URL schemes written to the AppImage desktop entry.
	protocols?: Array<{ name: string; schemes: string[] }>;
	// Any extra electron-builder `appImage` options, merged over our defaults.
	appImage?: Record<string, unknown>;
};

export default class MakerAppImage extends MakerBase<MakerAppImageConfig> {
	name = "appimage";
	defaultPlatforms: ForgePlatform[] = ["linux"];

	isSupportedOnCurrentPlatform(): boolean {
		return true;
	}

	async make({ dir, targetArch, appName }: MakerOptions): Promise<string[]> {
		const { buildForge } = await import("app-builder-lib");
		const cfg = this.config ?? {};
		// Mirror buildForge's own output layout (<dir>/../make) so artifacts land
		// where Forge's publisher expects them.
		const output = path.join(path.dirname(path.resolve(dir)), "make");
		return buildForge(
			{ dir },
			{
				linux: [`appImage:${targetArch}`],
				config: {
					appId: cfg.appId,
					productName: cfg.productName ?? appName,
					protocols: cfg.protocols,
					directories: { output },
					// A missing `toolsets.appimage` means "0.0.0" to app-builder-lib, which
					// builds with the AppImageKit runtime that dlopens libfuse.so.2 at
					// startup. Distros shipping only fuse3 (Arch, and any install without
					// the fuse2 compat package) cannot launch that artifact at all — it
					// exits 1 with "dlopen(): error loading libfuse.so.2" before Electron
					// starts, which also breaks `ao start`, since that downloads and runs
					// this same AppImage (#4006). Pinning a non-legacy toolset moves the
					// published artifact onto a runtime with no libfuse2 dependency.
					// `1.0.3` is the same Runtime 20251108 as `1.0.2`, re-released with an
					// upstream fix, so it is the only sensible choice of the two.
					toolsets: { appimage: "1.0.3" },
					// Forge owns publishing (the workflow uploads via `gh release`).
					// `null` stops electron-builder from inferring a GitHub publish
					// target from package.json `repository` and trying to upload,
					// which fails in CI with no GH_TOKEN set.
					publish: null,
					linux: {
						...(cfg.icon ? { icon: cfg.icon } : {}),
					},
					appImage: {
						...cfg.appImage,
					},
				},
			},
		);
	}
}

export { MakerAppImage };
