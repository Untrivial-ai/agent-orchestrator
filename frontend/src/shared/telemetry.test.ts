import { mkdtemp, readFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { afterEach, expect, test } from "vitest";
import {
	buildTelemetryBootstrap,
	defaultDataDir,
	loadOrCreateTelemetryInstallId,
	parseDisabledEvents,
	rendererTelemetryEnabled,
} from "./telemetry";

const tempDirs: string[] = [];
const enabledPolicy = { eventsEnabled: true, consentGeneration: "7f80c8a9-ec67-4a16-a067-a444ffcc5cca", updatedAt: "2026-08-28T10:15:30.000Z", acknowledged: true };

afterEach(async () => {
	await Promise.all(
		tempDirs
			.splice(0)
			.map((dir) => import("node:fs/promises").then(({ rm }) => rm(dir, { recursive: true, force: true }))),
	);
});

test("defaultDataDir prefers AO_DATA_DIR", () => {
	expect(defaultDataDir("linux", { AO_DATA_DIR: "/tmp/custom" }, "/home/test")).toBe("/tmp/custom");
});

test("loadOrCreateTelemetryInstallId persists a stable install id", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);

	const first = await loadOrCreateTelemetryInstallId(dir);
	const second = await loadOrCreateTelemetryInstallId(dir);
	const stored = (await readFile(path.join(dir, "telemetry_install_id"), "utf8")).trim();

	expect(first).toMatch(/^ins_/);
	expect(second).toBe(first);
	expect(stored).toBe(first);
});

test("buildTelemetryBootstrap returns null when no home dir is available", async () => {
	await expect(buildTelemetryBootstrap({}, "1.2.3", "linux", "", true, enabledPolicy)).resolves.toBeNull();
});

test("renderer telemetry is off on unpackaged builds unless explicitly opted in", () => {
	expect(rendererTelemetryEnabled({}, false)).toBe(false);
	expect(rendererTelemetryEnabled({}, true)).toBe(true);
	expect(rendererTelemetryEnabled({ AO_TELEMETRY_RENDERER: " ON " }, false)).toBe(true);
	expect(rendererTelemetryEnabled({ AO_TELEMETRY_RENDERER: "off" }, true)).toBe(false);
});

// A null bootstrap is the switch itself: initTelemetry bails on it, so an
// unpackaged build never constructs a PostHog client at all.
test("buildTelemetryBootstrap withholds the bootstrap on an unpackaged build", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);

	const disabled = await buildTelemetryBootstrap({}, "0.11.2", "linux", dir, false, enabledPolicy);
	expect(disabled).toBeNull();
	const optedIn = await buildTelemetryBootstrap({ AO_TELEMETRY_RENDERER: "on" }, "0.11.2", "linux", dir, false, enabledPolicy);
	expect(optedIn?.appVersion).toBe("0.11.2");
});

test("failure-reporting opt-out does not withhold PostHog identity", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);
	const bootstrap = await buildTelemetryBootstrap({}, "0.11.2", "linux", dir, true, {
		...enabledPolicy,
		eventsEnabled: false,
	});
	expect(bootstrap?.eventsEnabled).toBe(false);
	expect(bootstrap?.distinctId).toMatch(/^ins_/);
});

test("buildTelemetryBootstrap carries the deny list across the process boundary", async () => {
	const dir = await mkdtemp(path.join(os.tmpdir(), "ao-telemetry-"));
	tempDirs.push(dir);

	const bootstrap = await buildTelemetryBootstrap(
		{ AO_TELEMETRY_DISABLED_EVENTS: "ao.v2.app.active, ao.renderer.* ,, " },
		"0.11.2",
		"linux",
		dir,
		true,
		enabledPolicy,
	);
	expect(bootstrap?.disabledEvents).toEqual(["ao.v2.app.active", "ao.renderer.*"]);
});

test("parseDisabledEvents treats absent or blank policy as no policy", () => {
	expect(parseDisabledEvents(undefined)).toEqual([]);
	expect(parseDisabledEvents(" , , ")).toEqual([]);
});
