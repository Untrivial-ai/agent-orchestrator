import { requireOptionalNativeModule } from "expo";
import * as Application from "expo-application";
import { Linking, Platform } from "react-native";
import { localeRegion, lookupUrl, parseLookup, playStoreUrls, type StoreCheck } from "./storeUpdate";

// The effect side of `storeUpdate.ts`: ask the stores, and open them. Split out
// so the policy stays unit-testable, like `githubLink.ts`/`openGitHub.ts`.
//
// Android goes through Play In-App Updates — there is no public Play version API.
// iOS uses the iTunes lookup, a plain HTTPS GET needing no native code, which is
// why `expo-in-app-updates` is excluded from Apple autolinking (package.json) and
// stays out of the iOS fingerprint. Nothing here throws: a store we cannot reach
// has to be indistinguishable from a store with no update.

/**
 * `expo-in-app-updates`, bound to its native module rather than through the
 * package's JS wrapper: that wrapper calls `requireNativeModule` at import,
 * which throws on iOS where the pod is not linked. This answers `null` there.
 */
type PlayUpdates = {
	readonly IMMEDIATE: number;
	checkForUpdate(): Promise<StoreCheck>;
	startUpdate(updateType?: number): Promise<boolean>;
};

const play = requireOptionalNativeModule<PlayUpdates>("ExpoInAppUpdates");

const LOOKUP_TIMEOUT_MS = 12_000;

let inflight: Promise<StoreCheck | null> | null = null;

/** Ask the store whether a newer native binary exists. Never throws; `null` means "could not tell". */
export function checkStore(): Promise<StoreCheck | null> {
	if (inflight) return inflight;
	inflight = run().finally(() => {
		inflight = null;
	});
	return inflight;
}

async function run(): Promise<StoreCheck | null> {
	try {
		if (Platform.OS === "ios") return await lookupAppStore();
		// Play rejects on anything not installed from the Play Store — dev builds,
		// sideloads, emulators without Play — which is the common local case and
		// has to stay silent.
		if (!play) return null;
		const answer = await play.checkForUpdate();
		// The library calls it `daysSinceRelease`, which it is not: it counts days
		// since this device's Play Store learned of the update.
		return { ...answer, playStalenessDays: (answer as { daysSinceRelease?: number | null }).daysSinceRelease ?? null };
	} catch {
		return null;
	}
}

/**
 * The device's storefront, or null when the locale does not name one — a guess
 * sent as `country` would ask for a storefront that does not exist. Locale
 * source matches `lib/voice/deviceProvider.ts`; the subtag walk lives in
 * `localeRegion` so it is unit-testable.
 */
function storefront(): string | null {
	try {
		return localeRegion(Intl.DateTimeFormat().resolvedOptions().locale);
	} catch {
		return null;
	}
}

async function lookupAppStore(): Promise<StoreCheck | null> {
	const bundleId = Application.applicationId;
	if (!bundleId) return null;
	// Same reason as `api.ts`: without a timeout an unreachable host hangs for the
	// OS TCP timeout. This runs at launch, so it must never hold anything up.
	const controller = new AbortController();
	const timer = setTimeout(() => controller.abort(), LOOKUP_TIMEOUT_MS);
	try {
		// `no-store`: the answer changes the moment a release goes live, and a
		// cached "no update" would outlast the release we are trying to announce.
		const res = await fetch(lookupUrl(bundleId, storefront()), { signal: controller.signal, cache: "no-store" });
		if (!res.ok) return null;
		// `nativeApplicationVersion` is CFBundleShortVersionString, read from the
		// binary. Deliberately not `Constants.expoConfig.version`, which is the JS
		// bundle's number and can be swapped by an OTA update independently of the
		// binary — exactly the case this check exists to catch.
		return parseLookup(await res.json(), Application.nativeApplicationVersion);
	} finally {
		clearTimeout(timer);
	}
}

/**
 * Hand over to Play's immediate flow, reporting whether it took over. Android
 * only — iOS has no in-app update flow, so there is nothing to hand over to.
 *
 * Note this is insistent, not a lock: `startUpdateFlowForResult` resolves as
 * soon as the dialog opens, and cancelling leaves the app running.
 *
 * We never ask for the flexible flow: `expo-in-app-updates` calls
 * `completeUpdate()` as soon as the download lands, restarting the app
 * unannounced, where Google's contract is that a flexible update restarts only
 * when the user chooses to. The dismissible sheet is our soft tier instead.
 */
export async function startPlayUpdate(): Promise<boolean> {
	if (Platform.OS !== "android" || !play) return false;
	try {
		return await play.startUpdate(play.IMMEDIATE);
	} catch {
		return false;
	}
}

/**
 * The one way to send a user to their update: Play's in-app flow when it will
 * start, the store listing otherwise. Callers should not reach for the two parts
 * separately — a button wired straight to the Play flow silently does nothing
 * whenever Play declines, which is most of the time on a floor-driven prompt.
 *
 * Reports which path it took rather than just "something happened": only the
 * Play flow leaves a download in progress worth recovering on the next resume,
 * and a caller that cannot tell the two apart will chase a listing that is not
 * downloading anything.
 */
export async function openOrStartUpdate(check: StoreCheck | null): Promise<"play" | "store" | "none"> {
	if (await startPlayUpdate()) return "play";
	return (await openStore(check)) ? "store" : "none";
}

/** Public App Store listing — the fallback when the version lookup fails. */
const IOS_APP_STORE_URL = "https://apps.apple.com/app/ao-mobile/id6792552173";

/** Open the store listing. Never rejects; returns whether a URL actually opened. */
export async function openStore(check: StoreCheck | null): Promise<boolean> {
	if (Platform.OS === "ios") {
		// The lookup carries the app's own store page, so there is no id to
		// configure. The constant is the fallback for when the lookup failed
		// outright (offline, or the iTunes API is having a day) — the listing is
		// public, so there is always somewhere real to send people.
		return await open(check?.storeUrl ?? IOS_APP_STORE_URL);
	}
	const pkg = Application.applicationId;
	if (!pkg) return false;
	// market:// opens the Play app directly; the https form is what a device
	// without it can still handle.
	const { app, web } = playStoreUrls(pkg);
	return (await open(app)) || (await open(web));
}

async function open(url: string): Promise<boolean> {
	try {
		await Linking.openURL(url);
		return true;
	} catch {
		return false;
	}
}
