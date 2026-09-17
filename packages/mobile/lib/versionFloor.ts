// The version floor, carried in the JS bundle so it can be moved with an
// `eas update` instead of a store release. The values are EAS environment
// variables, so the dashboard is the source of truth and `--environment`
// re-inlines them on every publish. Unset means inert, which is how this ships.
//
// Two ways to silently disable it, both worth knowing before touching this:
// Metro only inlines a literal `process.env.X` dot access, so destructuring or
// bracket access reads as undefined in a build (same reason as
// `telemetry/config.ts`); and a variable set to secret visibility is not
// readable outside EAS servers, so it does not resolve during `eas update` and
// falls through to "" here without any error.

import type { Floor } from "./storeUpdate";

export const VERSION_FLOOR: Floor = {
	/** Below this the update stops being optional — see the interlock in `tierOf`. */
	min: process.env.EXPO_PUBLIC_AO_MIN_APP_VERSION ?? "",
	/** Below this we mention it once a day. The only lever iOS has before listing. */
	latest: process.env.EXPO_PUBLIC_AO_LATEST_APP_VERSION ?? "",
};
