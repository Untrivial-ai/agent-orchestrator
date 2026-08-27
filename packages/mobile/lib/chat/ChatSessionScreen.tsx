import { Feather } from "@expo/vector-icons";
import { useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import {
	ActivityIndicator,
	Alert,
	InteractionManager,
	Keyboard,
	KeyboardAvoidingView,
	LayoutAnimation,
	Modal,
	Platform,
	Pressable,
	ScrollView,
	StyleSheet,
	Text,
	TextInput,
	View,
} from "react-native";
import { restoreSession, resumeSessionAgent, type DashboardSession, type OrchestratorLink } from "../api";
import { haptics } from "../haptics";
import { headerActionStyle } from "../headerAction";
import { deferRouteContent, resetHeaderRightForSwap } from "../headerRightSwap";
import { useApp } from "../store";
import {
	mobileInterfaceTransitionIsActive,
	mobileInterfaceTransitionIsCancellable,
	useInterfaceTransition,
} from "../session/useInterfaceTransition";
import { screenKeyboardAvoidance } from "../session/keyboardInset";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { getWorkspacePaths, openSessionShell } from "./api";
import { ChatComposer } from "./ChatComposer";
import { ChatTimeline } from "./ChatTimeline";
import { chatSheetRoute } from "./chatSheetRegistry";
import { centeredConversationMenu } from "./chatLayout";
import { elapsedLabel, mcpServerFailureLabel, quotaWarning, resetLabel } from "./conversationChrome";
import { conversationActionError, conversationActionUnsupported } from "./conversationErrors";
import { conversationMarkers } from "./timelineModel";
import { brokenMcpServers, can } from "./types";
import { useMobileConversation } from "./useConversation";

type MobileChatSession = DashboardSession | OrchestratorLink;

export function ChatSessionScreen({ session }: { session: MobileChatSession }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const router = useRouter();
	const [headerRightReady, setHeaderRightReady] = useState(false);
	const [contentReadySessionId, setContentReadySessionId] = useState<string>();
	useLayoutEffect(
		() => resetHeaderRightForSwap(
			() => navigation.setOptions({ headerRight: undefined }),
			() => setHeaderRightReady(true),
		),
		[navigation],
	);
	useEffect(() => deferRouteContent(
		() => setContentReadySessionId(session.id),
		(callback) => {
			const task = InteractionManager.runAfterInteractions(callback);
			return () => task.cancel();
		},
	), [session.id]);
	const { config, refresh: refreshBoard, setActiveProject } = useApp();
	const conversation = useMobileConversation(config, session.id);
	const interfaceSwitch = useInterfaceTransition(config, session.id, refreshBoard);
	const [menuOpen, setMenuOpen] = useState(false);
	const [jumpToSequence, setJumpToSequence] = useState<number>();
	const clearJumpToSequence = useCallback(() => setJumpToSequence(undefined), []);
	const [filePaths, setFilePaths] = useState<string[]>([]);
	const [filePathsTruncated, setFilePathsTruncated] = useState(false);
	const filePathsRequest = useRef<Promise<{ paths: string[]; truncated: boolean }> | null>(null);
	const [openingShell, setOpeningShell] = useState(false);
	const [resuming, setResuming] = useState(false);
	const [keyboardHeight, setKeyboardHeight] = useState(0);
	const terminated = "projectName" in session ? Boolean(session.isTerminal) : Boolean(session.isTerminated);
	const interfaceTransitionActive = mobileInterfaceTransitionIsActive(interfaceSwitch.transition);
	const interfaceTransitionNotice =
		!interfaceTransitionActive &&
		!interfaceSwitch.transition?.noticeAcknowledgedAt &&
		(interfaceSwitch.transition?.phase === "failed" ||
			interfaceSwitch.transition?.phase === "recovery_required")
			? interfaceSwitch.transition
			: undefined;
	const interfaceTransitionRecovered =
		interfaceTransitionNotice?.phase === "recovery_required" &&
		interfaceTransitionNotice.errorCode === "DAEMON_RESTARTED";
	const interfaceTransitionNoticeText =
		interfaceTransitionNotice?.errorDetail ||
		(interfaceTransitionRecovered
			? "AO restored the session in its last committed interface."
			: interfaceTransitionNotice?.phase === "recovery_required"
				? "The interface switch needs recovery before more work is sent."
				: "The interface switch failed; the current interface remains available.");
	const turnActive = Boolean(
		conversation.snapshot?.turns.some((turn) => turn.state === "running" || turn.state === "queued"),
	);
	const turnWaiting = Boolean(
		conversation.snapshot?.items.some(
			(item) =>
				item.kind === "activity" &&
				(item.activityKind === "approval" || item.activityKind === "user_input") &&
				item.status === "pending",
		),
	);

	useEffect(() => {
		if (Platform.OS !== "android") return;
		const avoidance = screenKeyboardAvoidance("android", 0);
		const animate = (duration?: number) => LayoutAnimation.configureNext({
			duration: duration || 250,
			update: { type: LayoutAnimation.Types.keyboard },
		});
		const show = Keyboard.addListener(avoidance.showEvent, (event) => {
			animate(event.duration);
			setKeyboardHeight(event.endCoordinates.height);
		});
		const hide = Keyboard.addListener(avoidance.hideEvent, (event) => {
			animate(event?.duration);
			setKeyboardHeight(0);
		});
		return () => {
			show.remove();
			hide.remove();
		};
	}, []);

	const title = conversation.snapshot?.title || sessionTitle(session);
	useLayoutEffect(() => {
		if (!headerRightReady) {
			navigation.setOptions({ headerRight: undefined });
			return;
		}
		navigation.setOptions({
			title: title.length > 24 ? `${title.slice(0, 22)}…` : title,
			headerRight: () => (
				<Pressable accessibilityRole="button" accessibilityLabel="Conversation actions" hitSlop={11} onPress={() => { haptics.tap(); setMenuOpen(true); }} style={headerActionStyle}>
					<Feather name="more-horizontal" size={20} color={t.textSecondary} />
				</Pressable>
			),
		});
	}, [headerRightReady, navigation, title, styles, t]);

	const loadWorkspaceFiles = useCallback(async () => {
		if (!config || !conversation.snapshot) return { paths: filePaths, truncated: filePathsTruncated };
		if (filePathsRequest.current) return filePathsRequest.current;
		const request = getWorkspacePaths(config, session.id)
			.then((result) => {
				setFilePaths(result.paths);
				setFilePathsTruncated(result.truncated);
				return result;
			})
			.catch(() => ({ paths: filePaths, truncated: filePathsTruncated }))
			.finally(() => { filePathsRequest.current = null; });
		filePathsRequest.current = request;
		return request;
	}, [config, conversation.snapshot, filePaths, filePathsTruncated, session.id]);

	const openTurnSettings = useCallback(async () => {
		const current = conversation.snapshot;
		if (!current) return;
		let catalog = { models: conversation.models, configOptions: conversation.configOptions };
		let catalogError = conversation.actionErrors.settings ?? conversation.actionErrors.config;
		try {
			catalog = await conversation.loadTurnOptions();
		} catch (cause) {
			catalogError = conversationActionError(cause);
		}
		router.push(chatSheetRoute({
			kind: "turn-settings",
			snapshot: current,
			models: catalog.models,
			options: catalog.configOptions,
			disabled: current.controller.state === "stopped" || conversation.pendingActions.includes("settings") || conversation.pendingActions.includes("config"),
			error: catalogError,
			onSettings: conversation.chooseSettings,
			onOption: conversation.setConfigOption,
			onRefresh: () => conversation.loadTurnOptions({ refresh: true }),
		}));
	}, [conversation, router]);

	const openShell = useCallback(async () => {
		if (!config || openingShell) return;
		setOpeningShell(true);
		try {
			const shell = await openSessionShell(config, session.id, session.projectId);
			setMenuOpen(false);
			router.push({ pathname: "/shell/[handleId]", params: { handleId: shell.handleId, projectId: session.projectId, sessionId: session.id, title: shell.title } });
		} catch (cause) {
			Alert.alert("Could not open shell", cause instanceof Error ? cause.message : String(cause));
		} finally { setOpeningShell(false); }
	}, [config, openingShell, router, session.id, session.projectId]);

	const resume = useCallback(async () => {
		if (resuming) return;
		setResuming(true);
		try {
			if (!config) throw new Error("No AO server configured");
			if (terminated) await restoreSession(config, session.id);
			else await resumeSessionAgent(config, session.id);
			await refreshBoard();
			await conversation.refresh();
		} catch (cause) {
			Alert.alert("Could not resume agent", cause instanceof Error ? cause.message : String(cause));
		} finally { setResuming(false); }
	}, [config, conversation.refresh, refreshBoard, resuming, session.id, terminated]);

	const startInterfaceSwitch = useCallback(
		async (policy: "drain" | "interrupt") => {
			setMenuOpen(false);
			try {
				await interfaceSwitch.start("tui", policy);
			} catch (cause) {
				Alert.alert("Could not switch interface", cause instanceof Error ? cause.message : String(cause));
			}
		},
		[interfaceSwitch],
	);

	const requestInterfaceSwitch = useCallback(() => {
		if (!interfaceSwitch.status?.supported) {
			Alert.alert("Terminal UI unavailable", interfaceSwitch.status?.reason || interfaceSwitch.error || "This agent cannot resume the same native conversation in Terminal UI.");
			return;
		}
		if (!turnActive) {
			void startInterfaceSwitch("drain");
			return;
		}
		Alert.alert(
			"Switch to Terminal UI?",
			turnWaiting
				? "This turn is waiting for your input. Finish waits for your answer; stop cancels it and switches now."
				: "Keep the same AO session, worktree, and native agent conversation.",
			[
				{ text: "Keep Chat", style: "cancel" },
				{ text: "Finish, then switch", onPress: () => void startInterfaceSwitch("drain") },
				{ text: "Stop and switch", style: "destructive", onPress: () => void startInterfaceSwitch("interrupt") },
			],
		);
	}, [interfaceSwitch, startInterfaceSwitch, turnActive, turnWaiting]);

	if (conversation.loading && !conversation.snapshot) return <Centered icon="message-square" title="Loading conversation…" spinning />;
	if (conversation.unavailable) return <Unavailable message={conversation.unavailable.message} onShell={() => void openShell()} openingShell={openingShell} />;
	if (!conversation.snapshot) return <Centered icon="alert-triangle" title="Could not load conversation" message={conversation.error || "The daemon did not return a conversation."} action="Retry" onAction={() => void conversation.refresh()} />;
	if (contentReadySessionId !== session.id) return <Centered icon="message-square" title="Preparing conversation…" spinning />;

	const snapshot = conversation.snapshot;
	const active = snapshot.turns.some((turn) => turn.state === "running" || turn.state === "queued");
	const activeTurn = snapshot.turns.find((turn) => turn.state === "running") ?? snapshot.turns.find((turn) => turn.state === "queued");
	const brokenServers = brokenMcpServers(snapshot);
	const rolledBack = snapshot.turns.filter((turn) => turn.rolledBack).length;
	const quota = quotaWarning(snapshot.rateLimits);
	const compactSupported = can(snapshot, "compaction") && !conversationActionUnsupported("compact", conversation.actionCodes.compact);
	const mcpReloadSupported = can(snapshot, "mcp_reload") && !conversationActionUnsupported("mcp", conversation.actionCodes.mcp);
	const steerUnsupported = conversationActionUnsupported("steer", conversation.actionCodes.steer);

	return (
		<KeyboardAvoidingView
			style={[
				styles.screen,
				Platform.OS === "android" && keyboardHeight > 0
					? { paddingBottom: screenKeyboardAvoidance("android", keyboardHeight).paddingBottom }
					: undefined,
			]}
			behavior={Platform.OS === "ios" ? "padding" : undefined}
			keyboardVerticalOffset={Platform.OS === "ios" ? 86 : 0}
		>
			<ChatMetaBar
				snapshot={snapshot}
				refreshing={conversation.refreshing}
				onRefresh={() => void conversation.refresh()}
			/>
			{interfaceTransitionActive ? (
				<InlineBanner
					tone="warning"
					icon="repeat"
					text={`Switching to Terminal UI · ${interfacePhaseLabel(interfaceSwitch.transition?.phase)}`}
					action={mobileInterfaceTransitionIsCancellable(interfaceSwitch.transition) ? (interfaceSwitch.cancelling ? "Cancelling…" : "Cancel") : undefined}
					onPress={interfaceSwitch.cancelling ? undefined : () => void interfaceSwitch.cancel().catch(() => {})}
				/>
			) : interfaceTransitionNotice ? (
				<InlineBanner
					tone={interfaceTransitionRecovered ? "warning" : "danger"}
					icon={interfaceTransitionRecovered ? "check-circle" : "alert-triangle"}
					text={`${interfaceTransitionNoticeText}${
						interfaceSwitch.acknowledgeNoticeError
							? ` Could not dismiss: ${interfaceSwitch.acknowledgeNoticeError}`
							: ""
					}`}
					action={interfaceSwitch.acknowledgingNotice ? "Dismissing…" : "Dismiss"}
					onPress={
						interfaceSwitch.acknowledgingNotice
							? undefined
							: () =>
									void interfaceSwitch
										.acknowledgeNotice(interfaceTransitionNotice.id)
										.catch(() => {})
					}
				/>
			) : null}
			<ConversationBanners
				snapshot={snapshot}
				brokenServers={brokenServers}
				resuming={resuming}
				terminated={terminated}
				mcpReloading={conversation.pendingActions.includes("mcp")}
				mcpError={conversation.actionErrors.mcp}
				mcpReloadSupported={mcpReloadSupported}
				turnInFlight={active}
				onResume={() => void resume()}
				onReload={() => void conversation.reloadMcp().catch(() => {})}
				onOpenShell={() => void openShell()}
			/>
			{conversation.error ? <InlineBanner tone="danger" icon="wifi-off" text={conversation.error} action="Retry" onPress={() => void conversation.refresh()} /> : null}
			{quota ? <InlineBanner tone={quota.severity === "critical" ? "danger" : "warning"} icon="alert-triangle" text={`${quota.percent}% of the${quota.planLabel ? ` ${quota.planLabel}` : ""} account quota is used${resetLabel(quota.resetsInSeconds) ? `; resets in ${resetLabel(quota.resetsInSeconds)}` : ""}. ${quota.severity === "critical" ? "Turns may start failing for reasons unrelated to your request." : "Turns will stop when the limit is reached."}`} action="Details" onPress={() => setMenuOpen(true)} /> : null}
			{conversation.actionError ? <InlineBanner tone="danger" icon="alert-circle" text={conversation.actionError} /> : null}
			{rolledBack ? <InlineBanner tone="muted" icon="rotate-ccw" text={`${rolledBack} ${rolledBack === 1 ? "turn was" : "turns were"} rolled back. The agent no longer remembers ${rolledBack === 1 ? "it" : "them"}.`} /> : null}
			{conversation.pendingSends.map((pendingSend) => pendingSend.state === "failed" ? <InlineBanner key={pendingSend.id} tone="danger" icon="send" text={`Message not sent: ${pendingSend.error || "Delivery failed"}`} action="Retry" secondary="Discard" onPress={() => void conversation.retrySend(pendingSend.id).catch(() => {})} onSecondary={() => conversation.discardSend(pendingSend.id)} /> : null)}
			<ChatTimeline
				snapshot={snapshot}
				loadingOlder={conversation.loadingOlder}
				onLoadOlder={conversation.loadOlder}
				approvalPending={conversation.pendingActions.includes("approval")}
				inputPending={conversation.pendingActions.includes("input")}
				onDecide={conversation.resolveApproval}
				onResolveInput={conversation.resolveInput}
				onRollback={conversation.rollback}
				jumpToSequence={jumpToSequence}
				onJumpHandled={clearJumpToSequence}
			/>
			{activeTurn ? <LiveTurnBar snapshot={snapshot} startedAt={activeTurn.startedAt ?? activeTurn.requestedAt} stopping={conversation.pendingActions.includes("interrupt")} onInterrupt={() => void conversation.interrupt().catch(() => {})} /> : null}
			<ChatComposer
				sessionId={session.id}
				snapshot={snapshot}
				skills={conversation.skills}
				filePaths={filePaths}
				filePathsTruncated={filePathsTruncated}
				onLoadSkills={conversation.loadSkills}
				onLoadFiles={loadWorkspaceFiles}
				configOptions={conversation.configOptions}
				steerUnavailable={steerUnsupported}
				pending={interfaceTransitionActive || conversation.pendingSends.some((item) => item.state === "sending")}
				error={conversation.actionError}
				onSend={conversation.send}
				onSteer={conversation.steer}
				onInterrupt={() => void conversation.interrupt().catch(() => {})}
				onOpenSettings={() => void openTurnSettings()}
			/>
			<ConversationMenu
				visible={menuOpen}
				onClose={() => setMenuOpen(false)}
				snapshot={snapshot}
				openingShell={openingShell}
				compacting={conversation.pendingActions.includes("compact")}
				mcpReloading={conversation.pendingActions.includes("mcp")}
				compactSupported={compactSupported}
				mcpReloadSupported={mcpReloadSupported}
				interfaceSupported={Boolean(interfaceSwitch.status?.supported)}
				interfaceReason={interfaceSwitch.status?.reason || interfaceSwitch.error}
				interfaceSwitching={interfaceTransitionActive || interfaceSwitch.starting}
				onMap={() => { setMenuOpen(false); router.push(chatSheetRoute({ kind: "conversation-map", markers: conversationMarkers(snapshot), onSelect: setJumpToSequence })); }}
				onOpenShell={() => void openShell()}
				onPreview={() => { setMenuOpen(false); router.push({ pathname: "/preview/[id]", params: { id: session.id, title, previewUrl: "previewUrl" in session ? session.previewUrl ?? undefined : undefined } }); }}
				onPullRequests={() => { setMenuOpen(false); setActiveProject(session.projectId); router.push("/(tabs)/prs"); }}
				onSettings={() => { setMenuOpen(false); void openTurnSettings(); }}
				onSwitchInterface={requestInterfaceSwitch}
				onCompact={() => { setMenuOpen(false); void conversation.compact().catch(() => {}); }}
				onReload={() => { setMenuOpen(false); void conversation.reloadMcp().catch(() => {}); }}
				onRename={(next) => { setMenuOpen(false); void conversation.rename(next).catch(() => {}); }}
			/>
		</KeyboardAvoidingView>
	);
}

function ChatMetaBar({ snapshot, refreshing, onRefresh }: { snapshot: NonNullable<ReturnType<typeof useMobileConversation>["snapshot"]>; refreshing: boolean; onRefresh(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const state = snapshot.controller.state;
	const stateColor = state === "busy" ? t.orange : state === "ready" ? t.green : state === "stopped" ? t.red : t.amber;
	return <View style={styles.meta}><View style={[styles.dot, { backgroundColor: stateColor }]} /><Text style={styles.harness}>{snapshot.harness || "agent"}</Text><Text style={styles.mode}>CHAT</Text><View style={{ flex: 1 }} />{/* Context usage and the compact shortcut are intentionally hidden from the mobile header for now. Compaction remains available in the conversation menu. */}<Pressable accessibilityRole="button" accessibilityLabel="Refresh conversation" hitSlop={9} onPress={() => { haptics.tap(); void onRefresh(); }}><Feather name="refresh-cw" size={13} color={t.textTertiary} style={refreshing ? { opacity: 0.4 } : undefined} /></Pressable></View>;
}

function ConversationBanners({ snapshot, brokenServers, resuming, terminated, mcpReloading, mcpError, mcpReloadSupported, turnInFlight, onResume, onReload, onOpenShell }: { snapshot: NonNullable<ReturnType<typeof useMobileConversation>["snapshot"]>; brokenServers: ReturnType<typeof brokenMcpServers>; resuming: boolean; terminated: boolean; mcpReloading: boolean; mcpError?: string; mcpReloadSupported: boolean; turnInFlight: boolean; onResume(): void; onReload(): void; onOpenShell(): void }) {
	const thread = snapshot.threadState;
	const signIn = signInCommand(snapshot.harness);
	return <>
		{snapshot.account?.reauthRequiredAt ? <InlineBanner tone="danger" icon="key" text={`${snapshot.account.reauthReason || "The provider rejected this session's credentials."} ${signIn ? `Run “${signIn}” on the AO host, then try again.` : "Sign in with the agent's CLI on the AO host, then try again."} AO holds no credentials of its own. The worktree is untouched.`} action="Open shell" onPress={onOpenShell} /> : null}
		{snapshot.controller.state === "stopped" ? <InlineBanner tone="danger" icon="power" text={terminated ? "This AO session is terminated. Its conversation and worktree are preserved." : snapshot.controller.error || "The agent controller is stopped."} action={terminated ? (resuming ? "Restoring…" : "Restore session") : (resuming ? "Resuming…" : "Resume agent")} secondary="Shell" onPress={resuming ? undefined : onResume} onSecondary={onOpenShell} /> : null}
		{snapshot.controller.state === "recovering" || snapshot.controller.state === "connecting" ? <InlineBanner tone="warning" icon="loader" text={snapshot.controller.state === "recovering" ? "Reconnecting to the agent…" : "Starting the agent controller…"} /> : null}
		{thread?.status === "system_error" ? <InlineBanner tone="danger" icon="alert-triangle" text={`The provider reports an internal fault in this thread; AO's connection may still be healthy. The conversation and worktree are kept.${thread.waitingOn?.length ? ` Waiting on: ${thread.waitingOn.join(", ")}.` : ""}`} /> : thread?.status === "closed" ? <InlineBanner tone="warning" icon="alert-triangle" text={`The provider closed this thread. AO kept its history, but the agent no longer holds it.${thread.waitingOn?.length ? ` Waiting on: ${thread.waitingOn.join(", ")}.` : ""}`} /> : null}
		{brokenServers.length ? <InlineBanner tone="warning" icon="tool" text={`${brokenServers.map(mcpServerFailureLabel).join(", ")} did not start. The agent has none of their tools and will not say so—it works around them silently.${mcpError ? ` Reload failed: ${mcpError}` : ""}`} action={mcpReloadSupported && !turnInFlight ? (mcpReloading ? "Reloading…" : "Reload") : undefined} onPress={mcpReloading ? undefined : onReload} /> : null}
	</>;
}

function LiveTurnBar({ snapshot, startedAt, stopping, onInterrupt }: { snapshot: NonNullable<ReturnType<typeof useMobileConversation>["snapshot"]>; startedAt?: string; stopping: boolean; onInterrupt(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [now, setNow] = useState(() => Date.now());
	useEffect(() => { const timer = setInterval(() => setNow(Date.now()), 1_000); return () => clearInterval(timer); }, []);
	const queued = snapshot.turns.filter((turn) => turn.state === "queued").length;
	const blocked = snapshot.items.some((item) => item.kind === "activity" && (item.activityKind === "approval" || item.activityKind === "user_input") && item.status === "pending");
	const elapsed = elapsedLabel(startedAt, now);
	const stopLabel = queued ? "Stop and clear queue" : "Stop turn";
	return <View style={styles.live}><ActivityIndicator size="small" color={blocked ? t.amber : t.orange} /><Text style={styles.liveText}>{blocked ? "Waiting for your input" : "Agent is working"}{elapsed ? ` · ${elapsed}` : ""}{queued ? ` · ${queued} queued` : ""}</Text><Pressable accessibilityRole="button" accessibilityLabel={stopLabel} accessibilityState={{ busy: stopping }} disabled={stopping} onPress={() => { haptics.tap(); void onInterrupt(); }} style={styles.stopTurn}><Feather name="square" size={11} color={t.textPrimary} /><Text style={styles.stopTurnText}>{stopping ? "Stopping…" : stopLabel}</Text></Pressable></View>;
}

function InlineBanner({ tone, icon, text, action, secondary, onPress, onSecondary }: { tone: "warning" | "danger" | "muted"; icon: keyof typeof Feather.glyphMap; text: string; action?: string; secondary?: string; onPress?(): void; onSecondary?(): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const color = tone === "danger" ? t.red : tone === "warning" ? t.amber : t.textTertiary;
	const fill = tone === "danger" ? t.tintRed : tone === "warning" ? t.tintAmber : t.bgSubtle;
	return <View style={[styles.banner, { backgroundColor: fill }]}><Feather name={icon} size={13} color={color} /><Text style={styles.bannerText}>{text}</Text>{secondary ? <Pressable hitSlop={7} onPress={() => { haptics.tap(); onSecondary?.(); }}><Text style={styles.bannerSecondary}>{secondary}</Text></Pressable> : null}{action ? <Pressable hitSlop={7} onPress={() => { haptics.tap(); onPress?.(); }}><Text style={[styles.bannerAction, { color }]}>{action}</Text></Pressable> : null}</View>;
}

function ConversationMenu({ visible, onClose, snapshot, openingShell, compacting, mcpReloading, compactSupported, mcpReloadSupported, interfaceSupported, interfaceReason, interfaceSwitching, onMap, onOpenShell, onPreview, onPullRequests, onSettings, onSwitchInterface, onCompact, onReload, onRename }: { visible: boolean; onClose(): void; snapshot: NonNullable<ReturnType<typeof useMobileConversation>["snapshot"]>; openingShell: boolean; compacting: boolean; mcpReloading: boolean; compactSupported: boolean; mcpReloadSupported: boolean; interfaceSupported: boolean; interfaceReason?: string; interfaceSwitching: boolean; onMap(): void; onOpenShell(): void; onPreview(): void; onPullRequests(): void; onSettings(): void; onSwitchInterface(): void; onCompact(): void; onReload(): void; onRename(title: string): void }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const [renaming, setRenaming] = useState(false);
	const [title, setTitle] = useState(snapshot.title ?? "");
	const turnInFlight = snapshot.turns.some((turn) => turn.state === "running" || turn.state === "queued");
	useEffect(() => { if (visible) { setRenaming(false); setTitle(snapshot.title ?? ""); } }, [visible, snapshot.title]);
	return <Modal visible={visible} transparent animationType="fade" onRequestClose={onClose}><Pressable style={styles.menuScrim} onPress={() => { haptics.tap(); onClose(); }} /><View style={styles.menu}>
		{renaming ? <View style={styles.rename}><Text style={styles.menuHeading}>Rename conversation</Text><TextInput autoFocus value={title} onChangeText={setTitle} placeholder="Conversation title" placeholderTextColor={t.textFaint} style={styles.renameInput} /><View style={styles.menuButtons}><Pressable onPress={() => { haptics.tap(); setRenaming(false); }}><Text style={styles.menuCancel}>Cancel</Text></Pressable><Pressable disabled={!title.trim()} onPress={() => { haptics.tap(); onRename(title.trim()); }}><Text style={styles.menuSave}>Save</Text></Pressable></View></View> : <ScrollView>
			<Text style={styles.menuHeading}>Conversation</Text>
			<MenuRow icon="repeat" label={interfaceSwitching ? "Switching interface…" : "Open Terminal UI"} hint={interfaceSupported ? "Resume this native conversation in the agent's terminal" : interfaceReason || "This agent has not declared a compatible handoff"} disabled={!interfaceSupported || interfaceSwitching} onPress={onSwitchInterface} />
			<MenuRow icon="list" label="Conversation map" hint="Jump to any request and response" onPress={onMap} />
			<MenuRow icon="terminal" label={openingShell ? "Opening shell…" : "Open worktree shell"} hint="A plain terminal in this session's worktree" disabled={openingShell} onPress={onOpenShell} />
			<MenuRow icon="globe" label="Open preview" hint="View a page or document generated in this worktree" onPress={onPreview} />
			<MenuRow icon="git-pull-request" label="Pull requests" hint="Review CI, feedback and merge state" onPress={onPullRequests} />
			<MenuRow icon="sliders" label="Turn settings" hint="Model, effort, approvals and provider options" onPress={onSettings} />
			{can(snapshot, "rename") ? <MenuRow icon="edit-2" label="Rename" onPress={() => setRenaming(true)} /> : null}
			{compactSupported ? <MenuRow icon="archive" label={compacting ? "Compacting history…" : "Compact history"} hint={turnInFlight ? "Available after the current turn finishes" : snapshot.compactedAt ? `Last compacted ${new Date(snapshot.compactedAt).toLocaleString()}` : "Summarize older context without changing files"} disabled={turnInFlight || compacting} onPress={onCompact} /> : null}
			{mcpReloadSupported ? <MenuRow icon="refresh-cw" label={mcpReloading ? "Reloading MCP servers…" : "Reload MCP servers"} hint={turnInFlight ? "Available after the current turn finishes" : undefined} disabled={turnInFlight || mcpReloading} onPress={onReload} /> : null}
			{snapshot.usage ? <View style={styles.rateBox}><Text style={styles.rateTitle}>Context and usage</Text><Text style={styles.rateText}>{formatTokens(snapshot.usage.contextUsed)} / {formatTokens(snapshot.usage.contextWindow)} context · {formatTokens(snapshot.usage.inputTokens)} in · {formatTokens(snapshot.usage.outputTokens)} out{snapshot.usage.cachedTokens ? ` · ${formatTokens(snapshot.usage.cachedTokens)} cached` : ""}{snapshot.usage.cost != null ? ` · ${snapshot.usage.currency || "$"}${snapshot.usage.cost.toFixed(4)}` : ""}</Text></View> : null}
			{snapshot.rateLimits ? <View style={styles.rateBox}><Text style={styles.rateTitle}>{snapshot.rateLimits.planLabel || "Rate limits"}</Text><Text style={styles.rateText}>Primary: {Math.round(snapshot.rateLimits.primaryUsedPercent)}% used{formatReset(snapshot.rateLimits.primaryResetsInSeconds)}{snapshot.rateLimits.secondaryUsedPercent >= 0 ? ` · Secondary: ${Math.round(snapshot.rateLimits.secondaryUsedPercent)}%${formatReset(snapshot.rateLimits.secondaryResetsInSeconds)}` : ""}</Text></View> : null}
		</ScrollView>}
	</View></Modal>;
}

function MenuRow({ icon, label, hint, disabled, onPress }: { icon: keyof typeof Feather.glyphMap; label: string; hint?: string; disabled?: boolean; onPress(): void }) { const t = useTheme(); const styles = useThemedStyles(makeStyles); return <Pressable accessibilityRole="button" accessibilityState={{ disabled }} disabled={disabled} onPress={() => { haptics.tap(); onPress(); }} style={({ pressed }) => [styles.menuRow, pressed && { backgroundColor: t.bgSubtle }, disabled && { opacity: 0.45 }]}><Feather name={icon} size={16} color={t.textTertiary} /><View style={{ flex: 1 }}><Text style={styles.menuLabel}>{label}</Text>{hint ? <Text style={styles.menuHint}>{hint}</Text> : null}</View><Feather name="chevron-right" size={15} color={t.textFaint} /></Pressable>; }

function Unavailable({ message, onShell, openingShell }: { message: string; onShell(): void; openingShell: boolean }) { return <Centered icon="alert-triangle" title="Conversation unavailable" message={`${message}\n\nThe worktree is untouched. You can still open a plain shell in it.`} action={openingShell ? "Opening…" : "Open worktree shell"} onAction={onShell} />; }
function Centered({ icon, title, message, spinning, action, onAction }: { icon: keyof typeof Feather.glyphMap; title: string; message?: string; spinning?: boolean; action?: string; onAction?(): void }) { const t = useTheme(); const styles = useThemedStyles(makeStyles); return <View style={styles.center}>{spinning ? <ActivityIndicator color={t.blue} /> : <Feather name={icon} size={22} color={t.amber} />}<Text style={styles.centerTitle}>{title}</Text>{message ? <Text style={styles.centerCopy}>{message}</Text> : null}{action ? <Pressable onPress={() => { haptics.tap(); onAction?.(); }} style={styles.centerAction}><Text style={styles.centerActionText}>{action}</Text></Pressable> : null}</View>; }

function sessionTitle(session: MobileChatSession): string { return "displayName" in session ? session.displayName || session.issueTitle || session.issueLabel || session.id : session.projectName || session.id; }
function formatTokens(value: number): string { return value >= 1_000 ? `${(value / 1_000).toFixed(value >= 10_000 ? 0 : 1)}k` : String(value); }
function interfacePhaseLabel(phase?: string): string {
	switch (phase) {
		case "draining": return "finishing current work";
		case "source_stopping": return "stopping Chat controller";
		case "source_stopped": return "Chat controller stopped";
		case "target_starting": return "resuming terminal controller";
		case "activating": return "opening Terminal UI";
		default: return "preparing native handoff";
	}
}
function signInCommand(harness: string): string | undefined { return harness === "codex" ? "codex login" : harness === "claude-code" || harness === "claude" ? "claude auth login" : undefined; }
function formatReset(seconds?: number): string { if (seconds === undefined || seconds < 0) return ""; if (seconds < 60) return ` · resets in ${Math.ceil(seconds)}s`; if (seconds < 3600) return ` · resets in ${Math.ceil(seconds / 60)}m`; return ` · resets in ${Math.ceil(seconds / 3600)}h`; }

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	meta: { minHeight: 37, flexDirection: "row", alignItems: "center", gap: 8, paddingHorizontal: 14, backgroundColor: t.bgSurface, borderBottomWidth: 1, borderBottomColor: t.borderSubtle },
	dot: { width: 7, height: 7, borderRadius: 4 },
	harness: { color: t.textSecondary, fontSize: 11, fontWeight: "600" },
	mode: { color: t.textFaint, borderWidth: 1, borderColor: t.borderSubtle, borderRadius: 5, paddingHorizontal: 5, paddingVertical: 2, fontSize: 8, letterSpacing: 1 },
	usage: { width: 54, height: 5, borderRadius: 3, backgroundColor: t.bgSubtle, overflow: "hidden" },
	usageFill: { height: 5, borderRadius: 3, backgroundColor: t.blue },
	percent: { color: t.textTertiary, fontSize: 10, fontFamily: t.fontMono },
	banner: { minHeight: 35, flexDirection: "row", alignItems: "center", gap: 8, paddingHorizontal: 12, paddingVertical: 7, borderBottomWidth: 1, borderBottomColor: t.borderSubtle },
	bannerText: { flex: 1, color: t.textSecondary, fontSize: 11, lineHeight: 15 },
	bannerAction: { fontSize: 11, fontWeight: "700" },
	bannerSecondary: { color: t.textTertiary, fontSize: 11, fontWeight: "600" },
	live: { minHeight: 35, flexDirection: "row", alignItems: "center", gap: 8, paddingHorizontal: 13, backgroundColor: t.bgSurface, borderTopWidth: 1, borderTopColor: t.borderSubtle },
	liveText: { color: t.textSecondary, fontSize: 11 },
	stopTurn: { flexDirection: "row", alignItems: "center", gap: 5, borderRadius: 7, borderWidth: 1, borderColor: t.borderDefault, paddingHorizontal: 8, paddingVertical: 5 },
	stopTurnText: { color: t.textPrimary, fontSize: 10, fontWeight: "600" },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: 12, paddingHorizontal: 38, backgroundColor: t.bgBase },
	centerTitle: { color: t.textPrimary, fontSize: 17, fontWeight: "700", textAlign: "center" },
	centerCopy: { color: t.textSecondary, fontSize: 13, lineHeight: 19, textAlign: "center" },
	centerAction: { minHeight: 42, justifyContent: "center", backgroundColor: t.blue, borderRadius: 11, paddingHorizontal: 15, marginTop: 4 },
	centerActionText: { color: t.onAccent, fontSize: 13, fontWeight: "700" },
	menuScrim: { ...StyleSheet.absoluteFill, backgroundColor: t.scrim },
	menu: { ...centeredConversationMenu, top: 76, maxHeight: "75%", backgroundColor: t.bgSurface, borderRadius: 16, borderWidth: 1, borderColor: t.borderDefault, overflow: "hidden" },
	menuHeading: { color: t.textPrimary, fontSize: 14, fontWeight: "700", paddingHorizontal: 14, paddingTop: 14, paddingBottom: 8 },
	menuRow: { minHeight: 57, flexDirection: "row", alignItems: "center", gap: 11, paddingHorizontal: 14, paddingVertical: 9, borderTopWidth: StyleSheet.hairlineWidth, borderTopColor: t.borderSubtle },
	menuLabel: { color: t.textPrimary, fontSize: 13, fontWeight: "600" },
	menuHint: { color: t.textTertiary, fontSize: 10, lineHeight: 14, marginTop: 2 },
	rateBox: { padding: 14, borderTopWidth: 1, borderTopColor: t.borderSubtle, gap: 3 },
	rateTitle: { color: t.textSecondary, fontSize: 11, fontWeight: "700" },
	rateText: { color: t.textTertiary, fontSize: 10, lineHeight: 14 },
	rename: { padding: 14, gap: 10 },
	renameInput: { minHeight: 42, borderRadius: 10, backgroundColor: t.bgElevated, borderWidth: 1, borderColor: t.borderDefault, color: t.textPrimary, paddingHorizontal: 11 },
	menuButtons: { flexDirection: "row", justifyContent: "flex-end", gap: 18, paddingTop: 3 },
	menuCancel: { color: t.textTertiary, fontSize: 12, fontWeight: "600" },
	menuSave: { color: t.blue, fontSize: 12, fontWeight: "700" },
});
