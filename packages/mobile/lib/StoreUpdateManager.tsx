import { usePathname, useRootNavigationState, useRouter } from "expo-router";
import { useEffect, useRef, useState } from "react";
import * as Application from "expo-application";
import { AppState, Platform } from "react-native";
import { checkStore, openOrStartUpdate, openStore, startPlayUpdate } from "./inAppUpdates";
import { storeUpdateSheetRoute } from "./sheetResult";
import { floorSignal, floorTarget, nextSnooze, shouldPrompt, tierOf, type Snooze, type StoreCheck } from "./storeUpdate";
import { VERSION_FLOOR } from "./versionFloor";
import { loadSnooze, saveSnooze } from "./storeUpdateStore";
import { useApp } from "./store";

// Headless. Mounted beside UpdatesManager in `app/_layout.tsx`; asks the store
// once per launch whether a newer **native binary** exists, and either nudges
// (dismissible sheet) or hands over to Play's own fullscreen updater. Decision
// logic lives in `storeUpdate.ts` so it is unit-testable.
//
// The OTA manager next door covers JS updates. A new native build mints a new
// fingerprint runtime, which is the case OTA does not reach on its own — short of
// deliberately publishing from that older release's tag.
export function StoreUpdateManager(): null {
	const router = useRouter();
	const pathname = usePathname();
	const navState = useRootNavigationState();
	const { config } = useApp();
	const [snooze, setSnooze] = useState<Snooze | null | undefined>(undefined);
	// Runs once per launch, like OnboardingGate: a second prompt in one session
	// is nagging, not helpfulness.
	const ran = useRef(false);
	// Armed only by a handover Play actually accepted, and disarmed by the first
	// recovery: Play reports DEVELOPER_TRIGGERED_UPDATE_IN_PROGRESS for the whole
	// download, so without this a resume would bounce the user to the store every
	// time they came back to the app.
	const started = useRef(false);
	// `pathname` read at push time rather than at check time — see prompt().
	const pathnameRef = useRef(pathname);
	pathnameRef.current = pathname;

	useEffect(() => {
		loadSnooze().then(setSnooze);
	}, []);

	useEffect(() => {
		if (ran.current) return;
		// Dev builds are loaded from Metro, not installed from a store, so Play
		// rejects every call and the App Store has nothing to answer about.
		// Deliberately not `Updates.isEnabled`, which means "expo-updates is
		// configured" — a different axis that would also switch this off in a
		// release build that happened to ship without OTA.
		if (__DEV__) return;
		if (snooze === undefined) return; // still loading; deciding now could skip a due prompt
		if (!navState?.key) return; // wait until navigation can accept routes
		// Never on top of onboarding: `config` is null until the store's first load
		// resolves, and an unconfigured user is being redirected to /onboarding by
		// OnboardingGate right now.
		if (config === null || config.host.trim().length === 0) return;
		// Don't interrupt someone who navigated somewhere deliberately.
		if (pathname !== "/") return;
		ran.current = true;
		void prompt();
	}, [snooze, navState?.key, config, pathname, router]);

	async function prompt() {
		// Deliberately no pre-gate on the snooze before this call, tempting as the
		// saved round trip is: `shouldPrompt` prompts immediately when the store
		// version changes, so skipping the lookup would mean never noticing a new
		// release. One GET per cold start is the cheaper side of that trade.
		const check = await checkStore();
		const floor = floorSignal(Application.nativeApplicationVersion, VERSION_FLOOR);
		let tier = tierOf(check, Platform.OS, floor);
		if (tier === "none") return;

		if (tier === "required") {
			started.current = await startPlayUpdate();
			if (started.current) return;
			// Play would not take over, so in practice this is a nudge. Say that
			// honestly and let the snooze apply, rather than reprompting every
			// launch with a sheet the user can dismiss anyway.
			tier = "recommended";
		}
		const confirmed = check?.updateAvailable === true;
		// What the sheet names: the store's version when it answered, else the floor's.
		const display = confirmed ? check?.storeVersion : floorTarget(VERSION_FLOOR);
		// What the snooze is keyed on: the floor's version while the floor has an
		// opinion about this install. That stays constant whether or not the store
		// answers, so a device with flaky Play access does not reset its own
		// dismissal count every launch by alternating between the floor's version
		// string and Play's versionCode. The cost: a spent count then restarts when
		// the floor moves, not when the store does — the floor is the ask. Once the
		// install is above the floor, prompts are store-driven and the store's
		// version keys them, so a new release resets a spent count as before.
		const key = floor === "none" ? check?.storeVersion : floorTarget(VERSION_FLOOR);
		const args = { tier, version: key, snooze: snooze ?? null, now: Date.now() };
		if (!shouldPrompt({ ...args, stalenessDays: check?.playStalenessDays })) return;
		openSheet(check, display, key, confirmed);
	}

	function openSheet(check: StoreCheck | null, display: string | undefined, key: string | undefined, confirmed: boolean) {
		// The check above is allowed 12s; if the user opened something in the
		// meantime, don't land a sheet on top of it. Leaving `ran` false gives the
		// prompt another chance when they come back to the board.
		if (pathnameRef.current !== "/") {
			ran.current = false;
			return;
		}
		const snoozeNow = () => {
			const next = nextSnooze(snooze ?? null, key, Date.now());
			setSnooze(next);
			void saveSnooze(next);
		};
		router.push(
			storeUpdateSheetRoute({
				// A store-confirmed version is only nameable on iOS — Android's is a
				// versionCode. A floor version is a real version string either way.
				version: confirmed && Platform.OS !== "ios" ? undefined : display,
				storeConfirmed: confirmed,
				onAction: (action) => {
					if (action === "update") {
						// Only Play's flow leaves a download to recover; a listing does not.
						void openOrStartUpdate(check).then((via) => {
							started.current = via === "play";
							// Nothing opened — a floor-driven nudge on a device with no
							// TestFlight, say. The sheet is already gone, so without a
							// snooze the same dead end would replay every launch, never
							// counting toward MAX_DISMISSALS.
							if (via === "none") snoozeNow();
						});
						return;
					}
					snoozeNow();
				},
			}),
		);
	}

	useEffect(() => {
		if (__DEV__ || Platform.OS !== "android") return;
		const sub = AppState.addEventListener("change", (state) => {
			if (state !== "active" || !started.current) return;
			started.current = false; // one recovery per handover
			// Google asks that a stalled immediate update be picked up again when
			// the app returns. `expo-in-app-updates` cannot — its startUpdate bails
			// while Play reports DEVELOPER_TRIGGERED_UPDATE_IN_PROGRESS — so the
			// listing is the only recovery left, and beats stranding the user.
			void checkStore().then((check) => {
				if (check?.updateInProgress) void openStore(check);
			});
		});
		return () => sub.remove();
	}, []);

	return null;
}
