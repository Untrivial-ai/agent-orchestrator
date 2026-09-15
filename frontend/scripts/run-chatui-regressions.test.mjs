// @vitest-environment node

import { describe, expect, it, vi } from "vitest";
import {
	assessGoContractLog,
	assessExecutionInfrastructure,
	assessPlaywrightArtifacts,
	buildRunReport,
	formatRunStamp,
	goContractDefinitions,
	main,
	parseRunnerArgs,
	requiredGoContracts,
	requiredPlaywrightContracts,
	renderMarkdownSummary,
} from "./run-chatui-regressions.mjs";

function strictGateError({ consoleErrors = [], pageErrors = [], unexpectedRequests = [] }) {
	const encodedState = Buffer.from(
		JSON.stringify({ consoleErrors, pageErrors, unexpectedRequests }),
	).toString("base64");
	return {
		message: `ChatUI strict gate rejected browser state. CHATUI_STRICT_GATE_STATE_B64:${encodedState}`,
	};
}

function playwrightPayload(contracts = [requiredPlaywrightContracts[0]]) {
	return {
		errors: [],
		stats: { expected: contracts.length, flaky: 0, skipped: 0, unexpected: 0 },
		suites: [
			{
				title: "chatui-contracts.spec.ts",
				specs: contracts.map((contract) => ({
					title: contract.marker,
					tests: [
						{
							expectedStatus: contract.expectedFailureMarker ? "failed" : "passed",
							results: [
								{
									status: contract.expectedFailureMarker ? "failed" : "passed",
									errors: contract.expectedFailureMarker
										? [strictGateError({ pageErrors: [contract.expectedFailureMarker] })]
										: [],
									attachments: [
										...["screenshot", "video", "trace"].map((name) => ({
											name,
											path: `/tmp/evidence/${contract.id}-${name}`,
										})),
										...(contract.expectedFailureMarker
											? [
													{
														name: "chatui-state-and-requests",
														contentType: "application/json",
														body: Buffer.from(
															JSON.stringify({
																consoleErrors: [],
																pageErrors: [contract.expectedFailureMarker],
																unexpectedRequests: [],
															}),
														).toString("base64"),
													},
												]
											: []),
									],
								},
							],
						},
					],
				})),
				suites: [],
			},
		],
	};
}

function artifactInput(payload = playwrightPayload()) {
	return {
		attachmentIsPersisted: () => true,
		attachmentRoot: "/tmp/evidence",
		grep: "MQA-07",
		htmlPresent: true,
		htmlText: "<!doctype html><html><body>report</body></html>",
		jsonPresent: true,
		jsonText: JSON.stringify(payload),
	};
}

const validPlaywrightArtifacts = artifactInput();

const step = (exitCode, id = "playwright-contracts") => ({
	id,
	label: id === "playwright-contracts" ? "Playwright ChatUI contracts" : "Go opt-in ChatUI contracts",
	exitCode,
	durationMs: 1_250,
	logPath: `logs/${id}.log`,
});

function reportFor({ capture = false, infrastructureFailures = [], steps = [step(0)] } = {}) {
	return buildRunReport({
		artifactDir: "/tmp/evidence",
		startedAt: "2026-08-25T10:00:00.000Z",
		finishedAt: "2026-08-25T10:00:03.000Z",
		options: { capture, grep: undefined, headed: false, help: false },
		repository: { branch: "codex/chatui", commit: "abc123", dirty: false },
		infrastructureFailures,
		steps,
	});
}

describe("parseRunnerArgs", () => {
	it("parses capture, headed, and both grep spellings", () => {
		expect(parseRunnerArgs(["--capture", "--headed", "--grep", "switch interface"])).toEqual({
			capture: true,
			grep: "switch interface",
			headed: true,
			help: false,
		});
		expect(parseRunnerArgs(["--grep=@P1"])).toMatchObject({ grep: "@P1" });
	});

	it("rejects missing grep values and unknown options", () => {
		expect(() => parseRunnerArgs(["--grep"])).toThrow("--grep requires a non-empty value");
		expect(() => parseRunnerArgs(["--grep", "--headed"])).toThrow(
			"--grep requires a non-empty value",
		);
		expect(() => parseRunnerArgs(["--grep="])).toThrow("--grep requires a non-empty value");
		expect(() => parseRunnerArgs(["--wat"])).toThrow("unknown option: --wat");
	});

	it("does not hide argument failures when capture mode is requested", async () => {
		const consoleError = vi.spyOn(console, "error").mockImplementation(() => {});
		const stderrWrite = vi.spyOn(process.stderr, "write").mockImplementation(() => true);
		try {
			await expect(main(["--capture", "--wat"])).resolves.toBe(2);
		} finally {
			consoleError.mockRestore();
			stderrWrite.mockRestore();
		}
	});
});

