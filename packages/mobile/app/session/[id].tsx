import { useLocalSearchParams } from "expo-router";
import { useEffect, useState } from "react";
import { ActivityIndicator, StyleSheet, Text, View } from "react-native";
import { ChatSessionScreen } from "../../lib/chat/ChatSessionScreen";
import TerminalSessionScreen from "../../lib/session/TerminalSessionScreen";
import { useApp } from "../../lib/store";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import type { Theme } from "../../lib/theme";

/**
 * The committed session mode is daemon-authoritative, including after an
 * explicit controller handoff. The board's cached session supplies the fast
 * path; a missing row waits for the next refresh rather than guessing Terminal
 * and briefly attaching a nonexistent PTY.
 */
export default function MobileSessionRoute() {
	const { id: rawId } = useLocalSearchParams<{ id: string }>();
	const id = String(rawId ?? "");
	const { sessions, orchestrators, loading, refresh } = useApp();
	const session = sessions.find((item) => item.id === id) ?? orchestrators.find((item) => item.id === id);
	const [resolving, setResolving] = useState(!session);
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);

	// A notification can deep-link into a session before the board's next poll has
	// populated it. Resolve the daemon-owned mode before choosing a renderer; never
	// guess TUI merely because the local cache is cold.
	useEffect(() => {
		if (session) {
			setResolving(false);
			return;
		}
		let cancelled = false;
		setResolving(true);
		void refresh().finally(() => {
			if (!cancelled) setResolving(false);
		});
		return () => { cancelled = true; };
	}, [id, refresh, session]);

	if (session?.mode === "chat") return <ChatSessionScreen session={session} />;
	if (session?.mode === "tui") return <TerminalSessionScreen />;

	return (
		<View style={styles.center}>
			{loading || resolving ? <ActivityIndicator color={t.blue} /> : <Text style={styles.copy}>Session not found.</Text>}
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
		copy: { color: t.textSecondary, fontSize: 14 },
	});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
