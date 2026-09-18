import { Feather } from "@expo/vector-icons";
import { useLocalSearchParams, useNavigation } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { ActivityIndicator, Pressable, StyleSheet, Text, View } from "react-native";
import { WebView } from "react-native-webview";
import { getPreview } from "../../lib/api";
import { authHeaders } from "../../lib/config";
import { headerActionStyle, headerGlyphStyle } from "../../lib/headerAction";
import { haptics } from "../../lib/haptics";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemedStyles } from "../../lib/ThemeProvider";
import { iconSize, space, type } from "../../lib/tokens";

/** Session-scoped counterpart of the desktop Browser inspector. */
export default function SessionPreviewScreen() {
	const { id, title, previewUrl } = useLocalSearchParams<{ id: string; title?: string; previewUrl?: string }>();
	const navigation = useNavigation();
	const { config } = useApp();
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const web = useRef<WebView>(null);
	const [preview, setPreview] = useState<{ entry: string; url: string; authenticated: boolean } | null>(null);
	const [loading, setLoading] = useState(true);
	const [error, setError] = useState<string>();

	const refresh = useCallback(async () => {
		if (!config || !id) return;
		setError(undefined);
		try {
			setPreview(await getPreview(config, id, previewUrl));
		}
		catch (cause) { setError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setLoading(false); }
	}, [config, id, previewUrl]);

	useEffect(() => { void refresh(); const poll = setInterval(() => void refresh(), 5_000); return () => clearInterval(poll); }, [refresh]);
	useLayoutEffect(() => { navigation.setOptions({ title: title || preview?.entry || "Preview", headerRight: () => <Pressable accessibilityRole="button" accessibilityLabel="Reload preview" hitSlop={10} onPress={() => { haptics.tap(); if (preview) web.current?.reload(); else void refresh(); }} style={headerActionStyle}><Feather name="refresh-cw" size={iconSize.md} color={t.textSecondary} style={headerGlyphStyle} /></Pressable> }); }, [navigation, preview, refresh, t.textSecondary, title]);

	if (!config || loading) return <View style={styles.center}><ActivityIndicator color={t.accent} /><Text style={styles.copy}>Looking for a session preview…</Text></View>;
	if (!preview) return <View style={styles.center}><Feather name={error ? "alert-triangle" : "globe"} size={iconSize.xl} color={error ? t.red : t.textTertiary} /><Text style={styles.title}>{error ? "Couldn't load the preview" : "No preview yet"}</Text><Text style={styles.copy}>{error || "Waiting for the agent to generate a page or document. This screen will keep checking."}</Text><Pressable onPress={() => { haptics.tap(); void refresh(); }} style={styles.retry}><Text style={styles.retryText}>Check again</Text></Pressable></View>;
	return <View style={styles.screen}><WebView ref={web} source={{ uri: preview.url, headers: preview.authenticated ? authHeaders(config) : undefined }} style={styles.web} startInLoadingState renderLoading={() => <View style={styles.webLoading}><ActivityIndicator color={t.accent} /></View>} onLoadStart={() => setError(undefined)} onHttpError={(event) => setError(`Preview returned HTTP ${event.nativeEvent.statusCode}.`)} onError={(event) => setError(event.nativeEvent.description || "Couldn't load this preview.")} />{error ? <View accessibilityRole="alert" style={styles.webError}><Feather name="alert-triangle" size={iconSize.sm} color={t.red} /><Text style={styles.webErrorText}>{error}</Text><Pressable onPress={() => { haptics.tap(); setError(undefined); web.current?.reload(); }}><Text style={styles.retryText}>Retry</Text></Pressable></View> : null}</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	web: { flex: 1, backgroundColor: t.bgBase },
	webLoading: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	webError: { position: "absolute", left: 12, right: 12, bottom: 16, minHeight: 44, flexDirection: "row", alignItems: "center", gap: space.sm, borderRadius: 12, borderCurve: "continuous", borderWidth: 1, borderColor: t.tintRed, backgroundColor: t.bgElevated, paddingHorizontal: space.md, paddingVertical: space.sm },
	webErrorText: { flex: 1, color: t.textSecondary, fontSize: type.caption2.fontSize, lineHeight: type.caption2.lineHeight },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: space.md, paddingHorizontal: space.xxxl, backgroundColor: t.bgBase },
	title: { color: t.textPrimary, fontSize: type.body.fontSize, fontWeight: "700", textAlign: "center" },
	copy: { color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center" },
	retry: { marginTop: space.xxs, minHeight: 40, justifyContent: "center", borderRadius: 8, borderCurve: "continuous", backgroundColor: t.accent, paddingHorizontal: space.md },
	retryText: { color: t.onAccent, fontSize: type.caption1.fontSize, fontWeight: "700" },
});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
