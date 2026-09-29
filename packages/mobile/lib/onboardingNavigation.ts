/** Replace the entire root stack so a back gesture cannot reveal onboarding. */
export function completeOnboarding(navigation: {
	dispatch(action: { type: "RESET"; payload: { index: 0; routes: [{ name: "(tabs)" }] } }): void;
}): void {
	navigation.dispatch({ type: "RESET", payload: { index: 0, routes: [{ name: "(tabs)" }] } });
}
