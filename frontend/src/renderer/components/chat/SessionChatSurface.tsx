/**
 * The central surface for a chat-mode session.
 *
 * Mounted by SessionView when the session's persisted mode is `chat`. It owns the
 * conversation query and command wiring so ChatWorkspace stays a pure view of a
 * snapshot — which is what lets the same component render fixtures in the dev
 * preview and live data here.
 */

import { AlertTriangle, Loader2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { ChatWorkspace } from "./ChatWorkspace";
import { useSwitchAgentState } from "../../hooks/useSwitchAgent";
import { TerminalSwitchAgentButton } from "../TerminalSwitchAgentButton";
import {
	useConversation,
	useConversationCommands,
	useConversationConfigOptions,
	useConversationModels,
	useConversationSkills,
	useStageAttachments,
	useWorkspaceFilePaths,
} from "../../hooks/useConversation";
import { useSessionBrowserLink } from "../../hooks/useSessionBrowserLink";
import type { ShellTerminal } from "../../hooks/useShellTerminals";
import { can } from "../../types/conversation";
import type { ConversationSnapshot } from "../../types/conversation";
import type { Theme } from "../../stores/ui-store";
import type { TerminalTarget } from "../../types/terminal";
import type { WorkspaceSession } from "../../types/workspace";

export function SessionChatSurface({
	session,
	reviewerTerminal,
	onOpenReviewerTerminal,
	reviewerTarget,
	onSelectChat,
	shellTerminals,
	shellTarget,
	onSelectShellTerminal,
	onCloseShellTerminal,
	onRenameShellTerminal,
	daemonReady,
	theme,
	onOpenShell,
	openingShell,
	shellError,
	headerActions,
	controllerTransitioning,
}: {
	session: WorkspaceSession;
	reviewerTerminal?: { handleId: string; harness: string };
	onOpenReviewerTerminal?: (target: { handleId: string; harness: string }) => void;
	reviewerTarget?: Extract<TerminalTarget, { kind: "reviewer" }>;
	onSelectChat?: () => void;
	/** This session's standalone shells, rendered as tabs in the chat header. */
	shellTerminals?: ShellTerminal[];
	/** The selected shell pane, if any. Mirrors reviewerTarget. */
	shellTarget?: Extract<TerminalTarget, { kind: "shell" }>;
	onSelectShellTerminal?: (handleId: string) => void;
	onCloseShellTerminal?: (handleId: string) => void;
	onRenameShellTerminal?: (handleId: string, title: string) => void;
	daemonReady?: boolean;
	theme?: Theme;
	onOpenShell?: () => void;
	openingShell?: boolean;
	shellError?: string;
	headerActions?: ReactNode;
	/** The target controller is being installed by an interface handoff. */
	controllerTransitioning?: boolean;
}) {
	const {
		snapshot,
		isLoading,
		unavailable,
		error,
		hasOlder,
		isLoadingOlder,
		loadOlder,
	} = useConversation(session.id);
	const commands = useConversationCommands(session.id);
	const configOptions = useConversationConfigOptions(
		session.id,
		Boolean(snapshot && can(snapshot, "config_options")),
	);
	// A provider config catalog may cover only model, only mode, or both.
	// Suppress native controls only for dimensions the provider catalog replaces;
	// a model-only catalog must not hide the Approvals control.
	const providerOptions = configOptions.options ?? [];
	const hasProviderMode = providerOptions.some(
		(option) => option.category === "mode" || option.id === "mode",
	);
	const hasProviderModel = providerOptions.some(
		(option) => option.category === "model" || option.id === "model" || option.id === "agent",
	);
	// Only asked for once the conversation is actually readable: the catalog comes
	// from the live controller, so there is nothing to fetch before then.
	const { models } = useConversationModels(
		session.id,
		Boolean(snapshot) && !hasProviderModel,
	);
	const { skills } = useConversationSkills(session.id, Boolean(snapshot));
	const { paths, truncated } = useWorkspaceFilePaths(session.id, Boolean(snapshot));
	const stageAttachments = useStageAttachments(session.id);
	const openLinkInBrowser = useSessionBrowserLink(session);
	// In-place agent switching is the same session-level operation in either
	// interface; the chat header offers the same entry point the terminal pane's
	// tab strip does. Mirrors CenterPane: dialog open flag plus the element the
	// dialog anchors to (the workspace body, handed up by ChatWorkspace).
	const [switchSelectorOpen, setSwitchSelectorOpen] = useState(false);
	const [switchSelectorContainer, setSwitchSelectorContainer] = useState<HTMLDivElement | null>(null);
	const switchMutation = useSwitchAgentState(session.id);
	const renderShellFallback = Boolean(shellTarget && session);
	const renderSnapshot =
		snapshot ??
		(renderShellFallback
			? unavailableConversationSnapshot(session)
			: undefined);

	if (isLoading && !renderShellFallback) {
		return (
			<Centered>
				<Loader2 aria-hidden="true" className="size-4 animate-spin text-muted-foreground" />
				<span className="text-xs text-muted-foreground">Loading conversation…</span>
			</Centered>
		);
	}

	// A chat session whose controller has not started yet, or whose agent cannot
	// run Chat is a state to explain rather than an error to spin on. A compatible
	// session may switch interfaces, but retrying this failed controller by itself
	// cannot change the answer.
	if (unavailable && !renderShellFallback) {
		return (
			<Centered>
				<AlertTriangle aria-hidden="true" className="size-4 text-warning" />
				<strong className="text-sm text-foreground">Conversation unavailable</strong>
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					{unavailable.message}
				</p>
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					The worktree is untouched. Open a shell from the inspector to work in it directly.
				</p>
			</Centered>
		);
	}

	if (error || !renderSnapshot) {
		return (
			<Centered>
				<AlertTriangle aria-hidden="true" className="size-4 text-destructive" />
				<p className="max-w-sm text-center text-xs leading-relaxed text-muted-foreground">
					{error ?? "Could not load this conversation."}
				</p>
			</Centered>
		);
	}

	return (
		<ChatWorkspace
			snapshot={renderSnapshot}
			onLinkOpen={openLinkInBrowser}
			sessionTitle={session.title}
			sessionRole={session.kind}
			session={session}
			reviewerTerminal={reviewerTerminal}
			onOpenReviewerTerminal={onOpenReviewerTerminal}
			reviewerTarget={reviewerTarget}
			onSelectChat={onSelectChat}
			shellTerminals={shellTerminals}
			shellTarget={shellTarget}
			onSelectShellTerminal={onSelectShellTerminal}
			onCloseShellTerminal={onCloseShellTerminal}
			onRenameShellTerminal={onRenameShellTerminal}
			switchAgentControl={
				session.terminalHandleId ? (
					<TerminalSwitchAgentButton
						container={switchSelectorContainer}
						onOpenChange={setSwitchSelectorOpen}
						open={switchSelectorOpen}
						session={session}
						switchError={switchMutation.error}
					/>
				) : undefined
			}
			switchDialogContainer={setSwitchSelectorContainer}
			daemonReady={daemonReady}
			theme={theme}
			headerActions={headerActions}
			controllerTransitioning={controllerTransitioning}
			hasOlder={hasOlder}
			loadingOlder={isLoadingOlder}
			onLoadOlder={loadOlder}
			busy={commands.busy}
			onSend={(text, attachments) => commands.send({ text, attachments })}
			commandError={commands.error}
			onDecide={commands.resolve}
			onResolveInput={commands.resolveInput}
			onInterrupt={commands.interrupt}
			onResumeAgent={() => {
				void commands.resumeAgent().catch(() => {});
			}}
			resumingAgent={commands.resumingAgent}
			resumeError={commands.resumeError}
			onOpenShell={onOpenShell}
			openingShell={openingShell}
			shellError={shellError}
			models={models}
			onChooseSettings={hasProviderMode ? undefined : commands.chooseSettings}
			configOptions={configOptions.options}
			onChooseConfigOption={configOptions.setOption}
			configOptionPending={configOptions.pending}
			configOptionError={configOptions.error}
			onCompact={commands.compact}
			compacting={commands.compacting}
			compactUnavailable={commands.compactUnavailable}
			onRollback={commands.rollback}
			rollbackPending={commands.rollbackPending}
			rollbackError={commands.rollbackError}
			onEditMessage={commands.editMessage}
			editMessagePending={commands.editMessagePending}
			editMessageError={commands.editMessageError}
			onActivateBranch={commands.activateBranch}
			activateBranchPending={commands.activateBranchPending}
			activateBranchError={commands.activateBranchError}
			skills={skills}
			filePaths={paths}
			filePathsTruncated={truncated}
			onStageAttachments={stageAttachments}
			nativeImages={can(renderSnapshot, "images")}
			// Gated on what the daemon advertises, so the control is never drawn for a
			// harness that cannot steer. The refusal check stays as a backstop: it
			// covers the window before the controller reports, and it is the last word
			// afterwards, since the capability is a property of the driver.
			onSteer={can(renderSnapshot, "steer") && !commands.steerUnsupported ? commands.steer : undefined}
			onPromoteQueuedTurn={
				can(renderSnapshot, "steer") && !commands.steerUnsupported
					? commands.promoteQueuedTurn
					: undefined
			}
			steerPending={commands.steerPending}
			steerRefusal={commands.steerRefusal}
			onReloadMcpServers={
				!can(renderSnapshot, "mcp_reload") || commands.mcpReloadUnsupported
					? undefined
					: () => {
							// The rejection is already held by the mutation and rendered from
							// `mcpReloadError`; rethrowing it would only add a console error.
							void commands.reloadMcpServers().catch(() => {});
						}
			}
			reloadingMcpServers={commands.reloadingMcpServers}
			mcpReloadError={commands.mcpReloadError}
		/>
	);
}

function unavailableConversationSnapshot(session: WorkspaceSession): ConversationSnapshot {
	return {
		conversationId: session.id,
		sessionId: session.id,
		harness: session.provider,
		mode: "chat",
		controller: { state: "stopped", error: "Conversation unavailable" },
		latestSequence: 0,
		oldestSequence: 0,
		hasMoreBefore: false,
		activeBranchId: "branch-root",
		branchPoints: [],
		settings: {},
		mcpServers: [],
		capabilities: [],
		turns: [],
		items: [],
	};
}

function Centered({ children }: { children: React.ReactNode }) {
	return (
		<div className="flex h-full flex-col items-center justify-center gap-2 bg-background px-6">
			{children}
		</div>
	);
}
