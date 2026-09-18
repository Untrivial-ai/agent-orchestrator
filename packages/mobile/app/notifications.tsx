import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { useCallback, useEffect, useMemo, useState } from "react";
import {
	ActivityIndicator,
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
	notificationTarget,
	notificationVisual,
	relativeTime,
} from "../lib/notificationView";
import { useApp } from "../lib/store";
import { MINUTE_MS, useNow } from "../lib/useNow";
import type { Theme } from "../lib/theme";
import { useTheme, useThemedStyles } from "../lib/ThemeProvider";
import { Dot, EmptyState, HeaderIconButton, ScreenHeader } from "../lib/ui";

const PAGE_SIZE = 50;

// The durable record of what the daemon has notified about. Push only reaches a
// phone that was reachable at the time; this list is what the user can come back
// to afterwards.
export default function NotificationsScreen() {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { config, connection } = useApp();
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
				setError(cause instanceof Error ? cause.message : "Could not load notifications.");
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
		router.navigate(notificationTarget(notification));
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

	const subtitle = unreadCount > 0
		? `${unreadCount} ${unreadCount === 1 ? "update needs" : "updates need"} you`
		: "You're all caught up";

	return (
		<View style={styles.screen}>
			<View style={{ height: insets.top }} />
			<ScreenHeader
				title="Notifications"
				subtitle={subtitle}
				status={connection}
				left={<HeaderIconButton icon="back" label="Back" onPress={() => router.back()} />}
				right={
					unreadCount > 0 ? (
						<HeaderIconButton icon="check" label="Mark all read" onPress={() => void markAll()} />
					) : undefined
				}
			/>

			{loading ? (
				<View style={styles.center}>
					<ActivityIndicator color={t.blue} />
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
							tintColor={t.blue}
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
						<NotificationSectionHeader title={section.title} count={section.data.length} />
					)}
					renderItem={({ item }) => (
						<NotificationRow item={item} now={now} onPress={() => open(item)} />
					)}
					ListFooterComponent={
						loadingMore ? (
							<View style={styles.footer}>
								<ActivityIndicator color={t.blue} />
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

function NotificationRow({ item, now, onPress }: { item: NotificationRecord; now: number; onPress: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const visual = notificationVisual(t, item.type);
	const unread = item.status === "unread";

	return (
		<Pressable
			onPress={onPress}
			accessibilityRole="button"
			accessibilityLabel={`${item.title || visual.label}, ${visual.label}`}
			style={({ pressed }) => [styles.row, pressed && styles.rowPressed]}
		>
			<View style={styles.rowCopy}>
				<View style={styles.metaRow}>
					<NotificationTypeIcon icon={visual.icon} color={unread ? visual.color : t.textFaint} />
					<Text style={[styles.kind, unread && { color: visual.color }]} numberOfLines={1}>
						{visual.label}
					</Text>
					{unread ? <Dot color={t.blue} size={7} /> : null}
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
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		screen: { flex: 1, backgroundColor: t.bgBase },
		center: { flex: 1, alignItems: "center", justifyContent: "center", paddingVertical: 60 },
		inlineError: {
			flexDirection: "row",
			alignItems: "center",
			gap: 8,
			marginHorizontal: 18,
			paddingHorizontal: 12,
			paddingVertical: 10,
			borderRadius: 12,
			borderCurve: "continuous",
			backgroundColor: t.tintRed,
		},
		inlineErrorText: { color: t.red, fontSize: 13, lineHeight: 18, flex: 1 },
		sectionHeader: {
			flexDirection: "row",
			alignItems: "center",
			gap: 10,
			paddingHorizontal: 18,
			paddingTop: 18,
			paddingBottom: 5,
		},
		sectionLabel: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "500" },
		sectionRule: { flex: 1, height: StyleSheet.hairlineWidth, backgroundColor: t.borderSubtle },
		sectionCount: {
			color: t.textFaint,
			fontSize: 12,
			lineHeight: 16,
			fontWeight: "600",
			fontVariant: ["tabular-nums"],
		},
		row: {
			minHeight: 76,
			paddingHorizontal: 18,
			paddingVertical: 10,
			borderBottomWidth: StyleSheet.hairlineWidth,
			borderBottomColor: t.borderSubtle,
		},
		rowPressed: { backgroundColor: t.bgElevated },
		rowCopy: { flex: 1, gap: 3 },
		metaRow: { flexDirection: "row", alignItems: "center", gap: 8 },
		kind: { color: t.textTertiary, fontSize: 12, lineHeight: 16, fontWeight: "600" },
		time: {
			color: t.textFaint,
			fontSize: 12,
			lineHeight: 16,
			fontVariant: ["tabular-nums"],
			marginLeft: "auto",
		},
		title: { color: t.textSecondary, fontSize: 16, lineHeight: 21, fontWeight: "600" },
		titleUnread: { color: t.textPrimary, fontWeight: "700" },
		body: { color: t.textTertiary, fontSize: 13, lineHeight: 18 },
		footer: { paddingVertical: 18 },
	});
