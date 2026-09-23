"use client";

import { useEffect } from "react";

import { track } from "@/lib/analytics";
import { launchContextFromBrowser } from "@/lib/analytics/launch/context";
import {
	applyVisitPlan,
	planLaunchEvents,
	readVisitState,
	whenConsented,
} from "@/lib/analytics/launch/visit";

/**
 * Fires the once-per-visit launch events. Renders nothing.
 *
 * All decisions and storage/consent handling live in `launch/visit.ts`
 * (injectable, unit-tested); the launch super-properties are registered from
 * the init `loaded` callback in `instrumentation-client.ts`, before consent
 * opt-in, so even the first pageview of an already-consented visitor carries
 * them — registering from a React effect would lose that race (see
 * `marketing-consent.ts` for the same lesson). This component only wires the
 * two together.
 */
export function LaunchAnalytics() {
	useEffect(() => {
		if (typeof window === "undefined") return;

		const context = launchContextFromBrowser();

		return whenConsented(() => {
			applyVisitPlan(
				planLaunchEvents(readVisitState(context.source)),
				(event) => track(event),
			);
		});
	}, []);

	return null;
}
