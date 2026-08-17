// Client push-notification plumbing: permission, Expo token acquisition, and
// registration/unregistration with the daemon. Delivery + routing of taps lives
// in PushManager.tsx; this module owns the "get a token and tell the daemon"
// half. See docs/adr/0001-mobile-push-notifications.md (D1, D4, D7, D9).
import Constants from "expo-constants";
import * as Device from "expo-device";
import * as Notifications from "expo-notifications";
import * as SecureStore from "expo-secure-store";
import { Linking, Platform } from "react-native";
import { ApiError, registerPushDevice, unpairFromDaemon, unregisterPushDevice } from "./api";
import { getInstallId } from "./installId";
import type { ServerConfig } from "./config";
import { classifyServerFailure, hasServer, type PushRegisterResult, type PushStatus } from "./pushStatus";

export type { PushRegisterResult, PushStatus } from "./pushStatus";

// The last successful registration: the Expo token AND the daemon it was
// registered with (host/port/TLS/password). Persisting the daemon — not just the
// token — is what lets us unregister from the *right* daemon after an app restart
// or a config change, so an old daemon can't keep pushing to this device (D7).
// It lives in SecureStore because it contains the connection password.
const REGISTRATION_KEY = "ao.pushRegistration";
// Registrations we still owe an unregister to (the daemon was unreachable when we
// tried). Retried on the next register/foreground so a failed unregister is never
// silently lost — otherwise an old daemon could keep pushing to this device.
const PENDING_UNREG_KEY = "ao.pushPendingUnregister";
// Bound the pending list so a permanently-dead daemon can't grow it forever.
const MAX_PENDING_UNREG = 10;

type Registration = {
	token: string;
	host: string;
	httpPort: string;
	secure: boolean;
	password: string;
};

async function loadRegistration(): Promise<Registration | null> {
	try {
		const raw = await SecureStore.getItemAsync(REGISTRATION_KEY);
		return raw ? (JSON.parse(raw) as Registration) : null;
	} catch {
		return null;
	}
}

async function saveRegistration(reg: Registration): Promise<void> {
	await SecureStore.setItemAsync(REGISTRATION_KEY, JSON.stringify(reg));
}

async function clearRegistration(): Promise<void> {
	await SecureStore.deleteItemAsync(REGISTRATION_KEY);
}

async function loadPendingUnregisters(): Promise<Registration[]> {
	try {
		const raw = await SecureStore.getItemAsync(PENDING_UNREG_KEY);
		return raw ? (JSON.parse(raw) as Registration[]) : [];
	} catch {
		return [];
	}
}

async function savePendingUnregisters(list: Registration[]): Promise<void> {
	if (list.length === 0) {
		await SecureStore.deleteItemAsync(PENDING_UNREG_KEY);
		return;
	}
	await SecureStore.setItemAsync(PENDING_UNREG_KEY, JSON.stringify(list.slice(-MAX_PENDING_UNREG)));
}

// Queue a registration for a later unregister retry (deduped by token+host).
async function queuePendingUnregister(reg: Registration): Promise<void> {
	const list = await loadPendingUnregisters();
	if (list.some((r) => r.token === reg.token && sameDaemon(r, configOf(reg)))) return;
	list.push(reg);
	await savePendingUnregisters(list);
}

// Retry every queued unregister; keep the ones that still fail. Best-effort.
async function flushPendingUnregisters(): Promise<void> {
	const list = await loadPendingUnregisters();
	if (list.length === 0) return;
	const stillPending: Registration[] = [];
	for (const reg of list) {
		try {
			await unregisterPushDevice(configOf(reg), reg.token);
		} catch {
			stillPending.push(reg);
		}
	}
	await savePendingUnregisters(stillPending);
}

// Rebuild a minimal ServerConfig for talking to the daemon a registration names.
// muxPort is unused by the REST calls (register/unregister) so it's left empty.
function configOf(reg: Registration): ServerConfig {
	return { host: reg.host, httpPort: reg.httpPort, muxPort: "", secure: reg.secure, password: reg.password };
}

