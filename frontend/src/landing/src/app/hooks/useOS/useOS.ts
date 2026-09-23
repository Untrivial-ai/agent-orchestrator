import { useEffect, useState } from "react";

export const Platform = {
	MacAppleSilicon: "mac-apple-silicon",
	MacIntel: "mac-intel",
	Windows: "windows",
	Linux: "linux",
	Mobile: "mobile",
	Unknown: "unknown",
} as const;

export type Platform = (typeof Platform)[keyof typeof Platform];

export type MobileOS = "ios" | "android" | null;

export interface PlatformInfo {
	platform: Platform;
	mobileOS: MobileOS;
}

export function macPlatformFromRenderer(
	renderer: unknown,
): typeof Platform.MacAppleSilicon | typeof Platform.MacIntel {
	if (typeof renderer !== "string") return Platform.MacAppleSilicon;

	// Apple Silicon only exposes Apple GPUs. Intel Macs can expose Intel's
	// integrated GPU or a discrete AMD/Nvidia GPU, so require one of those
	// explicit signals before offering the x64 build. Masked and software
	// renderer strings are ambiguous and must keep the safer arm64 default.
	return /\b(?:intel|amd|ati|nvidia)\b/i.test(renderer)
		? Platform.MacIntel
		: Platform.MacAppleSilicon;
}

function detectMacArch():
	| typeof Platform.MacAppleSilicon
	| typeof Platform.MacIntel {
	// Browser-side arch detection is unreliable: navigator.userAgent always
	// reports "Intel Mac OS X" on Apple Silicon for compat. The most reliable
	// signal that works in Safari is the WebGL renderer string - Apple GPUs
	// expose "Apple GPU" / "Apple M*", Intel Macs expose Intel/AMD/Nvidia.
	try {
		const canvas = document.createElement("canvas");
		const gl =
			canvas.getContext("webgl") ||
			(canvas.getContext("experimental-webgl") as WebGLRenderingContext | null);
		if (!gl) return Platform.MacAppleSilicon;
		const debugInfo = gl.getExtension("WEBGL_debug_renderer_info");
		if (!debugInfo) return Platform.MacAppleSilicon;
		const renderer = gl.getParameter(debugInfo.UNMASKED_RENDERER_WEBGL) as
			| string
			| undefined;
		return macPlatformFromRenderer(renderer);
	} catch {
		return Platform.MacAppleSilicon;
	}
}

// Modern iPadOS reports a desktop "Macintosh" user agent for compat, so the
// only signal that separates an iPad from a Mac is that it reports touch
// points. This is used for the device noun in copy, and to route iPads to the
// touch flow rather than a QR they have no second device to scan.
//
// Match "macintosh" only, never "mac os x": every iPhone UA contains the
// substring "like Mac OS X" ("iPhone; CPU iPhone OS 17_5 like Mac OS X"), so
// matching that phrase labels every iPhone an iPad. The explicit iPhone/iPod
// exclusion keeps that true even if a future UA adds "Macintosh".
export function isIPadOS(userAgent: string, maxTouchPoints: number): boolean {
	if (/ipad/i.test(userAgent)) return true;
	if (/iphone|ipod/i.test(userAgent)) return false;
	return /macintosh/i.test(userAgent) && maxTouchPoints > 1;
}

function detectMobileOS(userAgent: string, maxTouchPoints: number): MobileOS {
	if (/iphone|ipod/i.test(userAgent) || isIPadOS(userAgent, maxTouchPoints)) {
		return "ios";
	}
	if (/android/i.test(userAgent)) return "android";
	return null;
}

export function detectPlatformInfo(
	userAgent: string,
	maxTouchPoints: number,
): PlatformInfo {
	const mobileOS = detectMobileOS(userAgent, maxTouchPoints);

	if (mobileOS !== null || /mobile|tablet/i.test(userAgent)) {
		return { platform: Platform.Mobile, mobileOS };
	}

	if (/mac os x|macintosh/i.test(userAgent)) {
		return { platform: detectMacArch(), mobileOS };
	}
	if (/windows/i.test(userAgent)) {
		return { platform: Platform.Windows, mobileOS };
	}
	if (/linux|x11/i.test(userAgent)) {
		return { platform: Platform.Linux, mobileOS };
	}
	return { platform: Platform.Unknown, mobileOS };
}

function detectPlatform(): PlatformInfo {
	if (typeof navigator === "undefined") {
		return { platform: Platform.Unknown, mobileOS: null };
	}
	return detectPlatformInfo(navigator.userAgent, navigator.maxTouchPoints);
}

const DEFAULT_PLATFORM: PlatformInfo = {
	platform: Platform.Unknown,
	mobileOS: null,
};

export function usePlatform(): PlatformInfo {
	const [platform, setPlatform] = useState<PlatformInfo>(DEFAULT_PLATFORM);

	useEffect(() => {
		setPlatform(detectPlatform());
	}, []);

	return platform;
}

export function isMacPlatform(platform: Platform): boolean {
	return (
		platform === Platform.MacAppleSilicon || platform === Platform.MacIntel
	);
}
