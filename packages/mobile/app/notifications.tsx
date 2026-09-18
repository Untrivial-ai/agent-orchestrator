import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useState } from "react";
import {
	ActivityIndicator,
	Alert,
	Pressable,
	RefreshControl,
	SectionList,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import {
	getNotifications,
	markAllNotificationsRead,
	markNotificationRead,
	type NotificationRecord,
} from "../lib/api";
import { haptics } from "../lib/haptics";
import { NotificationTypeIcon } from "../lib/notification-type-icon";
import {
	notificationSections,
	notificationAction,
	notificationVisual,
	relativeTime,
} from "../lib/notificationView";
import { useApp } from "../lib/store";
import { MINUTE_MS, useNow } from "../lib/useNow";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Dot, EmptyState, HeaderIconButton, ScreenHeader } from "../lib/ui";
import { press, space, type } from "../lib/tokens";

export { RouteErrorBoundary as ErrorBoundary } from "../lib/RouteErrorBoundary";

const PAGE_SIZE = 50;

// The durable record of what the daemon has notified about. Push only reaches a
// phone that was reachable at the time; this list is what the user can come back
// to afterwards.
export default function NotificationsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { config, connection, sessions, loading: sessionsLoading, restore } = useApp();
	const [restoringId, setRestoringId] = useState<string>();
	// A brief line rather than an Alert: the row is still there to act on, and
	// a modal would make a dead tap feel like an error.
	const [notice, setNotice] = useState<string>();
	const now = useNow(MINUTE_MS);
	const [items, setItems] = useState<NotificationRecord[]>([]);
	const [loading, setLoading] = useState(true);
	const [refreshing, setRefreshing] = useState(false);
	const [loadingMore, setLoadingMore] = useState(false);
	const [nextCursor, setNextCursor] = useState<string | undefined>(undefined);
	const [unreadCount, setUnreadCount] = useState(0);
	const [error, setError] = useState<string | null>(null);
	const sections = useMemo(() => notificationSections(items), [items]);

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
				setError(cause instanceof Error ? cause.message : "Couldn't load notifications.");
			} finally {
				setLoading(false);
				setRefreshing(false);
				setLoadingMore(false);
			}
		},
		[config, nextCursor, loadingMore],
	);

	useEffect(() => {
		void load("initial");
		// Paging state changes must not refetch the first page.
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [config]);

	function open(notification: NotificationRecord) {
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
		if (action.kind === "open") router.navigate(`/session/${action.sessionId}`);
		else if (action.kind === "prs") router.navigate("/prs");
		else if (action.kind === "restore") {
			haptics.warning();
			setNotice("This session is terminated. Tap restore to bring it back.");
		} else if (action.kind === "none") {
			haptics.warning();
			setNotice("That session is not available yet.");
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
		haptics.tap();
		setRestoringId(sessionId);
		void restore(sessionId)
			.then(() => {
				haptics.success();
				router.navigate(`/session/${sessionId}`);
			})
			.catch((cause) => Alert.alert("Couldn't restore the session", cause instanceof Error ? cause.message : String(cause)))
			.finally(() => setRestoringId(undefined));
	}

	async function markAll() {
		if (!config || unreadCount === 0) return;
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

	const subtitle = unreadCount > 0
		? `${unreadCount} ${unreadCount === 1 ? "update needs" : "updates need"} you`
		: "You're all caught up";

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Notifications"
				left={<HeaderIconButton icon="back" label="Back" onPress={() => router.back()} />}
				right={
					unreadCount > 0 ? (
						<HeaderIconButton icon="check" label="Mark all read" onPress={() => void markAll()} />
					) : undefined
				}
			/>

			{loading ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.accent} />
				</View>
			) : (
				<SectionList
					sections={sections}
					keyExtractor={(notification) => notification.id}
					contentInsetAdjustmentBehavior="automatic"
					contentContainerStyle={
						items.length === 0
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
						error && items.length > 0 ? (
							<View style={styles.inlineError}>
								<Feather name="alert-circle" size={15} color={t.red} />
								<Text selectable style={styles.inlineErrorText}>{error}</Text>
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
							onPress={() => open(item)}
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
						<EmptyState
							icon={error ? "alert-circle" : config ? "check-circle" : "server"}
							title={error ? "Couldn't load notifications" : config ? "All caught up" : "No desktop paired"}
							message={
								error ??
								(config
									? "Updates from workers and pull requests will appear here when they need you."
									: "Pair this phone with AO to receive worker and pull request updates.")
							}
						/>
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

function NotificationRow({ item, now, action, restoring, onPress, onRestore }: {
	item: NotificationRecord;
	now: number;
	action: "open" | "restore" | "prs" | "none";
	restoring: boolean;
	onPress: () => void;
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
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
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
		inlineErrorText: { color: t.red, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, flex: 1 },
		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.lg,
			paddingTop: space.lg,
			paddingBottom: space.xxs,
		},
		sectionLabel: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "500" },
		sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
		sectionCount: {
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
		noticeText: { flex: 1, color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
		rowPressed: { backgroundColor: t.bgElevated },
		rowCopy: { flex: 1, gap: space.hair },
		metaRow: { flexDirection: "row", alignItems: "center", gap: space.sm },
		kind: { color: t.textTertiary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
		time: {
			color: t.textFaint,
			fontSize: type.caption1.fontSize,
			lineHeight: type.caption1.lineHeight,
			fontVariant: ["tabular-nums"],
			marginLeft: "auto",
		},
		title: { color: t.textSecondary, fontSize: type.callout.fontSize, lineHeight: type.callout.lineHeight, fontWeight: "600" },
		titleUnread: { color: t.textPrimary, fontWeight: "700" },
		body: { color: t.textTertiary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
		footer: { paddingVertical: space.lg },
	});
