import { Feather } from "@expo/vector-icons";
import { Pressable, StyleSheet, TextInput, View } from "react-native";
import { MicKey } from "../voice/MicKey";
import type { VoiceMode, VoiceState } from "../voice/types";
import { useTheme, useThemedStyles, useThemeState } from "../ThemeProvider";
import type { Theme } from "../theme";
import { haptics } from "../haptics";
import type { SendTarget } from "./sendRoute";

// One field, one send button. The normal route sends a message to the agent; the
// terminal route is an explicit escape hatch for prompts that need a literal
// character, and the session screen auto-engages it when the daemon reports a
// blocked permission prompt (see sendRoute.ts).
export function Composer({
	value,
	onChangeText,
	onSend,
	sending,
	target,
	onTargetChange,
	voice,
	keyboardVisible,
	onDismissKeyboard,
	targetLocked,
}: {
	value: string;
	onChangeText: (v: string) => void;
	onSend: () => void;
	sending: boolean;
	target: SendTarget;
	onTargetChange: (target: SendTarget) => void;
	voice: { state: VoiceState; mode: VoiceMode; pressIn(): void; pressOut(): void };
	keyboardVisible: boolean;
	onDismissKeyboard: () => void;
	/** Plain worktree shells have no agent route; hide the misleading toggle. */
	targetLocked?: boolean;
}) {
	const t = useTheme();
	const { scheme } = useThemeState();
	const styles = useThemedStyles(makeStyles);
	const canSend = !!value.trim() && !sending;

	return (
		<View style={styles.bar}>
			<View style={styles.field}>
				<TextInput
					style={styles.input}
					value={value}
					onChangeText={onChangeText}
					placeholder={target === "terminal" ? "Send to terminal..." : "Message the agent..."}
					placeholderTextColor={t.textFaint}
					multiline
					keyboardAppearance={scheme}
					selectionColor={t.blue}
					// No autoFocus: the bar is always mounted now, so focusing on mount
					// would pop the keyboard over the terminal every time the screen opens.
				/>
				{!targetLocked ? <Pressable
					accessibilityRole="button"
					accessibilityLabel={target === "terminal" ? "Switch to chat" : "Switch to terminal"}
					accessibilityState={{ selected: target === "terminal" }}
					onPress={() => { haptics.select(); onTargetChange(target === "terminal" ? "agent" : "terminal"); }}
					hitSlop={6}
					style={({ pressed }) => [
						styles.routeToggle,
						target === "terminal" && styles.routeToggleActive,
						pressed && { opacity: 0.7 },
					]}
				>
					<Feather
						name={target === "terminal" ? "message-square" : "terminal"}
						size={15}
						color={target === "terminal" ? t.textTertiary : t.blue}
					/>
				</Pressable> : null}
				{/* Only offered while there is a keyboard to dismiss, instead of a
				    permanent toggle that claimed to hide a keyboard it did not own. */}
				{keyboardVisible ? (
					<Pressable
						accessibilityRole="button"
						accessibilityLabel="Hide keyboard"
						onPress={() => { haptics.tap(); onDismissKeyboard(); }}
						hitSlop={8}
						style={({ pressed }) => [styles.dismiss, pressed && { opacity: 0.6 }]}
					>
						<Feather name="chevron-down" size={16} color={t.textTertiary} />
					</Pressable>
				) : null}
			</View>

			<MicKey state={voice.state} mode={voice.mode} onPressIn={voice.pressIn} onPressOut={voice.pressOut} />

			<Pressable
				accessibilityRole="button"
				accessibilityLabel="Send"
				disabled={!canSend}
				onPress={() => { haptics.tap(); onSend(); }}
				style={({ pressed }) => [styles.send, !canSend && { opacity: 0.35 }, pressed && { opacity: 0.8 }]}
			>
				<Feather name="send" size={17} color={t.onAccent} />
			</Pressable>
		</View>
	);
}

const CONTROL_SIZE = 40;

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	bar: {
		flexDirection: "row",
		alignItems: "flex-end",
		gap: 7,
		paddingHorizontal: 8,
		paddingTop: 2,
		paddingBottom: 7,
	},
	field: {
		flex: 1,
		flexDirection: "row",
		alignItems: "flex-end",
		minHeight: CONTROL_SIZE,
		// Caps growth at roughly four lines so a long prompt can't swallow the
		// terminal above it.
		maxHeight: 108,
		borderRadius: 11,
		borderWidth: 1,
		borderColor: t.borderDefault,
		backgroundColor: t.bgElevated,
		paddingLeft: 11,
		paddingRight: 4,
	},
	input: {
		flex: 1,
		color: t.textPrimary,
		fontSize: 15,
		paddingTop: 10,
		paddingBottom: 10,
		maxHeight: 106,
	},
	dismiss: { width: 28, height: CONTROL_SIZE, alignItems: "center", justifyContent: "center" },
	routeToggle: {
		width: CONTROL_SIZE,
		height: CONTROL_SIZE,
		alignItems: "center",
		justifyContent: "center",
		borderRadius: 8,
	},
	routeToggleActive: { backgroundColor: t.tintBlue },
	// Rounded square at the same radius as the mic, so the two read as a pair and
	// match the field beside them rather than being the only circles in the dock.
	send: {
		width: CONTROL_SIZE,
		height: CONTROL_SIZE,
		borderRadius: 12,
		backgroundColor: t.blue,
		alignItems: "center",
		justifyContent: "center",
	},
});
