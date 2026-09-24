import { Feather } from "./icons";
import { usePathname, useRouter } from "expo-router";
import {
	createContext,
	useCallback,
	useContext,
	useEffect,
	useMemo,
	useRef,
	useState,
	type ReactNode,
} from "react";
import {
	Animated,
	BackHandler,
	FlatList,
	PanResponder,
	Pressable,
	StyleSheet,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { AgentLogo } from "./AgentLogo";
import { SidebarDestinationIcon } from "./sidebar-destination-icon";
import { MascotLamp } from "./ui";
import type { DashboardSession } from "./api";
import { haptics } from "./haptics";
import { sessionTitle } from "./sessionStatus";
import {
	activeSidebarDestination,
	sidebarDestinationBadge,
	RECENT_WORKERS_LABEL,
	selectedPrimarySidebarDestination,
	sidebarNavigationSettled,
	sidebarDestinations,
	sidebarSessions,
	type PrimarySidebarDestinationId,
	type SidebarDestination,
	type SidebarDestinationId,
} from "./sidebar-navigation";
import { sidebarGestureTarget, shouldCaptureSidebarGesture } from "./sidebar-gesture";
import { SidebarSettingsButton } from "./sidebar-settings-button";
import { SidebarSpawnButton } from "./sidebar-spawn-button";
import { useReducedMotion } from "./useReducedMotion";
import { useApp } from "./store";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";
import { SidebarNavigationContext, type SidebarScrollRequest } from "./sidebar-navigation-context";
import { type, space } from "./tokens";

let retainedDrawerOpen = false;

export function SidebarNavigationShell({ children }: { children: ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	const { sessions, projects, connection } = useApp();
	// See the iOS shell: cached sessions outlive a failed poll by design, so the
	// drawer has to admit when what it is showing is no longer live.
	const sessionsStale = connection !== "open";
	const router = useRouter();
	const pathname = usePathname();
	const insets = useSafeAreaInsets();
	const { width } = useWindowDimensions();
	const [open, setOpen] = useState(retainedDrawerOpen);
	const reduceMotion = useReducedMotion();
	const progress = useRef(new Animated.Value(retainedDrawerOpen ? 1 : 0)).current;
	const gestureStartedOpen = useRef(false);
	const pendingClosePath = useRef<string | null>(null);
	const [scrollRequest, setScrollRequest] = useState<SidebarScrollRequest | null>(null);
	const activeDestination = activeSidebarDestination(pathname);
	const lastPrimaryDestination = useRef<PrimarySidebarDestinationId>("agents");
	const selectedPrimaryDestination = selectedPrimarySidebarDestination(
		pathname,
		lastPrimaryDestination.current,
	);
	lastPrimaryDestination.current = selectedPrimaryDestination;
	const drawerWidth = Math.min(width * 0.76, 320);
	const liveSessions = useMemo(() => sidebarSessions(sessions), [sessions]);
	const projectNames = useMemo(
		() => new Map(projects.map((project) => [project.id, project.name])),
		[projects],
	);

	const animateSidebar = useCallback((nextOpen: boolean) => {
		retainedDrawerOpen = nextOpen;
		setOpen(nextOpen);
		Animated.spring(progress, {
			toValue: nextOpen ? 1 : 0,
			useNativeDriver: true,
			damping: 24,
			stiffness: 240,
			mass: 0.8,
		}).start();
	}, [progress]);

	const openSidebar = useCallback(() => {
		haptics.tap();
		animateSidebar(true);
	}, [animateSidebar]);
	const closeSidebar = useCallback(() => animateSidebar(false), [animateSidebar]);

	useEffect(() => {
		if (!sidebarNavigationSettled(pendingClosePath.current, pathname)) return;
		pendingClosePath.current = null;
		closeSidebar();
	}, [closeSidebar, pathname]);

	const panResponder = useMemo(
		() => PanResponder.create({
			onMoveShouldSetPanResponderCapture: (event, gesture) =>
				shouldCaptureSidebarGesture({
					open,
					startX: event.nativeEvent.pageX - gesture.dx,
					dx: gesture.dx,
					dy: gesture.dy,
					edgeWidth: 64,
				}),
			onPanResponderGrant: () => {
				gestureStartedOpen.current = open;
				progress.stopAnimation();
			},
			onPanResponderMove: (_event, gesture) => {
				const initialProgress = gestureStartedOpen.current ? 1 : 0;
				progress.setValue(Math.max(0, Math.min(1, initialProgress + gesture.dx / drawerWidth)));
			},
			onPanResponderRelease: (_event, gesture) => {
				const nextOpen = sidebarGestureTarget({
					open: gestureStartedOpen.current,
					dx: gesture.dx,
					velocityX: gesture.vx,
					drawerWidth,
				});
				if (nextOpen !== gestureStartedOpen.current) haptics.select();
				animateSidebar(nextOpen);
			},
			onPanResponderTerminate: () => animateSidebar(gestureStartedOpen.current),
		}),
		[animateSidebar, drawerWidth, open, progress],
	);

	useEffect(() => {
		if (!open || pathname === "/settings") return;
		const subscription = BackHandler.addEventListener("hardwareBackPress", () => {
			closeSidebar();
			return true;
		});
		return () => subscription.remove();
	}, [closeSidebar, open, pathname]);

	const selectDestination = useCallback((destination: SidebarDestination) => {
		haptics.select();
		if (destination.id === activeDestination) {
			setScrollRequest((current) => ({
				destination: destination.id,
				sequence: (current?.sequence ?? 0) + 1,
			}));
		} else {
			pendingClosePath.current = destination.href;
			router.replace(destination.href);
			return;
		}
		closeSidebar();
	}, [activeDestination, closeSidebar, router]);

	const selectSession = useCallback((session: DashboardSession) => {
		haptics.select();
		pendingClosePath.current = `/session/${session.id}`;
		router.push({ pathname: "/session/[id]", params: { id: session.id, projectId: session.projectId } });
	}, [router]);

	const spawnWorker = useCallback(() => {
		haptics.tap();
		closeSidebar();
		router.push("/spawn");
	}, [closeSidebar, router]);

	// Settings belongs to the root modal stack. Deliberately leave the native
	// drawer open so dismissing the sheet reveals the exact drawer state beneath.
	const openSettings = useCallback(() => {
		haptics.tap();
		router.push("/settings");
	}, [router]);

	const context = useMemo(() => ({ openSidebar, scrollRequest }), [openSidebar, scrollRequest]);
	const contentTransform = {
		transform: [
			{ translateX: progress.interpolate({ inputRange: [0, 1], outputRange: [0, drawerWidth] }) },
			{ scale: progress.interpolate({ inputRange: [0, 1], outputRange: [1, 0.97] }) },
		],
	};
	const sidebarContentTransform = {
		opacity: reduceMotion
			? 1
			: progress.interpolate({ inputRange: [0, 1], outputRange: [0.86, 1] }),
		transform: reduceMotion
			? []
			: [
				{ translateY: progress.interpolate({ inputRange: [0, 1], outputRange: [8, 0] }) },
				{ scale: progress.interpolate({ inputRange: [0, 1], outputRange: [0.96, 1] }) },
			],
	};

	const navigationView = () => (
		<Animated.View
			pointerEvents={open ? "auto" : "none"}
			importantForAccessibility={open ? "yes" : "no-hide-descendants"}
			style={[
				styles.sidebar,
				sidebarContentTransform,
				{ width: drawerWidth, paddingTop: insets.top + 10, paddingBottom: insets.bottom + 10 },
			]}
			accessibilityViewIsModal={open}
		>
			<View style={styles.sidebarTop}>
				<View style={styles.brandMascotSlot}>
					<MascotLamp status={connection} size={55} />
				</View>
				<View style={styles.destinations}>
					{sidebarDestinations.slice(0, -1).map((destination) => (
						<DestinationRow
							key={destination.id}
							destination={destination}
							active={destination.id === selectedPrimaryDestination}
							badge={sidebarDestinationBadge(destination.id, sessions)}
							onPress={() => selectDestination(destination)}
						/>
					))}
				</View>
			</View>

			<Text style={styles.sectionLabel}>
				{RECENT_WORKERS_LABEL.toUpperCase()}
				{sessionsStale ? <Text style={styles.sectionLabelStale}>{"  ·  DISCONNECTED"}</Text> : null}
			</Text>
			<FlatList
				data={liveSessions}
				keyExtractor={(session) => `${session.projectId}:${session.id}`}
				style={[styles.sessionList, sessionsStale && styles.sessionListStale]}
			contentContainerStyle={[
				liveSessions.length === 0 ? styles.emptySessionList : styles.sessionListContent,
				{ paddingBottom: insets.bottom + 76 },
			]}
				showsVerticalScrollIndicator={false}
				renderItem={({ item }) => (
					<SessionRow
						session={item}
						projectName={projectNames.get(item.projectId) ?? item.projectId}
						onPress={() => selectSession(item)}
					/>
				)}
				ListEmptyComponent={<Text style={styles.emptySessions}>No active sessions</Text>}
			/>

			<View pointerEvents="box-none" style={[styles.sidebarActions, { bottom: insets.bottom + 10 }]}>
				<SidebarSettingsButton active={activeDestination === "settings"} onPress={openSettings} />
				<SidebarSpawnButton onPress={spawnWorker} />
			</View>
		</Animated.View>
	);

	return (
		<SidebarNavigationContext.Provider value={context}>
			<View style={styles.shell} {...(open ? panResponder.panHandlers : {})}>
				{navigationView()}

				<Animated.View style={[styles.contentFrame, contentTransform]}>
					<View style={[styles.contentSurface, open && styles.contentSurfaceOpen]}>
						{children}
						{open ? (
							<Pressable
								accessibilityRole="button"
								accessibilityLabel="Close navigation"
								onPress={closeSidebar}
								style={styles.dismissLayer}
							/>
						) : null}
					</View>
				</Animated.View>

				{!open ? <View style={styles.edgeGestureTarget} {...panResponder.panHandlers} /> : null}
			</View>
		</SidebarNavigationContext.Provider>
	);
}

function DestinationRow({ destination, active, badge, onPress }: {
	destination: SidebarDestination;
	active: boolean;
	badge?: number;
	onPress: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			testID={`sidebar-${destination.id}`}
			accessibilityRole="button"
			accessibilityState={{ selected: active }}
			android_ripple={{ color: t.accentTint }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.destination,
				(active || pressed) && { backgroundColor: t.accentTint },
			]}
		>
			<SidebarDestinationIcon destination={destination} active={active} color={active ? t.accent : t.textSecondary} />
			<Text numberOfLines={1} style={[styles.destinationLabel, active && { fontFamily: "Geist_600SemiBold", color: t.accent, fontWeight: "600" }]}>
				{destination.label}
			</Text>
			{/* No check: the tinted row and the blue label already say which
			    destination you are on, and every drawer worth copying settles for
			    one or two such signals. The slot carries a count instead — the
			    workers waiting on a person, which is why you opened the app. */}
			{badge ? <Text style={styles.destinationBadge}>{badge}</Text> : null}
		</Pressable>
	);
}

function SessionRow({ session, projectName, onPress }: {
	session: DashboardSession;
	projectName: string;
	onPress: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const visual = statusVisual(t, session.status);
	const statusLabel = session.status === "idle" ? "Active" : visual.label;
	return (
		<Pressable
			onPress={onPress}
			accessibilityRole="button"
			accessibilityLabel={`${sessionTitle(session)}, ${statusLabel}, ${projectName}`}
			android_ripple={{ color: t.bgElevatedHover }}
			style={({ pressed }) => [styles.sessionRow, pressed && styles.sessionRowPressed]}
		>
			<AgentLogo harness={session.harness} size={28} />
			<View style={styles.sessionText}>
				<Text numberOfLines={1} style={styles.sessionTitle}>{sessionTitle(session)}</Text>
				<View style={styles.sessionMetaRow}>
					<View style={[styles.statusDot, { backgroundColor: visual.color }]} />
					<Text numberOfLines={1} style={styles.sessionMeta}>{statusLabel} · {projectName}</Text>
				</View>
			</View>
			{/* Upright, like the desktop's own row (`{isPinned ? <PinOff/> : <Pin/>}` with
			    no rotation anywhere). It was tilted 28° here and in the rail to match a
			    tilt the desktop does not have — and a rotated glyph does not sit in the
			    middle of its button: on Android the rail's pin measured 11px left of
			    centre in an 81px circle, where the untilted trash beside it was 0.6px. */}
			{session.isPinned ? <Feather name="pin" size={14} color={t.textTertiary} /> : null}
		</Pressable>
	);
}

const makeStyles = (t: Theme) => StyleSheet.create({
	shell: { flex: 1, overflow: "hidden", backgroundColor: t.bgSide },
	contentFrame: {
		...StyleSheet.absoluteFill,
		backgroundColor: t.bgBase,
	},
	contentSurface: {
		flex: 1,
		backgroundColor: t.bgBase,
	},
	contentSurfaceOpen: {
		borderLeftWidth: StyleSheet.hairlineWidth,
		borderLeftColor: t.borderStrong,
	},
	dismissLayer: {
		...StyleSheet.absoluteFill,
		backgroundColor: t.scrim,
	},
	edgeGestureTarget: {
		position: "absolute",
		left: 0,
		// Leave both the header control and the floating footer actions tappable;
		// edge swipes only need the page-content strip between them.
		top: 96,
		bottom: 88,
		width: 64,
	},
	sidebar: { flex: 1, paddingHorizontal: space.lg, backgroundColor: t.bgSide },
	sidebarTop: { height: 232 },
	brandMascotSlot: { width: 72, height: 62, paddingLeft: space.md, justifyContent: "center" },
	brandMascot: { width: 58, height: 48 },
	destinations: { gap: space.xs, paddingTop: space.sm },
	destination: {
		height: 52,
		paddingHorizontal: space.md,
		borderRadius: 12,
		borderCurve: "continuous",
		flexDirection: "row",
		alignItems: "center",
		gap: space.md,
		overflow: "hidden",
	},
	destinationLabel: { fontFamily: "Geist_600SemiBold", flex: 1, color: t.textPrimary, fontSize: type.body.fontSize, lineHeight: type.body.lineHeight, fontWeight: "600" },
	// Amber, not the selection blue: this is attention owed, and it must read
	// the same whether or not you are standing on that destination.
	destinationBadge: { fontFamily: "Geist_600SemiBold", minWidth: 22, textAlign: "center", color: t.amber, fontSize: type.footnote.fontSize, fontWeight: "600", fontVariant: ["tabular-nums"] },
	sectionLabel: { fontFamily: "Geist_600SemiBold",
		paddingTop: space.sm,
		paddingBottom: space.sm,
		paddingHorizontal: space.md,
		color: t.textTertiary,
		fontSize: type.caption1.fontSize,
		fontWeight: "600",
		letterSpacing: 0.7,
	},
	sectionLabelStale: { color: t.amber },
	sessionList: { flex: 1 },
	sessionListStale: { opacity: 0.55 },
	sessionListContent: { paddingBottom: space.sm },
	emptySessionList: { flexGrow: 1 },
	emptySessions: { fontFamily: "Geist_400Regular", paddingHorizontal: space.md, paddingTop: space.sm, color: t.textTertiary, fontSize: type.subheadline.fontSize },
	sessionRow: {
		minHeight: 58,
		paddingHorizontal: space.md,
		paddingVertical: space.sm,
		borderRadius: 12,
		borderCurve: "continuous",
		flexDirection: "row",
		alignItems: "center",
		gap: space.md,
		overflow: "hidden",
	},
	sessionRowPressed: { backgroundColor: t.bgSubtle },
	sessionText: { flex: 1, minWidth: 0 },
	sessionTitle: { fontFamily: "Geist_600SemiBold", color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "600", includeFontPadding: false },
	sessionMetaRow: { marginTop: 2, flexDirection: "row", alignItems: "center", gap: space.xs },
	statusDot: { width: 6, height: 6, borderRadius: 4 },
	sessionMeta: { fontFamily: "Geist_400Regular", flex: 1, color: t.textTertiary, fontSize: type.caption1.fontSize, includeFontPadding: false },
	sidebarActions: {
		position: "absolute",
		left: 28,
		right: 28,
		height: 48,
		flexDirection: "row",
		alignItems: "center",
		justifyContent: "space-between",
	},
});
