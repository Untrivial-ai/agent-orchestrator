import { useEffect, useRef, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, TextInput, View } from "react-native";
import { Feather } from "../icons";
import type { Theme } from "../theme";
import { useThemedStyles } from "../ThemeProvider";
import { iconSize, space, type } from "../tokens";
import { BrowserGlassSurface, browserGlassSupported } from "./BrowserGlassSurface";
import { displayBrowserUrl } from "./browserUrl";

export type BrowserToolbarProps = {
	url: string;
	title?: string;
	contentColorScheme?: "light" | "dark";
	loading: boolean;
	canGoBack: boolean;
	canGoForward: boolean;
	canOpenExternal: boolean;
	onBack: () => void;
	onForward: () => void;
	onReload: () => void;
	onStop: () => void;
	onSubmitUrl: (value: string) => void;
	onOpenExternal: () => void;
	onShare: () => void;
};

export function BrowserToolbar({
	url,
	title,
	loading,
	canGoBack,
	canGoForward,
	canOpenExternal,
	onBack,
	onForward,
	onReload,
	onStop,
	onSubmitUrl,
	onOpenExternal,
	onShare,
}: BrowserToolbarProps) {
	const styles = useThemedStyles(makeStyles);
	const inputRef = useRef<TextInput>(null);
	const [editing, setEditing] = useState(false);
	const [draft, setDraft] = useState(url);
	useEffect(() => {
		if (!editing) setDraft(url);
	}, [editing, url]);
	useEffect(() => {
		if (!editing) return;
		const timer = setTimeout(() => inputRef.current?.focus(), 50);
		return () => clearTimeout(timer);
	}, [editing]);

	const submitDraft = () => {
		const next = draft.trim();
		setEditing(false);
		if (next) onSubmitUrl(next);
	};

	return (
		<View style={styles.toolbar}>
			<View style={styles.addressRow}>
				{editing ? (
					<View style={styles.inputWrap}>
						<BrowserGlassSurface shape="field" />
						<TextInput
							autoCapitalize="none"
							autoCorrect={false}
							clearButtonMode="while-editing"
							keyboardType="url"
							onBlur={() => setEditing(false)}
							onChangeText={setDraft}
							onSubmitEditing={submitDraft}
							placeholder="Enter URL"
							placeholderTextColor={styles.colors.textFaint}
							ref={inputRef}
							returnKeyType="go"
							selectTextOnFocus
							selectionColor={styles.colors.accent}
							style={styles.input}
							value={draft}
						/>
					</View>
				) : (
					<View style={styles.location}>
						<BrowserGlassSurface shape="field" />
						<View style={styles.locationIcon}>
							<Feather name={url.startsWith("https://") ? "lock" : "globe"} size={iconSize.xs} color={styles.colors.textTertiary} />
						</View>
						<Pressable accessibilityRole="button" accessibilityLabel={url ? "Edit browser URL" : "Enter browser URL"} onPress={() => setEditing(true)} style={({ pressed }) => [styles.locationMain, pressed && styles.locationPressed]}>
							<View style={styles.locationText}>
								<Text numberOfLines={1} style={styles.title}>{title || displayBrowserUrl(url) || "Enter a URL"}</Text>
								{url ? <Text numberOfLines={1} style={styles.url}>{displayBrowserUrl(url)}</Text> : null}
							</View>
						</Pressable>
						<Pressable
							accessibilityRole="button"
							accessibilityLabel={loading ? "Stop loading" : "Reload"}
							accessibilityState={{ disabled: !url && !loading }}
							disabled={!url && !loading}
							hitSlop={6}
							onPress={loading ? onStop : onReload}
							style={({ pressed }) => [styles.locationAction, pressed && styles.locationPressed, !url && !loading && styles.disabled]}
						>
							{loading ? <ActivityIndicator size="small" color={styles.colors.accent} /> : <Feather name="refresh-cw" size={iconSize.sm} color={styles.colors.textSecondary} />}
						</Pressable>
					</View>
				)}
			</View>
			<View style={styles.controlsRow}>
				<ToolbarButton label="Back" icon="chevron-left" disabled={!canGoBack} onPress={onBack} />
				<ToolbarButton label="Forward" icon="chevron-right" disabled={!canGoForward} onPress={onForward} />
				<ToolbarButton label="Open externally" icon="external-link" disabled={!canOpenExternal} onPress={onOpenExternal} />
				<ToolbarButton label="Share URL" icon="share" disabled={!url} onPress={onShare} />
			</View>
		</View>
	);
}

function ToolbarButton({ disabled, icon, label, onPress }: { disabled?: boolean; icon: string; label: string; onPress: () => void }) {
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable accessibilityRole="button" accessibilityLabel={label} accessibilityState={{ disabled }} disabled={disabled} hitSlop={6} onPress={onPress} style={({ pressed }) => [styles.toolbarButton, pressed && !disabled && styles.pressed, disabled && styles.disabled]}>
			<BrowserGlassSurface shape="circle" />
			<Feather name={icon} size={iconSize.md} color={disabled ? styles.colors.textFaint : styles.colors.textPrimary} />
		</Pressable>
	);
}

const makeStyles = (t: Theme) => Object.assign(StyleSheet.create({
	toolbar: { backgroundColor: browserGlassSupported ? "transparent" : t.bgSurface, paddingHorizontal: space.md, paddingTop: space.sm, paddingBottom: space.sm, gap: space.sm },
	addressRow: { minHeight: 44, flexDirection: "row", alignItems: "center" },
	controlsRow: { minHeight: 44, flexDirection: "row", alignItems: "center", justifyContent: "space-around", paddingHorizontal: space.sm },
	toolbarButton: { width: 44, height: 44, alignItems: "center", justifyContent: "center", borderRadius: 22, borderCurve: "continuous", overflow: "hidden", borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderStrong, backgroundColor: browserGlassSupported ? "transparent" : t.bgElevated },
	pressed: { backgroundColor: t.bgElevated },
	disabled: { opacity: 0.45 },
	location: { flex: 1, minHeight: 44, flexDirection: "row", alignItems: "stretch", borderRadius: 22, borderCurve: "continuous", borderWidth: StyleSheet.hairlineWidth, borderColor: t.borderStrong, backgroundColor: browserGlassSupported ? "transparent" : t.bgElevated, overflow: "hidden" },
	locationIcon: { width: 48, alignItems: "center", justifyContent: "center" },
	locationMain: { flex: 1, minWidth: 0, alignItems: "center", justifyContent: "center", paddingHorizontal: space.xs },
	locationAction: { width: 48, alignItems: "center", justifyContent: "center" },
	locationPressed: { backgroundColor: t.bgElevatedHover },
	locationText: { width: "100%", minWidth: 0, alignItems: "center" },
	title: { width: "100%", textAlign: "center", fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.caption1.fontSize, lineHeight: type.caption1.lineHeight, fontWeight: "600" },
	url: { width: "100%", textAlign: "center", fontFamily: t.fontMono, color: t.textTertiary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	inputWrap: { flex: 1, minHeight: 44, justifyContent: "center", borderRadius: 22, borderCurve: "continuous", borderWidth: 1, borderColor: t.accentBorder, backgroundColor: browserGlassSupported ? "transparent" : t.bgElevated, paddingHorizontal: space.md, overflow: "hidden" },
	input: { minHeight: 38, padding: 0, textAlign: "center", fontFamily: t.fontMono, color: t.textPrimary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight },
}), { colors: t });
