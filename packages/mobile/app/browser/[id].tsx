import { Redirect, useLocalSearchParams } from "expo-router";

/** Backward-compatible deep link; Preview now owns the session browser. */
export default function LegacyBrowserRoute() {
	const { id: rawID } = useLocalSearchParams<{ id: string }>();
	return <Redirect href={{ pathname: "/preview/[id]", params: { id: String(rawID ?? "") } }} />;
}

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
