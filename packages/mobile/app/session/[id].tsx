import { useLocalSearchParams, useRouter } from "expo-router";
import { useCallback, useEffect, useRef, useState } from "react";
import { ActivityIndicator, AppState, StyleSheet, View } from "react-native";
import { shouldPoll } from "../../lib/appStatePoll";
import { ChatSessionScreen } from "../../lib/chat/ChatSessionScreen";
import { isConfigured, machineIdentity } from "../../lib/config";
import { lookUpSession } from "../../lib/session/sessionLookup";
import TerminalSessionScreen from "../../lib/session/TerminalSessionScreen";
import {
	currentSessionLookup,
	sessionLookupDue,
	sessionLookupKey,
	sessionRouteView,
	type KeyedSessionLookup,
} from "../../lib/session/sessionRoute";
import { useApp } from "../../lib/store";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import type { Theme } from "../../lib/theme";
import { Button, EmptyState } from "../../lib/ui";

/**
 * The committed session mode is daemon-authoritative, including after an
 * explicit controller handoff, for every session the board lists: the lists are
 * refreshed on every poll. A missing row is looked up rather than guessing
 * Terminal and briefly attaching a nonexistent PTY. What each outcome shows is
 * `sessionRouteView`, and when the route asks is `sessionLookupDue`.
 */
export default function MobileSessionRoute() {
	const { id: rawId } = useLocalSearchParams<{ id: string }>();
	const id = String(rawId ?? "");
	const router = useRouter();
	const { sessions, orchestrators, config, connection, loading } = useApp();
	const listed = sessions.find((item) => item.id === id) ?? orchestrators.find((item) => item.id === id);
	const isListed = Boolean(listed);
	const configured = config === null ? null : isConfigured(config);
	const machine = config ? machineIdentity(config) : "";
	const key = sessionLookupKey(machine, id);
	const [stored, setStored] = useState<KeyedSessionLookup | null>(null);
	const [attempt, setAttempt] = useState(0);
	const lookup = currentSessionLookup(stored, key);
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);

	// Read by the lookup effect, not dependencies of it. An answer landing must not
	// itself cause another request, or a persistent failure would loop; and the
	// config object changes identity when a re-race lands on a different endpoint
	// for the same machine, which restarts the store's poll and so re-runs the
	// effect through `connection` anyway. Declared first, so it has run by the time
	// that effect does.
	const latest = useRef({ config, configured, lookup });
	useEffect(() => {
		latest.current = { config, configured, lookup };
	});
	const machineRef = useRef(machine);

	// A notification can deep-link into a session before the board's next poll has
	// populated it, and the board never lists an orchestrator it dropped. Ask the
	// daemon directly instead of reading the miss as "not found".
	useEffect(() => {
		const machineChanged = machineRef.current !== machine;
		machineRef.current = machine;
		if (isListed) {
			// Drop the last answer so a later miss starts from a fresh question
			// instead of briefly showing what the lookup said before it was listed.
			setStored(null);
			return;
		}
		const now = latest.current;
		if (
			!now.config ||
			!sessionLookupDue({
				listed: isListed,
				configured: now.configured,
				connection,
				// Read, not a dependency: a foreground restarts the store's poll, which
				// re-runs this effect through `connection` once it opens.
				appActive: shouldPoll(AppState.currentState),
				machineChanged,
				lookup: now.lookup,
			})
		) {
			return;
		}
		// A failure from before the reconnect would otherwise stay on screen, Retry
		// and all, while this request is in flight.
		setStored(null);
		let cancelled = false;
		void lookUpSession(now.config, id).then((answer) => {
			if (!cancelled) setStored({ key, lookup: answer });
		});
		return () => {
			cancelled = true;
		};
		// `attempt` is read only through this list: bumping it is how Retry asks
		// again. `connection` turning "open" is how a lookup that failed, or was
		// rejected, gets asked again once the board has reconnected.
	}, [attempt, connection, id, isListed, key, machine]);

	const retry = useCallback(() => {
		// Clearing first swaps the button for the spinner, so a second tap cannot
		// land while the retry is in flight.
		setStored(null);
		setAttempt((n) => n + 1);
	}, []);

	const view = sessionRouteView({ listed, configured, connection, loading, lookup });

	switch (view.kind) {
		case "screen":
			return view.session.mode === "chat" ? (
				<ChatSessionScreen session={view.session} />
			) : (
				<TerminalSessionScreen session={view.session} />
			);
		case "loading":
			return (
				<View style={styles.center}>
					<ActivityIndicator color={t.accent} />
				</View>
			);
		case "unpaired":
			// The board's own unpaired state, word for word.
			return (
				<View style={styles.center}>
					<EmptyState
						icon="server"
						title="No desktop paired"
						message="Scan the pairing code from AO → Settings → Connect Mobile to drive your agents from here."
						action={<Button title="Scan pairing code" icon="maximize" onPress={() => router.push("/pair")} />}
					/>
				</View>
			);
		case "offline":
			return (
				<View style={styles.center}>
					<EmptyState
						icon="wifi-off"
						title="Not connected to your desktop"
						message="This session loads once the app reconnects."
						action={<Button title="Open board" icon="activity" variant="ghost" onPress={() => router.navigate("/")} />}
					/>
				</View>
			);
		case "ended":
			return (
				<View style={styles.center}>
					<EmptyState icon="archive" title="This session has ended" />
				</View>
			);
		case "missing":
			return (
				<View style={styles.center}>
					<EmptyState icon="search" title="Session not found" />
				</View>
			);
		case "failed":
			return (
				<View style={styles.center}>
					<EmptyState
						icon="alert-circle"
						title="Couldn't load this session"
						action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={retry} />}
					/>
				</View>
			);
	}
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		center: { flex: 1, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
