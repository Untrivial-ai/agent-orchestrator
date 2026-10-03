import { Feather } from "../lib/icons";
import { useLocalSearchParams, useRouter, type Href } from "expo-router";
import { useOpenPage } from "../lib/pageNavigation";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
	ActivityIndicator,
	Alert,
	Pressable,
	RefreshControl,
	ScrollView,
	SectionList,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import {
	clearNotification,
	getNotifications,
	markAllNotificationsRead,
	markNotificationRead,
	type NotificationRecord,
} from "../lib/api";
import { haptics } from "../lib/haptics";
import { NotificationTypeIcon } from "../lib/notification-type-icon";
import {
	notificationSections,
	notificationRowsForHost,
	notificationAction,
	notificationTarget,
	notificationVisual,
	relativeTime,
} from "../lib/notificationView";
import { HostScope, useApp } from "../lib/store";
import { MINUTE_MS, useNow } from "../lib/useNow";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Button, Dot, EmptyState, HeaderIconButton, ScreenHeader } from "../lib/ui";
import { UnpairedState } from "../lib/UnpairedState";
import { press, space, type } from "../lib/tokens";
import { backOr } from "../lib/backNavigation";
import { shouldKeepPolling, userFacingError } from "../lib/connectionError";

export { RouteErrorBoundary as ErrorBoundary } from "../lib/RouteErrorBoundary";

const PAGE_SIZE = 50;

// The durable record of what the daemon has notified about. Push only reaches a
// phone that was reachable at the time; this list is what the user can come back
// to afterwards.
export default function NotificationsScreen() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	const { hostStates } = useApp();
	if (hostId) return <HostScope key={hostId} hostId={hostId}><NotificationsContent /></HostScope>;
	if (hostStates.length > 1) return <NotificationsHostPicker />;
	return <NotificationsContent />;
}

function NotificationsHostPicker() {
	const { hostStates } = useApp();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader title="Notifications" left={<HeaderIconButton icon="back" label="Back" onPress={() => backOr(router)} />} />
			<ScrollView contentContainerStyle={[styles.hostPicker, { paddingBottom: insets.bottom + space.lg }]}>
				<Text style={styles.hostPickerHint}>Choose a machine to view its notifications.</Text>
				{hostStates.map((host) => (
					<Button
						key={host.hostId}
						title={`${host.name}${host.notificationsUnread > 0 ? ` · ${host.notificationsUnread} unread` : ""}`}
						icon="server"
						variant="ghost"
						onPress={() => router.push({ pathname: "/notifications", params: { hostId: host.hostId } })}
					/>
				))}
			</ScrollView>
		</View>
	);
}

