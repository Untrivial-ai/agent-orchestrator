import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, TextInput, View } from "react-native";
import { useTheme } from "./ThemeProvider";
import { workerDockVisibility } from "./worker-dock-layout";
import type { WorkerDockProps } from "./worker-dock";
import { iconSize, radius, space, type } from "./tokens";

export function WorkerDock({
	query,
	onQueryChange,
	onSpawn,
	searchOpen,
	onSearchClose,
	onOpenControls,
	projectFiltered,
}: WorkerDockProps) {
	const t = useTheme();
	const visibility = workerDockVisibility(searchOpen);
	return (
		<View style={styles.row}>
			{visibility.showControls ? (
				<RoundButton
					icon="sliders"
					label="Filter and search workers"
					active={projectFiltered}
					onPress={onOpenControls}
					testID="worker-controls"
				/>
			) : null}
			{visibility.showSearch ? (
				<View style={[styles.searchWrap, { backgroundColor: t.bgElevated, borderColor: t.borderDefault }]}>
					<TextInput
						value={query}
						onChangeText={onQueryChange}
						placeholder="Search workers"
						placeholderTextColor={t.textTertiary}
						selectionColor={t.accent}
						autoFocus
						autoCapitalize="none"
						autoCorrect={false}
						returnKeyType="search"
						testID="worker-search"
						style={[styles.search, { color: t.textPrimary }]}
					/>
					<Pressable
						testID="worker-search-close"
						accessibilityRole="button"
						accessibilityLabel="Close worker search"
						hitSlop={6}
						onPress={onSearchClose}
						style={({ pressed }) => [styles.searchClose, pressed && { backgroundColor: t.bgSubtle }]}
					>
						<Feather name="x" size={iconSize.lg} color={t.textSecondary} />
					</Pressable>
				</View>
			) : visibility.showControls && visibility.showSpawn ? <View style={styles.flexSpacer} /> : null}
			{visibility.showSpawn ? (
				<RoundButton icon="plus" label="Spawn worker" onPress={onSpawn} testID="spawn-worker" />
			) : null}
		</View>
	);
}

function RoundButton({ icon, label, onPress, active = false, testID }: {
	icon: keyof typeof Feather.glyphMap;
	label: string;
	onPress: () => void;
	active?: boolean;
	testID: string;
}) {
	const t = useTheme();
	return (
		<Pressable
			testID={testID}
			accessibilityRole="button"
			accessibilityLabel={label}
			android_ripple={{ color: t.accentTint, borderless: true, radius: 26 }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.action,
				{
					backgroundColor: active || pressed ? t.accentTint : t.bgElevated,
					borderColor: active ? t.accent : t.borderDefault,
				},
			]}
		>
			<Feather name={icon} size={iconSize.lg} color={active ? t.accent : t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	// The parent dock is itself a horizontal row, so explicitly claim its full
	// width before asking the spacer to separate the two actions.
	row: { flex: 1, height: 52, flexDirection: "row", gap: space.sm },
	flexSpacer: { flex: 1 },
	searchWrap: {
		flex: 1,
		height: 52,
		flexDirection: "row",
		alignItems: "center",
		borderRadius: radius.pill,
		borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		overflow: "hidden",
	},
	search: {
		flex: 1,
		height: 52,
		paddingLeft: space.lg,
		paddingRight: space.xs,
		fontSize: type.callout.fontSize,
	},
	searchClose: { width: 44, height: 44, borderRadius: radius.pill, borderCurve: "continuous", alignItems: "center", justifyContent: "center", marginRight: space.xxs },
	action: {
		width: 44,
		height: 44,
		borderRadius: radius.pill, borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
