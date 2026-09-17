import { describe, expect, it } from "vitest";
import { PostHog } from "posthog-js/dist/module.full.no-external";
import {
	buildPostHogConfig,
	buildTelemetryContext,
	isDeniedEvent,
	postHogEventName,
	reserveCapture,
	reserveDailyActiveCapture,
	reserveRouteViewCapture,
	routeSurface,
	sanitizePostHogEvent,
	sanitizeReplayRequestName,
	sanitizeRendererExceptionProperties,
	sanitizeRendererProperties,
	startDailyActiveHeartbeat,
	withTelemetryContext,
	releaseChannelFrom,
	versionChannelFrom,
} from "./telemetry";
import { ORCHESTRATOR_SPAWN_SOURCES } from "./orchestrator-spawn-sources";

function memoryStorage(initial: Record<string, string> = {}) {
	const values = new Map(Object.entries(initial));
	return {
		getItem: (key: string) => values.get(key) ?? null,
		setItem: (key: string, value: string) => {
			values.set(key, value);
		},
	};
}

describe("telemetry sanitizers", () => {
	it("isolates anonymous AO installation identity from persisted PostHog person state", () => {
		const config = buildPostHogConfig("ins_stable-install-id");

		expect(config.persistence).toBe("memory");
		expect(config.person_profiles).toBe("never");
		expect(config.autocapture).toBe(false);
		expect(config.capture_performance).toBe(false);
		expect(config.disable_session_recording).toBe(true);
		expect(config.bootstrap).toEqual({
			distinctID: "ins_stable-install-id",
			isIdentifiedID: false,
		});
	});

	// The daemon enforces the same list on its own sink, but renderer events go
	// straight to PostHog, so the policy has to be honoured here too or the kill
	// switch only covers half the producers.
	it("honours the supervisor deny list, by either internal or exported name", () => {
		expect(isDeniedEvent("ao.app.active", ["ao.v2.app.active"])).toBe(true);
		expect(isDeniedEvent("ao.app.active", ["ao.app.active"])).toBe(true);
		expect(isDeniedEvent("ao.app.active", ["  AO.V2.APP.ACTIVE  "])).toBe(true);
		expect(isDeniedEvent("ao.renderer.route_viewed", ["ao.renderer.*"])).toBe(true);
		expect(isDeniedEvent("$exception", ["$exception"])).toBe(true);

		expect(isDeniedEvent("ao.session.spawned", ["ao.renderer.*"])).toBe(false);
		expect(isDeniedEvent("ao.app.active", [])).toBe(false);
		// A bare "*" must not silence everything by accident.
		expect(isDeniedEvent("ao.app.active", ["", "  ", "*"])).toBe(false);
	});

	it("allowlists only counts and the fixed agent-id list on agent inventory", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.agents_available", {
			installed_count: 2,
			authorized_count: 1,
			supported_count: 23,
			authorized_agents: "claude-code,codex",
			// Must be dropped: never part of the contract, and a future caller could
			// pass a path or a label by mistake.
			binary_path: "/Users/someone/.local/bin/codex",
			label: "Codex (someone's machine)",
		});
		expect(safe).toEqual({
			installed_count: 2,
			authorized_count: 1,
			supported_count: 23,
			authorized_agents: "claude-code,codex",
		});
	});

	it("allowlists only enum-like fields on update events", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.update_failed", {
			to_version: "0.11.2",
			error_category: "network",
			phase: "download",
			trigger: "automatic",
			// Must be dropped: raw updater text can carry feed URLs and local paths.
			message: "EACCES /Users/someone/Library/Caches/ao-updater",
			stack: "at Object.<anonymous>",
		});
		expect(safe).toEqual({
			to_version: "0.11.2",
			error_category: "network",
			phase: "download",
			trigger: "automatic",
		});

		// An unrecognized phase is not passed through as-is.
		const bogus = await sanitizeRendererProperties("ao.renderer.update_failed", { phase: "sideload" });
		expect(bogus.phase).toBeUndefined();
	});

	it("reports a support submission with only the destination and outcome", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.support_submitted", {
			destination: "discord",
			outcome: "succeeded",
			// Everything the dialog collects is passed in deliberately: the user's own
			// bug report, and the diagnostics block that carries machine state.
			summary: "crashes when I open my repo",
			details: "stack trace from /Users/me/work/secret-client",
			diagnostics: { platform: "darwin", appVersion: "0.11.2" },
		});
		expect(safe).toEqual({ destination: "discord", outcome: "succeeded" });
	});

	it("drops an unrecognised support destination rather than forwarding it", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.support_submitted", {
			destination: "mailto:founder@example.com",
			outcome: "failed",
		});
		expect(safe).toEqual({ outcome: "failed" });
	});

	it("reports the support open with no properties at all", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.support_opened", {
			summary: "everything is broken",
		});
		expect(safe).toEqual({});
	});

	it("reports the session auto-review switch with only its direction", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.review_auto_review_toggled", {
			enabled: true,
			// Must be dropped: the session's id and the title the user typed.
			session_id: "sess-1",
			session_name: "fix the login bug",
		});
		expect(safe).toEqual({ enabled: true });
	});

	it("reports the mobile connect open with only the bridge state", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.mobile_connect_opened", {
			bridge_enabled: true,
			// Everything the QR encodes is in scope for leaking here, so it is all
			// passed in deliberately and must all be dropped.
			host: "192.168.1.20",
			port: 3011,
			password: "hunter2secret",
		});
		expect(safe).toEqual({ bridge_enabled: true });
	});

	it("reports the bridge toggle without the pairing payload", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.mobile_bridge_toggled", {
			enabled: true,
			outcome: "succeeded",
			password: "hunter2secret",
			host: "192.168.1.20",
		});
		expect(safe).toEqual({ enabled: true, outcome: "succeeded" });
	});

	it("drops an unrecognised toggle outcome rather than forwarding it", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.mobile_bridge_toggled", {
			enabled: false,
			outcome: "kind-of-worked",
		});
		expect(safe).toEqual({ enabled: false });
	});

	it("disables every billable PostHog product AO does not consume", () => {
		const config = buildPostHogConfig("ins_stable-install-id");

		// Replay is billed per recording, so it escapes the per-name rate limits
		// in this module entirely. Disabling it client-side means the project-side
		// toggle cannot switch it back on for shipped builds.
		expect(config.disable_session_recording).toBe(true);
		// AO reads no flags and ships no surveys, and /flags requests are billed
		// per request, so any request here is cost for data nothing reads.
		expect(config.advanced_disable_flags).toBe(true);
		expect(config.disable_surveys).toBe(true);
		// Already-disabled capture paths that would otherwise autocapture volume.
		expect(config.autocapture).toBe(false);
		expect(config.capture_pageview).toBe(false);
		expect(config.capture_exceptions).toBe(false);
		expect(config.capture_performance).toBe(false);
	});

	it("emits the stable AO installation id without creating a person profile", () => {
		const client = new PostHog();
		client.init("phc_test", {
			...buildPostHogConfig("ins_stable-install-id"),
			advanced_disable_decide: true,
			advanced_disable_flags: true,
			disable_session_recording: true,
			disable_surveys: true,
		});
		try {
			const captured = client.capture("ao.app.active", { channel: "renderer" });

			expect(captured?.properties).toMatchObject({
				distinct_id: "ins_stable-install-id",
				$device_id: "ins_stable-install-id",
				$is_identified: false,
				$process_person_profile: false,
			});
		} finally {
			const queue = client._requestQueue as unknown as
				{ _queue: unknown[]; _clearFlushTimeout: () => void } | undefined;
			if (queue) {
				queue._queue.length = 0;
				queue._clearFlushTimeout();
			}
		}
	});

	it("builds stable AO version context for PostHog events", () => {
		expect(buildTelemetryContext(" 1.2.3-nightly.20260707 ", "linux")).toMatchObject({
			app_version: "1.2.3-nightly.20260707",
			ao_version: "1.2.3-nightly.20260707",
			platform: "linux",
			telemetry_schema_version: 2,
		});
		expect(buildTelemetryContext("", "darwin")).toMatchObject({
			app_version: "unknown",
			ao_version: "unknown",
			platform: "darwin",
			telemetry_schema_version: 2,
		});
	});

	it("uses v2 PostHog event names for streams that have noisy legacy producers", () => {
		expect(postHogEventName("ao.app.active")).toBe("ao.v2.app.active");
		expect(postHogEventName("ao.renderer.route_viewed")).toBe("ao.v2.renderer.route_viewed");
		expect(postHogEventName("ao.renderer.api_error")).toBe("ao.v2.renderer.api_error");
		expect(postHogEventName("ao.session.spawned")).toBe("ao.session.spawned");
	});

	it("forces renderer events to stay anonymous in PostHog", () => {
		expect(withTelemetryContext({ $process_person_profile: true })).toMatchObject({
			$process_person_profile: false,
		});
	});

	it("categorizes routes without exporting raw paths", () => {
		expect(routeSurface("/")).toBe("home");
		expect(routeSurface("/settings")).toBe("global_settings");
		expect(routeSurface("/projects/demo")).toBe("project_board");
		expect(routeSurface("/projects/demo/settings")).toBe("project_settings");
		expect(routeSurface("/projects/demo/sessions/demo-1")).toBe("session_detail");
	});

	it("hashes renderer ids and drops raw route identifiers", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.project_removed", { project_id: "demo-project" });
		expect(props).toHaveProperty("project_id_hash");
		expect(props).not.toHaveProperty("project_id");

		const routeProps = await sanitizeRendererProperties("ao.renderer.route_viewed", {
			surface: "project_board",
			pathname: "/projects/demo",
			search: "?token=secret",
		});
		expect(routeProps).toEqual({ surface: "project_board" });
	});

	it("keeps only the renderer channel on app active events", async () => {
		const props = await sanitizeRendererProperties("ao.app.active", {
			channel: "renderer",
			project_id: "demo-project",
			ip: "203.0.113.10",
			country: "US",
		});

		expect(props).toEqual({ channel: "renderer" });
		expect(await sanitizeRendererProperties("ao.app.active", { channel: "cli" })).toEqual({});
	});

	it("keeps only bounded session-state fallback diagnostics", async () => {
		expect(
			await sanitizeRendererProperties("ao.renderer.session_state_unknown", {
				field: "status",
				reason: "unrecognized",
				raw_value: "future-backend-state",
				session_id: "private-session-id",
			}),
		).toEqual({ field: "status", reason: "unrecognized" });
	});

	it("strips exception details down to coarse metadata", async () => {
		const props = await sanitizeRendererExceptionProperties(new TypeError("local path /tmp/private"), {
			source: "window-error",
			operation: "project_add",
			unhandled: true,
			project_id: "demo-project",
			component_stack: "App > Shell",
		});
		expect(props).toMatchObject({
			error_name: "TypeError",
			source: "window-error",
			operation: "project_add",
			unhandled: true,
		});
		expect(props).toHaveProperty("project_id_hash");
		expect(props).not.toHaveProperty("project_id");
		expect(props).not.toHaveProperty("component_stack");
	});

	it("sanitizes exception step context", async () => {
		const props = await sanitizeRendererExceptionProperties(new Error("boom"), {
			source: "orchestrator-open",
			operation: "open_orchestrator",
			surface: "session_detail",
			project_id: "demo-project",
		});
		expect(props).toMatchObject({
			source: "orchestrator-open",
			operation: "open_orchestrator",
			surface: "session_detail",
		});
		expect(props).toHaveProperty("project_id_hash");
	});

	it("redacts local urls and filesystem paths from outgoing PostHog payloads", () => {
		const event = sanitizePostHogEvent({
			event: "$exception",
			properties: {
				$current_url: "app://renderer/index.html?token=secret",
				$initial_current_url: "file:///Users/alice/private/index.html",
				$referrer: "https://app.localhost:5173/private?token=secret",
				message:
					"failed to fetch http://localhost:3037/api/v1/projects?token=secret from app://renderer/index.html?token=secret and open /Users/alice/reverb/file.txt",
				$exception_list: [
					{
						type: "TypeError",
						value:
							"failed to load /home/alice/.config/reverb/settings.json via http://127.0.0.1:3037/api/v1/projects?token=secret",
						stacktrace: {
							frames: [
								{ filename: "file:///Users/alice/reverb/dist/main.js" },
								{ filename: "http://[::1]:3037/api/v1/projects?token=secret" },
							],
						},
					},
				],
			},
		});
		const props = event.properties as Record<string, unknown>;
		expect(props.$current_url).toBe("[redacted-local-url]");
		expect(props.$initial_current_url).toBe("[redacted-local-url]");
		expect(props.$referrer).toBe("[redacted-local-url]");
		expect(props.message).toBe(
			"failed to fetch [redacted-local-url] from [redacted-local-url] and open [redacted-local-path]",
		);
		const exceptionList = props.$exception_list as Array<Record<string, unknown>>;
		expect(exceptionList[0].value).toBe("failed to load [redacted-local-path] via [redacted-local-url]");
		expect((exceptionList[0].stacktrace as { frames: Array<{ filename: string }> }).frames[0].filename).toBe(
			"[redacted-local-url]",
		);
		expect((exceptionList[0].stacktrace as { frames: Array<{ filename: string }> }).frames[1].filename).toBe(
			"[redacted-local-url]",
		);
	});

	it("redacts replay request names before they leave the renderer", () => {
		expect(sanitizeReplayRequestName("file:///Users/alice/private/index.html?token=secret")).toBe(
			"[redacted-local-url]",
		);
		expect(sanitizeReplayRequestName("http://[::1]:3037/api/v1/projects?token=secret")).toBe("[redacted-local-url]");
		expect(sanitizeReplayRequestName("https://api.example.com/endpoint?token=secret")).toBe(
			"https://api.example.com/endpoint",
		);
	});

	it("hashes project ids and drops everything else on CTA triads", async () => {
		const triads = [
			"ao.renderer.task_create_requested",
			"ao.renderer.task_create_succeeded",
			"ao.renderer.task_create_failed",
			"ao.renderer.session_kill_requested",
			"ao.renderer.session_kill_succeeded",
			"ao.renderer.session_kill_failed",
			"ao.renderer.settings_save_requested",
			"ao.renderer.settings_save_succeeded",
			"ao.renderer.settings_save_failed",
		];
		for (const event of triads) {
			const props = await sanitizeRendererProperties(event, {
				project_id: "demo-project",
				title: "raw user text",
				error: "raw error with /Users/alice/path",
			});
			expect(Object.keys(props)).toEqual(["project_id_hash"]);
		}
	});

	it("keeps only the source enum on orchestrator_spawn events", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.orchestrator_spawn_requested", {
			project_id: "demo-project",
			source: "board",
		});
		expect(Object.keys(props).sort()).toEqual(["project_id_hash", "source"]);
		expect(props.source).toBe("board");

		const badSource = await sanitizeRendererProperties("ao.renderer.orchestrator_spawn_failed", {
			project_id: "demo-project",
			source: "/Users/alice/private",
		});
		expect(badSource).not.toHaveProperty("source");
	});

	it("keeps every whitelisted spawn source (the shared ORCHESTRATOR_SPAWN_SOURCES list)", async () => {
		for (const source of ORCHESTRATOR_SPAWN_SOURCES) {
			const props = await sanitizeRendererProperties("ao.renderer.orchestrator_spawn_succeeded", {
				project_id: "demo-project",
				source,
			});
			expect(Object.keys(props).sort()).toEqual(["project_id_hash", "source"]);
			expect(props.source).toBe(source);
		}
	});

	it("keeps only enum values on notification events", async () => {
		expect(await sanitizeRendererProperties("ao.renderer.notification_opened", { target: "pr" })).toEqual({
			target: "pr",
		});
		expect(await sanitizeRendererProperties("ao.renderer.notification_opened", { target: "http://x" })).toEqual({});
		expect(await sanitizeRendererProperties("ao.renderer.notification_mark_read_requested", { scope: "all" })).toEqual({
			scope: "all",
		});
		expect(await sanitizeRendererProperties("ao.renderer.notification_mark_read_succeeded", { scope: "all" })).toEqual({
			scope: "all",
		});
		expect(await sanitizeRendererProperties("ao.renderer.notification_mark_read_failed", { scope: "all" })).toEqual({
			scope: "all",
		});
		expect(
			await sanitizeRendererProperties("ao.renderer.notification_mark_read_requested", { scope: "everything" }),
		).toEqual({});
	});

	it("keeps a hashed project id, shared editor id, and target kind on open_in_editor_requested", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.open_in_editor_requested", {
			project_id: "demo-project",
			editor_id: "vscode",
			target_kind: "editor",
		});
		expect(Object.keys(props).sort()).toEqual(["editor_id", "project_id_hash", "target_kind"]);
		expect(props.editor_id).toBe("vscode");
		expect(props.target_kind).toBe("editor");

		const bogus = await sanitizeRendererProperties("ao.renderer.open_in_editor_requested", {
			project_id: "demo-project",
			// Not a real editor id — must not be forwarded as-is.
			editor_id: "/Users/alice/bin/notepad",
			target_kind: "everything",
		});
		expect(bogus).not.toHaveProperty("editor_id");
		expect(bogus).not.toHaveProperty("target_kind");
	});

	it("keeps the editor id and target kind on open_in_editor_succeeded", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.open_in_editor_succeeded", {
			project_id: "demo-project",
			editor_id: "cursor",
			target_kind: "editor",
		});
		expect(Object.keys(props).sort()).toEqual(["editor_id", "project_id_hash", "target_kind"]);
		expect(props.editor_id).toBe("cursor");
		expect(props.target_kind).toBe("editor");
	});

	it("reports open_in_editor_failed with only the hashed project id", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.open_in_editor_failed", {
			project_id: "demo-project",
			// Never part of the contract; must not leak through.
			error: "EDITOR_LAUNCH_FAILED: Could not launch VS Code",
		});
		expect(Object.keys(props)).toEqual(["project_id_hash"]);
	});

	it("whitelists coarse daemon failure fields and drops messages", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.daemon_failure", {
			daemon_state: "error",
			code: "spawn_failed",
			exit_code: 1,
			signal: "SIGKILL",
			message: "spawn /Users/alice/ao failed",
		});
		expect(props).toEqual({ daemon_state: "error", code: "spawn_failed", exit_code: 1, signal: "SIGKILL" });
	});

	it("whitelists normalized api_error fields", async () => {
		const props = await sanitizeRendererProperties("ao.renderer.api_error", {
			operation: "GET /api/v1/projects/:id",
			error_category: "http_5xx",
			status: 500,
			body: "raw response body",
		});
		expect(props).toEqual({ operation: "GET /api/v1/projects/:id", error_category: "http_5xx", status: 500 });
	});

	it("keeps only the reason enum on terminal_attach_failed", async () => {
		expect(await sanitizeRendererProperties("ao.renderer.terminal_attach_failed", { reason: "open_timeout" })).toEqual({
			reason: "open_timeout",
		});
		expect(
			await sanitizeRendererProperties("ao.renderer.terminal_attach_failed", { reason: "something else" }),
		).toEqual({});
	});
});

