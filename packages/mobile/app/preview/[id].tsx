import { Feather } from "../../lib/icons";
import { useFocusEffect, useLocalSearchParams, useNavigation } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { ActivityIndicator, AppState, Linking, Platform, Pressable, Share, StyleSheet, Text, View } from "react-native";
import { WebView, type WebViewMessageEvent, type WebViewNavigation } from "react-native-webview";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { getPreview } from "../../lib/api";
import { authHeaders } from "../../lib/config";
import { BrowserErrorBanner } from "../../lib/browser/BrowserErrorBanner";
import { BrowserToolbar } from "../../lib/browser/BrowserToolbar";
import { browserSessionUrlKey, loadBrowserSessionUrl, saveBrowserSessionUrl } from "../../lib/browser/browserSessionStore";
import { executeMobileBrowserAct } from "../../lib/browser/mobileBrowserAct";
import { bridgeResult, browserCommandScript, MOBILE_BROWSER_APPEARANCE_SCRIPT, MOBILE_BROWSER_BOOTSTRAP, parseBrowserBridgeMessage, parseBrowserContentAppearance, type BrowserContentAppearance } from "../../lib/browser/mobileBrowserBridge";
import { clearPendingMobileBrowserNavigationTimers, failPendingMobileBrowserNavigation, schedulePendingMobileBrowserNavigationSuccess, type PendingMobileBrowserNavigation } from "../../lib/browser/mobileBrowserNavigation";
import { MobileBrowserRuntimeClient, type MobileBrowserCommand, type MobileBrowserCommandResult } from "../../lib/browser/mobileBrowserRuntime";
import { MobileBrowserUrlWaits } from "../../lib/browser/mobileBrowserUrlWait";
import { inAppWebNavigation, isHttpUrl, normalizeBrowserInput, shouldAttachPreviewAuth } from "../../lib/browser/browserUrl";
import { browserLoadEnd, browserLoadError, browserLoadStart, browserNavigationChanged, initialBrowserState, type MobileBrowserState } from "../../lib/browser/browserState";
import { haptics } from "../../lib/haptics";
import { getInstallId } from "../../lib/installId";
import { useApp } from "../../lib/store";
import type { Theme } from "../../lib/theme";
import { useTheme, useThemeState, useThemedStyles } from "../../lib/ThemeProvider";
import { iconSize, space, type } from "../../lib/tokens";

type BrowserSource = { url: string; entry: string };

