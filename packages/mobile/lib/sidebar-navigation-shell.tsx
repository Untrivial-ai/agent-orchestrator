import { Button, Column, Host, Icon, RNHostView, Row, Spacer, Text } from "@expo/ui";
import { rotationEffect } from "@expo/ui/swift-ui/modifiers";
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
	FlatList,
	PanResponder,
	Pressable,
	StyleSheet,
	Text as RNText,
	useWindowDimensions,
	View,
} from "react-native";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import { AgentLogo } from "./AgentLogo";
import { MascotLamp } from "./ui";
import type { DashboardSession } from "./api";
import { haptics } from "./haptics";
import { sessionTitle } from "./sessionStatus";
import { SidebarDestinationIcon } from "./sidebar-destination-icon";
import { sidebarDestinationHitModifiers } from "./sidebar-destination-hit-modifiers";
import {
	activeSidebarDestination,
	sidebarDestinationBadge,
	RECENT_WORKERS_LABEL,
	selectedPrimarySidebarDestination,
	sidebarDestinations,
	sidebarSessions,
	type PrimarySidebarDestinationId,
	type SidebarDestination,
	type SidebarDestinationId,
} from "./sidebar-navigation";
import { sidebarGestureTarget, shouldCaptureSidebarGesture } from "./sidebar-gesture";
import { SidebarSettingsButton } from "./sidebar-settings-button";
import { useReducedMotion } from "./useReducedMotion";
import { SidebarSpawnButton } from "./sidebar-spawn-button";
import { useApp } from "./store";
import { statusVisual, type Theme } from "./theme";
import { useTheme, useThemedStyles, useThemeState } from "./ThemeProvider";
import { SidebarNavigationContext, type SidebarScrollRequest } from "./sidebar-navigation-context";
import { type, space } from "./tokens";