describe("focused contract lanes", () => {
	it("maps each tagged Go contract to its matching Playwright scenario", () => {
		expect(goContractDefinitions).toEqual([
			expect.objectContaining({
				name: requiredGoContracts[0],
				playwrightMarker: expect.stringContaining("MQA-07"),
			}),
			expect.objectContaining({
				name: requiredGoContracts[1],
				playwrightMarker: expect.stringContaining("MQA-06"),
			}),
			expect.objectContaining({
				name: requiredGoContracts[2],
				playwrightMarker: expect.stringContaining("MQA-08"),
			}),
		]);
	});
});

describe("run report", () => {
	it("returns nonzero when any strict contract gate fails", () => {
		const report = reportFor({ steps: [step(1), step(0, "go-contracts")] });
		expect(report).toMatchObject({ outcome: "failed", passed: false, exitCode: 1 });
	});

	it("records completed contract failures but returns zero in capture mode", () => {
		const report = reportFor({ capture: true, steps: [step(1)] });
		expect(report).toMatchObject({
			outcome: "captured_failures",
			passed: false,
			captureMode: true,
			exitCode: 0,
		});
	});

	it("keeps infrastructure failures nonzero in capture mode", () => {
		const report = reportFor({
			capture: true,
			infrastructureFailures: ["Playwright report missing"],
			steps: [step(1)],
		});
		expect(report).toMatchObject({
			outcome: "infrastructure_failed",
			passed: false,
			exitCode: 2,
		});
	});

	it("renders links to each gate log and available Playwright evidence", () => {
		const markdown = renderMarkdownSummary(reportFor(), {
			playwrightHtml: true,
			playwrightJson: true,
		});
		expect(markdown).toContain("**PASS**");
		expect(markdown).toContain("[log](logs/playwright-contracts.log)");
		expect(markdown).toContain("[Playwright HTML report](playwright/html/index.html)");
		expect(markdown).toContain("[Playwright JSON results](playwright/results.json)");
	});
});

describe("execution prerequisites", () => {
	it("treats local ChatUI typecheck failures as infrastructure in capture mode", () => {
		expect(
			assessExecutionInfrastructure([
				{
					id: "frontend-chatui-typecheck",
					label: "Frontend ChatUI typecheck",
					exitCode: 2,
				},
			]),
		).toContainEqual(expect.stringContaining("contract results are not trustworthy"));
	});

	it("reports spawn failures without duplicating a failed typecheck", () => {
		expect(
			assessExecutionInfrastructure([
				{
					id: "frontend-chatui-typecheck",
					label: "Frontend ChatUI typecheck",
					exitCode: 2,
					spawnError: "npm missing",
				},
			]),
		).toEqual(["Frontend ChatUI typecheck: npm missing"]);
	});
});