describe("reserveCapture", () => {
	it("allows up to 5 captures of a name within a minute", () => {
		const start = 1_000_000;
		for (let i = 0; i < 5; i += 1) {
			expect(reserveCapture("ao.renderer.route_viewed", start + i)).toBe(true);
		}
		expect(reserveCapture("ao.renderer.route_viewed", start + 5)).toBe(false);
	});

	it("tracks each name in its own window", () => {
		const start = 2_000_000;
		for (let i = 0; i < 5; i += 1) {
			reserveCapture("ao.renderer.route_viewed", start + i);
		}
		expect(reserveCapture("exception:TypeError", start)).toBe(true);
	});

	it("resets the burst window once a minute elapses", () => {
		const start = 3_000_000;
		for (let i = 0; i < 5; i += 1) {
			reserveCapture("ao.renderer.loaded", start + i);
		}
		expect(reserveCapture("ao.renderer.loaded", start + 60_001)).toBe(true);
	});

	it("caps the daily total even when calls are paced under the burst limit", () => {
		const name = "ao.renderer.paced_loop";
		let start = 10_000_000;
		let allowed = 0;
		for (let i = 0; i < 210; i += 1) {
			if (reserveCapture(name, start)) allowed += 1;
			start += 60_000; // one call per simulated minute: never trips the burst cap
		}
		expect(allowed).toBe(200);
	});

	it("resets the daily ceiling after 24 hours", () => {
		const name = "ao.renderer.daily_reset";
		let start = 20_000_000;
		for (let i = 0; i < 200; i += 1) {
			expect(reserveCapture(name, start)).toBe(true);
			start += 60_000;
		}
		expect(reserveCapture(name, start)).toBe(false);
		expect(reserveCapture(name, start + 24 * 60 * 60_000)).toBe(true);
	});
});

