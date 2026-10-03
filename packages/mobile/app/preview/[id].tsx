import { Feather } from "../../lib/icons";
import { useLocalSearchParams, useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from "react-native";
import { WebView } from "react-native-webview";
import { getPreview } from "../../lib/api";
import { authHeaders } from "../../lib/config";
import { haptics } from "../../lib/haptics";
import { hostRouteMatches, previewForConfig } from "../../lib/hostRoute";
import { BrowserLivePane } from "../../lib/preview/BrowserLivePane";
import { HostScope, useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { iconSize, space, type } from "../../lib/tokens";
import { Button, EmptyState } from "../../lib/ui";
import { userFacingError } from "../../lib/connectionError";

/** One preview surface for the session browser and any generated app/document preview. */
export default function SessionPreviewScreen() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	return hostId ? <HostScope key={hostId} hostId={hostId}><SessionPreviewContent /></HostScope> : <SessionPreviewContent />;
}

function SessionPreviewContent() {
	const { id: rawID, title, previewUrl, hostId: routeHostId } = useLocalSearchParams<{ id: string; title?: string; previewUrl?: string; hostId?: string }>();
	const sessionID = String(rawID ?? "");
	const navigation = useNavigation();
	const router = useRouter();
	const { config, currentHostId, connection } = useApp();
	const hostMatches = hostRouteMatches(routeHostId, currentHostId);
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const web = useRef<WebView>(null);
	const [active, setActive] = useState<"browser" | "app">("browser");
	const [loaded, setLoaded] = useState<{ config: NonNullable<typeof config>; id: string; value: Awaited<ReturnType<typeof getPreview>> } | null>(null);
	const preview = loaded?.id === sessionID ? previewForConfig(loaded, config, routeHostId) : null;
	const currentConfig = useRef(config);
	currentConfig.current = config;
	const currentRoute = useRef(`${routeHostId ?? ""}|${sessionID}`);
	currentRoute.current = `${routeHostId ?? ""}|${sessionID}`;
	const [previewError, setPreviewError] = useState<string>();

	useLayoutEffect(() => {
		navigation.setOptions({ title: title || "Preview" });
	}, [navigation, title]);

	const refreshPreview = useCallback(async () => {
		if (!hostMatches || !config || !sessionID) return;
		const route = `${routeHostId ?? ""}|${sessionID}`;
		try {
			const value = await getPreview(config, sessionID, previewUrl);
			if (currentConfig.current === config && currentRoute.current === route) {
				setLoaded({ config, id: sessionID, value });
				setPreviewError(undefined);
			}
		} catch (cause) {
			if (currentConfig.current === config && currentRoute.current === route) setPreviewError(userFacingError(cause));
		}
	}, [config, hostMatches, previewUrl, routeHostId, sessionID]);

	useEffect(() => {
		setLoaded(null);
		setPreviewError(undefined);
		if (!hostMatches) return;
		void refreshPreview();
		const poll = setInterval(() => void refreshPreview(), 5_000);
		return () => clearInterval(poll);
	}, [hostMatches, refreshPreview]);

	if (!hostMatches) return <View style={styles.center}><EmptyState icon="globe" title="Preview belongs to another machine" message="Open it from that machine's session." action={<Button title="Open board" icon="activity" onPress={() => router.navigate("/")} />} /></View>;
	if (!config && connection === "closed") return <View style={styles.center}><EmptyState icon="wifi-off" title="Machine offline" message="This preview loads once the app reconnects." /></View>;
	if (!config) return <View style={styles.center}><ActivityIndicator color={t.accent} /><Text style={styles.copy}>Connecting to this machine…</Text></View>;

	return <View style={styles.screen}>
		{preview ? <View accessibilityRole="tablist" style={styles.switcher}>
			<PreviewTab label="Browser" icon="monitor" selected={active === "browser"} onPress={() => setActive("browser")} />
			<PreviewTab label="App preview" icon="globe" selected={active === "app"} onPress={() => setActive("app")} />
		</View> : null}
		{active === "browser" || !preview ? <BrowserLivePane sessionID={sessionID} /> : <View style={styles.appPane}>
			<View style={styles.appBar}>
				<Feather name="globe" size={iconSize.sm} color={t.textTertiary} />
				<Text numberOfLines={1} style={styles.appPath}>{preview.entry}</Text>
				<Pressable accessibilityRole="button" accessibilityLabel="Reload app preview" hitSlop={10} onPress={() => { haptics.tap(); web.current?.reload(); }} style={styles.appAction}>
					<Feather name="refresh-cw" size={iconSize.sm} color={t.textSecondary} />
				</Pressable>
			</View>
			<WebView
				ref={web}
				source={{ uri: preview.url, headers: preview.authenticated && config ? authHeaders(config) : undefined }}
				style={styles.web}
				startInLoadingState
				renderLoading={() => <View style={styles.webLoading}><ActivityIndicator color={t.accent} /></View>}
				onLoadStart={() => setPreviewError(undefined)}
				onHttpError={(event) => setPreviewError(previewHttpErrorCopy(event.nativeEvent.statusCode))}
				onError={(event) => setPreviewError(event.nativeEvent.description || "Couldn't load this preview.")}
			/>
			{previewError ? <View accessibilityRole="alert" style={styles.webError}>
				<Feather name="alert-triangle" size={iconSize.sm} color={t.red} />
				<Text style={styles.webErrorText}>{previewError}</Text>
				<Pressable onPress={() => { haptics.tap(); setPreviewError(undefined); web.current?.reload(); }}><Text style={styles.retryText}>Retry</Text></Pressable>
			</View> : null}
		</View>}
	</View>;
}

function PreviewTab({ label, icon, selected, onPress }: { label: string; icon: "monitor" | "globe"; selected: boolean; onPress(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return <Pressable accessibilityRole="tab" accessibilityState={{ selected }} onPress={() => { haptics.tap(); onPress(); }} style={[styles.switcherTab, selected && styles.switcherTabActive]}>
		<Feather name={icon} size={iconSize.sm} color={selected ? t.textPrimary : t.textTertiary} />
		<Text style={[styles.switcherLabel, selected && styles.switcherLabelActive]}>{label}</Text>
	</Pressable>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: space.md, paddingHorizontal: space.xxxl, backgroundColor: t.bgBase },
	copy: { color: t.textSecondary, fontFamily: "Geist_400Regular", fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center" },
	switcher: { flexDirection: "row", gap: space.xs, paddingHorizontal: space.sm, paddingVertical: space.xs, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle, backgroundColor: t.bgSurface },
	switcherTab: { flex: 1, minHeight: 40, flexDirection: "row", alignItems: "center", justifyContent: "center", gap: space.xs, borderRadius: 11 },
	switcherTabActive: { backgroundColor: t.bgElevated },
	switcherLabel: { color: t.textTertiary, fontFamily: "Geist_500Medium", fontSize: type.footnote.fontSize },
	switcherLabelActive: { color: t.textPrimary },
	appPane: { flex: 1, backgroundColor: t.bgBase },
	appBar: { minHeight: 48, flexDirection: "row", alignItems: "center", gap: space.sm, paddingLeft: space.md, paddingRight: space.xs, borderBottomWidth: StyleSheet.hairlineWidth, borderBottomColor: t.borderSubtle, backgroundColor: t.bgSurface },
	appPath: { flex: 1, color: t.textSecondary, fontFamily: "Geist_400Regular", fontSize: type.footnote.fontSize },
	appAction: { width: 42, height: 42, alignItems: "center", justifyContent: "center", borderRadius: 12 },
	web: { flex: 1, backgroundColor: t.bgBase },
	webLoading: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	webError: { position: "absolute", left: 12, right: 12, bottom: 16, minHeight: 48, flexDirection: "row", alignItems: "center", gap: space.sm, borderRadius: 12, borderCurve: "continuous", borderWidth: 1, borderColor: t.tintRed, backgroundColor: t.bgElevated, paddingHorizontal: space.md, paddingVertical: space.sm },
	webErrorText: { flex: 1, color: t.textSecondary, fontFamily: "Geist_400Regular", fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	retryText: { color: t.accent, fontFamily: "Geist_600SemiBold", fontSize: type.caption1.fontSize, fontWeight: "600" },
});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

function previewHttpErrorCopy(status: number): string {
	if (status === 404 || status === 410) return "This page wasn't found. The agent may have moved or removed it.";
	if (status === 401 || status === 403) return "This page needs access this phone doesn't have.";
	if (status >= 500) return "The page's server hit an error. Check that the agent's dev server is running, then retry.";
	return "This page didn't load. Retry, or check it on that machine.";
}
