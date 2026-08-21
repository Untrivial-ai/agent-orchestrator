import { query } from "@anthropic-ai/claude-agent-sdk";

const executable = process.env.CLAUDE_CODE_EXECUTABLE;
if (!executable) throw new Error("CLAUDE_CODE_EXECUTABLE is required");

async function* idleInput() {
	await new Promise(() => {});
}

const usageQuery = query({
	prompt: idleInput(),
	options: {
		cwd: process.cwd(),
		pathToClaudeCodeExecutable: executable,
		settingSources: ["user"],
		tools: [],
	},
});

try {
	await usageQuery.initializationResult();
	const usage = await usageQuery.usage_EXPERIMENTAL_MAY_CHANGE_DO_NOT_RELY_ON_THIS_API_YET();
	process.stdout.write(JSON.stringify(usage));
} finally {
	usageQuery.close();
}