export function SidebarNavigationShell({ children }: { children: ReactNode }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const { scheme } = useThemeState();
	const { sessions, projects, connection } = useApp();
	// The store keeps the last good sessions when a poll fails — that is what lets
	// the board show rows with a stale banner rather than blanking. The drawer had
	// no such tell, so a disconnected phone still listed workers as if they were
	// live. Same data, so say the same thing about it.
	const sessionsStale = connection !== "open";
	const router = useRouter();
	const pathname = usePathname();
	const insets = useSafeAreaInsets();
	const { width } = useWindowDimensions();
	const [open, setOpen] = useState(false);
	const reduceMotion = useReducedMotion();
	const [scrollRequest, setScrollRequest] = useState<SidebarScrollRequest | null>(null);
	const progress = useRef(new Animated.Value(0)).current;
	const gestureStartedOpen = useRef(false);
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

	const panResponder = useMemo(
		() =>
			PanResponder.create({
				onMoveShouldSetPanResponderCapture: (event, gesture) =>
					shouldCaptureSidebarGesture({
						open,
						startX: event.nativeEvent.pageX - gesture.dx,
						dx: gesture.dx,
						dy: gesture.dy,
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

	const selectDestination = useCallback(
		(destination: SidebarDestination) => {
			haptics.select();
			if (destination.id === activeDestination) {
				setScrollRequest((current) => ({
					destination: destination.id,
					sequence: (current?.sequence ?? 0) + 1,
				}));
			} else {
				router.replace(destination.href);
			}
			closeSidebar();
		},
		[activeDestination, closeSidebar, router],
	);
	const selectSession = useCallback(
		(session: DashboardSession) => {
			haptics.select();
			closeSidebar();
			router.push({ pathname: "/session/[id]", params: { id: session.id, projectId: session.projectId } });
		},
		[closeSidebar, router],
	);
	const spawnWorker = useCallback(() => {
		haptics.tap();
		closeSidebar();
		router.push("/spawn");
	}, [closeSidebar, router]);
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

	return (
		<SidebarNavigationContext.Provider value={context}>
			<View style={styles.shell} {...panResponder.panHandlers}>
				<Animated.View
					style={[
						styles.sidebar,
						sidebarContentTransform,
						{
							width: drawerWidth,
							paddingTop: insets.top + 10,
							paddingBottom: insets.bottom + 10,
						},
					]}
					accessibilityElementsHidden={!open}
					importantForAccessibility={open ? "yes" : "no-hide-descendants"}
				>
					<View style={styles.sidebarTop}>
						<Host style={{ width: drawerWidth - 32, height: 232 }} colorScheme={scheme} seedColor={t.accent}>
							<Column
								alignment="start"
								spacing={0}
								style={{ width: drawerWidth - 32, height: 232 }}
							>
								<RNHostView matchContents>
									<View style={styles.brandMascotSlot}>
										<MascotLamp status={connection} size={55} />
									</View>
								</RNHostView>
								<Spacer size={14} />
								<Column spacing={7} style={{ width: drawerWidth - 32 }}>
									{sidebarDestinations.slice(0, -1).map((destination) => (
										<DestinationRow
											key={destination.id}
											destination={destination}
											active={destination.id === selectedPrimaryDestination}
											badge={sidebarDestinationBadge(destination.id, sessions)}
											onPress={() => selectDestination(destination)}
											drawerWidth={drawerWidth}
										/>
									))}
								</Column>
							</Column>
						</Host>
					</View>

					<RNText style={styles.sectionLabel}>
						{RECENT_WORKERS_LABEL.toUpperCase()}
						{sessionsStale ? <RNText style={styles.sectionLabelStale}>{"  ·  DISCONNECTED"}</RNText> : null}
					</RNText>
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
						ListEmptyComponent={<RNText style={styles.emptySessions}>No active sessions</RNText>}
					/>

					<View pointerEvents="box-none" style={[styles.sidebarActions, { bottom: insets.bottom + 10 }]}>
						<SidebarSettingsButton
							active={activeDestination === "settings"}
							onPress={openSettings}
						/>
						<SidebarSpawnButton onPress={spawnWorker} />
					</View>
				</Animated.View>

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
			</View>
		</SidebarNavigationContext.Provider>
	);
}

function SessionRow({
	session,
	projectName,
	onPress,
}: {
	session: DashboardSession;
	projectName: string;
	onPress: () => void;
}) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const visual = statusVisual(t, session.status);

	return (
		<Pressable
			onPress={onPress}
			accessibilityRole="button"
			accessibilityLabel={`${sessionTitle(session)}, ${visual.label}, ${projectName}`}
			style={({ pressed }) => [styles.sessionRow, pressed && styles.sessionRowPressed]}
		>
			<AgentLogo harness={session.harness} size={28} />
			<View style={styles.sessionText}>
				<RNText numberOfLines={1} style={styles.sessionTitle}>
					{sessionTitle(session)}
				</RNText>
				<View style={styles.sessionMetaRow}>
					<View style={[styles.statusDot, { backgroundColor: visual.color }]} />
					<RNText numberOfLines={1} style={styles.sessionMeta}>
						{visual.label} · {projectName}
					</RNText>
				</View>
			</View>
			{session.isPinned ? (
				<Host matchContents>
					<Icon name="pin.fill" size={13} color={t.textTertiary} modifiers={[rotationEffect(28)]} />
				</Host>
			) : null}
		</Pressable>
	);
}

function DestinationRow({
	destination,
	active,
	badge,
	onPress,
	drawerWidth,
}: {
	destination: SidebarDestination;
	active: boolean;
	badge?: number;
	onPress: () => void;
	drawerWidth: number;
}) {
	const t = useTheme();
	return (
		<Button
			onPress={onPress}
			testID={`sidebar-${destination.id}`}
			variant="text"
		>
			<Row
				alignment="center"
				spacing={13}
				modifiers={sidebarDestinationHitModifiers}
				style={{
					width: drawerWidth - 32,
					height: 52,
					paddingHorizontal: space.md,
					borderRadius: 12,
					backgroundColor: active ? t.accentTint : "transparent",
				}}
			>
				<SidebarDestinationIcon destination={destination} active={active} color={active ? t.accent : t.textSecondary} />
				<Text textStyle={{ color: active ? t.accent : t.textPrimary, fontSize: type.body.fontSize, fontWeight: active ? "700" : "600" }}>
					{destination.label}
				</Text>
				<Spacer flexible />
				{/* No check: the tinted row and the blue label already say which
				    destination you are on. The slot carries a count instead — workers
				    waiting on a person, in amber because it is attention owed and must
				    read the same on the row you are standing on. */}
				{badge ? <Text textStyle={{ color: t.amber, fontSize: type.subheadline.fontSize, fontWeight: "700" }}>{String(badge)}</Text> : null}
			</Row>
		</Button>
	);
}

const makeStyles = (t: Theme) =>
	StyleSheet.create({
		shell: { flex: 1, backgroundColor: t.bgSide },
		sidebar: {
			position: "absolute",
			left: 0,
			top: 0,
			bottom: 0,
			paddingHorizontal: space.lg,
		},
		sidebarTop: { height: 232 },
		brandMascotSlot: { width: 72, height: 48, paddingLeft: space.md },
		brandMascot: { width: 58, height: 48 },
		sectionLabel: {
			marginTop: space.sm,
			marginBottom: space.sm,
			paddingHorizontal: space.md,
			color: t.textTertiary,
			fontSize: type.caption1.fontSize,
			fontWeight: "700",
			letterSpacing: 0.7,
		},
		sectionLabelStale: { color: t.amber },
		sessionListStale: { opacity: 0.55 },
		sessionList: { flex: 1 },
		sessionListContent: { paddingBottom: space.sm },
		emptySessionList: { flexGrow: 1 },
		emptySessions: { paddingHorizontal: space.md, paddingTop: space.sm, color: t.textTertiary, fontSize: type.subheadline.fontSize },
		sessionRow: {
			minHeight: 58,
			paddingHorizontal: space.md,
			paddingVertical: space.sm,
			borderRadius: 12,
			flexDirection: "row",
			alignItems: "center",
			gap: space.md,
		},
		sessionRowPressed: { backgroundColor: t.bgSubtle },
		sessionText: { flex: 1, minWidth: 0 },
		sessionTitle: { color: t.textPrimary, fontSize: type.subheadline.fontSize, fontWeight: "600" },
		sessionMetaRow: { marginTop: space.xxs, flexDirection: "row", alignItems: "center", gap: space.xs },
		statusDot: { width: 6, height: 6, borderRadius: 4 },
		sessionMeta: { flex: 1, color: t.textTertiary, fontSize: type.caption1.fontSize },
		sidebarActions: {
			position: "absolute",
			left: 28,
			right: 28,
			height: 48,
			flexDirection: "row",
			alignItems: "center",
			justifyContent: "space-between",
		},
		contentFrame: { flex: 1 },
		contentSurface: { flex: 1, backgroundColor: t.bgBase },
		contentSurfaceOpen: {
			borderRadius: 28,
			overflow: "hidden",
			borderWidth: StyleSheet.hairlineWidth,
			borderColor: t.borderDefault,
		},
		dismissLayer: {
			position: "absolute",
			top: 0,
			right: 0,
			bottom: 0,
			left: 0,
			backgroundColor: t.scrim,
			opacity: 0.22,
		},
	});