// Same daemon? Keyed on the fields that address it — host/port/TLS. (The password
// can change without it being a different daemon, so it's not part of identity.)
function sameDaemon(reg: Registration, cfg: ServerConfig): boolean {
	return reg.host === cfg.host && reg.httpPort === cfg.httpPort && !!reg.secure === !!cfg.secure;
}

// Suppress the OS banner while the app is foregrounded (D9) — the live in-app UI
// is the signal, so a tray banner would be a redundant double-signal. When the
// app is backgrounded/killed the OS shows the notification normally (this handler
// only runs for notifications received while the JS runtime is alive/foreground).
export function configurePushHandler(): void {
	Notifications.setNotificationHandler({
		handleNotification: async () => ({
			shouldShowBanner: false,
			shouldShowList: false,
			shouldPlaySound: false,
			shouldSetBadge: false,
		}),
	});
}

// One high-importance Android channel so `needs_input` actually buzzes (D5).
// No-op on iOS. Safe to call repeatedly.
export async function ensureAndroidChannel(): Promise<void> {
	if (Platform.OS !== "android") return;
	await Notifications.setNotificationChannelAsync("default", {
		name: "Default",
		importance: Notifications.AndroidImportance.HIGH,
		sound: "default",
	});
}

// The EAS projectId is required by getExpoPushTokenAsync. It is written into
// app.json (extra.eas.projectId) by `eas init`; fall back to the runtime
// easConfig for classic builds.
function easProjectId(): string | undefined {
	const extra = Constants.expoConfig?.extra as { eas?: { projectId?: string } } | undefined;
	return extra?.eas?.projectId ?? Constants.easConfig?.projectId;
}

// Acquire the Expo push token and register it with the daemon. Returns the token
// on success, or a typed reason on failure so the UI can say something accurate —
// notably distinguishing "this build can't mint a token" from "the server wasn't
// reachable", which are very different problems. Idempotent: the daemon upserts
// by token, so this is also the foreground-refresh path (D7).
//
// `ask` decides whether this call may spend the user's one-shot OS permission
// prompt. Automatic callers (post-connect, foreground refresh) pass false and
// register only if permission was already granted; only a call the user
// deliberately initiated — where the app has just explained what notifications
// are for — passes true. Without this the prompt fires milliseconds after the
// first successful connect, while the user is still reading the result, with
// nothing having framed it.
export async function registerForPush(
	cfg: ServerConfig,
	{ ask }: { ask: boolean } = { ask: true },
): Promise<PushRegisterResult> {
	// Nothing to register with until the app is paired. Checked first, and here
	// rather than only in the UI, so no call site can spend the user's one-shot
	// permission prompt on a request that could only fail (an unpaired app still
	// holds a config object — it just has an empty host).
	if (!hasServer(cfg)) return { ok: false, reason: "not-configured" };

	// Remote push tokens are only issued on physical devices.
	if (!Device.isDevice) return { ok: false, reason: "unsupported" };

	// Ensure the Android channel exists BEFORE the permission prompt and before
	// any notification could arrive, so a notification is never mis-filed onto an
	// implicit default channel.
	await ensureAndroidChannel();

	const current = await Notifications.getPermissionsAsync();
	let status = current.status;
	if (status !== "granted" && ask && current.canAskAgain) {
		status = (await Notifications.requestPermissionsAsync()).status;
	}
	if (status !== "granted") return { ok: false, reason: "denied" };

	const projectId = easProjectId();
	if (!projectId) {
		// Without a projectId Expo can't mint a token — this is an EAS setup gap,
		// not a runtime error. Warn and no-op so the app still works without push.
		console.warn("[push] no EAS projectId (run `eas init`); skipping push registration");
		return { ok: false, reason: "no-project-id" };
	}

	// Retry any unregisters we still owe from a previous failure.
	await flushPendingUnregisters();

	// If we're now pointed at a different daemon than we last registered with,
	// unregister the token from the OLD daemon first so it stops pushing to this
	// device. This survives app restarts because the old daemon's address +
	// credentials are persisted. If that unregister fails (daemon unreachable),
	// queue it for retry rather than dropping it.
	const prior = await loadRegistration();
	if (prior && !sameDaemon(prior, cfg)) {
		try {
			await unregisterPushDevice(configOf(prior), prior.token);
		} catch {
			await queuePendingUnregister(prior);
		}
	}

	// Step 1 — mint the token. This throws when the build itself can't do push:
	// most commonly an iOS build with no APNs `aps-environment` entitlement, or a
	// simulator. Kept in its own try so it is never confused with a server error.
	let token: string;
	try {
		token = (await Notifications.getExpoPushTokenAsync({ projectId })).data;
	} catch (e) {
		console.warn("[push] could not obtain an Expo push token (build not provisioned for push?)", e);
		return { ok: false, reason: "token-failed" };
	}

	// Step 2 — hand the token to the daemon. The build is fine at this point, so
	// any failure here is about the server: either we never reached it (offline,
	// wrong host) or it answered and rejected us (bad password, lockout, 5xx).
	// An ApiError carries a status, which is exactly that distinction.
	try {
		await registerPushDevice(cfg, {
			token,
			platform: Platform.OS,
			deviceName: Device.deviceName ?? undefined,
		});
	} catch (e) {
		const httpStatus = e instanceof ApiError ? e.status : undefined;
		console.warn(`[push] could not register the token with the daemon (status: ${httpStatus ?? "no response"})`, e);
		return { ok: false, reason: classifyServerFailure(httpStatus), status: httpStatus };
	}

	await saveRegistration({
		token,
		host: cfg.host,
		httpPort: cfg.httpPort,
		secure: !!cfg.secure,
		password: cfg.password,
	});
	return { ok: true, token };
}

