import { Feather } from "../icons";
import { useEffect, useRef } from "react";
import { Animated, Pressable, StyleSheet, View } from "react-native";
import type { Theme } from "../theme";
import { haptics } from "../haptics";
import type { VoiceMode, VoiceState } from "./types";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { MicGlass } from "./mic-glass";

// The dictation control, with two gestures:
//
//   hold        - push-to-talk. Cheapest audio session, fastest to capture,
//                 built-in mic. For short phrases where warm-up dominates.
//   double-tap  - latch on, hands-free, Bluetooth-capable. Tap once more to
//                 finish. For long prompts where warm-up stops mattering.
//
// Sized to the send button and sitting beside it: dictation is a primary way to
// talk to an agent from a phone, and as one more outlined grey pill in the key
// row it was indistinguishable from `zoom-out`.
//
// It is never the only control that commits. In the chat composer it is a bare
// glyph beside a filled send button (`variant="plain"`), so the pair still has a
// clear hierarchy; where it sits in a row of outlined keys it keeps the glass
// disc (`variant="glass"`). Either way the only state that changes its shape is
// recording, which goes solid red — the one moment it should outrank the screen.
//
/** Matches the send button so the two controls are the same size. */
export const MIC_SIZE = 40;
const MIC_RADIUS = 12;

export function MicKey({
	state,
	mode,
	onPressIn,
	onPressOut,
	circular = false,
	size = MIC_SIZE,
	variant = "glass",
	glyphSize = 17,
}: {
	state: VoiceState;
	mode: VoiceMode;
	onPressIn(): void;
	onPressOut(): void;
	circular?: boolean;
	size?: number;
	/**
	 * `glass` is the control in a row of outlined keys, where the material is
	 * what separates it from its neighbours. `plain` is the one in the chat's
	 * composer pill: there, the filled send button beside it is the only solid
	 * shape, and a second disc turned the pair into two equally loud buttons.
	 */
	variant?: "glass" | "plain";
	glyphSize?: number;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const live = state === "recording" || state === "starting";
	const latched = live && mode === "latched";
	const denied = state === "denied";
	// Rendered even with no recogniser on the device. Returning null here used to
	// remove the control from the row entirely, reflowing everything beside it.
	const unavailable = state === "unavailable";
	const disabled = unavailable || denied;
	const controlShape = { width: size, height: size, borderRadius: circular ? size / 2 : MIC_RADIUS };

	// A breathing ring behind the circle while the mic is open — the same
	// Animated loop `Dot` uses, rather than a new animation dependency.
	const pulse = useRef(new Animated.Value(0)).current;
	useEffect(() => {
		if (!live) {
			pulse.setValue(0);
			return;
		}
		const loop = Animated.loop(
			Animated.sequence([
				Animated.timing(pulse, { toValue: 1, duration: 900, useNativeDriver: true }),
				Animated.timing(pulse, { toValue: 0, duration: 900, useNativeDriver: true }),
			]),
		);
		loop.start();
		return () => loop.stop();
	}, [live, pulse]);

	// Idle is the only glass state: recording, denied and unavailable each keep a
	// fill that says something the material would soften.
	const glass = variant === "glass" && !live && !denied && !unavailable;
	const fill = live ? t.red : denied ? t.tintRed : unavailable ? t.bgElevated : "transparent";
	const ink = live ? t.textPrimary : denied ? t.red : unavailable ? t.textFaint : t.textPrimary;
	const radius = circular ? size / 2 : MIC_RADIUS;

	return (
		<View style={[styles.slot, { width: size, height: size }]}>
			{live ? (
				<Animated.View
					pointerEvents="none"
					style={[
						styles.ring,
						controlShape,
						{
							opacity: pulse.interpolate({ inputRange: [0, 1], outputRange: [0.45, 0] }),
							transform: [{ scale: pulse.interpolate({ inputRange: [0, 1], outputRange: [1, 1.45] }) }],
						},
					]}
				/>
			) : null}
			{glass ? <MicGlass size={size} radius={radius} /> : null}
			<Pressable
				accessibilityRole="button"
				accessibilityLabel={
					unavailable
						? "Dictation unavailable on this device"
						: latched
							? "Stop dictating"
							: "Hold to dictate, or double-tap for hands-free"
				}
				accessibilityState={{ busy: live, disabled }}
				disabled={disabled}
				style={({ pressed }) => [
					styles.mic,
					controlShape,
					{ backgroundColor: fill },
					variant === "glass" && circular && !glass && styles.circular,
					latched && styles.latched,
					unavailable && styles.unavailable,
					pressed && !disabled && { opacity: 0.85 },
				]}
				onPressIn={() => {
					haptics.tap();
					onPressIn();
				}}
				onPressOut={onPressOut}
				// A finger sliding off still ends a held phrase — otherwise the mic
				// would stay open with no obvious way to close it. Latched mode is
				// unaffected: it ignores the finger by design.
				onTouchCancel={onPressOut}
			>
				<Feather name={denied || unavailable ? "mic-off" : "mic"} size={glyphSize} color={ink} />
			</Pressable>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	slot: { alignItems: "center", justifyContent: "center" },
	mic: {
		alignItems: "center",
		justifyContent: "center",
	},
	circular: { borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderDefault },
	ring: {
		position: "absolute",
		backgroundColor: t.red,
	},
	// Latched holds the mic open with no finger on it, so it gets an outline the
	// held state doesn't — this is the state that must never be missed.
	latched: { borderWidth: 2, borderColor: t.textPrimary },
	unavailable: { borderWidth: 1, borderColor: t.borderSubtle, opacity: 0.5 },
});