function NotificationsContent() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const openPage = useOpenPage();
	const insets = useSafeAreaInsets();
	const { config, connection, unreachable, errorStatus, sessions, loading: sessionsLoading, restore } = useApp();
	const [restoringId, setRestoringId] = useState<string>();
	const [clearingIds, setClearingIds] = useState<Set<string>>(() => new Set());
	// A brief line rather than an Alert: the row is still there to act on, and
	// a modal would make a dead tap feel like an error.
	const [notice, setNotice] = useState<string>();
	const now = useNow(MINUTE_MS);
	const [items, setItems] = useState<NotificationRecord[]>([]);
	const [itemsHostId, setItemsHostId] = useState<string>();
	const currentConfig = useRef(config);
	currentConfig.current = config;
	const [loading, setLoading] = useState(true);
	const [refreshing, setRefreshing] = useState(false);
	const [loadingMore, setLoadingMore] = useState(false);
	const [nextCursor, setNextCursor] = useState<string | undefined>(undefined);
	const [unreadCount, setUnreadCount] = useState(0);
	const [error, setError] = useState<string | null>(null);
	const visibleItems = useMemo(() => notificationRowsForHost(items, itemsHostId, config?.hostId), [items, itemsHostId, config?.hostId]);
	const sections = useMemo(() => notificationSections(visibleItems), [visibleItems]);
	const visibleUnreadCount = config?.hostId && itemsHostId === config.hostId ? unreadCount : 0;

	const load = useCallback(
		async (mode: "initial" | "refresh" | "more") => {
			if (!config) {
				setLoading(false);
				return;
			}
			if (mode === "more" && (!nextCursor || loadingMore)) return;
			if (mode === "refresh") setRefreshing(true);
			if (mode === "more") setLoadingMore(true);
			setError(null);
			try {
				const page = await getNotifications(config, {
					status: "all",
					limit: PAGE_SIZE,
					cursor: mode === "more" ? nextCursor : undefined,
				});
				if (currentConfig.current !== config) return;
				setItemsHostId(config.hostId);
				setItems((previous) => {
					if (mode !== "more") return page.notifications;
					const seen = new Set(previous.map((notification) => notification.id));
					return [
						...previous,
						...page.notifications.filter((notification) => !seen.has(notification.id)),
					];
				});
				setNextCursor(page.nextCursor);
				setUnreadCount(page.unreadCount);
			} catch (cause) {
				if (currentConfig.current !== config) return;
				setItemsHostId(config.hostId);
				setError(userFacingError(cause, "Couldn't load notifications."));
			} finally {
				if (currentConfig.current === config) {
					setLoading(false);
					setRefreshing(false);
					setLoadingMore(false);
				}
			}
		},
		[config, nextCursor, loadingMore],
	);

	useEffect(() => {
		setItems([]);
		setItemsHostId(undefined);
		setNextCursor(undefined);
		setUnreadCount(0);
		setError(null);
		setRefreshing(false);
		setLoadingMore(false);
		setLoading(Boolean(config));
		void load("initial");
		// Paging state changes must not refetch the first page.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [config]);

	// A load that failed while the desktop was unreachable retries as soon as the
	// board's poll reconnects, which is what the offline state promises. Keyed on
	// the reconnect itself: `load` clears `error` as it starts, so keying on the
	// error would loop against an endpoint that keeps failing while connected.
	const previousConnection = useRef(connection);
	useEffect(() => {
		const reconnected = previousConnection.current !== "open" && connection === "open";
		previousConnection.current = connection;
		if (reconnected && error) void load("refresh");
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [connection]);
	// The board's poll is the app's view of the link: when it is down, say so in
	// the board's words rather than as a failed load.
	const offline = Boolean(config) && unreachable && Boolean(error);
	// A rejected password (or the lockout it leads to) stops the board's poll for
	// good. Retrying would only spend another failed attempt toward the lockout,
	// so offer the fix instead, as the board does.
	const rejected = errorStatus !== null && !shouldKeepPolling(errorStatus);

	function open(notification: NotificationRecord) {
		if (!config?.hostId || itemsHostId !== config.hostId) return;
		haptics.tap();
		setItems((previous) =>
			previous.map((item) =>
				item.id === notification.id ? { ...item, status: "read" } : item,
			),
		);
		if (notification.status === "unread") {
			setUnreadCount((count) => Math.max(0, count - 1));
			if (config) markNotificationRead(config, notification.id).catch(() => {});
		}
		// What a tap does depends on the session behind it, exactly as the renderer
		// decides: a terminated agent waiting on input is restored, not opened.
		const action = notificationAction(notification, sessionState(notification.sessionId));
		if (action.kind === "open") router.navigate({ pathname: "/session/[id]", params: { id: action.sessionId, hostId: config.hostId } });
		else if (action.kind === "review") openPage(notificationTarget({ ...notification, hostId: config.hostId }, config.hostId) as Href);
		else if (action.kind === "prs") router.navigate({ pathname: "/prs", params: { hostId: config.hostId } });
		else if (action.kind === "restore") {
			haptics.warning();
			setNotice("This session is terminated. Tap restore to bring it back.");
		} else if (action.kind === "none") {
			haptics.warning();
			setNotice("That session is not available yet.");
		}
	}

	async function clear(notification: NotificationRecord) {
		if (!config?.hostId || itemsHostId !== config.hostId || clearingIds.has(notification.id)) return;
		const source = config;
		setClearingIds((current) => new Set(current).add(notification.id));
		try {
			await clearNotification(source, notification.id);
			if (currentConfig.current !== source) return;
			setItems((current) => current.filter((item) => item.id !== notification.id));
			if (notification.status === "unread") setUnreadCount((count) => Math.max(0, count - 1));
		} catch (cause) {
			if (currentConfig.current === source) setError(userFacingError(cause, "Couldn't clear notification."));
		} finally {
			setClearingIds((current) => {
				const next = new Set(current);
				next.delete(notification.id);
				return next;
			});
		}
	}

	function sessionState(sessionId?: string) {
		const session = sessionId ? sessions.find((item) => item.id === sessionId) : undefined;
		return {
			terminated: Boolean(session?.isTerminated || session?.status === "terminated"),
			// Without the board we cannot tell a terminated session from a live one,
			// and guessing lands on a screen that cannot resolve it.
			sessionsReady: !sessionsLoading && sessions.length > 0,
		};
	}

	function restoreSession(sessionId: string) {
		if (!config?.hostId || itemsHostId !== config.hostId) return;
		haptics.tap();
		setRestoringId(sessionId);
		void restore(sessionId)
			.then(() => {
				haptics.success();
				router.navigate({ pathname: "/session/[id]", params: { id: sessionId, hostId: config.hostId } });
			})
			.catch((cause) => Alert.alert("Couldn't restore the session", userFacingError(cause)))
			.finally(() => setRestoringId(undefined));
	}

	async function markAll() {
		if (!config?.hostId || itemsHostId !== config.hostId || unreadCount === 0) return;
		haptics.success();
		setItems((previous) => previous.map((item) => ({ ...item, status: "read" })));
		setUnreadCount(0);
		try {
			await markAllNotificationsRead(config);
		} catch {
			// Restore server truth if the optimistic update failed.
			void load("refresh");
		}
	}

	useEffect(() => {
		if (!notice) return;
		const timer = setTimeout(() => setNotice(undefined), 2800);
		return () => clearTimeout(timer);
	}, [notice]);

	const subtitle = visibleUnreadCount > 0
		? `${visibleUnreadCount} ${visibleUnreadCount === 1 ? "update needs" : "updates need"} you`
		: "You're all caught up";

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Notifications"
				left={<HeaderIconButton icon="back" label="Back" onPress={() => backOr(router)} />}
				right={
					visibleUnreadCount > 0 ? (
						<HeaderIconButton icon="check" label="Mark all read" onPress={() => void markAll()} />
					) : undefined
				}
			/>

			{loading || (config && itemsHostId !== config.hostId) ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.accent} />
				</View>
			) : (
				<SectionList
					sections={sections}
					keyExtractor={(notification) => notification.id}
					contentInsetAdjustmentBehavior="automatic"
					contentContainerStyle={
						visibleItems.length === 0
							? { flexGrow: 1 }
							: { paddingBottom: insets.bottom + 24 }
					}
					stickySectionHeadersEnabled={false}
					refreshControl={
						<RefreshControl
							refreshing={refreshing}
							onRefresh={() => {
								haptics.tap();
								void load("refresh");
							}}
							tintColor={t.accent}
						/>
					}
					onEndReached={() => void load("more")}
					onEndReachedThreshold={0.4}
					ListHeaderComponent={
						error && visibleItems.length > 0 ? (
							<View style={styles.inlineError}>
								<Feather name="alert-circle" size={15} color={t.red} />
								<Text selectable style={styles.inlineErrorText}>{offline ? "This machine is offline. Showing the last notifications loaded." : error}</Text>
							</View>
						) : null
					}
					renderSectionHeader={({ section }) => (
						section.title
							? <NotificationSectionHeader title={section.title} count={section.data.length} />
							: null
					)}
					renderItem={({ item }) => (
						<NotificationRow
							item={item}
							now={now}
							action={notificationAction(item, sessionState(item.sessionId)).kind}
							restoring={restoringId === item.sessionId}
							clearing={clearingIds.has(item.id)}
							onPress={() => open(item)}
							onClear={() => void clear(item)}
							onRestore={() => item.sessionId && restoreSession(item.sessionId)}
						/>
					)}
					ListFooterComponent={
						loadingMore ? (
							<View style={styles.footer}>
								<ActivityIndicator color={t.accent} />
							</View>
						) : null
					}
					ListEmptyComponent={
						offline ? (
							<EmptyState
								icon="wifi-off"
								title="This machine is offline"
								message="Notifications load once the app reconnects."
								action={<Button title="Retry" icon="refresh-cw" variant="ghost" onPress={() => void load("refresh")} />}
							/>
						) : !config && !error ? (
							// Shared with the tabs: "Connecting…" while the launch race runs,
							// the pairing prompt only once it has found no machine.
							<UnpairedState />
						) : (
							<EmptyState
								icon={error ? "alert-circle" : "check-circle"}
								title={error ? "Couldn't load notifications" : "All caught up"}
								message={error ?? "Updates from workers and pull requests will appear here when they need you."}
								action={
									!error ? undefined
										: rejected ? <Button title="Scan pairing code" icon="maximize" onPress={() => router.push("/pair")} />
										: <Button title="Retry" icon="refresh-cw" variant="ghost" onPress={() => void load("refresh")} />
								}
							/>
						)
					}
				/>
			)}

			{/* Sits above the list rather than replacing it: the row that prompted
			    this is still on screen and still has a restore button to press. */}
			{notice ? (
				<View pointerEvents="none" style={[styles.notice, { bottom: insets.bottom + 24 }]}>
					<Feather name="alert-circle" size={15} color={t.amber} />
					<Text style={styles.noticeText}>{notice}</Text>
				</View>
			) : null}
		</View>
	);
}