// Reads the live permission + registration state without prompting.
export async function getPushStatus(): Promise<PushStatus> {
	const perm = await Notifications.getPermissionsAsync();
	const reg = await loadRegistration();
	return {
		supported: Device.isDevice,
		granted: perm.status === "granted",
		canAskAgain: perm.canAskAgain ?? true,
		registered: !!reg,
	};
}

// Opens this app's OS settings page so the user can flip notifications back on
// after a permanent denial (the OS won't let us re-prompt in that case).
export async function openNotificationSettings(): Promise<void> {
	try {
		await Linking.openSettings();
	} catch {
		/* best-effort */
	}
}

// Best-effort unregister of the last-registered token from the daemon it was
// registered with (D7, disconnect/unpair). Uses the persisted daemon address +
// credentials, so it reaches the correct daemon even after a restart. If the
// unregister fails (daemon unreachable), the target is queued for retry instead
// of being dropped — so the old daemon can't keep pushing to this device. Never
// throws — the caller must not be blocked.
// Tell the daemon this phone has unpaired, so it drops the row rather than just
// clearing the token. Used by "Disconnect & forget server" and by the unpair
// effect in PushManager — the two places where the phone is genuinely leaving,
// as opposed to merely switching notifications off.
//
// Sends the install id when we have one and the token otherwise, so a daemon can
// find the row either way. Best-effort like unregisterFromPush: local state is
// cleared regardless, since a phone that cannot reach the old daemon must still
// be able to disconnect from it.
export async function unpairFromServer(): Promise<void> {
	const reg = await loadRegistration();
	await clearRegistration();
	if (!reg) return;
	let id = reg.token;
	try {
		id = (await getInstallId()) || reg.token;
	} catch {
		// Fall back to the token; an unreadable install id must not block unpairing.
	}
	try {
		await unpairFromDaemon(configOf(reg), id);
	} catch {
		// The daemon may be unreachable (that is often *why* the user is
		// disconnecting). Nothing to retry against: the phone is forgetting this
		// server's address and credentials, so a queued call could never be sent.
		// The row is left for the desktop to remove.
	}
}

export async function unregisterFromPush(): Promise<void> {
	const reg = await loadRegistration();
	// Clear the active registration up front: the device is disconnecting, so it
	// is no longer "currently registered" regardless of whether the network call
	// below succeeds. The retry is tracked separately in the pending queue.
	await clearRegistration();
	if (!reg) return;
	try {
		await unregisterPushDevice(configOf(reg), reg.token);
	} catch {
		await queuePendingUnregister(reg);
	}
	await flushPendingUnregisters();
}
