#!/usr/bin/env node

import { spawn } from "node:child_process";
import { createWriteStream, existsSync, statSync } from "node:fs";
import { mkdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptPath = fileURLToPath(import.meta.url);
const frontendRoot = path.resolve(path.dirname(scriptPath), "..");
const repositoryRoot = path.resolve(frontendRoot, "..");
const backendRoot = path.join(repositoryRoot, "backend");
const requiredPlaywrightEvidence = ["screenshot", "video", "trace"];
export const requiredPlaywrightContracts = Object.freeze([
	{
		id: "MQA-07:synthetic-root",
		marker: "MQA-07 synthetic root branch › activates the advertised synthetic root as one encoded route segment",
	},
	{
		id: "MQA-02:queued-controls",
		marker: "MQA-02 queued-turn controls › keeps queue-only actions separate from destructive Stop",
	},
	{
		id: "MQA-10:file-reader",
		marker: "MQA-10 attachment settlement and identity › waits for an in-flight FileReader before sending",
	},
	{
		id: "MQA-10:delivery-disclosure",
		marker: "MQA-10 attachment settlement and identity › discloses whether an image is sent by path, native bytes, or both",
	},
	{
		id: "MQA-10:filename",
		marker: "MQA-10 attachment settlement and identity › preserves the original filename in staged and native image payloads",
	},
	{
		id: "MQA-09:path-disclosure",
		marker: "MQA-09 at-sign path references › discloses that an at-sign chip is a path reference, not supplied context",
	},
	{
		id: "MQA-09:workspace-refresh",
		marker: "MQA-09 at-sign path references › refreshes the file catalog after workspace change events",
	},
	{
		id: "MQA-06:retry",
		marker: "MQA-06 failed target-history checkpoint › offers a retryable, announced action for the interface-switch failure",
	},
	{
		id: "MQA-05:drain",
		marker: "MQA-05 busy Chat to Terminal policy › offers Finish work before submitting the non-destructive drain policy",
	},
	{
		id: "MQA-05:interrupt",
		marker: "MQA-05 busy Chat to Terminal policy › maps explicit Stop now consent to the destructive interrupt policy",
	},
	{
		id: "MQA-04:drafts",
		marker: "MQA-04 composer durability › keeps drafts session-scoped across interface, navigation, reload, and accepted send",
	},
	{
		id: "MQA-08:imported-outcome",
		marker: "MQA-08 imported Terminal outcome › uses a neutral imported-history label when completion metadata is unavailable",
	},
	{
		id: "MQA-01:plan-disclosure",
		marker: "MQA-01 provider Plan mode disclosure › qualifies provider copy that falsely promises no tool execution",
	},
	{
		id: "MQA-03:handoff-copy",
		marker: "MQA-03 deterministic agent-switch copy companion › states that a switch can start fresh without claiming semantic handoff",
	},
	{
		id: "MQA-11:selection-semantics",
		marker: "MQA-11 selection semantics › exposes delivery and settings selection to assistive technology",
	},
	{
		id: "GAP-01:context-quota",
		marker: "GAP-01 implemented-but-unmounted signals › mounts context and quota telemetry where users can act on it",
	},
	{
		id: "GAP-01:credit-error",
		marker: "GAP-01 implemented-but-unmounted signals › shows actionable credit-exhaustion detail instead of only reconnect status",
	},
	{
		id: "MQA-12:lifecycle",
		marker: "MQA-12 renderer error gate › survives Terminal, shell-tab, Chat, close, and resize lifecycles without uncaught errors",
	},
	{
		id: "MQA-12:pageerror-gate",
		marker: "MQA-12 renderer error gate › strict fixture rejects a deliberately injected page error",
		expectedFailureMarker: "MQA-12-SYNTHETIC-PAGE-ERROR",
	},
]);
export const goContractDefinitions = Object.freeze([
	{
		name: "TestChatUIRegressionEncodedSyntheticBranchIDIsDecoded",
		playwrightMarker:
			"ChatUI conversation integrity › MQA-07 synthetic root branch › activates the advertised synthetic root as one encoded route segment",
	},
	{
		name: "TestChatUIRegressionTUIToChatRetriesFreshTargetAfterUnsettledHistory",
		playwrightMarker:
			"ChatUI interface switching › MQA-06 failed target-history checkpoint › offers a retryable, announced action for the interface-switch failure",
	},
	{
		name: "TestInterfaceHandoffImportsOutcomeUnknownNativeHistoryAsRecovered",
		playwrightMarker:
			"ChatUI interface switching › MQA-08 imported Terminal outcome › uses a neutral imported-history label when completion metadata is unavailable",
	},
]);
export const requiredGoContracts = Object.freeze(
	goContractDefinitions.map((contract) => contract.name),
);

const usage = `Usage: npm run qa:chatui -- [options]

Options:
  --capture       Record completed contract failures but return exit code zero.
  --headed        Run the Playwright contracts in a visible browser.
  --grep <value>  Run Playwright contracts matching a title/tag expression.
  --help          Show this help.
`;

export function parseRunnerArgs(argv) {
	const options = { capture: false, grep: undefined, headed: false, help: false };
	for (let index = 0; index < argv.length; index += 1) {
		const argument = argv[index];
		switch (argument) {
			case "--capture":
				options.capture = true;
				break;
			case "--headed":
				options.headed = true;
				break;
			case "--help":
			case "-h":
				options.help = true;
				break;
			case "--grep": {
				const value = argv[index + 1];
				if (!value || value.startsWith("--")) {
					throw new Error("--grep requires a non-empty value");
				}
				options.grep = value;
				index += 1;
				break;
			}
			default:
				if (argument.startsWith("--grep=")) {
					const value = argument.slice("--grep=".length);
					if (!value) throw new Error("--grep requires a non-empty value");
					options.grep = value;
					break;
				}
				throw new Error(`unknown option: ${argument}`);
		}
	}
	return options;
}

export function formatRunStamp(date) {
	return date.toISOString().replaceAll(":", "-").replace(".", "-");
}

function completedPlaywrightTests(suites) {
	const completed = [];
	const visit = (entries, ancestors = []) => {
		if (!Array.isArray(entries)) return;
		for (const suite of entries) {
			if (!suite || typeof suite !== "object") continue;
			const lineage = [...ancestors, suite.title].filter(
				(title) => typeof title === "string" && title.length > 0,
			);
			if (Array.isArray(suite.specs)) {
				for (const spec of suite.specs) {
					if (!spec || typeof spec !== "object" || !Array.isArray(spec.tests)) continue;
					for (const test of spec.tests) {
						const results = Array.isArray(test?.results) ? test.results : [];
						const result = results.findLast(
							(candidate) => candidate?.status && candidate.status !== "skipped",
						);
						if (!result) continue;
						completed.push({
							expectedStatus: test.expectedStatus,
							result,
							title: [...lineage, spec.title]
								.filter((title) => typeof title === "string" && title.length > 0)
								.join(" › "),
						});
					}
				}
			}
			visit(suite.suites, lineage);
		}
	};
	visit(suites);
	return completed;
}

function isWithinDirectory(root, candidate) {
	if (!root) return true;
	const relative = path.relative(path.resolve(root), path.resolve(candidate));
	return (
		relative === "" ||
		(!path.isAbsolute(relative) && !relative.startsWith(`..${path.sep}`) && relative !== "..")
	);
}

function persistedFile(candidate) {
	try {
		const stat = statSync(candidate);
		return stat.isFile() && stat.size > 0;
	} catch {
		return false;
	}
}

function parsedStrictGateState(error) {
	const message = typeof error?.message === "string" ? error.message : "";
	const match = message.match(/CHATUI_STRICT_GATE_STATE_B64:([A-Za-z0-9+/]+={0,2})/);
	if (!match?.[1]) return undefined;
	try {
		return JSON.parse(Buffer.from(match[1], "base64").toString("utf8"));
	} catch {
		return undefined;
	}
}

export function assessPlaywrightArtifacts({
	attachmentIsPersisted = persistedFile,
	attachmentRoot,
	grep,
	htmlPresent,
	htmlReadError,
	htmlText,
	jsonPresent,
	jsonReadError,
	jsonText,
}) {
	const failures = [];
	if (!htmlPresent) {
		failures.push("Playwright did not persist its configured HTML report");
	} else if (htmlReadError) {
		failures.push(`Playwright HTML report could not be read: ${htmlReadError}`);
	} else if (!htmlText || !/<html(?:\s|>)/i.test(htmlText)) {
		failures.push("Playwright HTML report is empty or malformed");
	}

	if (!jsonPresent) {
		failures.push("Playwright did not persist its configured JSON report");
		return failures;
	}
	if (jsonReadError) {
		failures.push(`Playwright JSON report could not be read: ${jsonReadError}`);
		return failures;
	}

	let payload;
	try {
		payload = JSON.parse(jsonText);
	} catch {
		failures.push("Playwright JSON report is malformed");
		return failures;
	}
	if (!payload || typeof payload !== "object" || Array.isArray(payload)) {
		failures.push("Playwright JSON report has an invalid top-level shape");
		return failures;
	}
	if (!Array.isArray(payload.errors)) {
		failures.push("Playwright JSON report has no valid suite-level errors collection");
	} else if (payload.errors.length > 0) {
		failures.push(
			`Playwright reported ${payload.errors.length} suite-level infrastructure ${payload.errors.length === 1 ? "error" : "errors"}`,
		);
	}

	const completedCounts = ["expected", "unexpected", "flaky"].map((key) => payload.stats?.[key]);
	const skippedCount = payload.stats?.skipped;
	if (
		completedCounts.some((count) => !Number.isInteger(count) || count < 0) ||
		!Number.isInteger(skippedCount) ||
		skippedCount < 0
	) {
		failures.push("Playwright JSON report has invalid completion statistics");
	} else if (completedCounts.reduce((total, count) => total + count, 0) === 0) {
		failures.push("Playwright completed no matching contract tests");
	}

	const completedTests = completedPlaywrightTests(payload.suites);
	if (!Array.isArray(payload.suites)) {
		failures.push("Playwright JSON report has no valid suite collection");
	} else if (completedTests.length === 0 && completedCounts.some((count) => count > 0)) {
		failures.push("Playwright JSON report lists completed tests without executable suite results");
	}
	if (skippedCount > 0) {
		failures.push(`Playwright run skipped ${skippedCount} configured contract tests`);
	}
	if (!grep) {
		const missingContracts = requiredPlaywrightContracts.filter(
			(contract) => !completedTests.some((test) => test.title.includes(contract.marker)),
		);
		if (missingContracts.length > 0) {
			failures.push(
				`Playwright unfiltered run did not execute required contracts: ${missingContracts.map((contract) => contract.id).join(", ")}`,
			);
		}
	}
	for (const contract of requiredPlaywrightContracts) {
		if (!contract.expectedFailureMarker) continue;
		const test = completedTests.find((candidate) => candidate.title.includes(contract.marker));
		if (!test) continue;
		const errors = Array.isArray(test.result.errors) ? test.result.errors : [];
		const state = errors.length === 1 ? parsedStrictGateState(errors[0]) : undefined;
		const failureIsConstrained =
			test.expectedStatus === "failed" &&
			test.result.status === "failed" &&
			Array.isArray(state?.pageErrors) &&
			state.pageErrors.length === 1 &&
			String(state.pageErrors[0]).includes(contract.expectedFailureMarker) &&
			Array.isArray(state.consoleErrors) &&
			state.consoleErrors.length === 0 &&
			Array.isArray(state.unexpectedRequests) &&
			state.unexpectedRequests.length === 0;
		if (!failureIsConstrained) {
			failures.push(
				`Playwright negative control ${contract.id} did not fail exclusively through ${contract.expectedFailureMarker}`,
			);
		}
	}
	for (const test of completedTests) {
		if (test.result.status === "interrupted") {
			failures.push(`Playwright test was interrupted before a complete result: ${test.title}`);
		}
		const attachments = Array.isArray(test.result.attachments) ? test.result.attachments : [];
		const missingEvidence = requiredPlaywrightEvidence.filter((name) => {
			const attachment = attachments.find(
				(candidate) => candidate?.name === name && typeof candidate.path === "string",
			);
			if (!attachment || !isWithinDirectory(attachmentRoot, attachment.path)) return true;
			try {
				return !attachmentIsPersisted(attachment.path);
			} catch {
				return true;
			}
		});
		if (missingEvidence.length > 0) {
			failures.push(
				`Playwright evidence missing for ${JSON.stringify(test.title)}: ${missingEvidence.join(", ")}`,
			);
		}
	}
	return failures;
}

export function assessGoContractLog(logText, exitCode, expectedContracts = requiredGoContracts) {
	const failures = [];
	let jsonEvents = 0;
	const states = new Map(
		expectedContracts.map((name) => [name, { ran: false, terminalAction: undefined }]),
	);
	for (const line of String(logText ?? "").split(/\r?\n/)) {
		let event;
		try {
			event = JSON.parse(line);
		} catch {
			continue;
		}
		if (!event || typeof event !== "object" || typeof event.Action !== "string") continue;
		jsonEvents += 1;
		const state = typeof event.Test === "string" ? states.get(event.Test) : undefined;
		if (!state) continue;
		if (event.Action === "run") state.ran = true;
		if (event.Action === "pass" || event.Action === "fail" || event.Action === "skip") {
			state.terminalAction = event.Action;
		}
	}
	if (jsonEvents === 0) {
		failures.push("Go contract log contains no readable go test JSON events");
	}
	for (const [name, state] of states) {
		if (!state.ran) {
			failures.push(`Go contract did not start: ${name}`);
			continue;
		}
		if (!state.terminalAction) {
			failures.push(`Go contract did not produce a terminal result: ${name}`);
		} else if (state.terminalAction === "skip") {
			failures.push(`Go contract was skipped: ${name}`);
		}
	}
	const terminalActions = [...states.values()].map((state) => state.terminalAction);
	if (exitCode === 0 && terminalActions.some((action) => action === "fail")) {
		failures.push("Go test exited zero despite a failed ChatUI contract result");
	}
	if (exitCode !== 0 && terminalActions.every((action) => action === "pass")) {
		failures.push("Go test failed outside the completed ChatUI contract tests");
	}
	return failures;
}

export function assessExecutionInfrastructure(steps) {
	const failures = steps
		.filter((step) => step.spawnError)
		.map((step) => `${step.label}: ${step.spawnError}`);
	const typecheckStep = steps.find((step) => step.id === "frontend-chatui-typecheck");
	if (typecheckStep && typecheckStep.exitCode !== 0 && !typecheckStep.spawnError) {
		failures.push("Frontend ChatUI typecheck failed; contract results are not trustworthy");
	}
	return failures;
}

export function buildRunReport({
	artifactDir,
	finishedAt,
	infrastructureFailures = [],
	options,
	repository,
	startedAt,
	steps,
}) {
	const infrastructureFailed = infrastructureFailures.length > 0;
	const passed = !infrastructureFailed && steps.every((step) => step.exitCode === 0);
	const outcome = infrastructureFailed
		? "infrastructure_failed"
		: passed
			? "passed"
			: options.capture
				? "captured_failures"
				: "failed";
	return {
		schemaVersion: 1,
		suite: "chatui-regression",
		outcome,
		passed,
		captureMode: options.capture,
		exitCode: infrastructureFailed ? 2 : options.capture ? 0 : passed ? 0 : 1,
		startedAt,
		finishedAt,
		durationMs: Math.max(0, Date.parse(finishedAt) - Date.parse(startedAt)),
		artifactDir,
		options: {
			headed: options.headed,
			...(options.grep ? { grep: options.grep } : {}),
		},
		repository,
		infrastructureFailures,
		steps,
	};
}

function markdownCell(value) {
	return String(value).replaceAll("|", "\\|").replaceAll("\n", " ");
}

function formatDuration(durationMs) {
	return `${(durationMs / 1_000).toFixed(1)}s`;
}

export function renderMarkdownSummary(report, availableArtifacts = {}) {
	const resultLabel =
		report.outcome === "passed"
			? "PASS"
			: report.outcome === "infrastructure_failed"
				? "INFRASTRUCTURE FAILURE"
			: report.outcome === "captured_failures"
				? "FAILURES CAPTURED"
				: "FAIL";
	const lines = [
		"# ChatUI regression run",
		"",
		`- Result: **${resultLabel}**`,
		`- Mode: ${report.captureMode ? "capture (non-blocking)" : "strict"}`,
		`- Started: ${report.startedAt}`,
		`- Finished: ${report.finishedAt}`,
		`- Duration: ${formatDuration(report.durationMs)}`,
		`- Commit: ${report.repository.commit || "unknown"}`,
		`- Branch: ${report.repository.branch || "unknown"}`,
		`- Worktree dirty at start: ${report.repository.dirty ? "yes" : "no"}`,
		"",
		"| Contract gate | Result | Duration | Log |",
		"| --- | --- | ---: | --- |",
	];
	for (const step of report.steps) {
		lines.push(
			`| ${markdownCell(step.label)} | ${step.exitCode === 0 ? "PASS" : "FAIL"} | ${formatDuration(step.durationMs)} | [log](${markdownCell(step.logPath)}) |`,
		);
	}
	if (report.infrastructureFailures.length > 0) {
		lines.push("", "## Infrastructure failures", "");
		lines.push(...report.infrastructureFailures.map((failure) => `- ${failure}`));
	}
	const artifacts = [];
	if (availableArtifacts.playwrightHtml) {
		artifacts.push("[Playwright HTML report](playwright/html/index.html)");
	}
	if (availableArtifacts.playwrightJson) {
		artifacts.push("[Playwright JSON results](playwright/results.json)");
	}
	lines.push("", "## Evidence", "");
	lines.push(
		artifacts.length > 0
			? artifacts.map((artifact) => `- ${artifact}`).join("\n")
			: "- Playwright produced no report artifacts; inspect the step log for setup failures.",
	);
	lines.push("", "The machine-readable aggregate is in [`summary.json`](summary.json).", "");
	return lines.join("\n");
}

function displayCommand(executable, args) {
	const quote = (value) => (/^[A-Za-z0-9_./:@=-]+$/.test(value) ? value : JSON.stringify(value));
	return [executable, ...args].map(quote).join(" ");
}

function closeStream(stream) {
	return new Promise((resolve) => stream.end(resolve));
}

async function runStep(step, runDir, aggregateLog) {
	const startedAt = new Date();
	const logPath = path.join("logs", `${step.id}.log`);
	const absoluteLogPath = path.join(runDir, logPath);
	const stepLog = createWriteStream(absoluteLogPath, { flags: "w" });
	const command = displayCommand(step.executable, step.args);
	const header = `\n== ${step.label} ==\n$ ${command}\n`;
	process.stdout.write(header);
	stepLog.write(header);
	aggregateLog.write(header);

	const result = await new Promise((resolve) => {
		let spawnError;
		const child = spawn(step.executable, step.args, {
			cwd: step.cwd,
			env: step.env,
			stdio: ["ignore", "pipe", "pipe"],
		});
		const tee = (destination, chunk) => {
			destination.write(chunk);
			stepLog.write(chunk);
			aggregateLog.write(chunk);
		};
		child.stdout.on("data", (chunk) => tee(process.stdout, chunk));
		child.stderr.on("data", (chunk) => tee(process.stderr, chunk));
		child.once("error", (error) => {
			spawnError = error;
			const message = `Unable to start ${step.label}: ${error.message}\n`;
			tee(process.stderr, Buffer.from(message));
		});
		child.once("close", (code, signal) => {
			resolve({
				exitCode: spawnError ? 1 : (code ?? 1),
				signal: signal ?? undefined,
				spawnError: spawnError?.message,
			});
		});
	});

	await closeStream(stepLog);
	const finishedAt = new Date();
	return {
		id: step.id,
		label: step.label,
		command,
		cwd: step.cwd,
		startedAt: startedAt.toISOString(),
		finishedAt: finishedAt.toISOString(),
		durationMs: finishedAt.getTime() - startedAt.getTime(),
		logPath,
		...result,
	};
}

function gitValue(args) {
	return new Promise((resolve) => {
		const child = spawn("git", args, { cwd: repositoryRoot, stdio: ["ignore", "pipe", "ignore"] });
		let output = "";
		child.stdout.on("data", (chunk) => {
			output += chunk;
		});
		child.once("error", () => resolve(""));
		child.once("close", (code) => resolve(code === 0 ? output.trim() : ""));
	});
}

async function repositoryState() {
	const [commit, branch, status] = await Promise.all([
		gitValue(["rev-parse", "HEAD"]),
		gitValue(["branch", "--show-current"]),
		gitValue(["status", "--short"]),
	]);
	return { commit, branch, dirty: Boolean(status) };
}

function npmExecutable() {
	return process.platform === "win32" ? "npm.cmd" : "npm";
}

export async function runChatUIRegressions(options, now = new Date()) {
	const startedAt = now.toISOString();
	const runDir = path.join(
		repositoryRoot,
		"e2e-artifacts",
		"chatui-regression",
		formatRunStamp(now),
	);
	const playwrightArtifactDir = path.join(runDir, "playwright");
	await mkdir(path.join(runDir, "logs"), { recursive: true });
	await mkdir(playwrightArtifactDir, { recursive: true });
	const repository = await repositoryState();

	const aggregateLog = createWriteStream(path.join(runDir, "run.log"), { flags: "w" });
	const commonEnvironment = {
		...process.env,
		AO_CHATUI_E2E_ARTIFACT_DIR: playwrightArtifactDir,
		AO_CHATUI_REGRESSION: "1",
	};
	const playwrightArgs = [
		path.join(frontendRoot, "node_modules", "playwright", "cli.js"),
		"test",
		"--config",
		path.join(frontendRoot, "playwright.chatui.config.ts"),
	];
	if (options.headed) playwrightArgs.push("--headed");
	if (options.grep) playwrightArgs.push("--grep", options.grep);
	let goContracts = [...requiredGoContracts];
	if (options.grep) {
		try {
			const matcher = new RegExp(options.grep, "i");
			goContracts = goContractDefinitions
				.filter((contract) => matcher.test(contract.playwrightMarker))
				.map((contract) => contract.name);
		} catch {
			// Playwright rejects malformed filters. Preserve every backend gate so
			// invalid input cannot accidentally weaken certification.
		}
	}

	const steps = [
		{
			id: "frontend-chatui-typecheck",
			label: "Frontend ChatUI typecheck",
			executable: npmExecutable(),
			args: ["run", "typecheck:chatui"],
			cwd: frontendRoot,
			env: commonEnvironment,
		},
		{
			id: "playwright-contracts",
			label: "Playwright ChatUI contracts",
			executable: process.execPath,
			args: playwrightArgs,
			cwd: frontendRoot,
			env: commonEnvironment,
		},
		...(goContracts.length > 0
			? [
					{
						id: "go-contracts",
						label: "Go opt-in ChatUI contracts",
						executable: "go",
						args: [
							"test",
							"-json",
							"-tags",
							"chatui_regression",
							"./internal/httpd/controllers",
							"./internal/session_manager",
							"./internal/service/chat",
							"-count=1",
							"-run",
							`^(?:${goContracts.join("|")})$`,
						],
						cwd: backendRoot,
						env: commonEnvironment,
					},
				]
			: []),
	];

	const results = [];
	try {
		for (const step of steps) {
			// Never short-circuit: a red browser contract must not hide whether the
			// daemon contracts stayed green, and vice versa.
			results.push(await runStep(step, runDir, aggregateLog));
		}
	} finally {
		await closeStream(aggregateLog);
	}

	const availableArtifacts = {
		playwrightHtml: existsSync(path.join(playwrightArtifactDir, "html", "index.html")),
		playwrightJson: existsSync(path.join(playwrightArtifactDir, "results.json")),
	};
	const htmlReportPath = path.join(playwrightArtifactDir, "html", "index.html");
	const jsonReportPath = path.join(playwrightArtifactDir, "results.json");
	let htmlText;
	let htmlReadError;
	let jsonText;
	let jsonReadError;
	if (availableArtifacts.playwrightHtml) {
		try {
			htmlText = await readFile(htmlReportPath, "utf8");
		} catch (error) {
			htmlReadError = error instanceof Error ? error.message : String(error);
		}
	}
	if (availableArtifacts.playwrightJson) {
		try {
			jsonText = await readFile(jsonReportPath, "utf8");
		} catch (error) {
			jsonReadError = error instanceof Error ? error.message : String(error);
		}
	}
	const infrastructureFailures = assessExecutionInfrastructure(results);
	infrastructureFailures.push(
		...assessPlaywrightArtifacts({
			attachmentRoot: playwrightArtifactDir,
			grep: options.grep,
			htmlPresent: availableArtifacts.playwrightHtml,
			htmlReadError,
			htmlText,
			jsonPresent: availableArtifacts.playwrightJson,
			jsonReadError,
			jsonText,
		}),
	);
	const goStep = results.find((step) => step.id === "go-contracts");
	if (goStep) {
		try {
			const goLog = await readFile(path.join(runDir, goStep.logPath), "utf8");
			infrastructureFailures.push(
				...assessGoContractLog(goLog, goStep.exitCode, goContracts),
			);
		} catch (error) {
			infrastructureFailures.push(
				`Go contract log could not be read: ${error instanceof Error ? error.message : String(error)}`,
			);
		}
	}
	const finishedAt = new Date().toISOString();
	const report = buildRunReport({
		artifactDir: runDir,
		finishedAt,
		infrastructureFailures,
		options,
		repository,
		startedAt,
		steps: results,
	});
	await Promise.all([
		writeFile(path.join(runDir, "summary.json"), `${JSON.stringify(report, null, 2)}\n`),
		writeFile(path.join(runDir, "summary.md"), renderMarkdownSummary(report, availableArtifacts)),
	]);

	const resultLabel = report.passed
		? "passed"
		: report.outcome === "infrastructure_failed"
			? "failed during infrastructure setup"
			: options.capture
				? "captured failures"
				: "failed";
	console.log(`\nChatUI regression run ${resultLabel}.`);
	console.log(`Artifacts: ${runDir}`);
	return report;
}

export async function main(argv) {
	let options;
	try {
		options = parseRunnerArgs(argv);
	} catch (error) {
		console.error(`chatui-regression: ${error instanceof Error ? error.message : String(error)}`);
		process.stderr.write(usage);
		return 2;
	}
	if (options.help) {
		process.stdout.write(usage);
		return 0;
	}
	try {
		const report = await runChatUIRegressions(options);
		return report.exitCode;
	} catch (error) {
		console.error(`chatui-regression: ${error instanceof Error ? error.message : String(error)}`);
		return 2;
	}
}

if (process.argv[1] && path.resolve(process.argv[1]) === scriptPath) {
	main(process.argv.slice(2)).then((exitCode) => {
		process.exitCode = exitCode;
	});
}
