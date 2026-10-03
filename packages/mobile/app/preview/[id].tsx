import { Feather } from "../../lib/icons";
import { useLocalSearchParams, useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from "react-native";
import { WebView } from "react-native-webview";
import { getPreview } from "../../lib/api";
import { authHeaders } from "../../lib/config";
import { headerActionStyle, headerGlyphStyle } from "../../lib/headerAction";
import { haptics } from "../../lib/haptics";
import { hostRouteMatches, previewForConfig } from "../../lib/hostRoute";
import { HostScope, useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { iconSize, space, type } from "../../lib/tokens";
import { Button, EmptyState } from "../../lib/ui";
import { userFacingError } from "../../lib/connectionError";

/** Session-scoped counterpart of the desktop Browser inspector. */
export default function SessionPreviewScreen() {
	const { hostId } = useLocalSearchParams<{ hostId?: string }>();
	return hostId ? <HostScope key={hostId} hostId={hostId}><SessionPreviewContent /></HostScope> : <SessionPreviewContent />;
}

function SessionPreviewContent() {
	const { id, title, previewUrl, hostId: routeHostId } = useLocalSearchParams<{ id: string; title?: string; previewUrl?: string; hostId?: string }>();
	const navigation = useNavigation();
	const router = useRouter();
	const { config, currentHostId, connection } = useApp();
	const hostMatches = hostRouteMatches(routeHostId, currentHostId);
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const web = useRef<WebView>(null);
	const [loaded, setLoaded] = useState<{ config: NonNullable<typeof config>; id: string; value: Awaited<ReturnType<typeof getPreview>> } | null>(null);
	const preview = loaded?.id === id ? previewForConfig(loaded, config, routeHostId) : null;
	const currentConfig = useRef(config);
	currentConfig.current = config;
	const currentRoute = useRef(`${routeHostId ?? ""}|${id}`);
	currentRoute.current = `${routeHostId ?? ""}|${id}`;
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState<string>();

	const refresh = useCallback(async () => {
		if (!hostMatches || !config || !id) return;
		const route = `${routeHostId ?? ""}|${id}`;
		setError(undefined);
		try {
			const value = await getPreview(config, id, previewUrl);
			if (currentConfig.current === config && currentRoute.current === route) setLoaded({ config, id, value });
		}
		catch (cause) {
			if (currentConfig.current === config && currentRoute.current === route) setError(userFacingError(cause));
		}
		finally {
			if (currentConfig.current === config && currentRoute.current === route) setLoading(false);
		}
	}, [config, hostMatches, id, previewUrl, routeHostId]);

	useEffect(() => {
		setLoaded(null);
		if (!hostMatches) return;
		setLoading(true);
		void refresh();
		const poll = setInterval(() => void refresh(), 5_000);
		return () => clearInterval(poll);
	}, [hostMatches, refresh]);
	useLayoutEffect(() => { navigation.setOptions({ title: title || preview?.entry || "Preview", headerRight: hostMatches ? () => <Pressable accessibilityRole="button" accessibilityLabel="Reload preview" hitSlop={10} onPress={() => { haptics.tap(); if (preview) web.current?.reload(); else void refresh(); }} style={headerActionStyle}><Feather name="refresh-cw" size={iconSize.md} color={t.textSecondary} style={headerGlyphStyle} /></Pressable> : undefined }); }, [hostMatches, navigation, preview, refresh, t.textSecondary, title]);

	if (!hostMatches) return <View style={styles.center}><EmptyState icon="globe" title="Preview belongs to another machine" message="Open it from that machine's session." action={<Button title="Open board" icon="activity" onPress={() => router.navigate("/")} />} /></View>;
	if (!config && connection === "closed") return <View style={styles.center}><EmptyState icon="wifi-off" title="Machine offline" message="This preview loads once the app reconnects." /></View>;
	if (!config || loading) return <View style={styles.center}><ActivityIndicator color={t.accent} /><Text style={styles.copy}>Looking for a session preview…</Text></View>;
	if (!preview) return <View style={styles.center}><Feather name={error ? "alert-triangle" : "globe"} size={iconSize.xl} color={error ? t.red : t.textTertiary} /><Text style={styles.title}>{error ? "Couldn't load the preview" : "No preview yet"}</Text><Text style={styles.copy}>{error || "Waiting for the agent to generate a page or document. This screen will keep checking."}</Text><Pressable onPress={() => { haptics.tap(); void refresh(); }} style={styles.retry}><Text style={styles.retryText}>Check again</Text></Pressable></View>;
	return <View style={styles.screen}><WebView ref={web} source={{ uri: preview.url, headers: preview.authenticated ? authHeaders(config) : undefined }} style={styles.web} startInLoadingState renderLoading={() => <View style={styles.webLoading}><ActivityIndicator color={t.accent} /></View>} onLoadStart={() => setError(undefined)} onHttpError={(event) => setError(previewHttpErrorCopy(event.nativeEvent.statusCode))} onError={(event) => setError(event.nativeEvent.description || "Couldn't load this preview.")} />{error ? <View accessibilityRole="alert" style={styles.webError}><Feather name="alert-triangle" size={iconSize.sm} color={t.red} /><Text style={styles.webErrorText}>{error}</Text><Pressable onPress={() => { haptics.tap(); setError(undefined); web.current?.reload(); }}><Text style={styles.retryText}>Retry</Text></Pressable></View> : null}</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	web: { flex: 1, backgroundColor: t.bgBase },
	webLoading: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	webError: { position: "absolute", left: 12, right: 12, bottom: 16, minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.sm, borderRadius: 12, borderCurve: "continuous", borderWidth: 1, borderColor: t.tintRed, backgroundColor: t.bgElevated, paddingHorizontal: space.md, paddingVertical: space.sm },
	webErrorText: { fontFamily: "Geist_400Regular", flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: space.md, paddingHorizontal: space.xxxl, backgroundColor: t.bgBase },
	title: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.body.fontSize, fontWeight: "600", textAlign: "center" },
	copy: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center" },
	retry: { marginTop: space.xxs, minHeight: 40, justifyContent: "center", borderRadius: 8, borderCurve: "continuous", backgroundColor: t.accent, paddingHorizontal: space.md },
	retryText: { fontFamily: "Geist_600SemiBold", color: t.onAccent, fontSize: type.caption1.fontSize, fontWeight: "600" },
});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";

// The preview is the agent's own page, so a failing status is about that page or
// the server behind it, not about AO. Say which, without the status code.
function previewHttpErrorCopy(status: number): string {
	if (status === 404 || status === 410) return "This page wasn't found. The agent may have moved or removed it.";
	if (status === 401 || status === 403) return "This page needs access this phone doesn't have.";
	if (status >= 500) return "The page's server hit an error. Check that the agent's dev server is running, then retry.";
	return "This page didn't load. Retry, or check it on that machine.";
}
