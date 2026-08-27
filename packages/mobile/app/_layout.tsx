import { Stack } from "expo-router";
import { StatusBar } from "expo-status-bar";
import { Platform } from "react-native";
import { SafeAreaProvider } from "react-native-safe-area-context";
import { OnboardingGate } from "../lib/OnboardingGate";
import { TelemetryManager } from "../lib/TelemetryManager";
import { PushManager } from "../lib/PushManager";
import { UpdatesManager } from "../lib/UpdatesManager";
import { MinimalBackButton } from "../lib/MinimalBackButton";
import { AppProvider } from "../lib/store";
import { ThemeProvider, useTheme, useThemeState } from "../lib/ThemeProvider";

// Sheet routes, and how tall each is allowed to be.
//
// The two scrolling lists need a real height to scroll within, so they take
// explicit detents — `fitToContents` cannot measure a scroll view. The theme
// sheet renders a plain view, so it wraps its content exactly.
//
// `sheets/connect` is registered separately below: it is the only one with text
// inputs, so its heights are chosen around the keyboard.
const SHEET_ROUTES = [
	{ name: "sheets/project", detents: [0.5, 0.95] },
	{ name: "sheets/agent", detents: [0.5, 0.95] },
	{ name: "sheets/model", detents: [0.5, 0.95] },
	{ name: "sheets/chat-settings", detents: [0.5, 0.95] },
	{ name: "sheets/conversation-map", detents: [0.5, 0.95] },
	{ name: "sheets/composer-picker", detents: [0.6, 0.95] },
	{ name: "sheets/theme", detents: "fitToContents" },
] as const;

// The manual-connect form — the only sheet with text inputs, and the only one
// that has to move for the keyboard.
//
// Android needs **two** detents to do that, which is not a styling preference:
// react-native-screens only expands a sheet for the IME when it has more than
// one detent. See `SheetDelegate.configureBottomSheetBehaviour`, `KeyboardVisible`
// branch — with 2 or 3 detents it calls `useTwoDetents(STATE_EXPANDED)`, but with
// a single detent (which is what `fitToContents` compiles to) it only attaches a
// callback and leaves the height alone. That is why this sheet sat behind the
// keyboard: a one-detent sheet structurally cannot get out of its way.
//
// So Android opens at the smaller detent and is expanded to the larger one by the
// OS as soon as a field is focused, then restored when the keyboard hides. iOS
// lifts a presented sheet by itself, so it keeps the exact-fit sizing.
const CONNECT_SHEET_OPTIONS = {
	presentation: "formSheet",
	sheetAllowedDetents: Platform.OS === "ios" ? "fitToContents" : [0.6, 0.95],
	sheetGrabberVisible: true,
	sheetCornerRadius: 20,
	headerShown: false,
} as const;

export default function RootLayout() {
	// ThemeProvider sits outside everything that reads a colour, including the
	// Stack's own screenOptions below — hence the inner component: a hook cannot
	// consume a provider its own component renders.
	return (
		<SafeAreaProvider>
			<ThemeProvider>
				<AppProvider>
					<Shell />
				</AppProvider>
			</ThemeProvider>
		</SafeAreaProvider>
	);
}

function Shell() {
	const t = useTheme();
	const { scheme } = useThemeState();
	return (
		<>
			{/* Light content on a dark app, dark content on a light one. */}
			<StatusBar style={scheme === "dark" ? "light" : "dark"} />
			<TelemetryManager />
			<PushManager />
			<UpdatesManager />
			<OnboardingGate />
			<Stack
				screenOptions={{
					headerStyle: { backgroundColor: t.bgSurface },
					headerTintColor: t.textPrimary,
					headerTitleStyle: { fontWeight: "700" },
					headerShadowVisible: false,
					headerBackButtonDisplayMode: "minimal",
					contentStyle: { backgroundColor: t.bgBase },
				}}
			>
				<Stack.Screen name="(tabs)" options={{ headerShown: false }} />
				<Stack.Screen name="session/[id]" options={{ title: "Session", headerBackButtonDisplayMode: "minimal", headerLeft: () => <MinimalBackButton /> }} />
				<Stack.Screen name="shell/[handleId]" options={{ title: "Worktree shell", headerBackButtonDisplayMode: "minimal", headerLeft: () => <MinimalBackButton /> }} />
				<Stack.Screen name="preview/[id]" options={{ title: "Preview", headerBackButtonDisplayMode: "minimal", headerLeft: () => <MinimalBackButton /> }} />
				<Stack.Screen name="spawn" options={{ presentation: "modal", title: "New agent" }} />
				{/* Reachable from Settings and from the board's bell, so naming either one
				    in the back label would be wrong half the time. "minimal" drops the
				    label entirely and leaves the bare chevron. */}
				<Stack.Screen
					name="notifications"
					options={{
						title: "Notifications",
						headerBackButtonDisplayMode: "minimal",
						headerLeft: () => <MinimalBackButton />,
					}}
				/>
				<Stack.Screen name="onboarding" options={{ headerShown: false, gestureEnabled: false }} />
				<Stack.Screen name="pair" options={{ presentation: "modal", headerShown: false }} />

				{/* Sheets. `formSheet` is a real UIKit sheet, so the drag-to-dismiss,
				    the grabber and the rubber-banding come from the OS rather than
				    being re-implemented on top of RN's `Modal` (which has no gesture
				    support at all — that's why the hand-rolled version never felt
				    right). Heights come from SHEET_ROUTES. */}
				{SHEET_ROUTES.map(({ name, detents }) => (
					<Stack.Screen
						key={name}
						name={name}
						options={{
							presentation: "formSheet",
							sheetAllowedDetents: detents === "fitToContents" ? "fitToContents" : [...detents],
							sheetInitialDetentIndex: 0,
							sheetGrabberVisible: true,
							sheetCornerRadius: 20,
							headerShown: false,
							contentStyle: { backgroundColor: t.bgSurface },
						}}
					/>
				))}
				<Stack.Screen
					name="sheets/connect"
					options={{ ...CONNECT_SHEET_OPTIONS, contentStyle: { backgroundColor: t.bgSurface } }}
				/>
			</Stack>
		</>
	);
}
