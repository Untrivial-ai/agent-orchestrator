// Headless component mounted once under the root layout. It owns the runtime
// side of push notifications: register after pairing (+ refresh on foreground),
// unregister when switching/leaving a daemon, and route notification taps
// (warm + cold start). See docs/adr/0001-mobile-push-notifications.md (D6, D7, D9).
import * as Device from "expo-device";
import * as Notifications from "expo-notifications";
import { useRootNavigationState, useRouter } from "expo-router";
import { useEffect, useRef } from "react";
import { AppState, Platform } from "react-native";
import { announceDevice, markNotificationRead } from "./api";
import { getInstallId } from "./installId";
import { notificationTarget } from "./notificationView";
import { configurePushHandler, ensureAndroidChannel, registerForPush, unpairFromServer } from "./push";
import { useApp } from "./store";
import { MOBILE_EVENTS } from "./telemetry/events";
import { mobileTelemetry } from "./telemetry/runtime";

// Set the foreground presentation policy before any notification can arrive.
configurePushHandler();

type PushData = {
	type?: string;
	sessionId?: string;
	projectId?: string;
	prUrl?: string;
	notificationId?: string;
};

export function PushManager(): null {
	const { config, configured, connection } = useApp();
	const router = useRouter();
	const navState = useRootNavigationState();

	const handledColdStart = useRef(false);
	const wasConfigured = useRef(false);

	// Create the Android channel once at startup.
	useEffect(() => {
		void ensureAndroidChannel();
	}, []);

	// When the user clears the server config, this phone has unpaired: tell that
	// daemon to drop the row entirely, not merely clear the token, so the old
	// desktop stops listing a phone that has moved on. Uses the persisted creds.
	useEffect(() => {
		if (wasConfigured.current && !configured) {
			void unpairFromServer();
		}
		wasConfigured.current = configured;
	}, [configured]);

	// Announce this device's identity as soon as it connects, and again on every
	// foreground while connected — with NO permission gate. This is what lets the
	// desktop roster show a paired phone that hasn't (yet, or ever) granted
	// notification permission or minted a push token, instead of it having no row
	// at all. The daemon upserts by install ID, so when registerForPush later
	// succeeds, the token attaches to this same row rather than creating a
	// second one.
	useEffect(() => {
		if (!config || connection !== "open") return;
		const safeAnnounce = () => {
			(async () => {
				const installId = await getInstallId();
				await announceDevice(config, {
					installId,
					platform: Platform.OS,
					deviceName: Device.deviceName ?? undefined,
				});
			})().catch((e) => console.warn("[push] announce failed", e));
		};
		safeAnnounce();
		const sub = AppState.addEventListener("change", (s) => {
			if (s === "active") safeAnnounce();
		});
		return () => sub.remove();
	}, [config, connection]);

	// Register after a successful pairing/connection, and refresh on every
	// foreground while connected. registerForPush persists the daemon it
	// registered with and, when the config points at a different daemon,
	// unregisters the old one first — so switching daemons (even across app
	// restarts) is handled inside push.ts, not here.
	//
	// `ask: false`: this fires automatically, milliseconds after connect, so it
	// must never spend the one-shot OS prompt. It re-registers users who already
	// granted permission; asking is left to a call the user initiated.
	useEffect(() => {
		if (!config || connection !== "open") return;
		const safeRegister = () => {
			registerForPush(config, { ask: false }).catch((e) => console.warn("[push] registration failed", e));
		};
		safeRegister();
		const sub = AppState.addEventListener("change", (s) => {
			if (s === "active") safeRegister();
		});
		return () => sub.remove();
	}, [config, connection]);

	// Route notification taps: warm via the response listener, cold start via
	// getLastNotificationResponseAsync (the listener alone misses the launch tap).
	useEffect(() => {
		if (!navState?.key) return; // wait until navigation is ready to accept routes

		const handle = (resp: Notifications.NotificationResponse | null, coldStart: boolean) => {
			if (!resp) return;
			route((resp.notification.request.content.data ?? {}) as PushData, coldStart);
		};

		if (!handledColdStart.current) {
			handledColdStart.current = true;
			void Notifications.getLastNotificationResponseAsync().then((r) => handle(r, true));
		}
		const sub = Notifications.addNotificationResponseReceivedListener((r) => handle(r, false));
		return () => sub.remove();
		// route() reads the latest config via ref-free closure; re-bind when it changes.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [navState?.key, config]);

	function route(data: PushData, coldStart = false) {
		// Reuse the one routing rule so the reported target can't disagree with
		// where the tap actually lands: notificationTarget returns /session/:id
		// only for a needs_input with a sessionId, and /prs for everything else.
		const destination = notificationTarget({ type: data.type ?? "", sessionId: data.sessionId });
		const target = destination.startsWith("/session") ? "session" : "prs";
		mobileTelemetry()?.capture(MOBILE_EVENTS.notificationOpened, { target, cold_start: coldStart });
		// Best-effort mark-read so unread counts stay consistent with the dashboard.
		if (config && data.notificationId) {
			markNotificationRead(config, data.notificationId).catch(() => {});
		}
		router.navigate(destination);
	}

	return null;
}
