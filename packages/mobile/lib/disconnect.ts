import { clearConfig } from "./config";
import { activeHostMetadata, removeHost, type HostMetadata } from "./hosts";
import { clearOnboardingSkipped } from "./onboardingStore";
import { unpairFromServer } from "./push";
import { clearEventCursorsForHost } from "./chat/eventCursor";

// Forget the selected machine. Unpair before deleting its stored token so the
// request can authenticate to that machine, never to another paired machine.
// Network failure cannot block local cleanup; storage failure is reported only
// after the remaining deletions have been attempted.
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
		await unpairFromServer(host);
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
