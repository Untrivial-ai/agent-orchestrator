import { Host } from "@expo/ui";
import { Group, Spacer } from "@expo/ui/swift-ui";
import { frame } from "@expo/ui/swift-ui/modifiers";
import { Platform, StyleSheet, View } from "react-native";
import { glassCircle, glassPanel } from "../glass";
import { useTheme, useThemeState } from "../ThemeProvider";

export const browserGlassSupported = parseInt(String(Platform.Version), 10) >= 26;

/** Native Liquid Glass rendered behind React Native browser controls. */
export function BrowserGlassSurface({ shape }: { shape: "field" | "circle" }) {
	const t = useTheme();
	const { scheme } = useThemeState();
	if (!browserGlassSupported) return null;
	return (
		<View pointerEvents="none" style={StyleSheet.absoluteFill}>
			<Host style={StyleSheet.absoluteFill} colorScheme={scheme} seedColor={t.accent}>
				<Group modifiers={[
					frame({ maxWidth: 2000, maxHeight: 2000 }),
					shape === "circle" ? glassCircle(undefined, false) : glassPanel(22, undefined, false),
				]}>
					<Spacer />
				</Group>
			</Host>
		</View>
	);
}
