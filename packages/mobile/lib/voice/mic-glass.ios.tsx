import { Host } from "@expo/ui";
import { HStack, Spacer } from "@expo/ui/swift-ui";
import { frame, glassEffect } from "@expo/ui/swift-ui/modifiers";
import { StyleSheet, View } from "react-native";
import { useThemeState } from "../ThemeProvider";

/**
 * The glass behind the idle mic: the system Liquid Glass material, the same one
 * the Workers dock and the drawer buttons use.
 *
 * Only the material comes from SwiftUI. The touch stays on the React Native
 * Pressable above it, because a SwiftUI Button has no press-in / press-out and
 * the mic needs both for hold-to-talk.
 */
export function MicGlass({ size, radius }: { size: number; radius: number }) {
	const { scheme } = useThemeState();
	const circle = radius >= size / 2;
	return (
		<View pointerEvents="none" style={[StyleSheet.absoluteFill, { width: size, height: size }]}>
			<Host style={{ width: size, height: size }} colorScheme={scheme}>
				<HStack
					spacing={0}
					modifiers={[
						frame({ width: size, height: size }),
						glassEffect(
							circle
								? { glass: { variant: "regular" }, shape: "circle" }
								: { glass: { variant: "regular" }, shape: "roundedRectangle", cornerRadius: radius },
						),
					]}
				>
					<Spacer />
				</HStack>
			</Host>
		</View>
	);
}
