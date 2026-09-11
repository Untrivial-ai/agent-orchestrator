import { mkdtempSync, readFileSync, rmSync, unlinkSync, writeFileSync } from "node:fs";
import { join } from "node:path";

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
