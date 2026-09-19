import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { Platform, Pressable } from "react-native";
import { haptics } from "./haptics";
import { withHaptic } from "./hapticPress";
import { minimalBackButtonStyle } from "./navigationChrome";
import { NativeHeaderButton } from "./native-header-button";
import { useTheme } from "./ThemeProvider";
import { iconSize, type } from "./tokens";

export function MinimalBackButton({ onPress, label = "Back" }: { onPress?(): void; label?: string }) {
	const router = useRouter();
	const t = useTheme();
	const goBack = () => {
		if (onPress) onPress();
		else if (router.canGoBack()) router.back();
		else router.replace("/");
	};

	// iOS: the same glass circle as every other top control, rather than the
	// navigation bar's own header-item material. That material is the system's —
	// brighter than ours and tinted its way — which is why the back button on a
	// sub-page read brighter than the identical-looking buttons on the board.
	if (Platform.OS === "ios") {
		return <NativeHeaderButton icon="back" label={label} onPress={withHaptic(haptics.tap, goBack)} />;
	}

	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={withHaptic(haptics.tap, goBack)}
			style={minimalBackButtonStyle}
		>
			<Feather name="chevron-left" size={iconSize.xl} color={t.textPrimary} />
		</Pressable>
	);
}
