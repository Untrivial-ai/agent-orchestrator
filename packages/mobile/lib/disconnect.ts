import { clearConfig } from "./config";
import { activeHostMetadata, removeHost, type HostMetadata } from "./hosts";
import { clearOnboardingSkipped } from "./onboardingStore";
import { unpairFromServer } from "./push";
import { clearEventCursorsForHost } from "./chat/eventCursor";

// "Disconnect & forget server" — the inverse of pairing. Until this existed
// there was no way to un-pair a phone at all: clearing the host by hand left the
// device registered with the daemon, which kept pushing to it.
//
// Order matters. The push unregister needs the *persisted registration* (its own
// copy of the daemon address and password, held in SecureStore by push.ts), so
// it must run before the config is cleared — though in practice it reads its own
// copy, doing it first also means a failure there is queued for retry before we
// throw the credentials away.
//
// The `finally` is the point, not a formality. `unpairFromServer` catches its
// *network* failures, so a dead daemon is already handled — but it also does
// unguarded SecureStore writes (clearRegistration, savePendingUnregisters), and
// any of those throwing used to abort the disconnect with the host and password
// still on disk. Disconnecting is the one operation that must not leave
// credentials behind: whatever happens upstream, the config gets cleared.
export async function forgetServer(): Promise<void> {
	// The host record and its token are the actual pairing now; the legacy
	// config is only the last resolved address. Clearing that alone left the
	// machine in the list with its token in the keystore, so the next launch
	// raced its endpoints and silently reconnected to the server just forgotten.
	let host: HostMetadata | null = null;
	let upstreamError: unknown;
	try {
		host = await activeHostMetadata();
	} catch (error) {
		// Metadata is non-secret and normally cannot fail, but a storage failure
		// must not skip the cleanup we can still perform.
		upstreamError = error;
	}
	try {
		await unpairFromServer();
	} catch (error) {
		upstreamError ??= error;
	}

	// Each local deletion is independent. A rejected SecureStore operation must
	// not prevent the config, event cursor, or onboarding flag from being
	// cleared; otherwise "forget" can leave a partially paired phone behind.
	const cleanup = await Promise.allSettled([
		host ? removeHost(host.id) : Promise.resolve(),
		host ? clearEventCursorsForHost(host) : Promise.resolve(),
		clearConfig(),
		clearOnboardingSkipped(),
	]);
	if (upstreamError) throw upstreamError;
	const failed = cleanup.find((result): result is PromiseRejectedResult => result.status === "rejected");
	if (failed) throw failed.reason;
}