function NotificationSectionHeader({ title, count }: { title: string; count: number }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.sectionHeader}>
			<Text style={styles.sectionLabel}>{title}</Text>
			<View style={styles.sectionRule} />
			<Text style={styles.sectionCount}>{count}</Text>
		</View>
	);
}

function NotificationRow({ item, now, action, restoring, clearing, onPress, onClear, onRestore }: {
	item: NotificationRecord;
	now: number;
	action: "open" | "review" | "restore" | "prs" | "none";
	restoring: boolean;
	clearing: boolean;
	onPress: () => void;
	onClear: () => void;
	onRestore: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const visual = notificationVisual(t, item.type);
	const unread = item.status === "unread";

	return (
		<View style={[styles.row, action === "none" && styles.rowInert]}>
		<Pressable
			onPress={onPress}
			accessibilityRole="button"
			accessibilityState={{ disabled: action === "none" }}
			accessibilityLabel={`${item.title || visual.label}, ${visual.label}`}
			accessibilityHint={action === "restore" ? "This session is terminated. Use the restore button to bring it back." : undefined}
			style={({ pressed }) => [styles.rowTap, pressed && action !== "none" && styles.rowPressed]}
		>
			<View style={styles.rowCopy}>
				<View style={styles.metaRow}>
					<NotificationTypeIcon icon={visual.icon} color={unread ? visual.color : t.textFaint} />
					<Text style={[styles.kind, unread && { color: visual.color }]} numberOfLines={1}>
						{visual.label}
					</Text>
					{unread ? <Dot color={t.accent} size={7} /> : null}
					<Text style={styles.time}>{relativeTime(item.createdAt, now)}</Text>
				</View>
				<Text style={[styles.title, unread && styles.titleUnread]} numberOfLines={1}>
					{item.title || visual.label}
				</Text>
				{item.body ? (
					<Text style={styles.body} numberOfLines={1}>
						{item.body}
					</Text>
				) : null}
			</View>
		</Pressable>
		{action === "restore" ? (
			<Pressable
				onPress={onRestore}
				disabled={restoring}
				accessibilityRole="button"
				accessibilityLabel={`Restore ${item.title || visual.label}`}
				accessibilityState={{ busy: restoring, disabled: restoring }}
				style={({ pressed }) => [styles.restoreButton, pressed && styles.restorePressed]}
			>
				{restoring
					? <ActivityIndicator size="small" color={t.textSecondary} />
					: <Feather name="rotate-ccw" size={20} color={t.textSecondary} />}
			</Pressable>
		) : null}
		<Pressable
			onPress={onClear}
			disabled={clearing}
			accessibilityRole="button"
			accessibilityLabel={`Clear ${item.title || visual.label}`}
			accessibilityState={{ busy: clearing, disabled: clearing }}
			style={({ pressed }) => [styles.clearButton, pressed && styles.rowPressed]}
		>
			{clearing ? <ActivityIndicator size="small" color={t.textSecondary} /> : <Feather name="x" size={18} color={t.textTertiary} />}
		</Pressable>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		hostPicker: { paddingHorizontal: space.lg, paddingTop: space.xl, gap: space.md },
		hostPickerHint: { ...type.body, color: t.textSecondary, marginBottom: space.xs },
		inlineError: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			marginHorizontal: space.lg,
			paddingHorizontal: space.md,
			paddingVertical: space.sm,
			borderRadius: 12,
			borderCurve: "continuous",
			backgroundColor: t.tintRed,
		},
		inlineErrorText: { fontFamily: "Geist_400Regular", color: t.red, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, flex: 1 },
		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.lg,
			paddingTop: space.lg,
			paddingBottom: space.xxs,
		},
		sectionLabel: { fontFamily: "Geist_500Medium", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
		sectionCount: { fontFamily: "Geist_600SemiBold",
			color: t.textFaint,
			fontSize: type.caption1.fontSize,
			lineHeight: type.caption1.lineHeight,
			fontWeight: "600",
			fontVariant: ["tabular-nums"],
		},
		row: {
			minHeight: 76,
			flexDirection: "row",
			alignItems: "center",
			borderBottomWidth: StyleSheet.hairlineWidth,
			borderBottomColor: t.borderSubtle,
		},
		rowTap: { flex: 1, minWidth: 0, paddingLeft: space.lg, paddingRight: space.sm, paddingVertical: space.sm },
		// Its own column, wide enough to hit without aiming: restoring is the only
		// thing a terminated row can do, and it should not share the row's tap.
		restoreButton: { width: 56, alignSelf: "stretch", alignItems: "center", justifyContent: "center" },
		clearButton: { width: 48, alignSelf: "stretch", alignItems: "center", justifyContent: "center" },
		restorePressed: { backgroundColor: t.bgElevated },
		rowInert: { opacity: 0.55 },
		notice: {
			position: "absolute",
			left: 18,
			right: 18,
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.md,
			paddingVertical: space.md,
			borderRadius: 12,
			borderCurve: "continuous",
			backgroundColor: t.bgElevated,
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
		},
		noticeText: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
		rowPressed: { backgroundColor: t.bgElevated },
		rowCopy: { flex: 1, gap: space.hair },
		metaRow: { flexDirection: "row", alignItems: "center", gap: space.sm },
		kind: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
		time: { fontFamily: "Geist_400Regular",
			color: t.textFaint,
			fontSize: type.caption1.fontSize,
			lineHeight: type.caption1.lineHeight,
			fontVariant: ["tabular-nums"],
			marginLeft: "auto",
		},
		title: { fontFamily: "Geist_600SemiBold", color: t.textSecondary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600" },
		titleUnread: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontWeight: "600" },
		body: { fontFamily: "Geist_400Regular", color: t.textTertiary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
		footer: { paddingVertical: space.lg },
	});
