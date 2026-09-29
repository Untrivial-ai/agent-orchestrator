import { Feather } from "./icons";
import { useRef } from "react";
import { Pressable, StyleSheet } from "react-native";
import { useTheme } from "./ThemeProvider";
import type { NativeHeaderButtonIcon } from "./native-header-button";
import { iconSize, radius, space } from "./tokens";

const icons: Record<NativeHeaderButtonIcon, keyof typeof Feather.glyphMap> = {
	menu: "menu",
	bell: "bell",
	close: "x",
	check: "check",
	back: "chevron-left",
	more: "more-horizontal",
};

const TAP_SLOP = space.md;
const PRESS_FALLBACK_DELAY_MS = 32;

export function NativeHeaderButton({
	icon,
	label,
	onPress,
}: {
	icon: NativeHeaderButtonIcon;
	label: string;
	onPress: () => void;
}) {
	const t = useTheme();
	const pressSequence = useRef(0);
	const pressHandledSequence = useRef(-1);
	const pressCancelledSequence = useRef(-1);
	const pressStart = useRef({ x: 0, y: 0 });
	const pressMaxMovement = useRef(0);
	return (
		<Pressable
			testID={`header-${icon}`}
			accessibilityRole="button"
			accessibilityLabel={label}
			hitSlop={{ top: space.sm, right: space.md, bottom: space.sm, left: space.xl }}
			android_ripple={{ color: t.accentTint, borderless: true, radius: 22 }}
			onTouchStart={(event) => {
				pressSequence.current += 1;
				pressStart.current = { x: event.nativeEvent.pageX, y: event.nativeEvent.pageY };
				pressMaxMovement.current = 0;
			}}
			onTouchMove={(event) => {
				const dx = event.nativeEvent.pageX - pressStart.current.x;
				const dy = event.nativeEvent.pageY - pressStart.current.y;
				pressMaxMovement.current = Math.max(pressMaxMovement.current, Math.hypot(dx, dy));
			}}
			onTouchEnd={() => {
				const sequence = pressSequence.current;
				const movement = pressMaxMovement.current;
				setTimeout(() => {
					if (pressHandledSequence.current === sequence) return;
					if (pressCancelledSequence.current === sequence) return;
					if (movement > TAP_SLOP) return;
					pressHandledSequence.current = sequence;
					onPress();
				}, PRESS_FALLBACK_DELAY_MS);
			}}
			onTouchCancel={() => {
				pressCancelledSequence.current = pressSequence.current;
			}}
			onPress={() => {
				pressHandledSequence.current = pressSequence.current;
				onPress();
			}}
			style={({ pressed }) => [
				styles.button,
				{ backgroundColor: pressed ? t.accentTint : t.bgElevatedHover, borderColor: t.borderStrong },
			]}
		>
			<Feather name={icons[icon]} size={iconSize.lg} color={t.textSecondary} />
		</Pressable>
	);
}

const styles = StyleSheet.create({
	button: {
		width: 44,
		height: 44,
		borderRadius: radius.pill, borderCurve: "continuous",
		borderWidth: StyleSheet.hairlineWidth,
		alignItems: "center",
		justifyContent: "center",
		overflow: "hidden",
	},
});
