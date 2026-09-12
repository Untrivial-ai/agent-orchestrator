import { useEffect, useLayoutEffect, useState } from "react";
import type { AgentSwitchPresentationKind, AgentSwitchVisibilityOperation } from "../../shared/agent-switch-observability";
import { agentSwitchVisibility, type RendererAgentSwitchVisibility } from "../lib/agent-switch-visibility";
import type { AgentSwitch } from "./useAgentSwitches";

let fallbackToken = 0;

export function useAgentSwitchRouteVisibility(
	localRouteKey: string,
	operation: AgentSwitchVisibilityOperation,
	coordinator: RendererAgentSwitchVisibility = agentSwitchVisibility,
	trackWindow = true,
): void {
	useEffect(() => coordinator.registerRoute(localRouteKey, operation), [coordinator, localRouteKey, operation]);
	useEffect(() => {
		if (!trackWindow) return undefined;
		const focus = () => coordinator.setFocused(true);
		const blur = () => coordinator.setFocused(false);
		const online = () => coordinator.setOnline(true);
		const offline = () => coordinator.setOnline(false);
		coordinator.setFocused(typeof document !== "undefined" && document.hasFocus());
		coordinator.setOnline(typeof navigator === "undefined" || navigator.onLine);
		window.addEventListener("focus", focus); window.addEventListener("blur", blur);
		window.addEventListener("online", online); window.addEventListener("offline", offline);
		return () => {
			window.removeEventListener("focus", focus); window.removeEventListener("blur", blur);
			window.removeEventListener("online", online); window.removeEventListener("offline", offline);
		};
	}, [coordinator, trackWindow]);
}

export function useAgentSwitchPresentationVisibility({
	localRouteKey,
	agentSwitch,
	presentationKind,
	visible,
	coordinator = agentSwitchVisibility,
	tokenFactory = createToken,
}: {
	localRouteKey: string;
	agentSwitch: AgentSwitch | undefined;
	presentationKind: AgentSwitchPresentationKind | undefined;
	visible: boolean;
	coordinator?: RendererAgentSwitchVisibility;
	tokenFactory?: () => string;
}): void {
	const [token, setToken] = useState<string>();
	const switchId = agentSwitch?.id;
	const updatedAt = agentSwitch?.updatedAt;
	const durableState = visible && isObservableDurableState(agentSwitch?.state) ? agentSwitch.state : undefined;

	useEffect(() => {
		if (!visible || !switchId || !updatedAt || !presentationKind || !durableState) {
			setToken(undefined);
			return;
		}
		const nextToken = tokenFactory();
		coordinator.expectPresentation({ token: nextToken, switchId, updatedAt, localRouteKey, presentationKind, durableState });
		setToken(nextToken);
		return () => coordinator.cancel(nextToken);
	}, [coordinator, durableState, localRouteKey, presentationKind, switchId, tokenFactory, updatedAt, visible]);

	useLayoutEffect(() => {
		if (token && visible) coordinator.presented(token);
	}, [coordinator, token, visible]);
}

function createToken(): string {
	if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") return crypto.randomUUID();
	fallbackToken += 1;
	return `visibility-${Date.now().toString(36)}-${fallbackToken.toString(36)}`;
}

function isObservableDurableState(state: AgentSwitch["state"] | undefined): state is "failed" | "stopping_source" | "source_stopped" | "starting_target" {
	return state === "failed" || state === "stopping_source" || state === "source_stopped" || state === "starting_target";
}
