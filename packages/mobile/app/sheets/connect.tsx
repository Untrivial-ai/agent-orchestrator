import { useLocalSearchParams, useRouter } from "expo-router";
import { useEffect, useState } from "react";
import { ActivityIndicator, Text, View } from "react-native";
import { ManualConnectSheet } from "../../lib/ManualConnectSheet";
import { findHost, type Host } from "../../lib/hosts";
import { releaseSheetResult, takeSheetResult } from "../../lib/sheetResult";
import { backOr } from "../../lib/backNavigation";
import { useApp } from "../../lib/store";
import { useTheme } from "../../lib/ThemeProvider";

// Manual pairing and editing use the same native connection form.
export default function ConnectSheetRoute() {
	const router = useRouter();
	const { resultKey, hostId } = useLocalSearchParams<{ resultKey?: string; hostId?: string }>();
	const { config, currentHostId, reloadConfig } = useApp();
	const theme = useTheme();
	const [editingHost, setEditingHost] = useState<Host | null | undefined>(hostId === undefined ? null : undefined);

	useEffect(() => () => releaseSheetResult(resultKey), [resultKey]);
	useEffect(() => {
		if (hostId === undefined) return;
		let current = true;
		void findHost(hostId).then((host) => { if (current) setEditingHost(host); }).catch(() => { if (current) setEditingHost(null); });
		return () => { current = false; };
	}, [hostId]);

	if (editingHost === undefined) return <ActivityIndicator style={{ flex: 1 }} color={theme.accent} />;
	if (hostId !== undefined && !editingHost) return <View style={{ flex: 1, alignItems: "center", justifyContent: "center" }}><Text style={{ color: theme.textPrimary }}>This machine is no longer paired.</Text></View>;
	const activeEndpointIndex = editingHost && editingHost.id === currentHostId
		? editingHost.endpoints.findIndex((endpoint) => endpoint.host === config?.host && endpoint.port === Number(config.httpPort) && endpoint.secure === !!config.secure)
		: -1;

	return (
		<ManualConnectSheet
			editingHost={editingHost ?? undefined}
			editingEndpointIndex={activeEndpointIndex < 0 ? 0 : activeEndpointIndex}
			onConnected={() => {
				const done = takeSheetResult<void>(resultKey);
				if (editingHost) void reloadConfig().catch(() => {});
				backOr(router);
				done?.();
			}}
		/>
	);
}

export { SheetErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