describe("daily active heartbeat", () => {
	it("derives the running channel from the version string", () => {
		expect(versionChannelFrom("0.11.3")).toBe("stable");
		expect(versionChannelFrom("0.11.3-nightly.5")).toBe("nightly");
		expect(versionChannelFrom("0.12.0-NIGHTLY.1")).toBe("nightly");
		// A pinned feature build is not nightly, and an absent version is unknown.
		expect(versionChannelFrom("0.11.3-feature.42")).toBe("stable");
		expect(versionChannelFrom("")).toBe("unknown");
		expect(versionChannelFrom("unknown")).toBe("unknown");
	});

	it("classifies the desktop app with client=desktop on every event", () => {
		const ctx = buildTelemetryContext("0.11.3", "darwin", "stable");
		expect(ctx.client).toBe("desktop");
	});

	it("carries intent and reality as separate context properties", () => {
		const ctx = buildTelemetryContext("0.11.3", "darwin", "nightly");
		// opted into nightly, still running a stable binary: the gap is the signal.
		expect(ctx.release_channel).toBe("nightly");
		expect(ctx.version_channel).toBe("stable");
	});

	it("maps the Updates setting to a release channel, not the version string", () => {
		expect(releaseChannelFrom({ channel: "latest", feature: null })).toBe("stable");
		expect(releaseChannelFrom({ channel: "nightly", feature: null })).toBe("nightly");
		// A pinned feature build wins regardless of the underlying channel, which
		// is what version-string parsing got wrong.
		expect(releaseChannelFrom({ channel: "latest", feature: 1234 })).toBe("feature");
		expect(releaseChannelFrom({ channel: "nightly", feature: 1234 })).toBe("feature");
		expect(releaseChannelFrom(null)).toBe("unknown");
		expect(releaseChannelFrom({})).toBe("unknown");
	});

	it("reports only the two channel names on a switch", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.update_channel_changed", {
			from_channel: "stable",
			to_channel: "nightly",
			feature: 1234,
			branch: "feat/secret-thing",
		});
		expect(safe).toEqual({ from_channel: "stable", to_channel: "nightly" });
	});

	it("drops an unrecognised channel value", async () => {
		const safe = await sanitizeRendererProperties("ao.renderer.update_channel_changed", {
			from_channel: "canary",
			to_channel: "nightly",
		});
		expect(safe).toEqual({ to_channel: "nightly" });
	});

	it("reserves one active capture per UTC day, not per six-hour slot", () => {
		const storage = memoryStorage();
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T00:05:00.000Z"))).toBe(true);
		// Every later hour of the same UTC day is refused. Under the old
		// six-hour slotting, 06:00, 12:00 and 18:00 each reserved again, so one
		// install could report four times a day.
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T05:59:59.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T06:00:00.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T12:00:00.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T18:00:00.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T23:59:59.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-13T00:00:00.000Z"))).toBe(true);
	});

	it("treats a slot record left by an older build as today already reported", () => {
		// The upgrade path: a build that emitted per slot wrote { date, slots }.
		// Reading it must not hand out a fresh reservation for the same day.
		const storage = memoryStorage();
		storage.setItem("ao.telemetry.activeSlotsByDate", JSON.stringify({ date: "2026-07-12", slots: [0] }));
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-12T14:00:00.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(storage, new Date("2026-07-13T00:00:00.000Z"))).toBe(true);
	});

	it("reserves once per day without storage, so a private-mode window still reports once", () => {
		expect(reserveDailyActiveCapture(undefined, new Date("2026-07-12T01:00:00.000Z"))).toBe(true);
		expect(reserveDailyActiveCapture(undefined, new Date("2026-07-12T19:00:00.000Z"))).toBe(false);
		expect(reserveDailyActiveCapture(undefined, new Date("2026-07-13T01:00:00.000Z"))).toBe(true);
	});


	it("emits once at startup and stays silent for the rest of the UTC day", () => {
		const storage = memoryStorage();
		const captured: string[] = [];
		let now = new Date("2026-07-12T08:00:00.000Z");

		const stop = startDailyActiveHeartbeat({
			storage,
			now: () => now,
			capture: () => {
				captured.push(now.toISOString());
			},
			window,
			document,
		});
		try {
			expect(captured).toEqual(["2026-07-12T08:00:00.000Z"]);

			window.dispatchEvent(new Event("focus"));
			document.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter" }));
			expect(captured).toHaveLength(1);

			// Later the same day, focus no longer produces a second event. This is
			// the four-per-day reduction, and it is the whole point of the change.
			now = new Date("2026-07-12T12:00:00.000Z");
			window.dispatchEvent(new Event("focus"));
			now = new Date("2026-07-12T20:00:00.000Z");
			window.dispatchEvent(new Event("focus"));
			expect(captured).toEqual(["2026-07-12T08:00:00.000Z"]);

			// The next UTC day reports again.
			now = new Date("2026-07-13T09:00:00.000Z");
			window.dispatchEvent(new Event("focus"));
			expect(captured).toEqual(["2026-07-12T08:00:00.000Z", "2026-07-13T09:00:00.000Z"]);
		} finally {
			stop();
		}
	});

	it("retries the same UTC day when PostHog rejects the first capture", async () => {
		const storage = memoryStorage();
		let attempts = 0;
		const dayTime = new Date("2026-07-23T08:00:00.000Z");
		const stop = startDailyActiveHeartbeat({
			storage,
			now: () => dayTime,
			capture: () => {
				attempts += 1;
				return attempts > 1;
			},
			window,
			document,
		});
		try {
			await Promise.resolve();
			window.dispatchEvent(new Event("focus"));
			await Promise.resolve();

			expect(attempts).toBe(2);
			expect(reserveDailyActiveCapture(storage, new Date("2026-07-23T09:00:00.000Z"))).toBe(false);
		} finally {
			stop();
		}
	});
});

describe("route view reservation", () => {
	it("reserves one capture per surface per UTC date", () => {
		const storage = memoryStorage();

		expect(reserveRouteViewCapture(storage, "session_detail", new Date("2026-07-12T08:00:00.000Z"))).toBe(true);
		expect(reserveRouteViewCapture(storage, "session_detail", new Date("2026-07-12T09:00:00.000Z"))).toBe(false);
		expect(reserveRouteViewCapture(storage, "project_board", new Date("2026-07-12T09:00:00.000Z"))).toBe(true);
		expect(reserveRouteViewCapture(storage, "session_detail", new Date("2026-07-13T00:00:00.000Z"))).toBe(true);
	});
});
