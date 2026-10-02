import { Host } from "@expo/ui";
import { Button, Image, Menu } from "@expo/ui/swift-ui";
import { accessibilityLabel as a11yLabel, buttonStyle, contentShape, frame, opacity, shapes, tint } from "@expo/ui/swift-ui/modifiers";
import type { ComponentProps } from "react";
import { ActivityIndicator, StyleSheet, View } from "react-native";
import { haptics } from "./haptics";
import type { ItemActionsMenuProps } from "./item-actions-menu.types";
import { useTheme, useThemeState } from "./ThemeProvider";
import { iconSize } from "./tokens";

const SIZE = 32;

/** A per-item "⋯" native pull-down, like desktop's per-comment and per-review menus. */
export function ItemActionsMenu({ actions, accessibilityLabel, disabled = false, loading = false }: ItemActionsMenuProps) {
	const t = useTheme();
	const { scheme } = useThemeState();
	if (loading) return <View style={styles.slot}><ActivityIndicator size="small" color={t.accent} /></View>;
	if (!actions.length) return null;
	return (
		<Host style={styles.slot} colorScheme={scheme}>
			<Menu
				// The label is the hit area: sized and shaped, or only the glyph (16×5pt) takes a tap.
				label={<Image systemName="ellipsis" size={iconSize.sm} modifiers={[frame({ width: SIZE, height: SIZE }), contentShape(shapes.rectangle())]} />}
				modifiers={[buttonStyle("plain"), tint(t.textSecondary), frame({ width: SIZE, height: SIZE }), opacity(disabled ? 0.4 : 1), a11yLabel(accessibilityLabel)]}
			>
				{actions.map((action) => (
					<Button
						key={action.id}
						label={action.label}
						systemImage={action.systemImage as ComponentProps<typeof Button>["systemImage"]}
						role={action.destructive ? "destructive" : undefined}
						onPress={() => { if (disabled) return; haptics.select(); action.onPress(); }}
					/>
				))}
			</Menu>
		</Host>
	);
}

const styles = StyleSheet.create({
	slot: { width: SIZE, height: SIZE, alignItems: "center", justifyContent: "center" },
});
