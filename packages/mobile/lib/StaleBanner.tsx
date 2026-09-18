import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, Text, View } from "react-native";

import { haptics } from "./haptics";
import { staleAgeLabel } from "./screenState";
import { useApp } from "./store";
import type { Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { fontScaleCap, space, type } from "./tokens";
import { useStaleness } from "./useStaleness";

/**
 * Says out loud that the rows below are older than they look.
 *
 * The screens used to put their failure copy inside `ListEmptyComponent`, which
 * means it only ever appeared when there was nothing to show. A list that had
 * already loaded kept its rows and said nothing at all — so a board whose daemon
 * died an hour ago looked exactly like a live one, and people act on those rows.
 *
 * This component owns the clock rather than taking an age as a prop, and that is
 * deliberate: `useStaleness` re-renders whatever calls it every tick, so a screen
 * that called it would re-render its whole list on a timer. Here the cost is one
 * small component. It also self-gates to null when the data is current, so
 * screens can render it unconditionally.
 */
export function StaleBanner({ error = false, onRetry }: { error?: boolean; onRetry?: () => void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { getLastSyncAt, config } = useApp();
	const { stale, ageMs } = useStaleness(getLastSyncAt);

	// Fresh and healthy: say nothing. Silence is the correct state here, and it
	// keeps the caller free of a conditional.
	if (!stale && !error) return null;

	// A reachability failure is worth more alarm than mere age: one means the
	// desktop is gone, the other can just be a laptop that slept.
	const tone = error ? t.red : t.amber;
	const fill = error ? t.tintRed : t.tintAmber;
	const host = config?.host?.trim();
	const age = staleAgeLabel(ageMs);
	const text = error
		? host
			? `Can't reach ${host} — showing data from ${age}`
			: `Can't reach your desktop — showing data from ${age}`
		: `Showing data from ${age}`;

	return (
		<View
			accessibilityRole="alert"
			accessibilityLabel={text}
			style={[styles.banner, { backgroundColor: fill }]}
		>
			<Feather name={error ? "wifi-off" : "clock"} size={12} color={tone} />
			<Text
				numberOfLines={1}
				maxFontSizeMultiplier={fontScaleCap.chrome}
				style={styles.bannerText}
			>
				{text}
			</Text>
			{onRetry ? (
				<Pressable
					accessibilityRole="button"
					accessibilityLabel="Retry now"
					hitSlop={7}
					onPress={() => {
						haptics.tap();
						onRetry();
					}}
				>
					<Text maxFontSizeMultiplier={fontScaleCap.chrome} style={[styles.bannerAction, { color: tone }]}>
						Retry
					</Text>
				</Pressable>
			) : null}
		</View>
	);
}

// Metrics mirror the chat screen's InlineBanner so the app has one banner
// language rather than two that nearly match.
const makeStyles = (t: Theme) =>
	StyleSheet.create({
		banner: {
			minHeight: 35,
			flexDirection: "row",
			alignItems: "center",
			gap: space.sm,
			paddingHorizontal: space.md,
			paddingVertical: space.xs,
			borderBottomWidth: 1,
			borderBottomColor: t.borderSubtle,
		},
		bannerText: { flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
		bannerAction: { fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight, fontWeight: "700" },
	});
