// Headless component mounted once under the root layout. It owns the runtime
// side of push notifications: register after pairing (+ refresh on foreground),
// and route notification taps
// (warm + cold start). See docs/adr/0001-mobile-push-notifications.md (D6, D7, D9).
import * as Device from "expo-device";
import * as Notifications from "expo-notifications";
import { useRootNavigationState, useRouter, type Href } from "expo-router";
import { useOpenPage } from "./pageNavigation";
import { useEffect, useRef } from "react";
import { AppState, Platform } from "react-native";
import { announceDevice, markNotificationRead } from "./api";
import { shouldPoll } from "./appStatePoll";
import { getInstallId } from "./installId";
import { findHost } from "./hosts";
import { notificationTarget } from "./notificationView";
import { configurePushHandler, ensureAndroidChannel, onManualPushRegistration, registerForPush } from "./push";
import { useApp } from "./store";
import { MOBILE_EVENTS } from "./telemetry/events";
import { mobileTelemetry } from "./telemetry/runtime";

// Set the foreground presentation policy before any notification can arrive.
configurePushHandler();

type PushData = {
	type?: string;
	hostId?: string;
	sessionId?: string;
	projectId?: string;
	prUrl?: string;
	notificationId?: string;
};

export function PushManager(): null {
	const { hostStates } = useApp();
	const router = useRouter();
	const openPage = useOpenPage();
	const navState = useRootNavigationState();

	const handledColdStart = useRef(false);
	const hostsRef = useRef(hostStates);
	hostsRef.current = hostStates;
	const connected = shouldPoll(AppState.currentState)
		? hostStates.flatMap(({ config, connection }) => config && connection === "open" ? [config] : [])
		: [];
	// Polls can refresh host snapshots without changing a connection. Only a
	// changed endpoint, credential or open/closed transition refreshes push.
	const connectedKey = JSON.stringify(connected.map(({ hostId, host, httpPort, secure, password }) => [hostId, host, httpPort, secure, password, hostStates.find((state) => state.hostId === hostId)?.name]));
	const connectedRef = useRef(connected);
	connectedRef.current = connected;

	// Create the Android channel once at startup.
	useEffect(() => {
		void ensureAndroidChannel();
	}, []);

	// Each daemon keeps its own device roster and push registration. Reconnect
	// re-verifies identity before a foreground refresh can use a bearer.
	useEffect(() => {
		if (connectedRef.current.length === 0) return;
		const refresh = () => {
			const configs = connectedRef.current;
			void getInstallId().then((installId) => {
				for (const config of configs) {
					void announceDevice(config, {
						installId,
						platform: Platform.OS,
						deviceName: Device.deviceName ?? undefined,
					}).catch((e) => console.warn("[push] announce failed", e));
				}
			}).catch((e) => console.warn("[push] announce failed", e));
			// Automatic refresh never spends the one-shot OS permission prompt.
			for (const config of configs) {
				void registerForPush(config, { ask: false }).catch((e) => console.warn("[push] registration failed", e));
			}
		};
		refresh();
		const offManualRegistration = onManualPushRegistration(refresh);
		return offManualRegistration;
	}, [connectedKey]);

	// Route notification taps: warm via the response listener, cold start via
	// getLastNotificationResponseAsync (the listener alone misses the launch tap).
	useEffect(() => {
		if (!navState?.key) return; // wait until navigation is ready to accept routes

		const handle = (resp: Notifications.NotificationResponse | null, coldStart: boolean) => {
			if (!resp) return;
			void route((resp.notification.request.content.data ?? {}) as PushData, coldStart);
		};

		if (!handledColdStart.current) {
			handledColdStart.current = true;
			void Notifications.getLastNotificationResponseAsync().then((r) => handle(r, true));
		}
		const sub = Notifications.addNotificationResponseReceivedListener((r) => handle(r, false));
		return () => sub.remove();
		// route() reads the latest host snapshots through hostsRef.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [navState?.key]);

	async function route(data: PushData, coldStart = false) {
		// A cold-start tap can arrive before the host runtimes are hydrated. The
		// saved pairing is the authority for whether this host may be opened.
		const paired = data.hostId ? await findHost(data.hostId).catch(() => null) : null;
		const hosts = hostsRef.current;
		const destination = notificationTarget({ type: data.type ?? "", sessionId: data.sessionId, prUrl: data.prUrl, hostId: data.hostId }, paired?.id);
		if (destination === "/") {
			// Legacy or forgotten-machine push has no safe destination here.
			router.navigate("/");
			return;
		}
		const source = hosts.find(({ hostId }) => hostId === data.hostId);
		const target = destination.startsWith("/review")
			? "review"
			: destination.startsWith("/session") ? "session" : "prs";
		mobileTelemetry()?.capture(MOBILE_EVENTS.notificationOpened, { target, cold_start: coldStart });
		// Best-effort mark-read so unread counts stay consistent with the dashboard.
		if (source?.config && source.connection === "open" && data.notificationId) {
			markNotificationRead(source.config, data.notificationId).catch(() => {});
		}
		// A review opens as a page even when a sheet is up when the tap arrives.
		if (target === "review") openPage(destination as Href);
		else router.navigate(destination);
	}

	return null;
}
