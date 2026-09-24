import { existsSync, readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { describe, expect, it } from "vitest";

function source(relativePath: string): string {
	return readFileSync(fileURLToPath(new URL(relativePath, import.meta.url)), "utf8");
}

describe("Android native compatibility boundaries", () => {
	it("renders the Android drawer as one React Native tree", () => {
		const path = fileURLToPath(new URL("./sidebar-navigation-shell.android.tsx", import.meta.url));
		expect(existsSync(path)).toBe(true);
		const android = existsSync(path) ? source("./sidebar-navigation-shell.android.tsx") : "";
		expect(android).toContain("PanResponder");
		expect(android).toContain("edgeGestureTarget");
		expect(android).toContain("retainedDrawerOpen");
		expect(android).not.toContain("DrawerLayoutAndroid");
		expect(android).not.toContain("@expo/ui");
		expect(android).not.toContain("RNHostView");
		expect(android).toContain('pointerEvents={open ? "auto" : "none"}');
		expect(android).toContain('importantForAccessibility={open ? "yes" : "no-hide-descendants"}');
		expect(android).toMatch(/edgeGestureTarget:[\s\S]*bottom:\s*88/);
	});

	it("uses Android-native pressable icon controls rather than Unicode Expo buttons", () => {
		for (const file of [
			"./native-header-button.android.tsx",
			"./sidebar-settings-button.android.tsx",
			"./sidebar-spawn-button.android.tsx",
		]) {
			const path = fileURLToPath(new URL(file, import.meta.url));
			expect(existsSync(path)).toBe(true);
			const android = existsSync(path) ? source(file) : "";
			expect(android).toContain("Pressable");
			expect(android).toContain("Feather");
			expect(android).not.toContain("@expo/ui");
		}
	});

	it("keeps SwiftUI and percentage native widths out of the cross-platform Spawn route", () => {
		const spawn = source("../app/spawn.tsx");
		expect(spawn).not.toContain("@expo/ui/swift-ui");
		expect(spawn).not.toContain("NativeTextInput");
		expect(spawn).toContain("SpawnPromptInput");

		const path = fileURLToPath(new URL("./spawn-prompt-input.android.tsx", import.meta.url));
		expect(existsSync(path)).toBe(true);
		const android = existsSync(path) ? source("./spawn-prompt-input.android.tsx") : "";
		expect(android).toContain("TextInput");
		expect(android).not.toContain("autoFocus");
		expect(android).not.toContain('width: "100%"');
		expect(spawn).toContain('@expo/ui/community/bottom-sheet');
		expect(spawn).toContain("BottomSheetView");
		expect(spawn).toContain("enablePanDownToClose");
		expect(spawn).not.toContain("KeyboardAvoidingView");
		// The controls stick to the keyboard instead, which is the only approach
		// that moves in step with it inside a native form sheet.
		expect(spawn).toContain("KeyboardStickyView");
		expect(spawn).not.toContain("androidGrabber");
		// Only iOS's prompt flexes to fill the sheet; Android's sheet sizes to its
		// content, so a flexing child there would have nothing to fill.
		expect(spawn).toContain('Platform.OS === "ios" && styles.promptHostFill');
		expect(spawn).toContain("const PROMPT_MIN_HEIGHT = 112;");
		expect(spawn).toContain('promptHost: { width: "100%", height: PROMPT_MIN_HEIGHT }');
		expect(source("../app/_layout.tsx")).toContain('presentation: Platform.OS === "ios" ? "formSheet" : "transparentModal"');
	});

	// Spawn is itself a bottom sheet on Android, so its choices open inside it.
	// They used to be a second sheet over the first: two grabbers, and only the
	// top one answered a swipe down.
	it("shows Spawn's choices inside Spawn's own sheet", () => {
		const android = source("./spawn-composer-controls.android.tsx");
		expect(android).toContain("OptionList");
		expect(android).not.toMatch(/\bModal\b/);
		expect(android).not.toContain("@expo/ui/community/bottom-sheet");
		expect(android).not.toContain("Picker");
		expect(android).not.toContain("@expo/ui");
		expect(android).toContain("AgentLogo");
	});

	it("uses a rounded native Android attachment chooser instead of the square popup menu", () => {
		const path = fileURLToPath(new URL("./chat/ChatAttachmentMenu.android.tsx", import.meta.url));
		expect(existsSync(path)).toBe(true);
		const android = existsSync(path) ? source("./chat/ChatAttachmentMenu.android.tsx") : "";
		expect(android).toContain('@expo/ui/community/bottom-sheet');
		expect(android).toContain("enablePanDownToClose");
		expect(android).toContain("borderRadius: radius.pill");
		expect(android).not.toContain("MenuView");
	});

	it("uses React Native Android inputs for worker search and elicitation", () => {
		for (const file of ["./worker-dock.android.tsx", "./chat/elicitation-native-controls.android.tsx"]) {
			const path = fileURLToPath(new URL(file, import.meta.url));
			expect(existsSync(path)).toBe(true);
			const android = existsSync(path) ? source(file) : "";
			expect(android).toContain("TextInput");
			expect(android).not.toContain("@expo/ui");
			expect(android).not.toContain('width: "100%"');
		}
		expect(source("./worker-dock.android.tsx")).toContain('testID="worker-search-close"');
	});

	it("keeps the Android worker actions at opposite edges and hosts their native sheet", () => {
		const dock = source("./worker-dock.android.tsx");
		expect(dock).toContain("styles.flexSpacer");
		expect(dock).toMatch(/flexSpacer:\s*\{\s*flex:\s*1\s*\}/);
		expect(dock).toContain('row: { flex: 1, height: 52, flexDirection: "row"');

		const controls = source("./worker-controls-sheet.android.tsx");
		expect(controls).toContain("BottomSheet");
		expect(controls).toContain('@expo/ui/community/bottom-sheet');
	});

	it("uses a draggable native Android worker-controls sheet", () => {
		const controls = source("./worker-controls-sheet.android.tsx");
		expect(controls).toContain('@expo/ui/community/bottom-sheet');
		expect(controls).toContain("BottomSheetView");
		expect(controls).toContain("enablePanDownToClose");
		expect(controls).not.toMatch(/\bModal\b/);
		expect(controls).not.toContain("styles.grabber");
		expect(controls).toContain('snapPoints={["55%", "85%"]}');
		expect(controls).toContain("enableDynamicSizing={false}");
		expect(controls).toMatch(/projectList:\s*\{[^}]*flex:\s*1/s);
	});

	it("keeps the iOS worker-controls host full width", () => {
		const controls = source("./worker-controls-sheet.tsx");
		expect(controls).toContain('@expo/ui/community/bottom-sheet');
		expect(controls).toContain('snapPoints={["55%", "85%"]}');
		expect(controls).not.toContain("<Host");
	});

	it("keeps the iOS Spawn prompt geometry aligned with Android", () => {
		const ios = source("./spawn-prompt-input.ios.tsx");
		// 112 is now the default for the optional `height` prop rather than a literal
		// in the style, so the field can grow into whatever room the sheet has left.
		expect(ios).toContain("height = 112");
		expect(ios).toMatch(/paddingHorizontal:\s*space\.lg/);
		expect(ios).toMatch(/paddingVertical:\s*space\.md/);
		expect(ios).not.toContain("height: 154");
	});

	it("waits for the Android destination route before closing the drawer", () => {
		const drawer = source("./sidebar-navigation-shell.android.tsx");
		expect(drawer).toContain("pendingClosePath");
		expect(drawer).toContain("sidebarNavigationSettled");
		expect(drawer).toMatch(/pendingClosePath[\s\S]*router\.replace\(destination\.href\)/);
	});

	it("uses compact app-native choice rows instead of the unstable Compose picker", () => {
		const path = fileURLToPath(new URL("./chat/ChatSettingsModal.android.tsx", import.meta.url));
		expect(existsSync(path)).toBe(true);
		const android = existsSync(path) ? source("./chat/ChatSettingsModal.android.tsx") : "";
		expect(android).toContain("ChoicePage");
		expect(android).toContain("SettingRow");
		expect(android).not.toContain("Picker");
		// The turn-settings route is already a native form sheet, so a choice list
		// opens as a page within it rather than a sheet on top.
		expect(android).not.toContain('@expo/ui/community/bottom-sheet');
		expect(android).not.toMatch(/\bModal\b/);
		expect(android).toContain('numberOfLines={1}');
		expect(android).not.toContain('description ? <Text numberOfLines={1} style={styles.rowDescription}');
		expect(android).toContain('<View style={styles.choiceCopy}>');
		expect(android).toMatch(/choiceCopy:\s*\{\s*flex:\s*1,\s*minWidth:\s*0/);
		expect(android).toMatch(/choiceRow:\s*\{[^}]*alignItems:\s*"flex-start"/s);
		expect(android).not.toContain('width: "100%"');
	});

	it("dismisses the Android keyboard before presenting chat sheets", () => {
		const screen = source("./chat/ChatSessionScreen.tsx");
		expect(screen).toContain("dismissKeyboardBeforeSheet");
		expect(screen).toMatch(/openTurnSettings[\s\S]*await dismissKeyboardBeforeSheet/);
		expect(screen).toMatch(/menuOpen[\s\S]*dismissKeyboardBeforeSheet/);
	});

	it("pins conversation actions to one Android detent on first open", () => {
		const layout = source("../app/_layout.tsx");
		expect(layout).toContain('name === "sheets/conversation-actions" && Platform.OS === "android"');
		expect(layout).toContain("sheetAllowedDetents: [0.6]");
	});

	it("does not register the built-in Android sound as a missing custom asset", () => {
		expect(source("./push.ts")).not.toContain('sound: "default"');
	});

	it("presents Settings from the root stack above the preserved drawer", () => {
		const rootSettingsPath = fileURLToPath(new URL("../app/settings.tsx", import.meta.url));
		const nestedSettingsPath = fileURLToPath(new URL("../app/(tabs)/settings.tsx", import.meta.url));
		expect(existsSync(rootSettingsPath)).toBe(true);
		expect(existsSync(nestedSettingsPath)).toBe(false);
		const rootLayout = source("../app/_layout.tsx");
		expect(rootLayout).toMatch(/name="settings"[\s\S]*presentation:\s*"formSheet"/);
	});

	it("renders conversation actions in a single native list with its conversation header", () => {
		const actions = source("./chat/ConversationActionsSheet.tsx");
		const registry = source("./chat/chatSheetRegistry.ts");
		expect(actions).toContain("<FlatList");
		expect(actions).toMatch(/ListHeaderComponent=\{<SheetHeader[\s\S]*?\/>}/);
		expect(actions).not.toContain("<ScrollView");
		expect(registry).toContain("sessionTitle: string");
		expect(actions).toContain('title={entry.snapshot.title || "Untitled conversation"}');
		expect(actions).toContain('subtitle={`Session · ${entry.sessionTitle}`}');
		expect(actions).toContain("backgroundColor: t.bgBase");
		expect(actions).toContain("backgroundColor: t.bgElevated");
		expect(actions).not.toMatch(/<ScrollView[\s\S]*<View style=\{styles\.header\}>/);
	});

	it("keeps Android conversation-menu drags with the list instead of dismissing its sheet", () => {
		const actions = source("./chat/ConversationActionsSheet.tsx");
		expect(actions).toMatch(/<FlatList[\s\S]*nestedScrollEnabled/);
	});
});
