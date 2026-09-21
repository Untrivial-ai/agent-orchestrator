import { Feather } from "@expo/vector-icons";
import { useHeaderHeight } from "expo-router/build/react-navigation/elements";
import { useNavigation, useRouter } from "expo-router";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { useKeyboardState } from "react-native-keyboard-controller";
import { useSafeAreaInsets } from "react-native-safe-area-context";
import {
	ActivityIndicator,
	Alert,
	InteractionManager,
	Keyboard,
	KeyboardAvoidingView,
	Platform,
	Pressable,
	StyleSheet,
	Text,
	View,
} from "react-native";
import { mobileReachablePreviewURL, restoreSession, resumeSessionAgent, type DashboardSession, type OrchestratorLink } from "../api";
import { haptics } from "../haptics";
import { headerActionStyle } from "../headerAction";
import { deferRouteContent, resetHeaderRightForSwap } from "../headerRightSwap";
import { openGitHub } from "../openGitHub";
import { useApp } from "../store";
import {
	mobileInterfaceTransitionIsActive,
	mobileInterfaceTransitionIsBusy,
	mobileInterfaceTransitionIsCancellable,
	mobileInterfaceTransitionRecoveryMessage,
	useInterfaceTransition,
} from "../session/useInterfaceTransition";
import { dockInset, keyboardVerticalOffset, screenKeyboardAvoidance } from "../session/keyboardInset";
import type { Theme } from "../theme";
import { useTheme, useThemedStyles } from "../ThemeProvider";
import { getWorkspacePaths, openSessionShell } from "./api";
import { requestDockModel } from "./requestDockModel";
import { ChatComposer } from "./ChatComposer";
import { ChatLinkProvider } from "./ChatMarkdown";
import { ChatTimeline } from "./ChatTimeline";
import { ConversationTitle } from "./ConversationTitle";
import { chatSheetRoute } from "./chatSheetRegistry";
import { quotaWarning } from "./conversationChrome";
import { controllerStoppedBanner, errorBanner, mcpBanner, quotaBanner, reauthBanner, rolledBackBanner, threadBanner, type BannerCopy } from "./conversationBanners";
import { conversationActionError, conversationActionUnsupported } from "./conversationErrors";
import { conversationMarkers } from "./timelineModel";
import { brokenMcpServers, can } from "./types";
import { useMobileConversation } from "./useConversation";

type MobileChatSession = DashboardSession | OrchestratorLink;

async function dismissKeyboardBeforeSheet(keyboardVisible: boolean): Promise<void> {
	Keyboard.dismiss();
	if (!keyboardVisible) return;
	await new Promise<void>((resolve) => {
		let settled = false;
		let subscription: ReturnType<typeof Keyboard.addListener> | undefined;
		const finish = () => {
			if (settled) return;
			settled = true;
			subscription?.remove();
			clearTimeout(timeout);
			resolve();
		};
		const timeout = setTimeout(finish, 320);
		subscription = Keyboard.addListener("keyboardDidHide", finish);
	});
}

