import { clearConfig } from "./config";
import { clearOnboardingSkipped } from "./onboardingStore";
import { unpairFromServer } from "./push";

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
	try {
		await unpairFromServer();
	} finally {
		await clearConfig();
		// Re-arm onboarding: a user with no server should be offered the pairing
		// flow again, not dropped on a bare Agents empty state.
		await clearOnboardingSkipped();
	}
}
