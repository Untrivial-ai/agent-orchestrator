import { ClaudeAcpAgent } from "@agentclientprotocol/claude-agent-acp";

const originalPrompt = ClaudeAcpAgent.prototype.prompt;

ClaudeAcpAgent.prototype.prompt = async function aoPromptWithPlanUsage(params) {
	const response = originalPrompt.call(this, params);
	void publishPlanUsage(this, params.sessionId);
	return await response;
};

async function publishPlanUsage(agent, sessionId) {
	const query = agent.sessions[sessionId]?.query;
	if (!query?.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET) return;

	try {
		const usage = await query.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET();
		await agent.client.sessionUpdate({
			sessionId,
			update: {
				sessionUpdate: "usage_update",
				used: 0,
				size: 0,
				_meta: { "_claude/planUsage": usage },
			},
		});
	} catch (error) {
		agent.logger.error("Failed to fetch Claude plan usage from SDK:", error);
	}
}

await import("@agentclientprotocol/claude-agent-acp/dist/index.js");
