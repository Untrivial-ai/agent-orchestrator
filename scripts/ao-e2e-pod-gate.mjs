#!/usr/bin/env node
// Stable-release e2e pod gate runner.
//
//   node scripts/ao-e2e-pod-gate.mjs --repo <owner/repo> --sha <sha> --tag <release-tag> --suite T0
//   - Downloads the release's Linux .deb on the runner (public asset, no token),
//     spins an ephemeral Daytona pod (env DAYTONA_API_KEY, used only to CREATE
//     the pod — never passed into it), uploads the build + harness, and runs the
//     real-app T0 Playwright suite inside the pod. The pod holds no secret and
//     needs no egress.
//   - Prints a final line: AO_VERDICT {"passed":true|false,...}
//   - Exits 0 on green, non-zero on red.
//
// Invoked by the private release conductor, which reflects the outcome into an
// `ao-stable-gate` commit status. The verdict->exit-code->status contract lives
// in the pure, tested deriveGateOutcome.

import { fileURLToPath } from "node:url";
import { readFile } from "node:fs/promises";
import { appendFileSync, mkdirSync, writeFileSync } from "node:fs";
import { join, dirname } from "node:path";

export function parseArgs(argv) {
	const args = {};
	for (let i = 0; i < argv.length; i++) {
		const token = argv[i];
		if (token.startsWith("--")) {
			const key = token.slice(2);
			const next = argv[i + 1];
			if (next !== undefined && !next.startsWith("--")) {
				args[key] = next;
				i++;
			} else {
				args[key] = true;
			}
		}
	}
	return args;
}

/**
 * Pure verdict logic for the stable e2e gate.
 *
 * The key distinction: a RED (failure) verdict is reserved for the ONE
 * actionable case — the smoke suite ran and the app under test failed it.
 * Everything else that goes wrong is INFRA/setup (no key, Daytona unavailable,
 * download/exec error, timeout) and maps to a NEUTRAL check, not red, so an
 * infrastructure hiccup never makes an otherwise-good release look failed.
 *
 * The infra-vs-app split falls out of throw-vs-return in the runner: infra
 * problems throw (→ ranOk=false here); an app smoke failure returns
 * testsPassed=false.
 *
 * Rule table (first match wins):
 *   ranOk === false      -> infra      / neutral / exit 0  (could not run)
 *   timedOut === true    -> infra      / neutral / exit 0  (treated as infra/flake)
 *   testsPassed !== true -> app_failed / failure / exit 1  (the only RED)
 *   testsPassed === true -> passed     / success / exit 0
 *
 * exitCode: app_failed=1 (so a future blocking gate blocks); infra=0 (a Daytona
 * outage must never block a good release); passed=0.
 *
 * @param {object} facts
 * @param {boolean} facts.ranOk       runner produced a verdict (false = threw = infra)
 * @param {boolean} facts.testsPassed the T0 suite reported all green
 * @param {boolean} [facts.timedOut]  the run hit its wall-clock timeout
 * @param {string}  [facts.artifactsUrl] link to logs/traces/screenshots
 * @returns {{classification:"passed"|"app_failed"|"infra", conclusion:"success"|"failure"|"neutral", exitCode:0|1, description:string, artifactsUrl:string|null}}
 */
export function deriveGateOutcome({ ranOk, testsPassed, timedOut = false, artifactsUrl = null } = {}) {
	const link = artifactsUrl || null;
	const infra = (description) => ({
		classification: "infra",
		conclusion: "neutral",
		exitCode: 0,
		description,
		artifactsUrl: link,
	});

	if (ranOk === false) {
		return infra("gate could not run (infrastructure/setup) — not a release-build result");
	}
	if (timedOut === true) {
		return infra("gate timed out (infrastructure) — not a release-build result");
	}
	if (testsPassed !== true) {
		return {
			classification: "app_failed",
			conclusion: "failure",
			exitCode: 1,
			description: "T0 pod smoke failed — the app under test failed the suite",
			artifactsUrl: link,
		};
	}
	return {
		classification: "passed",
		conclusion: "success",
		exitCode: 0,
		description: "T0 pod smoke passed",
		artifactsUrl: link,
	};
}

