type InterfaceTransition = { phase: string };

const activePhases = new Set([
	"requested",
	"preflighting",
	"draining",
	"source_stopping",
	"source_stopped",
	"target_starting",
	"activating",
]);

export function mobileInterfaceTransitionIsActive(transition?: InterfaceTransition): boolean {
	return Boolean(transition && activePhases.has(transition.phase));
}

export function mobileInterfaceTransitionIsCancellable(transition?: InterfaceTransition): boolean {
	return Boolean(
		transition && ["requested", "preflighting", "draining"].includes(transition.phase),
	);
}

export function interfaceTransitionPollInterval(transition?: InterfaceTransition): number | undefined {
	return mobileInterfaceTransitionIsActive(transition) ? 300 : undefined;
}
