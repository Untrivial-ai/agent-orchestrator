import { useEffect } from "react";
import { useRouter } from "expo-router";
import { Image, Pressable, ScrollView, StyleSheet, Text, View } from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { setOnboardingSkipped } from "../lib/onboardingStore";
import { useApp } from "../lib/store";
import { Button, NumberedStep } from "../lib/ui";
import MASCOT from "../assets/mascot.png";
import { useThemedStyles } from "../lib/ThemeProvider";
import type { Theme } from "../lib/theme";
import { haptics } from "../lib/haptics";
import { MOBILE_EVENTS } from "../lib/telemetry/events";
import { mobileTelemetry } from "../lib/telemetry/runtime";
import { space, type } from "../lib/tokens";

export default function OnboardingScreen() {
	const styles = useThemedStyles(makeStyles);
	const router = useRouter();
	const insets = useSafeAreaInsets();
	const { reloadConfig } = useApp();

	useEffect(() => {
		mobileTelemetry()?.capture(MOBILE_EVENTS.onboardingStarted);
	}, []);

	async function skip() {
		mobileTelemetry()?.capture(MOBILE_EVENTS.onboardingSkipped);
		await setOnboardingSkipped();
		await reloadConfig();
		router.replace("/");
	}

	return (
		<View style={[styles.screen, { paddingTop: insets.top }]}>
			<View style={styles.topBar}>
				<View style={styles.brand}>
					<Image source={MASCOT} style={styles.mascot} resizeMode="contain" />
					<Text style={styles.brandName}>AO</Text>
				</View>
				<Pressable onPress={() => { haptics.tap(); void skip(); }} hitSlop={12} accessibilityRole="button">
					<Text style={styles.skip}>Skip</Text>
				</Pressable>
			</View>

			<ScrollView
				style={styles.scroll}
				contentContainerStyle={[styles.body, { paddingBottom: insets.bottom + 24 }]}
				showsVerticalScrollIndicator={false}
			>
				<View style={styles.hero}>
					<Text style={styles.title}>Connect your desktop</Text>
					<Text style={styles.lede}>
						Pair with AO on your computer to check on your agents, jump into any terminal, and drive work from your
						phone.
					</Text>
					<Button
						title="Pair Desktop"
						icon="maximize"
						onPress={() => router.push("/pair?from=onboarding")}
						style={styles.cta}
					/>
				</View>

				<View style={styles.how}>
					<Text style={styles.howLabel}>HOW IT WORKS</Text>
					<NumberedStep
						n={1}
						title="Open AO on your computer"
						hint="Go to Settings → Connect Mobile and turn it on."
					/>
					<View style={styles.divider} />
					<NumberedStep
						n={2}
						title="Scan the code"
						hint="Tap Pair Desktop above and point at the QR code on your screen."
					/>
					<View style={styles.divider} />
					<NumberedStep
						n={3}
						title="You're connected"
						hint="Your sessions appear here, and you can drive them from your phone."
					/>
				</View>
			</ScrollView>
		</View>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	topBar: {
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "space-between",
		paddingHorizontal: space.xl,
		paddingTop: space.xs,
		paddingBottom: space.xxs,
	},
	brand: { flexDirection: "row", alignItems: "center", gap: space.sm },
	mascot: { width: 26, height: 23 },
	brandName: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.body.fontSize, fontWeight: "600", letterSpacing: -0.2 },
	skip: { fontFamily: "Geist_600SemiBold", color: t.textTertiary, fontSize: type.subheadline.fontSize, fontWeight: "600" },

	scroll: { flex: 1 },
	body: { flexGrow: 1, paddingHorizontal: space.xxl },
	hero: { flexGrow: 1, alignItems: "center", justifyContent: "center", paddingVertical: space.xxxl },
	title: { fontFamily: "Geist_600SemiBold",
		color: t.textPrimary,
		fontSize: type.largeTitle.fontSize,
		fontWeight: "600",
		letterSpacing: -0.8,
		textAlign: "center",
	},
	lede: { fontFamily: "Geist_400Regular",
		color: t.textSecondary,
		fontSize: type.subheadline.fontSize,
		lineHeight: type.subheadline.lineHeight,
		textAlign: "center",
		marginTop: space.md,
		maxWidth: 330,
	},
	cta: { marginTop: space.xxxl, alignSelf: "center", width: "100%", maxWidth: 300 },
	how: {},
	howLabel: { fontFamily: "Geist_600SemiBold",
		color: t.textTertiary,
		fontSize: type.caption2.fontSize,
		fontWeight: "600",
		letterSpacing: 1.3,
		marginBottom: space.xxs,
	},
	divider: { height: 1, backgroundColor: t.borderSubtle, marginLeft: 43 },
});

export { RouteErrorBoundary as ErrorBoundary } from "../lib/RouteErrorBoundary";
