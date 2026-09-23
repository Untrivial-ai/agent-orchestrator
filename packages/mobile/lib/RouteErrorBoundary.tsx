import { useNavigation, useRouter, type ErrorBoundaryProps } from "expo-router";
import { useEffect, useLayoutEffect } from "react";
import { StyleSheet, View } from "react-native";
import { haptics } from "./haptics";
import { captureMobileException } from "./sentry";
import { Button, EmptyState } from "./ui";
import { space } from "./tokens";
import { backOr } from "./backNavigation";

/**
 * What a route shows when it throws while rendering, instead of the app
 * terminating. expo-router only installs one where a route file opts in with
 * `export { RouteErrorBoundary as ErrorBoundary }` (or `SheetErrorBoundary`).
 * `RouteErrorBoundary.test.ts` requires every route file to be placed.
 *
 * Not on `app/_layout.tsx` with these fallbacks. expo-router wraps the route's own
 * component, and for that layout the component is what renders ThemeProvider. Both
 * fallbacks read the theme, and `useTheme` throws outside the provider, so the
 * fallback would throw in its turn.
 *
 * A fallback on any route leaves expo-updates' rollback as it was.
 * `ErrorRecovery.handleContentDidAppear` counts a launch as good once native
 * content first appears, and ExpoRoot's own SafeAreaProvider mounts a native view
 * before any route renders (it renders its children only once that view reports
 * insets; expo-router 57 passes it no `initialMetrics` on native). So by the time
 * a route renders, rollback is already off, boundary or not; it still covers a
 * throw before that mount, while the router sets itself up.
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
 * callback that the route releases when it unmounts (`sheetResult.ts`). A throw
 * after the sheet mounted runs that release, so retrying in place would bring back
 * a sheet whose choice goes nowhere. A throw on its first render never mounted it,
 * so the callback stays parked until the app restarts. The fallback cannot tell
 * the two apart, so it closes; opening the sheet again parks a fresh callback.
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
				action={<Button title="Close" icon="x" variant="ghost" onPress={() => backOr(router)} />}
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
	actions: { flexDirection: "row", gap: space.sm, alignItems: "center" },
});
