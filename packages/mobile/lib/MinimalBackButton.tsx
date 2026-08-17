import { Feather } from "@expo/vector-icons";
import { useRouter } from "expo-router";
import { Pressable } from "react-native";
import { haptics } from "./haptics";
import { withHaptic } from "./hapticPress";
import { minimalBackButtonStyle } from "./navigationChrome";
import { useTheme } from "./ThemeProvider";

export function MinimalBackButton({ onPress, label = "Back" }: { onPress?(): void; label?: string }) {
	const router = useRouter();
	const t = useTheme();
	return (
		<Pressable
			accessibilityRole="button"
			accessibilityLabel={label}
			onPress={withHaptic(haptics.tap, () => {
				if (onPress) onPress();
				else if (router.canGoBack()) router.back();
				else router.replace("/");
			})}
			style={minimalBackButtonStyle}
		>
			<Feather name="chevron-left" size={25} color={t.textPrimary} />
		</Pressable>
	);
}
