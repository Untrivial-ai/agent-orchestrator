import { useCallback, useEffect, useRef, useState } from "react";
import type { AgentSwitchPresentation } from "../lib/agent-switch-presentation";
import type { AgentSwitchSummary } from "../types/workspace";
import { isTerminalAgentSwitch, type AgentSwitch } from "./useAgentSwitches";

type LifecycleState = {
	sessionId: string | undefined;
	visit: number;
	observedSwitchIds: Set<string>;
	retiredSwitchIds: Set<string>;
};

type SessionSwitchSelection = {
	sessionId: string | undefined;
	visit: number;
	switchId: string;
};

export type AgentSwitchSuccessNotice = {
	agentSwitch: AgentSwitch;
	presentation: AgentSwitchPresentation;
	switchId: string;
};

type SessionSuccessNotice = AgentSwitchSuccessNotice & SessionSwitchSelection;

/**
 * Owns the outcome lifecycle shared by the TUI and Chat session surfaces.
 * Terminal history is presentable only after this mount observed that switch
 * running. Settlement retires interaction ownership immediately while an
 * immutable success snapshot remains visible for its independent notice window.
 */
export function useObservedAgentSwitchLifecycle({
	sessionId,
	agentSwitches,
	nonterminalCandidates,
}: {
	sessionId: string | undefined;
	agentSwitches: AgentSwitch[];
	nonterminalCandidates: Array<AgentSwitchSummary | undefined>;
}) {
	const [transientSuccess, setTransientSuccess] = useState<SessionSuccessNotice>();
	const [dismissedFailure, setDismissedFailure] = useState<SessionSwitchSelection>();
	const stateRef = useRef<LifecycleState>({
		sessionId,
		visit: 0,
		observedSwitchIds: new Set(),
		retiredSwitchIds: new Set(),
	});
	if (stateRef.current.sessionId !== sessionId) {
		stateRef.current = {
			sessionId,
			visit: stateRef.current.visit + 1,
			observedSwitchIds: new Set(),
			retiredSwitchIds: new Set(),
		};
	}
	for (const candidate of nonterminalCandidates) {
		if (
			candidate &&
			!isTerminalAgentSwitch(candidate) &&
			!stateRef.current.retiredSwitchIds.has(candidate.id)
		) {
			stateRef.current.observedSwitchIds.add(candidate.id);
		}
	}

	const markObserved = useCallback((switchId: string) => {
		if (!stateRef.current.retiredSwitchIds.has(switchId)) {
			stateRef.current.observedSwitchIds.add(switchId);
		}
	}, []);
	const retire = useCallback((switchId: string) => {
		stateRef.current.observedSwitchIds.delete(switchId);
		stateRef.current.retiredSwitchIds.add(switchId);
	}, []);
	const isObserved = useCallback(
		(switchId: string) =>
			stateRef.current.observedSwitchIds.has(switchId) &&
			!stateRef.current.retiredSwitchIds.has(switchId),
		[],
	);
	const isRetired = useCallback(
		(switchId: string) => stateRef.current.retiredSwitchIds.has(switchId),
		[],
	);
	const settle = useCallback(
		(agentSwitch: AgentSwitch, presentation: AgentSwitchPresentation) => {
			if (
				presentation.outcome !== "success" ||
				!stateRef.current.observedSwitchIds.has(agentSwitch.id)
			) {
				return;
			}
			// Logical ownership ends as soon as takeover is proven. The success
			// notice below is only a visual snapshot and cannot reacquire the lock.
			retire(agentSwitch.id);
			setTransientSuccess({
				agentSwitch: { ...agentSwitch },
				presentation: { ...presentation, values: { ...presentation.values } },
				sessionId: stateRef.current.sessionId,
				switchId: agentSwitch.id,
				visit: stateRef.current.visit,
			});
		},
		[retire],
	);
	const dismissFailure = useCallback((switchId: string) => {
		setDismissedFailure({
			sessionId: stateRef.current.sessionId,
			switchId,
			visit: stateRef.current.visit,
		});
	}, []);
	const visibleTransientSuccess =
		transientSuccess &&
		transientSuccess.sessionId === sessionId &&
		transientSuccess.visit === stateRef.current.visit
			? transientSuccess
			: undefined;
	const transientSuccessNotice: AgentSwitchSuccessNotice | undefined = visibleTransientSuccess;
	const transientSuccessSwitchId = transientSuccessNotice?.switchId;
	const dismissedFailureSwitchId =
		dismissedFailure &&
		dismissedFailure.sessionId === sessionId &&
		dismissedFailure.visit === stateRef.current.visit
			? dismissedFailure.switchId
			: undefined;
	// Notice expiry is deliberately independent from logical retirement: routing
	// away may cancel this timer, but it cannot make the completed switch live.
	useEffect(() => {
		if (!visibleTransientSuccess) return;
		const timer = window.setTimeout(() => {
			setTransientSuccess((current) =>
				current === visibleTransientSuccess ? undefined : current,
			);
		}, 3_000);
		return () => window.clearTimeout(timer);
	}, [visibleTransientSuccess]);

	// History is newest-first. Only its newest terminal row can close the live
	// lifecycle; an unobserved newer outcome suppresses older observed outcomes.
	const latestTerminalSwitch = agentSwitches.find(isTerminalAgentSwitch);
	const observedTerminalSwitch =
		latestTerminalSwitch && isObserved(latestTerminalSwitch.id)
			? latestTerminalSwitch
			: undefined;

	return {
		dismissFailure,
		dismissedFailureSwitchId,
		isObserved,
		isRetired,
		markObserved,
		observedTerminalSwitch,
		retire,
		settle,
		transientSuccessNotice,
		transientSuccessSwitchId,
	};
}
