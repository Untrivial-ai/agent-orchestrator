import { useCallback, useEffect, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import type { AuthWorkflow } from "../components/AuthTerminalPanel";
import { useInstallRunner } from "../components/InstallDependencyDialog";
import { useCloseShellTerminal } from "./useShellTerminals";
import type { TerminalSessionState } from "./useTerminalSession";
import { useGitHubAuthRequirement, useGitHubAuthTerminal, useStartGitHubAuthTerminal, useSystemRequirementsGate } from "./useSystemRequirementsGate";

const GH_INSTALL_TARGET = "gh" as const;
/** The GitHub page watches long-running external work (a package install, a
 *  device-code sign-in), so its normal background poll stays deliberately slow. */
const STEP_POLL_INTERVAL_MS = 2_500;
const AUTH_RECHECK_INTERVAL_MS = 1_000;
const AUTH_RECHECK_TIMEOUT_MS = 10_000;

/**
 * GitHub readiness for onboarding: install the CLI in place when it is missing,
 * then run the daemon-owned `gh auth login` terminal when it is present but
 * signed out. GitHub stays advisory, so nothing here blocks the rest of setup.
 */
export function useGitHubSetup({ poll = false }: { poll?: boolean } = {}) {
	const { t } = useTranslation();
	const gate = useSystemRequirementsGate();
	const terminalQuery = useGitHubAuthTerminal();
	const startSignIn = useStartGitHubAuthTerminal();
	const { mutate: closeTerminal } = useCloseShellTerminal();
	const [terminalState, setTerminalState] = useState<{ handleId: string | null; state: TerminalSessionState }>({
		handleId: null,
		state: "idle",
	});
	const [authRechecking, setAuthRechecking] = useState(false);
	const terminal = terminalQuery.data;
	const resolvedTerminalState = terminalState.handleId === terminal?.handleId ? terminalState.state : "idle";
	const loginRunning = Boolean(terminal && (resolvedTerminalState === "connecting" || resolvedTerminalState === "attached" || resolvedTerminalState === "reattaching"));
	const loginEnded = Boolean(terminal && (resolvedTerminalState === "exited" || resolvedTerminalState === "error"));
	const gh = gate.requirements.find((requirement) => requirement.id === GH_INSTALL_TARGET);
	const auth = useGitHubAuthRequirement(loginRunning);
	const authRef = useRef(auth.refetch);
	authRef.current = auth.refetch;
	const requirementsRef = useRef(gate.query.refetch);
	requirementsRef.current = gate.query.refetch;
	// The gh install is a system install, the same one the startup gate runs, so
	// it uses that runner rather than a second POST-and-poll of its own.
	const installRunner = useInstallRunner(
		() => {
			void requirementsRef.current();
			startSignIn.mutate();
		},
		t("onboarding.installStartFailed"),
	);

	// No manual re-check on the GitHub page: it polls until both halves settle.
	// The CLI half matters while gh is missing (an install can land at any time)
	// and the auth half while GitHub is still signed out (a device flow can
	// finish after its terminal is gone).
	const cliMissing = gh?.satisfied === false;
	const authSatisfied = Boolean(auth.data?.satisfied);
	// "Not yet confirmed" is not the same as "needs nothing": a probe that ran
	// before the daemon was reachable leaves gh unknown, and that has to keep
	// polling or the page sits on its checking state forever.
	const cliReady = gh?.satisfied === true;
	useEffect(() => {
		if (!poll || (cliReady && authSatisfied)) return;
		const timer = window.setInterval(() => {
			// Each half is only worth probing while it can still change.
			if (!cliReady) void requirementsRef.current();
			if (!authSatisfied) void authRef.current();
		}, STEP_POLL_INTERVAL_MS);
		return () => window.clearInterval(timer);
	}, [authSatisfied, cliReady, poll]);

	// Browser-based authentication can finish just before the CLI writes its
	// credentials and exits. Recheck briefly after terminal completion instead
	// of flashing a stale "Try again" state at a successful user.
	useEffect(() => {
		if (!authRechecking || authSatisfied) return;
		const interval = window.setInterval(() => void authRef.current(), AUTH_RECHECK_INTERVAL_MS);
		const timeout = window.setTimeout(() => setAuthRechecking(false), AUTH_RECHECK_TIMEOUT_MS);
		return () => {
			window.clearInterval(interval);
			window.clearTimeout(timeout);
		};
	}, [authRechecking, authSatisfied]);

	const handleTerminalState = useCallback((state: TerminalSessionState) => {
		setTerminalState({ handleId: terminalQuery.data?.handleId ?? null, state });
		if (state !== "exited" && state !== "error") return;
		setAuthRechecking(true);
		void authRef.current();
	}, [terminalQuery.data?.handleId]);

	const signIn = useCallback(() => {
		setAuthRechecking(false);
		startSignIn.mutate();
	}, [startSignIn]);

	const closeSignIn = useCallback(() => {
		if (!terminal) return;
		setAuthRechecking(false);
		closeTerminal(terminal.handleId, {
			onSuccess: () => {
				terminalQuery.clear();
				void authRef.current();
			},
		});
	}, [closeTerminal, terminal, terminalQuery]);

	// A device flow can finish in the terminal before its PTY exit reaches the
	// renderer. When the requirement flips, retire the terminal and let the
	// surface collapse on its own.
	const completedRef = useRef<string | null>(null);
	useEffect(() => {
		if (!auth.data?.satisfied || !terminal) return;
		if (completedRef.current === terminal.handleId) return;
		completedRef.current = terminal.handleId;
		closeTerminal(terminal.handleId, {
			onSuccess: () => {
				terminalQuery.clear();
				void gate.query.refetch();
			},
		});
	}, [auth.data?.satisfied, closeTerminal, gate.query, terminal, terminalQuery]);

	const workflow: AuthWorkflow | null = terminal
		? {
				agentId: "github",
				action: "login",
				terminal,
				guidance: t(loginEnded ? "startup.githubLoginStopped" : "startup.githubLoginRunning"),
				phase: loginEnded ? "unauthorized" : "running",
				reason: loginEnded ? t("startup.githubLoginStopped") : undefined,
				startedAt: Date.parse(terminal.createdAt) || Date.now(),
			}
		: null;

	return {
		authChecking: startSignIn.isPending || loginRunning || authRechecking,
		authSatisfied,
		cliMissing,
		closeSignIn,
		gh,
		handleTerminalState,
		install: () => installRunner.start(GH_INSTALL_TARGET),
		installError: installRunner.startError ?? null,
		installing: installRunner.running,
		job: installRunner.jobFor(GH_INSTALL_TARGET),
		loginEnded,
		loginRunning,
		requirementsQuery: gate.query,
		signIn,
		signInError: startSignIn.isError ? startSignIn.error.message : null,
		signInPending: startSignIn.isPending,
		workflow,
	};
}