export function ChatSessionScreen({ session }: { session: MobileChatSession }) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const navigation = useNavigation();
	const router = useRouter();
	const headerHeight = useHeaderHeight();
	const insets = useSafeAreaInsets();
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
	const { config, projects, refresh: refreshBoard, setActiveProject, setWorkerPinned, renameWorker, kill } = useApp();
	const conversation = useMobileConversation(config, session.id);
	const interfaceSwitch = useInterfaceTransition(config, session.id, refreshBoard);
	const [menuOpen, setMenuOpen] = useState(false);
	const [jumpToSequence, setJumpToSequence] = useState<number>();
	const clearJumpToSequence = useCallback(() => setJumpToSequence(undefined), []);
	// Which request the user pushed aside to type instead. It lives here because
	// both the composer and the timeline change shape depending on it.
	const [dismissedRequest, setDismissedRequest] = useState<number>();
	const restoreRequest = useCallback(() => setDismissedRequest(undefined), []);
	const [filePaths, setFilePaths] = useState<string[]>([]);
	const [filePathsTruncated, setFilePathsTruncated] = useState(false);
	const filePathsRequest = useRef<Promise<{ paths: string[]; truncated: boolean }> | null>(null);
	const [openingShell, setOpeningShell] = useState(false);
	const [resuming, setResuming] = useState(false);
	// Banners closed this visit, by what they report. See conversationBanners.
	const [dismissedBanners, setDismissedBanners] = useState<ReadonlySet<string>>(() => new Set());
	const dismissBanner = useCallback((key: string) => setDismissedBanners((current) => new Set(current).add(key)), []);
	// Listens on keyboardWillShow / keyboardDidHide on both platforms. The effect
	// this replaces took its event names from screenKeyboardAvoidance, which gave
	// Android keyboardDidShow — fired only after the IME had finished animating,
	// which is why the composer needed a hand-rolled LayoutAnimation to cover the
	// gap it left. Reporting early removes the gap rather than animating over it.
	const keyboardHeight = useKeyboardState((state) => state.height);
	const keyboardVisible = useKeyboardState((state) => state.isVisible);
	const turnOptionsRequestedFor = useRef<string | undefined>(undefined);
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
		if (!conversation.snapshot || turnOptionsRequestedFor.current === session.id) return;
		turnOptionsRequestedFor.current = session.id;
		void conversation.loadTurnOptions().catch(() => {});
	}, [conversation.loadTurnOptions, conversation.snapshot, session.id]);

	const sessionName = sessionTitle(session);
	// Desktop derives one name from the session (useWorkspaceQuery: displayName ??
	// issueId ?? id) and its chat header prefers it over the conversation's own
	// title (ChatWorkspace: sessionTitle || session.title || snapshot.title). This
	// had that precedence inverted, so renaming from the board changed the row but
	// left this header on the agent's auto-generated conversation title.
	const title = sessionName || conversation.snapshot?.title || session.id;
	const projectName = "projectName" in session
		? session.projectName
		: projects.find((project) => project.id === session.projectId)?.name;
	const headerHarness = conversation.snapshot?.harness || session.harness || "Agent";
	const headerState = conversation.snapshot?.controller.state;

	// The blocking request. It takes the composer's place until it is answered.
	// Computed here rather than at render because the back-swipe below is a hook
	// and cannot sit after this screen's early returns.
	const request = requestDockModel(conversation.snapshot, {
		approval: conversation.pendingActions.includes("approval"),
		input: conversation.pendingActions.includes("input"),
	});
	const requestDismissed = request ? dismissedRequest === request.sequence : false;
	// The timeline collapses a request the card is answering to a record of what
	// was asked — one live set of controls, never two.
	const answeredBelow = request && !requestDismissed && request.canAnswerInline ? request.sequence : undefined;
	// On iOS a left-to-right swipe is the screen's back gesture, and it beat the
	// card's own swipe — you got the board instead of the previous question. The
	// whole horizontal axis goes to the card while it is up; the header's back
	// button still leaves the session, and the card's ✕ still returns the
	// composer, so nothing becomes unreachable.
	const cardShowing = Boolean(request && !requestDismissed);
	useLayoutEffect(() => {
		navigation.setOptions({ gestureEnabled: !cardShowing });
	}, [cardShowing, navigation]);
	useLayoutEffect(() => {
		if (!headerRightReady) {
			navigation.setOptions({ headerRight: undefined });
			return;
		}
		navigation.setOptions({
			headerTitle: () => (
				<ConversationTitle
					title={title}
					subtitle={[projectName, headerHarness].filter(Boolean).join(" · ")}
					harness={headerHarness}
					state={headerState}
				/>
			),
			headerRight: () => (
				<Pressable accessibilityRole="button" accessibilityLabel="Conversation actions" hitSlop={11} onPress={() => { haptics.tap(); setMenuOpen(true); }} style={headerActionStyle}>
					<Feather name="more-horizontal" size={20} color={t.textSecondary} />
				</Pressable>
			),
		});
	}, [headerHarness, headerRightReady, headerState, navigation, projectName, title, t]);

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
		await dismissKeyboardBeforeSheet(keyboardVisible);
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
	}, [conversation, keyboardVisible, router]);

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

	// Links open in-app, like the desktop Browser inspector. A localhost link from
	// the agent's machine is mapped onto the AO host first (the phone's own
	// localhost is never the dev server the agent named); openGitHub then applies
	// the package's rule for a web page - GitHub app when it has a screen for it,
	// the in-app browser otherwise.
	const openLink = useCallback((url: string) => {
		void openGitHub(mobileReachablePreviewURL(url, config?.host ?? "")?.href ?? url);
	}, [config?.host]);

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

	useEffect(() => {
		const current = conversation.snapshot;
		if (!menuOpen || !current) return;
		setMenuOpen(false);
		void dismissKeyboardBeforeSheet(keyboardVisible).then(() => router.push(chatSheetRoute({
			kind: "conversation-actions",
			snapshot: current,
			sessionTitle: sessionName,
			openingShell,
			compacting: conversation.pendingActions.includes("compact"),
			mcpReloading: conversation.pendingActions.includes("mcp"),
			refreshing: conversation.refreshing,
			compactSupported: can(current, "compaction") && !conversationActionUnsupported("compact", conversation.actionCodes.compact),
			mcpReloadSupported: can(current, "mcp_reload") && !conversationActionUnsupported("mcp", conversation.actionCodes.mcp),
			interfaceSupported: Boolean(interfaceSwitch.status?.supported),
			interfaceReason: interfaceSwitch.status?.reason || interfaceSwitch.error,
			interfaceSwitching: interfaceTransitionActive || interfaceSwitch.starting,
			// Orchestrators are not deleted from here; the board owns their lifecycle.
			canDelete: !("projectName" in session),
			canPin: !("projectName" in session),
			pinned: "projectName" in session ? false : Boolean(session.isPinned),
			onMap: () => router.push(chatSheetRoute({ kind: "conversation-map", markers: conversationMarkers(current), onSelect: setJumpToSequence })),
			onOpenShell: () => void openShell(),
			onPreview: () => router.push({ pathname: "/preview/[id]", params: { id: session.id, title, previewUrl: "previewUrl" in session ? session.previewUrl ?? undefined : undefined } }),
			onPullRequests: () => { setActiveProject(session.projectId); router.push("/(tabs)/prs"); },
			onSettings: () => void openTurnSettings(),
			onSwitchInterface: requestInterfaceSwitch,
			onCompact: () => void conversation.compact().catch(() => {}),
			onReload: () => void conversation.reloadMcp().catch(() => {}),
			onRename: () => router.push(chatSheetRoute({
				kind: "conversation-rename",
				initialTitle: sessionName,
				onRename: (next) => renameWorker(session.id, next),
			})),
			onTogglePin: () => {
				if ("projectName" in session) return;
				void setWorkerPinned(session.id, !session.isPinned).catch(() => {});
			},
			onRefresh: () => void conversation.refresh(),
			onDelete: () => {
				haptics.warning();
				Alert.alert(
					"Delete session?",
					`This terminates ${sessionName}. Its conversation and worktree are preserved.`,
					[
						{ text: "Cancel", style: "cancel" },
						// Leave first: the session this screen is showing is about to stop
						// existing, and the board is where its row disappears from.
						{ text: "Delete session", style: "destructive", onPress: () => { router.back(); void kill(session.id).catch(() => {}); } },
					],
				);
			},
		})));
	}, [conversation, interfaceSwitch, interfaceTransitionActive, keyboardVisible, menuOpen, openShell, openTurnSettings, openingShell, requestInterfaceSwitch, router, session, sessionName, setActiveProject, setWorkerPinned, title]);

	// The poll keeps retrying on its own at up to 8s; this is for the user who can
	// see the network is back and does not want to wait for the tick. Nothing else
	// on this screen re-asks the hook, so without it a transition whose poll had
	// failed could only be checked by leaving and coming back. The ref is the
	// guard (state updates are async); the state drives the label.
	const [recheckingTransition, setRecheckingTransition] = useState(false);
	const recheckingTransitionRef = useRef(false);
	const retryInterfaceCheck = useCallback(async () => {
		if (recheckingTransitionRef.current) return;
		recheckingTransitionRef.current = true;
		setRecheckingTransition(true);
		try {
			await interfaceSwitch.refresh();
		} finally {
			recheckingTransitionRef.current = false;
			setRecheckingTransition(false);
		}
	}, [interfaceSwitch]);
	const interfaceRecoveryMessage = mobileInterfaceTransitionRecoveryMessage(interfaceSwitch.transition);
	const interfaceTransitionPhaseText = interfaceRecoveryMessage || `Switching to Terminal UI · ${interfacePhaseLabel(interfaceSwitch.transition?.phase)}`;
	const interfaceTransitionBanner = {
		text: interfaceSwitch.fetchFailed
			? `${interfaceTransitionPhaseText}. Could not check on it${interfaceSwitch.error ? `: ${interfaceSwitch.error}` : ""}`
			: interfaceTransitionPhaseText,
		// Cancel stays put while a check is failing: the phase the hook holds is
		// still cancellable and the cancel is its own request, so a user who wants
		// out should not have to prove the link first. Retry rides alongside it,
		// which is also how the terminal card lays the two out.
		action: mobileInterfaceTransitionIsCancellable(interfaceSwitch.transition) ? (interfaceSwitch.cancelling ? "Cancelling…" : "Cancel") : undefined,
		onPress: interfaceSwitch.cancelling ? undefined : () => void interfaceSwitch.cancel().catch(() => {}),
		secondary: interfaceSwitch.fetchFailed || interfaceRecoveryMessage ? (recheckingTransition ? "Retrying…" : "Retry") : undefined,
		onSecondary: recheckingTransition ? undefined : () => void retryInterfaceCheck(),
	};

	if (conversation.loading && !conversation.snapshot) return <Centered icon="message-square" title="Loading conversation…" spinning />;
	if (conversation.unavailable) return <Unavailable message={conversation.unavailable.message} onShell={() => void openShell()} openingShell={openingShell} />;
	if (!conversation.snapshot) return <Centered icon="alert-triangle" title="Could not load conversation" message={conversation.error || "The daemon did not return a conversation."} action="Retry" onAction={() => void conversation.refresh()} />;
	if (contentReadySessionId !== session.id) return <Centered icon="message-square" title="Preparing conversation…" spinning />;

	const snapshot = conversation.snapshot;
	const active = snapshot.turns.some((turn) => turn.state === "running" || turn.state === "queued");
	const brokenServers = brokenMcpServers(snapshot);
	const rolledBack = snapshot.turns.filter((turn) => turn.rolledBack).length;
	const quota = quotaWarning(snapshot.rateLimits);
	const compactSupported = can(snapshot, "compaction") && !conversationActionUnsupported("compact", conversation.actionCodes.compact);
	const mcpReloadSupported = can(snapshot, "mcp_reload") && !conversationActionUnsupported("mcp", conversation.actionCodes.mcp);
	const steerUnsupported = conversationActionUnsupported("steer", conversation.actionCodes.steer);

	return (
		<KeyboardAvoidingView
			style={[styles.screen, Platform.OS === "android" ? screenKeyboardAvoidance("android", keyboardHeight, insets.bottom).rootStyle : undefined]}
			behavior={Platform.OS === "ios" ? "padding" : undefined}
			keyboardVerticalOffset={Platform.OS === "ios" ? keyboardVerticalOffset(headerHeight) : 0}
		>
			{interfaceTransitionActive ? (
				<InlineBanner
					tone="warning"
					icon="repeat"
					title={interfaceTransitionBanner.text}
					action={interfaceTransitionBanner.action}
					secondary={interfaceTransitionBanner.secondary}
					onPress={interfaceTransitionBanner.onPress}
					onSecondary={interfaceTransitionBanner.onSecondary}
				/>
			) : interfaceTransitionNotice ? (
				<InlineBanner
					tone={interfaceTransitionRecovered ? "warning" : "danger"}
					icon={interfaceTransitionRecovered ? "check-circle" : "alert-triangle"}
					title={`${interfaceTransitionNoticeText}${
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
				dismissed={dismissedBanners}
				onDismiss={dismissBanner}
			/>
			{conversation.error ? <DismissibleBanner copy={errorBanner("load", conversation.error)} dismissed={dismissedBanners} onDismiss={dismissBanner} tone="danger" icon="wifi-off" action="Retry" onPress={() => void conversation.refresh()} /> : null}
			{quota ? <DismissibleBanner copy={quotaBanner(quota)} dismissed={dismissedBanners} onDismiss={dismissBanner} tone={quota.severity === "critical" ? "danger" : "warning"} icon="alert-triangle" action="Details" onPress={() => setMenuOpen(true)} /> : null}
			{conversation.actionError && conversation.actionError !== conversation.error ? <DismissibleBanner copy={errorBanner("action", conversation.actionError)} dismissed={dismissedBanners} onDismiss={dismissBanner} tone="danger" icon="alert-circle" /> : null}
			{rolledBack ? <DismissibleBanner copy={rolledBackBanner(rolledBack)} dismissed={dismissedBanners} onDismiss={dismissBanner} tone="muted" icon="rotate-ccw" /> : null}
			{conversation.pendingSends.map((pendingSend) => pendingSend.state === "failed" ? <InlineBanner key={pendingSend.id} tone="danger" icon="send" title="Message not sent" body={pendingSend.error || "Delivery failed"} action="Retry" secondary="Discard" onPress={() => void conversation.retrySend(pendingSend.id).catch(() => {})} onSecondary={() => conversation.discardSend(pendingSend.id)} /> : null)}
			<ChatLinkProvider onLinkOpen={openLink}>
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
					answeredBelow={answeredBelow}
				/>
			</ChatLinkProvider>
			<ChatComposer
				sessionId={session.id}
				snapshot={snapshot}
				quotaActive={Boolean(quota)}
				request={request}
				requestDismissed={requestDismissed}
				onRequestDecide={conversation.resolveApproval}
				onRequestResolveInput={conversation.resolveInput}
				onShowRequest={setJumpToSequence}
				onDismissRequest={() => setDismissedRequest(request?.sequence)}
				onRestoreRequest={restoreRequest}
				skills={conversation.skills}
				filePaths={filePaths}
				filePathsTruncated={filePathsTruncated}
				onLoadSkills={conversation.loadSkills}
				onLoadFiles={loadWorkspaceFiles}
				configOptions={conversation.configOptions}
				models={conversation.models}
				steerUnavailable={steerUnsupported}
				disabled={interfaceTransitionActive}
				pending={mobileInterfaceTransitionIsBusy(interfaceSwitch.transition) || conversation.pendingSends.some((item) => item.state === "sending")}
				interrupting={conversation.pendingActions.includes("interrupt")}
				onSend={conversation.send}
				onSteer={conversation.steer}
				onPromoteQueuedTurn={conversation.promoteQueuedTurn}
				onCancelQueuedTurn={conversation.cancelQueuedTurn}
				onInterrupt={() => void conversation.interrupt().catch(() => {})}
				onOpenSettings={() => void openTurnSettings()}
				onSettings={conversation.chooseSettings}
				onConfigOption={conversation.setConfigOption}
				bottomInset={dockInset(keyboardHeight, insets.bottom, keyboardVisible)}
			/>
		</KeyboardAvoidingView>
	);
}

function ConversationBanners({ snapshot, brokenServers, resuming, terminated, mcpReloading, mcpError, mcpReloadSupported, turnInFlight, onResume, onReload, onOpenShell, dismissed, onDismiss }: { snapshot: NonNullable<ReturnType<typeof useMobileConversation>["snapshot"]>; brokenServers: ReturnType<typeof brokenMcpServers>; resuming: boolean; terminated: boolean; mcpReloading: boolean; mcpError?: string; mcpReloadSupported: boolean; turnInFlight: boolean; onResume(): void; onReload(): void; onOpenShell(): void; dismissed: ReadonlySet<string>; onDismiss(key: string): void }) {
	const thread = snapshot.threadState;
	const reauthAt = snapshot.account?.reauthRequiredAt;
	return <>
		{reauthAt ? <DismissibleBanner copy={reauthBanner(reauthAt, signInCommand(snapshot.harness))} dismissed={dismissed} onDismiss={onDismiss} tone="danger" icon="key" action="Open shell" onPress={onOpenShell} /> : null}
		{snapshot.controller.state === "stopped" ? <DismissibleBanner copy={controllerStoppedBanner(terminated, snapshot.controller.error)} dismissed={dismissed} onDismiss={onDismiss} tone="danger" icon="power" action={terminated ? (resuming ? "Restoring…" : "Restore") : (resuming ? "Resuming…" : "Resume")} secondary="Shell" onPress={resuming ? undefined : onResume} onSecondary={onOpenShell} /> : null}
		{/* Passing states clear themselves, so there is nothing to close. */}
		{snapshot.controller.state === "recovering" || snapshot.controller.state === "connecting" ? <InlineBanner tone="warning" icon="loader" title={snapshot.controller.state === "recovering" ? "Reconnecting to the agent…" : "Starting the agent…"} /> : null}
		{threadBanner(thread?.status) ? <DismissibleBanner copy={threadBanner(thread?.status)!} dismissed={dismissed} onDismiss={onDismiss} tone={thread?.status === "system_error" ? "danger" : "warning"} icon="alert-triangle" /> : null}
		{brokenServers.length ? <DismissibleBanner copy={mcpBanner(brokenServers, mcpError)!} dismissed={dismissed} onDismiss={onDismiss} tone="warning" icon="tool" action={mcpReloadSupported && !turnInFlight ? (mcpReloading ? "Reloading…" : "Reload") : undefined} onPress={mcpReloading ? undefined : onReload} /> : null}
	</>;
}

type InlineBannerProps = { tone: "warning" | "danger" | "muted"; icon: keyof typeof Feather.glyphMap; title: string; body?: string; action?: string; secondary?: string; onPress?(): void; onSecondary?(): void; onDismiss?(): void };

/** A banner the reader can close; it stays closed until what it reports changes. */
function DismissibleBanner({ copy, dismissed, onDismiss, ...props }: Omit<InlineBannerProps, "title" | "body" | "onDismiss"> & { copy: BannerCopy | undefined; dismissed: ReadonlySet<string>; onDismiss(key: string): void }) {
	if (!copy || dismissed.has(copy.key)) return null;
	return <InlineBanner {...props} title={copy.title} body={copy.body} onDismiss={() => onDismiss(copy.key)} />;
}

// Headline plus at most two lines of detail, like the desktop's status banners.
function InlineBanner({ tone, icon, title, body, action, secondary, onPress, onSecondary, onDismiss }: InlineBannerProps) {
	const t = useTheme();
	const styles = useThemedStyles(makeStyles);
	const color = tone === "danger" ? t.red : tone === "warning" ? t.amber : t.textTertiary;
	const fill = tone === "danger" ? t.tintRed : tone === "warning" ? t.tintAmber : t.bgSubtle;
	return (
		<View style={[styles.banner, { backgroundColor: fill }]}>
			<Feather name={icon} size={13} color={color} style={styles.bannerIcon} />
			<View style={styles.bannerCopy}>
				<Text style={[styles.bannerTitle, { color: tone === "muted" ? t.textSecondary : color }]} numberOfLines={1}>{title}</Text>
				{body ? <Text style={styles.bannerText} numberOfLines={2}>{body}</Text> : null}
			</View>
			{secondary ? <Pressable hitSlop={7} onPress={() => { haptics.tap(); onSecondary?.(); }}><Text style={styles.bannerSecondary}>{secondary}</Text></Pressable> : null}
			{action ? <Pressable hitSlop={7} onPress={() => { haptics.tap(); onPress?.(); }}><Text style={[styles.bannerAction, { color }]}>{action}</Text></Pressable> : null}
			{onDismiss ? <Pressable accessibilityRole="button" accessibilityLabel={`Close: ${title}`} hitSlop={10} onPress={() => { haptics.tap(); onDismiss(); }} style={styles.bannerClose}><Feather name="x" size={14} color={t.textTertiary} /></Pressable> : null}
		</View>
	);
}

function Unavailable({ message, onShell, openingShell }: { message: string; onShell(): void; openingShell: boolean }) { return <Centered icon="alert-triangle" title="Conversation unavailable" message={`${message}\n\nThe worktree is untouched. You can still open a plain shell in it.`} action={openingShell ? "Opening…" : "Open worktree shell"} onAction={onShell} />; }
function Centered({ icon, title, message, spinning, action, onAction }: { icon: keyof typeof Feather.glyphMap; title: string; message?: string; spinning?: boolean; action?: string; onAction?(): void }) { const t = useTheme(); const styles = useThemedStyles(makeStyles); return <View style={styles.center}>{spinning ? <ActivityIndicator color={t.blue} /> : <Feather name={icon} size={22} color={t.amber} />}<Text style={styles.centerTitle}>{title}</Text>{message ? <Text style={styles.centerCopy}>{message}</Text> : null}{action ? <Pressable onPress={() => { haptics.tap(); onAction?.(); }} style={styles.centerAction}><Text style={styles.centerActionText}>{action}</Text></Pressable> : null}</View>; }

function sessionTitle(session: MobileChatSession): string { return "displayName" in session ? session.displayName || session.issueTitle || session.issueLabel || session.id : session.projectName || session.id; }
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

const makeStyles = (t: Theme) => StyleSheet.create({
	screen: { flex: 1, backgroundColor: t.bgBase },
	banner: { minHeight: 35, flexDirection: "row", alignItems: "center", gap: 8, paddingLeft: 12, paddingRight: 8, paddingVertical: 7, borderBottomWidth: 1, borderBottomColor: t.borderSubtle },
	bannerIcon: { alignSelf: "flex-start", marginTop: 2 },
	bannerCopy: { flex: 1, minWidth: 0, gap: 1 },
	bannerTitle: { fontSize: 12, lineHeight: 16, fontWeight: "600" },
	bannerText: { color: t.textTertiary, fontSize: 11, lineHeight: 15 },
	bannerClose: { width: 26, height: 26, alignItems: "center", justifyContent: "center" },
	bannerAction: { fontSize: 11, fontWeight: "700" },
	bannerSecondary: { color: t.textTertiary, fontSize: 11, fontWeight: "600" },
	center: { flex: 1, alignItems: "center", justifyContent: "center", gap: 12, paddingHorizontal: 38, backgroundColor: t.bgBase },
	centerTitle: { color: t.textPrimary, fontSize: 17, fontWeight: "700", textAlign: "center" },
	centerCopy: { color: t.textSecondary, fontSize: 13, lineHeight: 19, textAlign: "center" },
	centerAction: { minHeight: 42, justifyContent: "center", backgroundColor: t.blue, borderRadius: 11, paddingHorizontal: 15, marginTop: 4 },
	centerActionText: { color: t.onAccent, fontSize: 13, fontWeight: "700" },
});
