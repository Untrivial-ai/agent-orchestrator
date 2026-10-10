import { mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { join, win32 } from "node:path";

const ROOT_BUILD_TOOLS = ["corepack", "corepack.cmd", "npm", "npm.cmd", "npx", "npx.cmd"];
const BIN_BUILD_TOOLS = ["corepack", "npm", "npx"];
const BUILD_ONLY_CONTENT = ["include", "lib", "node_modules", "share", "CHANGELOG.md", "README.md"];

export function runtimeSourceFiles() {
	return ["package.json", "package-lock.json"];
}

export function createWorkDirectory(outputRoot) {
	// Windows runners commonly keep the checkout on D: and the OS temp directory
	// on C:. Keep extraction beside its destination so the final rename remains
	// an atomic, same-filesystem operation on every platform.
	return mkdtempSync(join(outputRoot, ".node-download-"));
}

export function npmInvocation(
	args,
	{
		platform = process.platform,
		execPath = process.execPath,
		npmExecPath = process.env.npm_execpath,
		commandInterpreter = process.env.ComSpec,
	} = {},
) {
	// npm exposes the JavaScript entry point of the npm instance running this
	// package script. Invoking it with Node avoids the npm.cmd shell boundary on
	// Windows and keeps the nested install on the same npm version as the build.
	if (npmExecPath) {
		return { command: execPath, args: [npmExecPath, ...args] };
	}
	if (platform === "win32") {
		return {
			command: commandInterpreter || "cmd.exe",
			args: ["/d", "/s", "/c", "npm.cmd", ...args],
		};
	}
	return { command: "npm", args };
}

/**
 * Preserve Claude's API-retry backoff in the ACP session-failure extension.
 *
 * claude-agent-acp 0.70 publishes retry count and category, but drops the
 * SDK's retry_delay_ms before the event reaches ACP clients. AO patches the
 * pinned compiled adapter during packaging so the extension's ordinary
 * `details` field carries the missing timing. The narrow block match is a
 * deliberate upgrade tripwire: if upstream changes this code, packaging fails
 * instead of silently returning to an unobservable retry loop.
 */
export function patchClaudeRetryDetails(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	const caseStart = source.indexOf('case "api_retry": {');
	const caseEnd = source.indexOf('case "model_refusal_fallback":', caseStart);
	if (caseStart < 0 || caseEnd < 0) {
		throw new Error("claude-agent-acp no longer contains the expected api_retry block");
	}

	let block = source.slice(caseStart, caseEnd);
	if (block.includes("const retryDetails =")) return false;

	const publishMarker = "await publishSessionFailure";
	const publishAt = block.indexOf(publishMarker);
	const severityMarker = '                                    severity: "warning",';
	if (publishAt < 0 || !block.includes(severityMarker)) {
		throw new Error("claude-agent-acp api_retry block no longer matches AO's retry patch");
	}

	const retryDetailLines = [
		"const retryDelay = message.retry_delay_ms >= 1000",
		"                                    ? `${Number((message.retry_delay_ms / 1000).toFixed(1))}s`",
		"                                    : `${message.retry_delay_ms}ms`;",
		'                                const retryDetails = `${message.error_status === null ? "Connection error." : `API error ${message.error_status}.`} Trying again in ${retryDelay}.`;',
		"                                ",
	].join("\n");
	block = block.slice(0, publishAt) + retryDetailLines + block.slice(publishAt);
	block = block.replace(severityMarker, `${severityMarker}\n                                    details: retryDetails,`);

	writeFileSync(adapterPath, source.slice(0, caseStart) + block + source.slice(caseEnd));
	return true;
}

/**
 * claude-agent-acp 0.70 reports the last assistant message's token usage as
 * context occupancy. The SDK's getContextUsage returns its current retained
 * context estimate. Publish that snapshot after each result so AO's existing
 * usage_update projection receives the provider's own estimate.
 */
export function patchClaudeContextUsage(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	const start = source.indexOf("// Send usage_update notification");
	const end = source.indexOf("if (session.cancelled) {", start);
	if (start < 0 || end < 0 || source.indexOf("// Send usage_update notification", start + 1) >= 0) {
		throw new Error("claude-agent-acp result usage block no longer matches AO's context patch");
	}
	const block = source.slice(start, end);
	if (source.includes("// AO: use the SDK's context snapshot.")) return false;
	if (!block.includes("used: lastAssistantTotalUsage,") || !block.includes("size: session.contextWindowSize,")) {
		throw new Error("claude-agent-acp result usage block no longer matches AO's context patch");
	}

	const snapshot = [
		"// AO: use the SDK's context snapshot.",
		"                            let contextUsageTimer;",
		"                            try {",
		"                                const contextUsage = await Promise.race([",
		"                                    session.query.getContextUsage(),",
		"                                    new Promise((_, reject) => {",
		"                                        contextUsageTimer = setTimeout(() => reject(new Error('Claude SDK context usage timed out')), 2000);",
		"                                    }),",
		"                                ]);",
		"                                if (Number.isFinite(contextUsage.totalTokens) && contextUsage.totalTokens > 0 &&",
		"                                    Number.isFinite(contextUsage.rawMaxTokens) && contextUsage.rawMaxTokens > 0) {",
		"                                    lastAssistantTotalUsage = contextUsage.totalTokens;",
		"                                    session.contextWindowSize = contextUsage.rawMaxTokens;",
		"                                    session.contextWindowAuthoritative = true;",
		"                                }",
		"                            } catch (error) {",
		"                                this.logger.error('Failed to fetch Claude SDK context usage:', error);",
		"                            } finally {",
		"                                clearTimeout(contextUsageTimer);",
		"                            }",
		"                            ",
	].join("\n");
	writeFileSync(adapterPath, source.slice(0, start) + snapshot + source.slice(start));
	return true;
}

// Reuse the pinned bridge's native task registry. Ending its process loses the
// task monitor even when a background shell's process group survives.
export function patchClaudeHibernationCheck(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	const method = '"_ao/session/can_hibernate"';
	if (source.includes(method)) return false;
	const marker = ".onRequest(GOAL_CONTROL_METHOD, { parse: parseGoalRequest }, (ctx) => agent.goal(ctx.params))";
	if (!source.includes(marker) || !source.includes("session.liveBackgroundTasks.set(message.task_id")) {
		throw new Error("claude-agent-acp no longer matches AO's native-task hibernation check");
	}
	const registration = `.onRequest(${method}, { parse: params => params }, (ctx) => {
            const id = ctx.params?.sessionId;
            const session = typeof id === "string" && Object.hasOwn(agent.sessions, id) ? agent.sessions[id] : undefined;
            if (!session || session.queryClosed || !(session.liveBackgroundTasks instanceof Map)) {
                throw RequestError.invalidParams(undefined, "Live Claude session unavailable.");
            }
            return { canHibernate: !Array.from(session.liveBackgroundTasks.values()).some(task => !task.endedPerLevel) };
        })
        `;
	writeFileSync(adapterPath, source.replace(marker, registration + marker));
	return true;
}

/**
 * Settle a prompt that Claude Code folded into a task-notification cycle.
 *
 * When a prompt arrives while Claude Code runs a cycle it started itself (for
 * example after a background command finished), the CLI adds the prompt to
 * that cycle. The cycle's single result keeps its task-notification origin, so
 * claude-agent-acp 0.70 treats it as autonomous and never answers the prompt:
 * the session stays "Working" until the user sends something else. The result
 * names the prompts it answered in user_message_uuid(s); route it through the
 * user lane when it names a pending, not held-open turn. Port of upstream
 * agentclientprotocol/claude-agent-acp#1233 for issue #1145.
 */
export function patchClaudeFoldedPromptSettlement(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	if (source.includes("// AO: a result naming a pending prompt answers it.")) return false;
	const original = "const isAutonomousResult = message.origin != null && AUTONOMOUS_RESULT_ORIGINS.has(message.origin.kind);";
	const at = source.indexOf(original);
	if (at < 0 || source.indexOf(original, at + 1) >= 0 || !source.includes("const findUnsettledTurn = (uuid) =>")) {
		throw new Error("claude-agent-acp autonomous result classification no longer matches AO's folded-prompt patch");
	}
	const replacement = [
		"// AO: a result naming a pending prompt answers it.",
		"                        const answeredPromptUuids = Array.isArray(message.user_message_uuids)",
		"                            ? message.user_message_uuids",
		'                            : typeof message.user_message_uuid === "string" ? [message.user_message_uuid] : [];',
		"                        const answersPendingPrompt = answeredPromptUuids.some((uuid) => {",
		"                            const turn = findUnsettledTurn(uuid);",
		"                            return turn !== undefined && !isHeldOpen(turn);",
		"                        });",
		"                        const isAutonomousResult = message.origin != null && AUTONOMOUS_RESULT_ORIGINS.has(message.origin.kind) && !answersPendingPrompt;",
	].join("\n");
	writeFileSync(adapterPath, source.slice(0, at) + replacement + source.slice(at + original.length));
	return true;
}

/**
 * Drop trailing-idle debt that can no longer be paid.
 *
 * claude-agent-acp 0.70 counts one owed `idle` per result and absorbs that many
 * idles before treating one as a turn-over signal. Claude Code 2.1.270+ emits a
 * single idle for a turn plus the task-notification cycle that follows it, so
 * one unit is never paid. The leftover then swallows the idle a steered turn
 * settles on, leaving that turn "Working" forever. A transition into `running`
 * proves every earlier idle was already emitted, so reset the debt there. Port
 * of the sweep upstream shipped in claude-agent-acp 0.79.
 */
export function patchClaudeStaleIdleDebt(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	if (source.includes("// AO: drop idle debt that can no longer be paid.")) return false;
	const original = [
		'case "session_state_changed": {',
		"                                session.lastSessionState = message.state;",
		'                                if (message.state === "idle") {',
	].join("\n");
	const at = source.indexOf(original);
	if (at < 0 || source.indexOf(original, at + 1) >= 0 || !source.includes("session.owedTrailingIdles--;")) {
		throw new Error("claude-agent-acp session state handling no longer matches AO's idle-debt patch");
	}
	const replacement = [
		'case "session_state_changed": {',
		"                                // AO: drop idle debt that can no longer be paid.",
		"                                const previousState = session.lastSessionState;",
		"                                session.lastSessionState = message.state;",
		'                                if (message.state === "running" && previousState !== "running") {',
		"                                    session.owedTrailingIdles = 0;",
		"                                }",
		'                                if (message.state === "idle") {',
	].join("\n");
	writeFileSync(adapterPath, source.slice(0, at) + replacement + source.slice(at + original.length));
	return true;
}

/**
 * Settle a turn on the result that acknowledges its steer.
 *
 * A result naming a steered message in user_message_uuid(s) is the steered
 * sequence's answer, not another autonomous cycle: retire the steer echo and
 * leave the steer lane so the ordinary result settlement answers the prompt.
 * Without this, the steered result is parked in Turn.steeredSettle and the
 * turn must wait for an idle that a held subagent turn's debt can absorb
 * first (the stargate-20 "Working forever" stall). Unstamped results keep
 * recording steeredSettle for the idle lane. Port of the exact-result
 * acknowledgement path upstream in claude-agent-acp#1166.
 */
export function patchClaudeSteerSettlement(adapterPath) {
	let source = readFileSync(adapterPath, "utf8");
	if (source.includes("// AO: settle a result that acknowledges the steer.")) return false;
	const patches = [
		[
			"(turnInFlight.steeredEchoes ??= new Set()).add(steeredUuid);",
			"(turnInFlight.steeredEchoes ??= new Set()).add(steeredUuid);\n        (turnInFlight.steeredUuids ??= new Set()).add(steeredUuid);",
		],
		[
			'                    case "result": {',
			[
				'                    case "result": {',
				"                        // AO: settle a result that acknowledges the steer.",
				"                        const steeredTurn = session.activeTurn;",
				"                        const consumedSteers = Array.isArray(message.user_message_uuids)",
				"                            ? message.user_message_uuids",
				'                            : typeof message.user_message_uuid === "string" ? [message.user_message_uuid] : [];',
				"                        let answersSteer = false;",
				"                        if (!session.cancelled && isSteering(steeredTurn)) {",
				"                            for (const uuid of consumedSteers) {",
				"                                steeredTurn.steeredEchoes.delete(uuid);",
				"                                if (steeredTurn.steeredUuids?.has(uuid)) answersSteer = true;",
				"                            }",
				"                            answersSteer &&= steeredTurn.steeredEchoes.size === 0;",
				"                            if (answersSteer) {",
				"                                steeredTurn.steeredEchoes = undefined;",
				"                                steeredTurn.steeredUuids = undefined;",
				"                                steeredTurn.steeredSettle = undefined;",
				"                            }",
				"                        }",
			].join("\n"),
		],
	];
	for (const [original, replacement] of patches) {
		const at = source.indexOf(original);
		if (at < 0 || source.indexOf(original, at + 1) >= 0) {
			throw new Error("claude-agent-acp no longer matches AO's steer settlement patch");
		}
		source = source.slice(0, at) + replacement + source.slice(at + original.length);
	}
	const classification = "const isAutonomousResult = message.origin != null && AUTONOMOUS_RESULT_ORIGINS.has(message.origin.kind)";
	const at = source.indexOf(classification);
	if (at < 0 || source.indexOf(classification, at + 1) >= 0) {
		throw new Error("claude-agent-acp result classification no longer matches AO's steer settlement patch");
	}
	source = source.slice(0, at) + source.slice(at).replace(classification, `${classification} && !answersSteer`);
	writeFileSync(adapterPath, source);
	return true;
}

/**
 * Keep idle debt from absorbing the idle a completed steer settles on.
 *
 * The SDK coalesces the interrupted and steered cycles' trailing idles into
 * one frame (a pending asyncRewake Stop hook suppresses every intermediate
 * idle), so idle debt accrued by the superseded cycles has no separate idle
 * to pay it. The debt-absorption branch deliberately runs before the steer
 * lane; letting it consume that single frame starves the steer lane and the
 * prompt hangs "Working" until cancel. Skip absorption only when the steer
 * sequence is complete (echo replayed and result recorded); incomplete steer
 * sequences keep absorbing because their terminal idle is still ahead.
 */
export function patchClaudeSteerIdleGuard(adapterPath) {
	const source = readFileSync(adapterPath, "utf8");
	if (source.includes("// AO: an idle-debt must not absorb the one idle")) return false;
	const original = [
		"else if (session.owedTrailingIdles > 0) {",
		"                                        // Absorb a settled turn's trailing idle. Also covers a",
	].join("\n");
	const at = source.indexOf(original);
	if (at < 0 || source.indexOf(original, at + 1) >= 0) {
		throw new Error("claude-agent-acp idle handling no longer matches AO's steer idle guard patch");
	}
	const replacement = [
		"else if (session.owedTrailingIdles > 0 &&",
		"                                        // AO: an idle-debt must not absorb the one idle a",
		"                                        // completed steer sequence settles on. The SDK",
		"                                        // coalesces the interrupted and steered cycles'",
		"                                        // trailing idles into this single frame (a pending",
		"                                        // asyncRewake Stop hook suppresses every",
		"                                        // intermediate idle), so a debt accrued by the",
		"                                        // superseded cycles has no separate idle to pay it —",
		'                                        // absorbing here starves the steer lane and the',
		'                                        // prompt hangs "Working" until cancel. Incomplete',
		"                                        // steer sequences (echo pending or no result yet)",
		"                                        // keep absorbing: their terminal idle is still",
		"                                        // ahead.",
		"                                        !(isSteering(session.activeTurn) &&",
		"                                          session.activeTurn?.steeredEchoes?.size === 0 &&",
		"                                          session.activeTurn?.steeredSettle !== undefined)) {",
		"                                        // Absorb a settled turn's trailing idle. Also covers a",
	].join("\n");
	writeFileSync(adapterPath, source.slice(0, at) + replacement + source.slice(at + original.length));
	return true;
}

/**
 * Stop counting idles a steered sequence's single idle already covers.
 *
 * Stock 0.70 counts one owed idle per result, including the held turn's
 * trailer (owed while its subagent runs) and an autonomous result the steer
 * aborted. The SDK coalesces the whole wake+steer+steered sequence into ONE
 * idle, so those trailers are never paid separately; the leftover then eats
 * the idle the steer lane settles on, and after settling it eats the next
 * turn's idle — masking a #825 failure when the next echo precedes the next
 * running transition. Move the held result's trailer onto the steer lane's
 * idle at steer time, and let an autonomous result aborted by a pending
 * steer share the sequence's idle instead of adding its own.
 */
export function patchClaudeSteerDebt(adapterPath) {
	let source = readFileSync(adapterPath, "utf8");
	if (source.includes("// AO: the held result's trailer is now paid by the steer lane's idle.")) return false;
	const steerAnchor = [
		"        if (turnInFlight.deferredSettle !== undefined) {",
		"            turnInFlight.steeredSettle = turnInFlight.deferredSettle;",
		"            turnInFlight.deferredSettle = undefined;",
		"        }",
	].join("\n");
	const steerAt = source.indexOf(steerAnchor);
	if (steerAt < 0 || source.indexOf(steerAnchor, steerAt + 1) >= 0) {
		throw new Error("claude-agent-acp steer registration no longer matches AO's steer debt patch");
	}
	const steerReplacement = [
		"        if (turnInFlight.deferredSettle !== undefined) {",
		"            turnInFlight.steeredSettle = turnInFlight.deferredSettle;",
		"            turnInFlight.deferredSettle = undefined;",
		"            // AO: the held result's trailer is now paid by the steer lane's idle.",
		'            if (session.lastSessionState !== "idle" && session.owedTrailingIdles > 0) {',
		"                session.owedTrailingIdles--;",
		"            }",
		"        }",
	].join("\n");
	source = source.slice(0, steerAt) + steerReplacement + source.slice(steerAt + steerAnchor.length);
	const resultAnchor = "                            const owesTrailingIdle = isAutonomousResult || !isSteering(session.activeTurn);";
	const resultAt = source.indexOf(resultAnchor);
	if (resultAt < 0 || source.indexOf(resultAnchor, resultAt + 1) >= 0) {
		throw new Error("claude-agent-acp result trailer accounting no longer matches AO's steer debt patch");
	}
	// When patchClaudeSteerSettlement ran first, a steer-answering result has
	// already retired the steer lane (isSteering false) and settled the turn;
	// it must not then be counted as owing a fresh trailer — its trailer is
	// the sequence's single idle, already covered by the decrement above.
	const portApplied = source.includes("// AO: settle a result that acknowledges the steer.");
	const owesExpression = portApplied
		? "                            const owesTrailingIdle = !abortedBySteer && !answersSteer && (isAutonomousResult || !isSteering(session.activeTurn));"
		: "                            const owesTrailingIdle = !abortedBySteer && (isAutonomousResult || !isSteering(session.activeTurn));";
	const resultReplacement = [
		"                            // AO: an autonomous cycle aborted by a pending steer shares the",
		"                            // steered sequence's single idle instead of owing its own.",
		"                            const abortedBySteer = isAutonomousResult && isSteering(session.activeTurn) &&",
		"                                session.activeTurn.steeredEchoes.size > 0;",
		owesExpression,
	].join("\n");
	writeFileSync(adapterPath, source.slice(0, resultAt) + resultReplacement + source.slice(resultAt + resultAnchor.length));
	return true;
}

export function pruneNodeDistribution(nodeRoot) {
	// The Unix archives expose npm/corepack as bin/ symlinks into lib/. Remove
	// the entry points before their targets so packagers never see dangling
	// links. Windows keeps the launchers at the archive root instead.
	for (const name of BIN_BUILD_TOOLS) {
		removeFile(join(nodeRoot, "bin", name));
	}
	for (const name of ROOT_BUILD_TOOLS) {
		removeFile(join(nodeRoot, name));
	}
	for (const name of BUILD_ONLY_CONTENT) {
		rmSync(join(nodeRoot, name), { recursive: true, force: true });
	}
}

function removeFile(path) {
	try {
		// unlink removes a symlink itself even when its target is already absent.
		unlinkSync(path);
	} catch (error) {
		if (error?.code !== "ENOENT") throw error;
	}
}

export function archiveExtraction(
	archivePath,
	workDir,
	{ platform = process.platform, systemRoot = process.env.SystemRoot } = {},
) {
	// Windows ships bsdtar as System32\tar.exe and it reads zip. PowerShell's
	// Expand-Archive is the obvious alternative but is bound by MAX_PATH: with
	// LongPathsEnabled=0 and a deep checkout, Node's bundled npm tree exceeds
	// 260 characters and extraction fails without a non-zero exit, so the build
	// only discovers it later, as a missing directory. bsdtar handles the same
	// archive at the same depth.
	if (platform === "win32") {
		if (!systemRoot) throw new Error("SystemRoot is required for Windows archive extraction");
		// Git Bash can put GNU tar ahead of System32 on PATH.
		return { command: win32.join(systemRoot, "System32", "tar.exe"), args: ["-xf", archivePath, "-C", workDir] };
	}
	return { command: "tar", args: ["-xzf", archivePath, "-C", workDir] };
}
