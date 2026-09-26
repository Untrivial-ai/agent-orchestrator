import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect } from "react";
import { ManualConnectSheet } from "../../lib/ManualConnectSheet";
import { releaseSheetResult, takeSheetResult } from "../../lib/sheetResult";
import { backOr } from "../../lib/backNavigation";

// Manual host/password entry, as a native form sheet. The pairing screen parks
// what should happen after a successful connect (it navigates on), so this only
// has to report that it succeeded.
export default function ConnectSheetRoute() {
	const router = useRouter();
	const { resultKey } = useLocalSearchParams<{ resultKey?: string }>();

	useEffect(() => () => releaseSheetResult(resultKey), [resultKey]);

	return (
		<ManualConnectSheet
			onConnected={() => {
				const done = takeSheetResult<void>(resultKey);
				backOr(router);
				done?.();
			}}
		/>
	);
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