describe("assessPlaywrightArtifacts", () => {
	it("accepts both readable reports after at least one contract completed", () => {
		expect(assessPlaywrightArtifacts(validPlaywrightArtifacts)).toEqual([]);
	});

	it.each([
		[
			"missing HTML",
			{ ...validPlaywrightArtifacts, htmlPresent: false, htmlText: undefined },
			"configured HTML report",
		],
		[
			"missing JSON",
			{ ...validPlaywrightArtifacts, jsonPresent: false, jsonText: undefined },
			"configured JSON report",
		],
		[
			"malformed HTML",
			{ ...validPlaywrightArtifacts, htmlText: "not a report" },
			"HTML report is empty or malformed",
		],
		[
			"malformed JSON",
			{ ...validPlaywrightArtifacts, jsonText: "{" },
			"JSON report is malformed",
		],
		[
			"suite-level startup error",
			{
				...validPlaywrightArtifacts,
				jsonText: JSON.stringify({
					...playwrightPayload(),
					errors: [{ message: "webServer failed" }],
				}),
			},
			"suite-level infrastructure error",
		],
		[
			"zero matching tests",
			{
				...validPlaywrightArtifacts,
				jsonText: JSON.stringify({
					errors: [],
					stats: { expected: 0, flaky: 0, skipped: 0, unexpected: 0 },
					suites: [],
				}),
			},
			"completed no matching contract tests",
		],
	])("rejects %s", (_label, artifacts, expectedFailure) => {
		expect(assessPlaywrightArtifacts(artifacts)).toContainEqual(
			expect.stringContaining(expectedFailure),
		);
	});

	it("requires every registered browser contract in an unfiltered run", () => {
		const input = artifactInput(
			playwrightPayload([requiredPlaywrightContracts[0], requiredPlaywrightContracts.at(-1)]),
		);
		input.grep = undefined;
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining(requiredPlaywrightContracts[1].id),
		);

		const completeInput = artifactInput(playwrightPayload(requiredPlaywrightContracts));
		completeInput.grep = undefined;
		expect(assessPlaywrightArtifacts(completeInput)).toEqual([]);
	});

	it("rejects skipped contracts in both unfiltered and filtered runs", () => {
		const payload = playwrightPayload(requiredPlaywrightContracts);
		payload.stats.skipped = 1;
		const input = artifactInput(payload);
		input.grep = undefined;
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining("skipped 1 configured contract"),
		);
		input.grep = "MQA-10";
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining("skipped 1 configured contract"),
		);
	});

	it("constrains the expected page-error negative control to its synthetic marker", () => {
		const payload = playwrightPayload(requiredPlaywrightContracts);
		const canary = payload.suites[0].specs.find((spec) =>
			spec.title.includes("strict fixture rejects a deliberately injected page error"),
		);
		canary.tests[0].results[0].errors = [{ message: "unrelated navigation failure" }];
		const input = artifactInput(payload);
		input.grep = "MQA-12";
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining("did not fail exclusively"),
		);
	});

	it("rejects a negative control whose final gate state contains another page error", () => {
		const payload = playwrightPayload(requiredPlaywrightContracts);
		const canary = payload.suites[0].specs.find((spec) =>
			spec.title.includes("strict fixture rejects a deliberately injected page error"),
		);
		const result = canary.tests[0].results[0];
		result.errors = [
			strictGateError({
				pageErrors: ["TypeError: dimensions", requiredPlaywrightContracts.at(-1).expectedFailureMarker],
			}),
		];
		const input = artifactInput(payload);
		input.grep = "MQA-12";
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining("did not fail exclusively"),
		);
	});

	it("requires persisted screenshot, video, and trace evidence inside the run directory", () => {
		const payload = playwrightPayload();
		payload.suites[0].specs[0].tests[0].results[0].attachments = [
			{ name: "screenshot", path: "/tmp/evidence/screenshot.png" },
			{ name: "video", path: "/outside/video.webm" },
		];
		const failures = assessPlaywrightArtifacts(artifactInput(payload));
		expect(failures).toContainEqual(expect.stringContaining("video, trace"));
	});

	it("rejects evidence paths that do not resolve to a persisted file", () => {
		const input = artifactInput();
		input.attachmentIsPersisted = () => false;
		expect(assessPlaywrightArtifacts(input)).toContainEqual(
			expect.stringContaining("screenshot, video, trace"),
		);
	});
});

function goContractLog(actions) {
	return requiredGoContracts
		.flatMap((testName, index) => [
			JSON.stringify({ Action: "run", Package: `example/package-${index}`, Test: testName }),
			...(actions[index]
				? [JSON.stringify({ Action: actions[index], Package: `example/package-${index}`, Test: testName })]
				: []),
		])
		.join("\n");
}

describe("assessGoContractLog", () => {
	it("accepts complete known-red and all-green contract results", () => {
		expect(assessGoContractLog(goContractLog(requiredGoContracts.map(() => "fail")), 1)).toEqual([]);
		expect(assessGoContractLog(goContractLog(requiredGoContracts.map(() => "pass")), 0)).toEqual([]);
	});

	it("rejects zero-test and incomplete executions", () => {
		expect(assessGoContractLog("", 0)).toEqual(
			expect.arrayContaining([
				expect.stringContaining("no readable go test JSON events"),
				expect.stringContaining(requiredGoContracts[0]),
				expect.stringContaining(requiredGoContracts[1]),
			]),
		);
		expect(assessGoContractLog(goContractLog(["fail", undefined, "pass"]), 1)).toContainEqual(
			expect.stringContaining(`terminal result: ${requiredGoContracts[1]}`),
		);
	});

	it("rejects skipped contracts and package-level failures after green contracts", () => {
		expect(assessGoContractLog(goContractLog(["skip", "pass", "pass"]), 0)).toContainEqual(
			expect.stringContaining(`was skipped: ${requiredGoContracts[0]}`),
		);
		expect(assessGoContractLog(goContractLog(requiredGoContracts.map(() => "pass")), 1)).toContainEqual(
			expect.stringContaining("failed outside the completed ChatUI contract tests"),
		);
	});

	it("validates only the backend contracts selected by a focused filter", () => {
		const selected = [requiredGoContracts[1]];
		const log = [
			JSON.stringify({ Action: "run", Package: "example/package", Test: selected[0] }),
			JSON.stringify({ Action: "pass", Package: "example/package", Test: selected[0] }),
		].join("\n");
		expect(assessGoContractLog(log, 0, selected)).toEqual([]);
	});
});

describe("formatRunStamp", () => {
	it("produces a filesystem-safe, millisecond-specific UTC directory", () => {
		expect(formatRunStamp(new Date("2026-08-25T10:11:12.345Z"))).toBe(
			"2026-08-25T10-11-12-345Z",
		);
	});
});
