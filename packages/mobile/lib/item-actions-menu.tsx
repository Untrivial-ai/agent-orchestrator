import { Feather } from "@expo/vector-icons";
import { ActivityIndicator, Alert, Pressable, StyleSheet, View } from "react-native";
import { haptics } from "./haptics";
import type { ItemActionsMenuProps } from "./item-actions-menu.types";
import { useTheme } from "./ThemeProvider";

const SIZE = 32;

/** Android/web "⋯": the same actions as the iOS pull-down, listed in an alert. */
export function ItemActionsMenu({ actions, accessibilityLabel, disabled = false, loading = false }: ItemActionsMenuProps) {
	const t = useTheme();
	if (loading) return <View style={styles.slot}><ActivityIndicator size="small" color={t.accent} /></View>;
	if (!actions.length) return null;
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={accessibilityLabel}
			disabled={disabled}
			hitSlop={8}
			style={[styles.slot, disabled && styles.disabled]}
			onPress={() => {
				haptics.tap();
				Alert.alert(accessibilityLabel, undefined, [
					...actions.map((action) => ({ text: action.label, style: action.destructive ? ("destructive" as const) : ("default" as const), onPress: action.onPress })),
					{ text: "Cancel", style: "cancel" as const },
				]);
			}}
		>
			<Feather name="more-horizontal" size={18} color={t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	slot: { width: SIZE, height: SIZE, alignItems: "center", justifyContent: "center" },
	disabled: { opacity: 0.4 },
});
