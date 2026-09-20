import { Feather } from "./icons";
import { Pressable, StyleSheet, View } from "react-native";
import { type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { iconSize, press, type } from "./tokens";

const ACTION_WIDTH = 64;
const CONTROL_SIZE = 44;

export function WorkerRowActions({
	title,
	pinned,
	onSetPinned,
	onDelete,
}: {
	title: string;
	pinned: boolean;
	onSetPinned(pinned: boolean): void;
	onDelete(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);

	return (
		<View style={styles.actions}>
			<PinAction
				accessibilityLabel={pinned ? `Unpin ${title}` : `Pin ${title}`}
				pinned={pinned}
				onPress={() => onSetPinned(!pinned)}
			/>
			<Action
				accessibilityLabel={`Delete ${title}`}
				icon="trash-2"
				iconColor={t.red}
				rippleColor={t.tintRed}
				style={styles.deleteControl}
				onPress={onDelete}
			/>
		</View>
	);
}

function PinAction({
	accessibilityLabel,
	pinned,
	onPress,
}: {
	accessibilityLabel: string;
	pinned: boolean;
	onPress(): void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.slot}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={accessibilityLabel}
				android_ripple={{ color: t.accentTint, borderless: false, radius: CONTROL_SIZE / 2 }}
				onPress={onPress}
				style={({ pressed }) => [styles.control, styles.pinControl, pressed && styles.pressed]}
			>
				<Feather
					name={pinned ? "pin-off" : "pin"}
					size={20}
					color={pinned ? t.amber : t.accent}
					style={{ transform: [{ rotate: "28deg" }] }}
				/>
			</Pressable>
		</View>
	);
}

function Action({
	accessibilityLabel,
	icon,
	iconColor,
	rippleColor,
	style,
	onPress,
}: {
	accessibilityLabel: string;
	icon: keyof typeof Feather.glyphMap;
	iconColor: string;
	rippleColor: string;
	style: object;
	onPress(): void;
}) {
	const styles = useThemedStyles(makeStyles);
	return (
		<View style={styles.slot}>
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={accessibilityLabel}
				android_ripple={{ color: rippleColor, borderless: false, radius: CONTROL_SIZE / 2 }}
				onPress={onPress}
				style={({ pressed }) => [styles.control, style, pressed && styles.pressed]}
			>
				<Feather name={icon} size={iconSize.lg} color={iconColor} />
			</Pressable>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		actions: { width: ACTION_WIDTH * 2, height: 76, flexDirection: "row" },
		slot: { width: ACTION_WIDTH, height: 76, alignItems: "center", justifyContent: "center" },
		control: {
			width: CONTROL_SIZE,
			height: CONTROL_SIZE,
			borderRadius: CONTROL_SIZE / 2, borderCurve: "continuous",
			borderWidth: StyleSheet.hairlineWidth,
			alignItems: "center",
			justifyContent: "center",
			overflow: "hidden",
		},
		pinControl: { borderColor: t.accent, backgroundColor: t.accentTint },
		deleteControl: { borderColor: t.red, backgroundColor: t.tintRed },
		pressed: { opacity: press.opacity },
	});