/**
 * Parse the pod's final AO_VERDICT line into { passed, infra }.
 *
 * The pod (test/e2e-pod/boot-real.sh) distinguishes setup/toolchain failures
 * (apt/npm/dpkg) from the real app-test result:
 *   {"passed":true}               -> app smoke passed
 *   {"passed":false}              -> app smoke failed (a real app failure)
 *   {"passed":false,"infra":true} -> setup problem (must NOT be a red app failure)
 * A missing or unparseable verdict means the pod never produced a result at all,
 * which is itself infra (the suite could not run), never an app failure.
 *
 * The pod may emit more than one AO_VERDICT line (e.g. a retry, or a later
 * stage that overrides an earlier optimistic result). The FINAL line is the
 * authoritative one — the pod's last word on the run — so we select the LAST
 * match, never the first, otherwise a stale passing line could mask a later
 * failure.
 *
 * @param {string} out combined pod stdout/stderr
 * @returns {{passed:boolean, infra:boolean}}
 */
export function parsePodVerdict(out) {
	const matches = [...(out ?? "").matchAll(/^.*AO_VERDICT (\{.*\})\s*$/gm)];
	if (matches.length === 0) return { passed: false, infra: true };
	const last = matches[matches.length - 1];
	let v;
	try {
		v = JSON.parse(last[1]);
	} catch {
		return { passed: false, infra: true };
	}
	return { passed: v.passed === true, infra: v.infra === true };
}

/**
 * Validate the required inputs before doing any work. Throws a single message
 * naming every missing input, so a user-visible boundary error is actionable.
 * @param {{apiKey?:string, repo?:string, tag?:string}} args
 */
export function validateGateArgs({ apiKey, repo, tag } = {}) {
	const missing = [];
	if (!apiKey) missing.push("DAYTONA_API_KEY (env)");
	if (!repo) missing.push("--repo");
	if (!tag) missing.push("--tag");
	if (missing.length > 0) {
		throw new Error(`ao-e2e-pod-gate: missing required input(s): ${missing.join(", ")}`);
	}
}

// GitHub release Linux .deb URL for a tag. Forge publishes to v<version>; the
// asset is agent-orchestrator_<version>_amd64.deb (version = tag without "v").
export function releaseDebUrl(repo, tag) {
	const version = String(tag).replace(/^v/, "");
	return `https://github.com/${repo}/releases/download/${tag}/agent-orchestrator_${version}_amd64.deb`;
}

// runPodSuite spins one ephemeral Daytona pod, installs the release build, and
// runs the real-app T0 suite in it, returning the observed facts. The pod holds
// NO secret and fetches NO application code: the .deb is fetched on the runner
// (public asset) and uploaded in; DAYTONA_API_KEY is used only to create the pod,
// never passed into it. (boot-real.sh may still fetch its toolchain from the OS/
// npm registries at boot unless the sandbox image has it baked — see that
// script's header.) The SDK is dynamically imported so the pure-function tests
// (deriveGateOutcome/parseArgs) load this module without needing @daytona/sdk.
async function runPodSuite({ repo, tag, apiKey, suite, artifactsDir, timeoutMs = 20 * 60_000 }) {
	validateGateArgs({ apiKey, repo, tag });

	const podDir = join(dirname(fileURLToPath(import.meta.url)), "..", "test", "e2e-pod");
	// Captured pod output. Written to pod.log on EVERY exit path (success, app
	// failure, infra, exception) by the finally below, so the uploaded artifact
	// is never silently absent — a missing log would hide why a run went neutral.
	let podLog = "";
	let sandbox;
	const startedAt = Date.now();
	try {
		const debUrl = releaseDebUrl(repo, tag);
		const res = await fetch(debUrl);
		if (!res.ok) throw new Error(`download ${debUrl} -> HTTP ${res.status}`);
		const deb = Buffer.from(await res.arrayBuffer());

		const { Daytona } = await import("@daytona/sdk");
		const daytona = new Daytona({ apiKey });
		sandbox = await daytona.create({ snapshot: process.env.AO_DAYTONA_SNAPSHOT || "daytona-small" });
		await sandbox.fs.uploadFile(deb, "/home/daytona/app.deb");
		for (const f of ["playwright.electron.config.ts", "real-app.spec.ts", "boot-real.sh"]) {
			await sandbox.fs.uploadFile(await readFile(join(podDir, f)), `/home/daytona/${f}`);
		}
		const suiteEnv = suite ? `AO_SUITE=${suite} ` : "";
		const r = await sandbox.process.executeCommand(
			`AO_DEB_PATH=/home/daytona/app.deb ${suiteEnv}bash /home/daytona/boot-real.sh`,
			"/home/daytona",
			undefined,
			Math.floor(timeoutMs / 1000),
		);
		podLog = r.result ?? "";
		process.stdout.write(podLog);
		// Pull the Playwright results tarball if the pod produced one. Best-effort
		// and must run BEFORE teardown in the finally. The pod log itself is always
		// persisted (finally), so a missing tarball never loses the run record.
		if (artifactsDir) {
			try {
				const tgz = await sandbox.fs.downloadFile("/home/daytona/artifacts.tgz");
				if (tgz) {
					mkdirSync(artifactsDir, { recursive: true });
					writeFileSync(join(artifactsDir, "artifacts.tgz"), Buffer.isBuffer(tgz) ? tgz : Buffer.from(tgz));
				}
			} catch {
				/* no results tarball produced; the pod log is still saved below */
			}
		}
		const { passed, infra } = parsePodVerdict(podLog);
		return { passed, infra, timedOut: Date.now() - startedAt >= timeoutMs };
	} catch (err) {
		// Record WHY the log is short so the always-uploaded pod.log is never a
		// silent empty file on an infra/exception path.
		if (!podLog) podLog = `ao-e2e-pod-gate: run failed before the pod produced output: ${err.message}\n`;
		throw err;
	} finally {
		// ALWAYS write the pod log — on green, red, infra, or exception — so the
		// artifact upload has something to attach on every run.
		if (artifactsDir) {
			try {
				mkdirSync(artifactsDir, { recursive: true });
				writeFileSync(join(artifactsDir, "pod.log"), podLog || "(no pod output captured)\n");
			} catch {
				/* artifact capture is best-effort — never fail the gate on it */
			}
		}
		if (sandbox) await sandbox.delete().catch(() => {});
	}
}

