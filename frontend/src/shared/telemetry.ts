import { mkdir, readFile, writeFile } from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { randomUUID } from "node:crypto";
import type { TelemetryPolicySnapshot } from "./telemetry-policy";

export type TelemetryBootstrap = {
	distinctId: string;
	appVersion: string;
	platform: NodeJS.Platform;
	/**
	 * Event streams the renderer must not send, from
	 * `AO_TELEMETRY_DISABLED_EVENTS`.
	 *
	 * The daemon enforces the same list on its own billed sink, but renderer
	 * events go straight to PostHog and never pass through it. Without carrying
	 * the policy across the boundary, denying `ao.v2.app.active` would silence
	 * the CLI producer while the renderer kept sending under the same exported
	 * name, which is a kill switch that only half works.
	 */
	disabledEvents: string[];
	eventsEnabled: boolean;
	consentGeneration: string;
};

/** Parses the comma-separated deny list. Never throws: an unusable entry is inert. */
export function parseDisabledEvents(raw: string | undefined): string[] {
	if (!raw) return [];
	return raw
		.split(",")
		.map((name) => name.trim())
		.filter((name) => name !== "");
}

/**
 * Whether the renderer may export to PostHog.
 *
 * Unpackaged builds are off by default so a developer's ordinary session does
 * not land in the production project as a real install. Setting
 * `AO_TELEMETRY_RENDERER=on` opts a dev build back in for deliberate testing.
 */
export function rendererTelemetryEnabled(
	env: Record<string, string | undefined>,
	isPackaged: boolean,
): boolean {
	const explicit = env.AO_TELEMETRY_RENDERER?.trim().toLowerCase();
	if (explicit === "on") return true;
	if (explicit === "off") return false;
	return isPackaged;
}

export function defaultDataDir(
	platform: NodeJS.Platform,
	env: Record<string, string | undefined>,
	homeDir: string,
): string | null {
	void platform;
	if (env.AO_DATA_DIR) return env.AO_DATA_DIR;
	if (!homeDir) return null;
	return path.join(homeDir, ".ao", "data");
}

export async function loadOrCreateTelemetryInstallId(dataDir: string): Promise<string> {
	const file = path.join(dataDir, "telemetry_install_id");
	try {
		const existing = (await readFile(file, "utf8")).trim();
		if (existing) return existing;
	} catch {
		// Create the id on first use.
	}
	await mkdir(dataDir, { recursive: true });
	const distinctId = `ins_${randomUUID()}`;
	await writeFile(file, `${distinctId}\n`, { mode: 0o600 });
	return distinctId;
}

export async function buildTelemetryBootstrap(
	env: Record<string, string | undefined>,
	appVersion: string,
	platform: NodeJS.Platform,
	homeDir = os.homedir(),
	isPackaged = true,
	policy?: TelemetryPolicySnapshot,
): Promise<TelemetryBootstrap | null> {
	// Returning null is the whole switch: initTelemetry() bails on a null
	// bootstrap, so the renderer never constructs a PostHog client at all. That
	// is stronger than gating each capture, because it also removes the SDK's
	// own background requests.
	if (!rendererTelemetryEnabled(env, isPackaged)) return null;
	const dataDir = defaultDataDir(platform, env, homeDir);
	if (!dataDir) return null;
	if (!policy) return null;
	// This policy controls failure reporting only. Existing anonymous PostHog
	// product analytics retain their own packaged/env gate above.
	const eventsEnabled = policy.eventsEnabled && policy.acknowledged;
	return {
		distinctId: await loadOrCreateTelemetryInstallId(dataDir),
		appVersion,
		platform,
		disabledEvents: parseDisabledEvents(env.AO_TELEMETRY_DISABLED_EVENTS),
		eventsEnabled,
		consentGeneration: policy.consentGeneration,
	};
}
