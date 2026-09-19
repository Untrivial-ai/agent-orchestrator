export type AgentRecoveryActionKind = "install" | "login" | "review" | "configure";

export type AgentRecoveryReadiness = {
	installation: { state: string };
	authentication: { state: string };
};

export function resolveAgentRecoveryAction(input: {
	readiness?: AgentRecoveryReadiness | null;
	isLoading?: boolean;
}): AgentRecoveryActionKind | null {
	if (input.isLoading) return null;
	if (!input.readiness) return "configure";

	const installation = input.readiness.installation.state;
	const authentication = input.readiness.authentication.state;
	if (installation === "not_installed") return "install";
	if (authentication === "unauthorized") return "login";
	if (authentication === "configured") return "review";
	if (installation !== "installed" || !["authorized", "not_applicable"].includes(authentication)) {
		return "configure";
	}
	return null;
}
