import { Feather, FontAwesome } from "@expo/vector-icons";
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
	AccessibilityInfo,
	BackHandler,
	FlatList,
	Image,
	PanResponder,
	Pressable,
	StyleSheet,
	Text,
	useWindowDimensions,
	View,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import MASCOT from "../assets/mascot.png";
import { AgentLogo } from "./AgentLogo";
import type { DashboardSession } from "./api";
import { haptics } from "./haptics";
import { sessionTitle } from "./sessionStatus";
import {
	activeSidebarDestination,
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
import { useApp } from "./store";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles } from "./ThemeProvider";

type ScrollRequest = { destination: SidebarDestinationId; sequence: number };
type SidebarNavigationContextValue = {
	openSidebar: () => void;
	scrollRequest: ScrollRequest | null;
};

const SidebarNavigationContext = createContext<SidebarNavigationContextValue | null>(null);
let retainedDrawerOpen = false;

export function useSidebarNavigation() {
	const context = useContext(SidebarNavigationContext);
	if (!context) throw new Error("useSidebarNavigation must be used within <SidebarNavigationShell>");
	return context;
}

export function useOptionalSidebarNavigation() {
	return useContext(SidebarNavigationContext);
}

export function SidebarNavigationShell({ children }: { children: ReactNode }) {
	const styles = useThemedStyles(makeStyles);
	const { sessions, projects } = useApp();
	const router = useRouter();
	const pathname = usePathname();
	const insets = useSafeAreaInsets();
	const { width } = useWindowDimensions();
	const [open, setOpen] = useState(retainedDrawerOpen);
	const [reduceMotion, setReduceMotion] = useState(false);
	const progress = useRef(new Animated.Value(retainedDrawerOpen ? 1 : 0)).current;
	const gestureStartedOpen = useRef(false);
	const pendingClosePath = useRef<string | null>(null);
	const [scrollRequest, setScrollRequest] = useState<ScrollRequest | null>(null);
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

	useEffect(() => {
		let mounted = true;
		void AccessibilityInfo.isReduceMotionEnabled()
			.then((enabled) => {
				if (mounted) setReduceMotion(enabled);
			})
			.catch(() => {});
		const subscription = AccessibilityInfo.addEventListener("reduceMotionChanged", setReduceMotion);
		return () => {
			mounted = false;
			subscription.remove();
		};
	}, []);

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
					<Image source={MASCOT} resizeMode="contain" style={styles.brandMascot} accessibilityLabel="AO mascot" />
				</View>
				<View style={styles.destinations}>
					{sidebarDestinations.slice(0, -1).map((destination) => (
						<DestinationRow
							key={destination.id}
							destination={destination}
							active={destination.id === selectedPrimaryDestination}
							onPress={() => selectDestination(destination)}
						/>
					))}
				</View>
			</View>

			<Text style={styles.sectionLabel}>{RECENT_WORKERS_LABEL.toUpperCase()}</Text>
			<FlatList
				data={liveSessions}
				keyExtractor={(session) => `${session.projectId}:${session.id}`}
				style={styles.sessionList}
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

function DestinationRow({ destination, active, onPress }: {
	destination: SidebarDestination;
	active: boolean;
	onPress: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	return (
		<Pressable
			testID={`sidebar-${destination.id}`}
			accessibilityRole="button"
			accessibilityState={{ selected: active }}
			android_ripple={{ color: t.tintBlue }}
			onPress={onPress}
			style={({ pressed }) => [
				styles.destination,
				(active || pressed) && { backgroundColor: t.tintBlue },
			]}
		>
			<Feather name={destination.icon} size={21} color={active ? t.blue : t.textSecondary} />
			<Text numberOfLines={1} style={[styles.destinationLabel, active && { color: t.blue, fontWeight: "700" }]}>
				{destination.label}
			</Text>
			{active ? <Feather name="check" size={21} color={t.blue} /> : null}
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
			{session.isPinned ? <FontAwesome name="thumb-tack" size={14} color={t.textTertiary} style={{ transform: [{ rotate: "28deg" }] }} /> : null}
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
	sidebar: { flex: 1, paddingHorizontal: 16, backgroundColor: t.bgSide },
	sidebarTop: { height: 232 },
	brandMascotSlot: { width: 72, height: 62, paddingLeft: 14, justifyContent: "center" },
	brandMascot: { width: 58, height: 48 },
	destinations: { gap: 7, paddingTop: 8 },
	destination: {
		height: 52,
		paddingHorizontal: 14,
		borderRadius: 13,
		borderCurve: "continuous",
		flexDirection: "row",
		alignItems: "center",
		gap: 13,
		overflow: "hidden",
	},
	destinationLabel: { flex: 1, color: t.textPrimary, fontSize: 17, lineHeight: 22, fontWeight: "600" },
	sectionLabel: {
		paddingTop: 8,
		paddingBottom: 8,
		paddingHorizontal: 12,
		color: t.textTertiary,
		fontSize: 12,
		fontWeight: "700",
		letterSpacing: 0.7,
	},
	sessionList: { flex: 1 },
	sessionListContent: { paddingBottom: 8 },
	emptySessionList: { flexGrow: 1 },
	emptySessions: { paddingHorizontal: 12, paddingTop: 8, color: t.textTertiary, fontSize: 14 },
	sessionRow: {
		minHeight: 58,
		paddingHorizontal: 12,
		paddingVertical: 9,
		borderRadius: 12,
		borderCurve: "continuous",
		flexDirection: "row",
		alignItems: "center",
		gap: 11,
		overflow: "hidden",
	},
	sessionRowPressed: { backgroundColor: t.bgSubtle },
	sessionText: { flex: 1, minWidth: 0 },
	sessionTitle: { color: t.textPrimary, fontSize: 15, fontWeight: "600" },
	sessionMetaRow: { marginTop: 4, flexDirection: "row", alignItems: "center", gap: 6 },
	statusDot: { width: 6, height: 6, borderRadius: 3 },
	sessionMeta: { flex: 1, color: t.textTertiary, fontSize: 12 },
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
