import AsyncStorage from "@react-native-async-storage/async-storage";

const KEY = "ao.installId";

// The install id identifies this app installation to the daemon. It keys the
// push-device registry, so one phone stays one row even when its Expo push token
// rotates on reinstall, and it is what marks the device "live" on the desktop.
//
// It lives in AsyncStorage, not SecureStore: SecureStore holds the connection
// password and nothing else. This is an identifier, not a secret.
//
// Generated from time + randomness rather than a UUID library on purpose — this
// app must not take on another native dependency, and uniqueness across a handful
// of household devices is all that is required.
function generate(): string {
	const rand = () => Math.random().toString(36).slice(2, 10);
	return `${Date.now().toString(36)}-${rand()}-${rand()}`;
}

let cached: string | null = null;
let inflight: Promise<string> | null = null;

// getInstallId reads the stored id, creating and persisting one on first run.
// Concurrent callers share a single read so two callers cannot generate two ids.
export async function getInstallId(): Promise<string> {
	if (cached) return cached;
	if (!inflight) {
		inflight = (async () => {
			const stored = await AsyncStorage.getItem(KEY);
			if (stored) {
				cached = stored;
				return stored;
			}
			const fresh = generate();
			await AsyncStorage.setItem(KEY, fresh);
			cached = fresh;
			return fresh;
		})().finally(() => {
			inflight = null;
		});
	}
	return inflight;
}

// primeInstallId warms the cache at app start so the synchronous request path can
// read it without awaiting.
export async function primeInstallId(): Promise<void> {
	await getInstallId();
}

// cachedInstallId returns the id if primed, else null. Callers that get null omit
// the header: the device then reads as offline on the desktop, which is a correct
// fallback rather than a failure.
export function cachedInstallId(): string | null {
	return cached;
}