async function main(argv) {
	const args = parseArgs(argv.slice(2));
	console.log("ao-e2e-pod-gate");
	console.log(`  repo=${args.repo ?? "(unset)"} tag=${args.tag ?? "(unset)"} suite=${args.suite ?? "T0"}`);
	console.log(`  DAYTONA_API_KEY: ${process.env.DAYTONA_API_KEY ? "present" : "absent"}`);

	// Where downloaded pod artifacts land (the workflow uploads this dir); the
	// check links to the workflow run, where that upload is attached.
	const artifactsDir =
		process.env.AO_ARTIFACTS_DIR || join(dirname(fileURLToPath(import.meta.url)), "..", "e2e-artifacts");
	const runUrl =
		process.env.GITHUB_SERVER_URL && process.env.GITHUB_REPOSITORY && process.env.GITHUB_RUN_ID
			? `${process.env.GITHUB_SERVER_URL}/${process.env.GITHUB_REPOSITORY}/actions/runs/${process.env.GITHUB_RUN_ID}`
			: null;

	let outcome;
	try {
		const res = await runPodSuite({
			repo: args.repo,
			tag: args.tag,
			apiKey: process.env.DAYTONA_API_KEY,
			suite: args.suite,
			artifactsDir,
		});
		// A pod-side setup/toolchain failure (res.infra) is NOT a release result:
		// map it to ranOk:false so it becomes a NEUTRAL infra outcome, not red.
		outcome = deriveGateOutcome({
			ranOk: !res.infra,
			testsPassed: res.passed,
			timedOut: res.timedOut,
			artifactsUrl: runUrl,
		});
	} catch (err) {
		console.error(`ao-e2e-pod-gate: run failed: ${err.message}`);
		// Guarantee a pod.log exists even when the run threw before the pod could
		// produce one (e.g. missing DAYTONA_API_KEY, or a throw before runPodSuite's
		// own finally): the artifact upload must never find an empty dir silently.
		try {
			mkdirSync(artifactsDir, { recursive: true });
			writeFileSync(join(artifactsDir, "pod.log"), `ao-e2e-pod-gate: run failed (infra/setup): ${err.message}\n`, {
				flag: "wx",
			});
		} catch {
			/* pod.log already written by runPodSuite, or best-effort — ignore */
		}
		outcome = deriveGateOutcome({ ranOk: false, artifactsUrl: runUrl });
	}

	const verdict = {
		classification: outcome.classification,
		conclusion: outcome.conclusion,
		passed: outcome.classification === "passed",
		summary: outcome.description,
		...(outcome.artifactsUrl ? { artifactsUrl: outcome.artifactsUrl } : {}),
	};
	console.log(`AO_VERDICT ${JSON.stringify(verdict)}`);
	// Hand the classification to the CI job deterministically so it maps to a
	// check-run conclusion (success/failure/neutral) without re-parsing stdout.
	if (process.env.GITHUB_OUTPUT) {
		try {
			appendFileSync(
				process.env.GITHUB_OUTPUT,
				`classification=${outcome.classification}\nconclusion=${outcome.conclusion}\n`,
			);
		} catch {
			/* best-effort; the check step falls back to a neutral conclusion */
		}
	}
	return outcome.exitCode;
}

// Only run the CLI when invoked directly, not when imported by the test.
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
	main(process.argv).then((code) => process.exit(code));
}
