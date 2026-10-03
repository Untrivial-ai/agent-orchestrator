import { useLocalSearchParams, useRouter } from "expo-router";
import { ActivityIndicator, View } from "react-native";
import { hostRouteMatches } from "../../lib/hostRoute";
import TerminalSessionScreen from "../../lib/session/TerminalSessionScreen";
import { HostScope, useApp } from "../../lib/store";
import { useTheme } from "../../lib/ThemeProvider";
import { Button, EmptyState } from "../../lib/ui";

/**
 * A Chat session's terminal escape hatch attaches the shell handle through the
 * same authenticated mux and xterm renderer as every other AO terminal. The
 * handle, not the Chat session id, identifies this PTY.
 */
export default function ShellRoute() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	return hostId ? <HostScope key={hostId} hostId={hostId}><ShellRouteContent /></HostScope> : <ShellRouteContent />;
}

function ShellRouteContent() {
	const { hostId: routeHostId } = useLocalSearchParams<{ hostId?: string }>();
	const { config, currentHostId, connection, loading } = useApp();
	const router = useRouter();
	const t = useTheme();
	if (!hostRouteMatches(routeHostId, currentHostId)) {
		return (
			<View style={{ flex: 1, justifyContent: "center", backgroundColor: t.bgBase }}>
				{loading && !config ? <ActivityIndicator color={t.accent} /> : (
					<EmptyState
						icon="terminal"
						title="Shell belongs to another machine"
						message="Open it from that machine's session."
						action={<Button title="Open board" icon="activity" onPress={() => router.navigate("/")} />}
					/>
				)}
			</View>
		);
	}
	if (!config && connection === "closed") {
		return <View style={{ flex: 1, justifyContent: "center", backgroundColor: t.bgBase }}><EmptyState icon="wifi-off" title="Machine offline" message="This shell opens once the app reconnects." /></View>;
	}
	return <TerminalSessionScreen />;
}

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