/** Session-scoped counterpart of the desktop Browser inspector. */
export default function SessionPreviewScreen() {
	const { id, title, previewUrl } = useLocalSearchParams<{ id: string; title?: string; previewUrl?: string }>();
	const navigation = useNavigation();
	const { config } = useApp();
	const t = useTheme();
	const { scheme } = useThemeState();
	const styles = useThemedStyles(makeStyles);
	const insets = useSafeAreaInsets();
	const web = useRef<WebView>(null);
	const [preview, setPreview] = useState<{ entry: string; url: string; authenticated: boolean } | null>(null);
	const [browserSource, setBrowserSource] = useState<BrowserSource | null>(null);
	const [browserState, setBrowserState] = useState<MobileBrowserState>(initialBrowserState);
	const [contentAppearance, setContentAppearance] = useState<BrowserContentAppearance>(scheme);
	const [loading, setLoading] = useState(true);
	const [discoveryError, setDiscoveryError] = useState<string>();
	const [toast, setToast] = useState<string>();
	const toastTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const commandResults = useRef(new Map<string, {
		resolve: (result: MobileBrowserCommandResult) => void;
		timer: ReturnType<typeof setTimeout>;
	}>());
	const pendingNavigation = useRef<PendingMobileBrowserNavigation | null>(null);
	const pendingUrlWaits = useRef(new MobileBrowserUrlWaits());
	const currentBrowserUrl = useRef("");
	const browserDidNavigate = useRef(false);
	const browserStorageKey = useMemo(() => config && id ? browserSessionUrlKey(config.host, config.httpPort, id) : "", [config, id]);

	const showToast = useCallback((message: string) => {
		setToast(message);
		if (toastTimer.current) clearTimeout(toastTimer.current);
		toastTimer.current = setTimeout(() => setToast(undefined), 1_500);
	}, []);

	useEffect(() => () => { if (toastTimer.current) clearTimeout(toastTimer.current); }, []);
	useEffect(() => {
		setPreview(null);
		setBrowserSource(null);
		setBrowserState(initialBrowserState);
		setContentAppearance(scheme);
		setLoading(true);
		setDiscoveryError(undefined);
		browserDidNavigate.current = false;
	}, [id, previewUrl, scheme]);
	useEffect(() => {
		if (!browserStorageKey) return;
		let cancelled = false;
		void loadBrowserSessionUrl(browserStorageKey).then((url) => {
			if (cancelled || !url || browserDidNavigate.current) return;
			setBrowserSource({ url, entry: new URL(url).hostname });
			setBrowserState((current) => ({ ...current, url }));
		});
		return () => { cancelled = true; };
	}, [browserStorageKey]);

	const refresh = useCallback(async () => {
		if (!config || !id) return;
		try {
			const next = await getPreview(config, id, previewUrl);
			setPreview(next);
			setDiscoveryError(undefined);
			if (next) {
				setBrowserSource((current) => current ?? { url: next.url, entry: next.entry });
				setBrowserState((current) => current.url ? current : { ...current, url: next.url });
			}
		}
		catch (cause) { setDiscoveryError(cause instanceof Error ? cause.message : String(cause)); }
		finally { setLoading(false); }
	}, [config, id, previewUrl]);

	useEffect(() => { void refresh(); const poll = setInterval(() => void refresh(), 5_000); return () => clearInterval(poll); }, [refresh]);

	const source = useMemo(() => {
		if (!config || !browserSource) return undefined;
		return {
			uri: browserSource.url,
			...(shouldAttachPreviewAuth(browserSource.url, config) ? { headers: authHeaders(config) } : {}),
		};
	}, [browserSource, config]);

	const pageTitle = browserState.title || title || preview?.entry || "Preview";
	useLayoutEffect(() => {
		navigation.setOptions({
			title: pageTitle,
			headerRight: undefined,
		});
	}, [navigation, pageTitle]);

	const navigateTo = useCallback((value: string) => {
		if (!config) return;
		const result = normalizeBrowserInput(value, config.host);
		if (!result.ok) {
			haptics.error();
			setBrowserState((current) => browserLoadError(current, result.message));
			return;
		}
		haptics.tap();
		const url = result.url.href;
		browserDidNavigate.current = true;
		if (browserStorageKey) void saveBrowserSessionUrl(browserStorageKey, url);
		setBrowserSource({ url, entry: result.url.hostname });
		setBrowserState((current) => browserLoadStart({ ...current, url }, url));
	}, [browserStorageKey, config]);

	const executeWebCommand = useCallback((command: MobileBrowserCommand): Promise<MobileBrowserCommandResult> => {
		if (!web.current) return Promise.resolve({ ok: false, error: { code: "BROWSER_TARGET_UNAVAILABLE", message: "The mobile browser page is not ready." } });
		return new Promise((resolve) => {
			const timer = setTimeout(() => {
				commandResults.current.delete(command.requestId);
				resolve({ ok: false, error: { code: "BROWSER_COMMAND_TIMEOUT", message: "The mobile browser did not answer in time." } });
			}, 55_000);
			commandResults.current.set(command.requestId, { resolve, timer });
			web.current?.injectJavaScript(browserCommandScript(command));
		});
	}, []);

	const executeAgentCommand = useCallback((command: MobileBrowserCommand): Promise<MobileBrowserCommandResult> => {
		if (command.action === "open") {
			const url = typeof command.args?.url === "string" ? command.args.url : "";
			if (!url) return Promise.resolve({ ok: false, error: { code: "URL_REQUIRED", message: "A URL is required." } });
			const normalized = config ? normalizeBrowserInput(url, config.host) : undefined;
			if (!normalized?.ok) return Promise.resolve({ ok: false, error: { code: "INVALID_URL", message: normalized?.message ?? "Browser is not connected." } });
			return new Promise((resolve) => {
				const previous = pendingNavigation.current;
				if (previous) {
					clearPendingMobileBrowserNavigationTimers(previous);
					previous.resolve({ ok: false, error: { code: "BROWSER_COMMAND_CANCELLED", message: "Browser navigation was replaced." } });
				}
				const timer = setTimeout(() => {
					const navigation = pendingNavigation.current;
					if (navigation?.requestId === command.requestId) {
						pendingNavigation.current = null;
						clearPendingMobileBrowserNavigationTimers(navigation);
					}
					resolve({ ok: false, error: { code: "BROWSER_COMMAND_TIMEOUT", message: "The mobile browser did not finish navigating in time." } });
				}, 55_000);
				pendingNavigation.current = { requestId: command.requestId, started: false, resolve, timer };
				navigateTo(normalized.url.href);
			});
		}
		if (command.action === "act") return executeMobileBrowserAct(command, executeWebCommand);
		if (command.action === "wait" && typeof command.args?.url === "string") {
			return pendingUrlWaits.current.wait(command.requestId, command.args.url, currentBrowserUrl.current, command.args.timeoutMs);
		}
		return executeWebCommand(command);
	}, [config, executeWebCommand, navigateTo]);
	const cancelAgentCommand = useCallback((requestId: string) => {
		const navigation = pendingNavigation.current;
		if (navigation?.requestId === requestId) {
			pendingNavigation.current = null;
			clearPendingMobileBrowserNavigationTimers(navigation);
			navigation.resolve({ ok: false, error: { code: "BROWSER_COMMAND_CANCELLED", message: "Browser navigation was cancelled." } });
			return;
		}
		if (pendingUrlWaits.current.cancel(requestId)) return;
		const pending = commandResults.current.get(requestId);
		if (!pending) return;
		commandResults.current.delete(requestId);
		clearTimeout(pending.timer);
		pending.resolve({ ok: false, error: { code: "BROWSER_COMMAND_CANCELLED", message: "Browser command was cancelled." } });
	}, []);

	useFocusEffect(useCallback(() => {
		if (!config || !id) return;
		let disposed = false;
		let client: MobileBrowserRuntimeClient | null = null;
		const sync = () => {
			if (!client) return;
			if (AppState.currentState === "active") client.start();
			else client.stop();
		};
		void getInstallId().then((deviceId) => {
			if (disposed) return;
			client = new MobileBrowserRuntimeClient(config, id, deviceId, {
				execute: executeAgentCommand,
				onCancel: cancelAgentCommand,
			});
			sync();
		});
		const subscription = AppState.addEventListener("change", sync);
		return () => {
			disposed = true;
			subscription.remove();
			client?.stop();
			for (const pending of commandResults.current.values()) {
				clearTimeout(pending.timer);
				pending.resolve({ ok: false, error: { code: "BROWSER_TARGET_UNAVAILABLE", message: "The mobile browser closed." } });
			}
			commandResults.current.clear();
			pendingUrlWaits.current.close();
			const navigation = pendingNavigation.current;
			pendingNavigation.current = null;
			if (navigation) {
				clearPendingMobileBrowserNavigationTimers(navigation);
				navigation.resolve({ ok: false, error: { code: "BROWSER_TARGET_UNAVAILABLE", message: "The mobile browser closed." } });
			}
		};
	}, [cancelAgentCommand, config, executeAgentCommand, id]));

	const onBridgeMessage = useCallback((event: WebViewMessageEvent) => {
		const appearance = parseBrowserContentAppearance(event.nativeEvent.data);
		if (appearance) {
			setContentAppearance(appearance);
			return;
		}
		const message = parseBrowserBridgeMessage(event.nativeEvent.data);
		if (!message) return;
		const pending = commandResults.current.get(message.requestId);
		if (!pending) return;
		commandResults.current.delete(message.requestId);
		clearTimeout(pending.timer);
		pending.resolve(bridgeResult(message));
	}, []);

	const currentUrl = browserState.url || browserSource?.url || "";
	const retry = useCallback(() => {
		haptics.tap();
		setBrowserState((current) => ({ ...current, error: undefined }));
		if (browserSource) web.current?.reload();
		else void refresh();
	}, [browserSource, refresh]);
	const openCurrentUrl = useCallback(() => {
		if (!isHttpUrl(currentUrl)) return;
		void Linking.openURL(currentUrl).catch(() => { haptics.error(); showToast("Couldn't open URL"); });
	}, [currentUrl, showToast]);
	const shareCurrentUrl = useCallback(() => {
		if (!currentUrl) return;
		void Share.share(Platform.OS === "ios" ? { url: currentUrl } : { message: currentUrl }).catch(() => undefined);
	}, [currentUrl]);

	return <View style={styles.screen}>
		<View style={styles.content}>
			{!config || loading ? (
				<View style={styles.center}><ActivityIndicator color={t.accent} /><Text style={styles.copy}>Looking for a session preview…</Text></View>
			) : browserSource ? (
				<WebView
					ref={web}
					source={source}
					style={styles.web}
					// A narrow originWhitelist makes react-native-webview call
					// Linking.openURL itself for anything else, before our navigation
					// policy runs. Sites such as X probe an app/custom URL while loading,
					// which escaped to Safari. Receive every request here, then allow only
					// HTTP(S) below so no site can silently leave AO.
					originWhitelist={["*"]}
					setSupportMultipleWindows={false}
					applicationNameForUserAgent="Version/18.0 Mobile/15E148 Safari/604.1 AO/1.0"
					injectedJavaScriptBeforeContentLoaded={MOBILE_BROWSER_BOOTSTRAP}
					injectedJavaScript={MOBILE_BROWSER_BOOTSTRAP}
					onMessage={onBridgeMessage}
					startInLoadingState
					renderLoading={() => <View style={styles.webLoading}><ActivityIndicator color={t.accent} /></View>}
					onLoadStart={(event) => {
						// React Native synthetic events are pooled after this callback. Copy
						// values now rather than dereferencing a released event inside the
						// asynchronous state updater.
						const nextUrl = event?.nativeEvent?.url;
						if (nextUrl) {
							currentBrowserUrl.current = nextUrl;
							pendingUrlWaits.current.update(nextUrl);
						}
						if (nextUrl && pendingNavigation.current) pendingNavigation.current.started = true;
						setBrowserState((current) => browserLoadStart(current, nextUrl));
					}}
					onLoadEnd={(event) => {
						setBrowserState(browserLoadEnd);
						web.current?.injectJavaScript(`${MOBILE_BROWSER_BOOTSTRAP}\n${MOBILE_BROWSER_APPEARANCE_SCRIPT}`);
						const navigation = pendingNavigation.current;
						if (navigation?.started) {
							// Android WebView emits finish before its matching error event. Defer
							// success one JS turn so onError can cancel it first.
							const result = { ok: true, result: { url: event.nativeEvent.url, title: event.nativeEvent.title } } as const;
							schedulePendingMobileBrowserNavigationSuccess(navigation, () => {
								if (pendingNavigation.current !== navigation) return;
								pendingNavigation.current = null;
								clearPendingMobileBrowserNavigationTimers(navigation);
								navigation.resolve(result);
							}, Platform.OS === "android");
						}
					}}
					onNavigationStateChange={(event: WebViewNavigation | null) => {
						if (!event) return;
						currentBrowserUrl.current = event.url;
						pendingUrlWaits.current.update(event.url);
						const update = {
							url: event.url,
							title: event.title,
							canGoBack: event.canGoBack,
							canGoForward: event.canGoForward,
							loading: event.loading,
						};
						setBrowserState((current) => browserNavigationChanged(current, update));
						if (event.url && isHttpUrl(event.url) && browserStorageKey) {
							browserDidNavigate.current = true;
							void saveBrowserSessionUrl(browserStorageKey, event.url);
						}
					}}
					onShouldStartLoadWithRequest={(request) => {
						if (isHttpUrl(request.url)) return true;
						const webFallback = inAppWebNavigation(request.url);
						if (webFallback) {
							navigateTo(webFallback);
							return false;
						}
						// Sites probe installed apps with custom schemes during page load.
						// Block those quietly; only a link the user actually tapped merits
						// feedback. Direct address-bar input is validated before it gets here.
						if (request.navigationType === "click") {
							setBrowserState((current) => browserLoadError(current, "Only HTTP and HTTPS URLs can be opened."));
						}
						return false;
					}}
					onHttpError={(event) => {
						const status = event?.nativeEvent?.statusCode;
						const message = status ? `Preview returned HTTP ${status}.` : "Preview returned an HTTP error.";
						setBrowserState((current) => browserLoadError(current, message));
						if (failPendingMobileBrowserNavigation(pendingNavigation.current, { code: "BROWSER_NAVIGATION_HTTP_ERROR", message })) {
							pendingNavigation.current = null;
						}
					}}
					onError={(event) => {
						const description = event?.nativeEvent?.description || "Couldn't load this page.";
						setBrowserState((current) => browserLoadError(current, description));
						if (failPendingMobileBrowserNavigation(pendingNavigation.current, { code: "BROWSER_NAVIGATION_FAILED", message: description })) {
							pendingNavigation.current = null;
						}
					}}
				/>
			) : (
				<View style={styles.center}><Feather name={discoveryError ? "alert-triangle" : "globe"} size={iconSize.xl} color={discoveryError ? t.red : t.textTertiary} /><Text style={styles.title}>{discoveryError ? "Couldn't load the preview" : "No preview yet"}</Text><Text style={styles.copy}>{discoveryError || "Waiting for the agent to generate a page or document. You can also enter a URL below."}</Text><Pressable onPress={() => { haptics.tap(); void refresh(); }} style={styles.retry}><Text style={styles.retryText}>Check again</Text></Pressable></View>
			)}
			{browserState.error ? <BrowserErrorBanner message={browserState.error} onDismiss={() => setBrowserState((current) => ({ ...current, error: undefined }))} onRetry={retry} /> : null}
			{toast ? <View style={styles.toast}><Text style={styles.toastText}>{toast}</Text></View> : null}
		</View>
		<View style={[styles.browserDock, { paddingBottom: Math.max(insets.bottom, space.sm) }]}>
			<BrowserToolbar
				url={currentUrl}
				title={browserState.title || preview?.entry}
				contentColorScheme={contentAppearance}
				loading={browserState.loading || loading}
				canGoBack={browserState.canGoBack}
				canGoForward={browserState.canGoForward}
				canOpenExternal={isHttpUrl(currentUrl)}
				onBack={() => { haptics.tap(); web.current?.["goBack"](); }}
				onForward={() => { haptics.tap(); web.current?.goForward(); }}
				onReload={() => { haptics.tap(); if (browserSource) web.current?.reload(); else void refresh(); }}
				onStop={() => { haptics.tap(); web.current?.stopLoading(); setBrowserState((current) => ({ ...current, loading: false })); }}
				onSubmitUrl={navigateTo}
				onOpenExternal={openCurrentUrl}
				onShare={shareCurrentUrl}
			/>
		</View>
	</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	content: { flex: 1 },
	browserDock: { position: "absolute", left: 0, right: 0, bottom: 0 },
	web: { flex: 1, backgroundColor: t.bgBase },
	webLoading: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", backgroundColor: t.bgBase },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: space.md, paddingHorizontal: space.xxxl, backgroundColor: t.bgBase },
	title: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.body.fontSize, fontWeight: "600", textAlign: "center" },
	copy: { fontFamily: "Geist_400Regular", color: t.textSecondary, fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center" },
	retry: { marginTop: space.xxs, minHeight: 40, justifyContent: "center", borderRadius: 8, borderCurve: "continuous", backgroundColor: t.accent, paddingHorizontal: space.md },
	retryText: { fontFamily: "Geist_600SemiBold", color: t.onAccent, fontSize: type.caption1.fontSize, fontWeight: "600" },
	toast: { position: "absolute", alignSelf: "center", bottom: 150, borderRadius: 999, borderCurve: "continuous", backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderDefault, paddingHorizontal: space.md, paddingVertical: space.sm },
	toastText: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.caption1.fontSize, fontWeight: "600" },
});

export { RouteErrorBoundary as ErrorBoundary } from "../../lib/RouteErrorBoundary";
