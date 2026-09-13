import { useNavigation, useRouter, type ErrorBoundaryProps } from "expo-router";
import { useEffect, useLayoutEffect } from "react";
import { StyleSheet, View } from "react-native";
import { haptics } from "./haptics";
import { captureMobileException } from "./sentry";
import { Button, EmptyState } from "./ui";

/**
 * What a route shows when it throws while rendering, instead of the app
 * terminating. expo-router only installs one where a route file opts in with
 * `export { RouteErrorBoundary as ErrorBoundary }` (or `SheetErrorBoundary`).
 *
 * Never export one from a route that can render the first content of a launch —
 * `app/_layout.tsx`, `app/(tabs)/_layout.tsx`, `app/(tabs)/index.tsx`, and
 * `app/pair.tsx`, which the desktop's pairing QR (`aomobile://pair#…`) opens
 * directly — and never set `unstable_settings.screenErrorBoundary` on either
 * layout, which would wrap the board. First content is what expo-updates takes as
 * proof a launch worked: `ErrorRecovery.handleContentDidAppear` marks the launched
 * update successful and keeps only `waitForRemoteUpdate` and `crash` in its
 * recovery pipeline, dropping `launchNew` and `launchCached`. A fallback rendered
 * there would record a broken over-the-air update as a good launch that can never
 * roll back, where a fatal throw is what triggers the rollback.
 *
 * Every other route is reached by navigating from content that has already
 * appeared, so rollback is already off by the time it renders, and the choice is
 * only between this screen and a crash. The exception is a hand-made
 * `aomobile://` link: expo-router would cold-start any route from one, but nothing
 * AO sends links anywhere except `pair`. A fixed update still arrives either way:
 * `checkAutomatically` is `ON_LOAD`, and UpdatesManager checks again on a resume
 * after MIN_BACKGROUND_MS. `RouteErrorBoundary.test.ts` requires every route file
 * to be placed on one side of that line.
 */
export function RouteErrorBoundary({ error, retry }: ErrorBoundaryProps) {
	const router = useRouter();
	const navigation = useNavigation();
	useReportOnShow(error);
	// Header options belong to the navigator, so a header button the route set
	// outlives it and would act on a screen that is no longer there. A successful
	// retry remounts the route, which sets its own again.
	useLayoutEffect(() => {
		navigation.setOptions({ headerRight: undefined });
	}, [navigation]);
	// Onboarding is entered with `router.replace` and has no header, so there is
	// nothing to go back to; the same rule as MinimalBackButton.
	const canGoBack = router.canGoBack();

	return (
		<View style={styles.center}>
			<EmptyState
				icon="alert-triangle"
				title="This screen hit an unexpected error"
				message={canGoBack ? "Try again, or go back and open it again." : "Try again, or go to the board."}
				action={
					<View style={styles.actions}>
						<Button title="Try again" icon="refresh-cw" variant="ghost" onPress={() => void retry()} />
						{canGoBack ? null : <Button title="Go to board" icon="activity" onPress={() => router.replace("/")} />}
					</View>
				}
			/>
		</View>
	);
}

/**
 * The same, for a sheet route: its only action is Close. A sheet's opener parks a
 * callback that the route releases when it unmounts (`sheetResult.ts`), and the
 * throw has already unmounted it, so retrying in place would bring back a sheet
 * whose choice goes nowhere. Opening it again parks a fresh one.
 */
export function SheetErrorBoundary({ error }: ErrorBoundaryProps) {
	const router = useRouter();
	useReportOnShow(error);
	return (
		<View style={styles.center}>
			<EmptyState
				icon="alert-triangle"
				title="This sheet hit an unexpected error"
				message="Close it and open it again."
				action={<Button title="Close" icon="x" variant="ghost" onPress={() => router.back()} />}
			/>
		</View>
	);
}

// Runs on every show, including after a Try again that threw again, so that tap
// is never a silent no-op. Reported with the category and operation the desktop's
// boundary ends up with (`telemetry.ts` maps its source to `render_crash`).
function useReportOnShow(error: Error) {
	useEffect(() => {
		haptics.error();
		captureMobileException(error, { category: "render_crash", operation: "react_render" });
	}, [error]);
}

// No background: the route's own contentStyle shows through, so a sheet keeps its
// surface colour and a pushed screen keeps the base one.
const styles = StyleSheet.create({
	center: { flex: 1, alignItems: "center", justifyContent: "center" },
	actions: { flexDirection: "row", gap: 10, alignItems: "center" },
});
