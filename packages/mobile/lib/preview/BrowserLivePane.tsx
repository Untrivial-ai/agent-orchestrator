import { useEffect, useMemo, useRef, useState } from "react";
import {
	ActivityIndicator,
	Image,
	PanResponder,
	Pressable,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { BrowserLiveClient } from "../browserLive";
import { browserJPEGDataURI, containBrowserFrame, type BrowserFrameRect } from "../browserLiveProtocol";
import { Feather } from "../icons";
import { useApp } from "../store";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import type { Theme } from "../theme";
import { iconSize, space, type } from "../tokens";

type BrowserStatus = "connecting" | "open" | "error";

export function BrowserLivePane({ sessionID }: { sessionID: string }) {
	const { config } = useApp();
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const client = useRef<BrowserLiveClient | null>(null);
	const [attempt, setAttempt] = useState(0);
	const [status, setStatus] = useState<BrowserStatus>("connecting");
	const [error, setError] = useState<string>();
	const [frameURI, setFrameURI] = useState<string>();
	const [frameSize, setFrameSize] = useState({ width: 1, height: 1 });
	const [viewportSize, setViewportSize] = useState({ width: 1, height: 1 });
	const frameRect = useMemo(() => containBrowserFrame(viewportSize, frameSize), [frameSize, viewportSize]);
	const frameRectRef = useRef<BrowserFrameRect>(frameRect);
	frameRectRef.current = frameRect;

	useEffect(() => {
		if (!config) {
			setStatus("error");
			setError("Pair this phone with the desktop app first.");
			return;
		}
		if (!sessionID) {
			setStatus("error");
			setError("This preview doesn't identify a session.");
			return;
		}
		let disposed = false;
		setStatus("connecting");
		setError(undefined);
		const live = new BrowserLiveClient(config, sessionID, {
			onStatus: (next, message) => {
				if (disposed || next === "closed") return;
				setStatus(next === "open" ? "open" : next === "connecting" ? "connecting" : "error");
				setError(message);
			},
			onState: () => {},
			onFrame: (frame) => {
				if (disposed) return;
				try {
					setFrameSize({ width: frame.width, height: frame.height });
					setFrameURI(browserJPEGDataURI(frame.jpeg));
				} catch {
					setStatus("error");
					setError("This mobile build couldn't display the browser frame.");
				}
			},
		});
		client.current = live;
		live.connect();

		return () => {
			disposed = true;
			live?.close();
			if (client.current === live) client.current = null;
		};
	}, [attempt, config, sessionID]);

	const normalizedPoint = (x: number, y: number) => {
		const rect = frameRectRef.current;
		if (rect.width <= 0 || rect.height <= 0) return null;
		if (x < rect.left || x > rect.left + rect.width || y < rect.top || y > rect.top + rect.height) return null;
		return { x: (x - rect.left) / rect.width, y: (y - rect.top) / rect.height };
	};
	const tap = (x: number, y: number) => {
		const point = normalizedPoint(x, y);
		if (!point) return;
		client.current?.send({ type: "input", payload: { kind: "pointer", phase: "down", ...point, button: "left" } });
		client.current?.send({ type: "input", payload: { kind: "pointer", phase: "up", ...point, button: "left" } });
	};
	const drag = useRef({ x: 0, y: 0, lastDX: 0, lastDY: 0, moved: false });
	const responder = useMemo(() => PanResponder.create({
		onStartShouldSetPanResponder: () => status === "open",
		onMoveShouldSetPanResponder: () => status === "open",
		onPanResponderGrant: (event) => {
			drag.current = { x: event.nativeEvent.locationX, y: event.nativeEvent.locationY, lastDX: 0, lastDY: 0, moved: false };
		},
		onPanResponderMove: (_event, gesture) => {
			if (!drag.current.moved && Math.hypot(gesture.dx, gesture.dy) < 7) return;
			drag.current.moved = true;
			const deltaX = gesture.dx - drag.current.lastDX;
			const deltaY = gesture.dy - drag.current.lastDY;
			drag.current.lastDX = gesture.dx;
			drag.current.lastDY = gesture.dy;
			client.current?.send({ type: "input", payload: { kind: "wheel", deltaX: -deltaX, deltaY: -deltaY } });
		},
		onPanResponderRelease: () => {
			if (!drag.current.moved) tap(drag.current.x, drag.current.y);
		},
	}), [status]);

	const retry = () => setAttempt((value) => value + 1);

	return <View style={styles.root}>
		<View
			style={styles.viewport}
			onLayout={(event) => setViewportSize(event.nativeEvent.layout)}
			{...responder.panHandlers}
		>
			{frameURI ? <Image source={{ uri: frameURI }} resizeMode="contain" style={[styles.frame, frameRect]} /> : null}
			{status === "open" && !frameURI ? <View style={styles.center}><ActivityIndicator color={t.accent} /><Text style={styles.supporting}>Waiting for the desktop browser…</Text></View> : null}
			{status !== "open" ? <BrowserState status={status} error={error} retry={retry} /> : null}
		</View>
	</View>;
}

function BrowserState({ status, error, retry }: { status: BrowserStatus; error?: string; retry(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	if (status === "connecting") {
		return <View style={styles.stateOverlay}><ActivityIndicator color={t.accent} /><Text style={styles.stateTitle}>Connecting to browser…</Text></View>;
	}
	return <View accessibilityRole="alert" style={styles.stateOverlay}>
		<View style={[styles.stateIcon, styles.stateIconError]}><Feather name="alert-triangle" size={iconSize.lg} color={t.red} /></View>
		<Text style={styles.stateTitle}>Couldn't connect to the browser</Text>
		<Text style={styles.stateCopy}>{error || "On the desktop, turn on browser sharing in Connect Mobile, then try again."}</Text>
		<Pressable accessibilityRole="button" onPress={retry} style={styles.retry}><Feather name="refresh-cw" size={iconSize.sm} color={t.onAccent} /><Text style={styles.retryText}>Check again</Text></Pressable>
	</View>;
}

const makeStyles = (t: Theme) => StyleSheet.create({
	root: { flex: 1, backgroundColor: t.bgBase },
	viewport: { flex: 1, overflow: "hidden", backgroundColor: "#050505" },
	frame: { position: "absolute" },
	center: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", gap: space.sm },
	supporting: { color: t.textTertiary, fontFamily: "Geist_400Regular", fontSize: type.footnote.fontSize },
	stateOverlay: { ...StyleSheet.absoluteFill, alignItems: "center", justifyContent: "center", gap: space.md, paddingHorizontal: space.xxxl, backgroundColor: t.bgBase },
	stateIcon: { width: 52, height: 52, borderRadius: 26, alignItems: "center", justifyContent: "center", backgroundColor: t.accentTint },
	stateIconError: { backgroundColor: t.tintRed },
	stateTitle: { color: t.textPrimary, fontFamily: "Geist_600SemiBold", fontSize: type.body.fontSize, fontWeight: "600", textAlign: "center" },
	stateCopy: { maxWidth: 310, color: t.textSecondary, fontFamily: "Geist_400Regular", fontSize: type.footnote.fontSize, lineHeight: type.footnote.lineHeight, textAlign: "center" },
	retry: { minHeight: 44, flexDirection: "row", alignItems: "center", justifyContent: "center", gap: space.sm, borderRadius: 12, paddingHorizontal: space.lg, backgroundColor: t.accent },
	retryText: { color: t.onAccent, fontFamily: "Geist_600SemiBold", fontSize: type.footnote.fontSize, fontWeight: "600" },
});
