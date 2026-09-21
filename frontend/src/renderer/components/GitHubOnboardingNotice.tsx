import { GitPullRequest, TerminalSquare, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { TerminalSessionState } from "../hooks/useTerminalSession";
import { useCloseShellTerminal } from "../hooks/useShellTerminals";
import { useGitHubAuthAutoLoginOffered, useGitHubAuthRequirement, useGitHubAuthTerminal, useStartGitHubAuthTerminal, useSystemRequirementsGate } from "../hooks/useSystemRequirementsGate";
import { aoBridge } from "../lib/bridge";
import { useShellMaybe } from "../lib/shell-context";
import { useResolvedTheme, useUiStore } from "../stores/ui-store";
import { TerminalPane } from "./TerminalPane";
import { TopbarButton } from "./TopbarButton";

const GITHUB_CLI_INSTALL_URL = "https://cli.github.com/";

/** Onboarding advisory. GitHub is not required for local work, but surfacing
 * missing auth before task creation prevents a late PR-creation failure inside
 * an agent session. */
export function GitHubOnboardingNotice() {
	const { t } = useTranslation();
	const gate = useSystemRequirementsGate();
	const startLogin = useStartGitHubAuthTerminal();
	const autoLogin = useGitHubAuthAutoLoginOffered();
	const terminalQuery = useGitHubAuthTerminal();
	const terminal = terminalQuery.data;
	const [terminalStatus, setTerminalStatus] = useState<{ handleId: string | null; state: TerminalSessionState }>({
		handleId: null,
		state: "idle",
	});
	const terminalState = terminalStatus.handleId === terminal?.handleId ? terminalStatus.state : "idle";
	const loginRunning = Boolean(terminal && (terminalState === "connecting" || terminalState === "attached" || terminalState === "reattaching"));
	const loginEnded = Boolean(terminal && (terminalState === "exited" || terminalState === "error"));
	const authQuery = useGitHubAuthRequirement(loginRunning);
	const { mutate: closeTerminal } = useCloseShellTerminal();
	const showGlobalToast = useUiStore((state) => state.showGlobalToast);
	const theme = useResolvedTheme();
	const shell = useShellMaybe();
	const requirements = gate.requirements ?? [];
	const terminalRef = useRef(terminal);
	const refetchAuthRef = useRef(authQuery.refetch);
	const exitCheckedTerminalRef = useRef<string | null>(null);
	const completedTerminalRef = useRef<string | null>(null);
	const [loginFocusRequested, setLoginFocusRequested] = useState(false);
	const [manualCheckPending, setManualCheckPending] = useState(false);
	const gh = requirements.find((requirement) => requirement.id === "gh");
	const auth = authQuery.data;

	terminalRef.current = terminal;
	refetchAuthRef.current = authQuery.refetch;
	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		const active = terminalRef.current;
		setTerminalStatus({ handleId: active?.handleId ?? null, state });
		if (!active || (state !== "exited" && state !== "error")) return;
		if (exitCheckedTerminalRef.current === active.handleId) return;
		exitCheckedTerminalRef.current = active.handleId;
		void refetchAuthRef.current();
	}, []);
	useEffect(() => {
		if (!auth?.satisfied || !terminal) return;
		if (completedTerminalRef.current === terminal.handleId) return;
		completedTerminalRef.current = terminal.handleId;
		showGlobalToast(t("startup.githubConnected"));
		closeTerminal(terminal.handleId, { onSuccess: terminalQuery.clear });
	}, [auth?.satisfied, closeTerminal, showGlobalToast, t, terminal, terminalQuery.clear]);
	useEffect(() => {
		if (
			!auth ||
			auth.satisfied ||
			gh?.satisfied !== true ||
			terminal ||
			startLogin.isPending ||
			autoLogin.offered
		) {
			return;
		}
		autoLogin.markOffered();
		startLogin.mutate();
	}, [auth, autoLogin.markOffered, autoLogin.offered, gh?.satisfied, startLogin.isPending, startLogin.mutate, terminal]);

	if (!auth || auth.satisfied) return null;

	const openLogin = () => {
		setLoginFocusRequested(true);
		autoLogin.markOffered();
		startLogin.mutate();
	};
	const retryLogin = () => {
		if (!terminal) return;
		setLoginFocusRequested(true);
		closeTerminal(terminal.handleId, {
			onSuccess: () => {
				terminalQuery.clear();
				startLogin.mutate();
			},
		});
	};

	const checkAgain = async () => {
		setManualCheckPending(true);
		try {
			await authQuery.refetch();
		} finally {
			setManualCheckPending(false);
		}
	};
	const closeLogin = () => {
		setLoginFocusRequested(false);
		if (!terminal) return;
		closeTerminal(terminal.handleId, { onSuccess: terminalQuery.clear });
	};
	const cliMissing = gh?.satisfied === false;
	return (
		<div className="flex w-full justify-center px-3">
			<div className="w-full max-w-[620px] rounded-welcome-panel border border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-card)] px-4 py-4" role="status">
				<div className="flex items-start gap-3">
					<span className="grid size-9 shrink-0 place-items-center rounded-lg bg-[var(--color-bg-import-chip)] text-[var(--color-text-import-muted)]">
						<GitPullRequest className="size-4" aria-hidden="true" />
					</span>
					<div className="min-w-0 flex-1">
						<p className="text-[14px] font-semibold text-[var(--color-text-import-title)]">{t("startup.githubSetupTitle")}</p>
						<p className="mt-0.5 text-[12px] leading-5 text-[var(--color-text-import-muted)]">
							{t(cliMissing ? "startup.githubSetupMissingCli" : "startup.githubSetupSignedOut")}
						</p>
						<div className="mt-2 flex flex-wrap items-center gap-2">
							<TopbarButton
								disabled={!cliMissing && (startLogin.isPending || Boolean(terminal && !loginEnded))}
								onClick={() => cliMissing
									? void aoBridge.app.openExternal(GITHUB_CLI_INSTALL_URL)
									: loginEnded
										? retryLogin()
										: openLogin()}
								variant="primary"
							>
								{cliMissing ? null : <TerminalSquare className="size-icon-sm" aria-hidden="true" />}
								{cliMissing
									? t("startup.openGithubCliDocs")
									: startLogin.isPending
										? t("startup.githubLoginStarting")
										: loginEnded
											? t("startup.githubLoginTryAgain")
											: t("startup.githubLogin")}
							</TopbarButton>
							{loginRunning ? null : (
								<TopbarButton
									disabled={manualCheckPending}
									onClick={() => void checkAgain()}
									variant="accent"
								>
									{manualCheckPending ? t("startup.checkingAgain") : t("startup.checkAgain")}
								</TopbarButton>
							)}
						</div>
						{startLogin.isError ? <p className="mt-2 text-xs text-destructive" role="alert">{startLogin.error.message}</p> : null}
						{terminal ? (
							<div className="mt-3 overflow-hidden rounded-lg border border-[var(--color-border-import-modal)] bg-terminal" data-testid="github-auth-terminal">
								<div className="flex min-h-9 items-center justify-between gap-3 border-b border-[var(--color-border-import-modal)] bg-[var(--color-bg-import-modal)] px-3 py-1.5">
									<div className="min-w-0">
										<p className="truncate text-xs font-medium text-[var(--color-text-import-title)]">{terminal.title}</p>
										<p className="truncate text-[11px] text-[var(--color-text-import-muted)]">
											{t(loginEnded ? "startup.githubLoginStopped" : "startup.githubLoginRunning")}
										</p>
									</div>
									<TopbarButton aria-label={t("common.close")} className="!size-7 shrink-0" onClick={closeLogin} variant="icon">
										<X className="size-4" aria-hidden="true" />
									</TopbarButton>
								</div>
								<div className="h-[240px] min-h-0">
									<TerminalPane daemonReady={shell ? shell.daemonStatus.state === "ready" : true} focusRequested={loginFocusRequested && terminalState === "attached"} fontSize={12} onTerminalStateChange={handleTerminalState} terminalTarget={{ kind: "shell", handleId: terminal.handleId, generation: terminal.createdAt, title: terminal.title }} theme={theme} />
								</div>
							</div>
						) : null}
					</div>
				</div>
			</div>
		</div>
	);
}
