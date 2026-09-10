import { randomUUID } from "node:crypto";
import { constants } from "node:fs";
import { lstat, mkdir, open, readFile, rename, rm } from "node:fs/promises";
import path from "node:path";
import {
	parseTelemetryPolicyDiskRecord,
	telemetryPolicySnapshot,
	type TelemetryPolicyDiskRecord,
	type TelemetryPolicySnapshot,
} from "../shared/telemetry-policy";

export const TELEMETRY_POLICY_FILE = "telemetry_policy.json";

type PolicyStat = { mode: number; isFile(): boolean; isSymbolicLink(): boolean };
type WritablePolicyHandle = { writeFile(data: string, encoding: "utf8"): Promise<void>; sync(): Promise<void>; close(): Promise<void> };

export type TelemetryPolicyFileSystem = {
	mkdir(path: string): Promise<void>;
	lstat(path: string): Promise<PolicyStat>;
	readFile(path: string): Promise<string>;
	openExclusive(path: string, mode: number): Promise<WritablePolicyHandle>;
	rename(from: string, to: string): Promise<void>;
	syncDirectory(path: string): Promise<void>;
	remove(path: string): Promise<void>;
};

export const nodeTelemetryPolicyFileSystem: TelemetryPolicyFileSystem = {
	mkdir: async (target) => { await mkdir(target, { recursive: true, mode: 0o700 }); },
	lstat,
	readFile: (target) => readFile(target, "utf8"),
	openExclusive: (target, mode) => open(target, constants.O_CREAT | constants.O_EXCL | constants.O_WRONLY, mode),
	rename,
	syncDirectory: async (target) => {
		const handle = await open(target, constants.O_RDONLY);
		try { await handle.sync(); } finally { await handle.close(); }
	},
	remove: async (target) => { await rm(target, { force: true }); },
};

export function resolveDesktopDataDir(
	env: Record<string, string | undefined>,
	homeDir: string,
	launchWorkingDirectory: string,
	isPackaged: boolean,
): string {
	const configured = env.AO_DATA_DIR?.trim();
	if (configured) return path.resolve(launchWorkingDirectory, configured);
	return path.resolve(homeDir, ".ao", isPackaged ? "data" : path.join("dev", "data"));
}

export class TelemetryPolicyAuthority {
	readonly durabilitySupported: boolean;
	private readonly policyPath: string;
	private current: TelemetryPolicySnapshot;
	private loaded = false;
	private writable = false;

	constructor(private readonly options: {
		dataDir: string;
		packagedDefault: boolean;
		platform?: NodeJS.Platform;
		fs?: TelemetryPolicyFileSystem;
		now?: () => Date;
		newGeneration?: () => string;
	}) {
		this.policyPath = path.join(options.dataDir, TELEMETRY_POLICY_FILE);
		this.durabilitySupported = (options.platform ?? process.platform) !== "win32";
		this.current = telemetryPolicySnapshot(this.newRecord(false), false);
	}

	snapshot(): TelemetryPolicySnapshot { return { ...this.current }; }

	async load(): Promise<TelemetryPolicySnapshot> {
		if (this.loaded) return this.snapshot();
		this.loaded = true;
		if (!this.durabilitySupported) return this.snapshot();
		const fs = this.options.fs ?? nodeTelemetryPolicyFileSystem;
		await fs.mkdir(this.options.dataDir);
		let stat: PolicyStat;
		try {
			stat = await fs.lstat(this.policyPath);
		} catch (error) {
			if (!isNotFound(error)) throw error;
			const record = this.newRecord(this.options.packagedDefault);
			this.current = telemetryPolicySnapshot(record, false);
			this.writable = true;
			await this.replace(record);
			return this.snapshot();
		}
		if (stat.isSymbolicLink() || !stat.isFile() || (stat.mode & 0o077) !== 0) return this.snapshot();
		let raw: string;
		try { raw = await fs.readFile(this.policyPath); } catch { return this.snapshot(); }
		const parsed = parseTelemetryPolicyDiskRecord(raw);
		if (!parsed.ok) return this.snapshot();
		this.writable = true;
		this.current = telemetryPolicySnapshot(parsed.record, true);
		return this.snapshot();
	}

	async setEventsEnabled(eventsEnabled: boolean): Promise<TelemetryPolicySnapshot> {
		if (!this.loaded) await this.load();
		if (!this.durabilitySupported) {
			this.current = { ...this.current, eventsEnabled: false, acknowledged: false };
			throw new Error("telemetry policy durable replacement is unsupported on Windows");
		}
		if (!this.writable) throw new Error("telemetry policy authority is unsafe and cannot be replaced");
		const record = this.newRecord(eventsEnabled);
		this.current = telemetryPolicySnapshot(record, false);
		await this.replace(record);
		return this.snapshot();
	}

	async retryPendingReplacement(): Promise<TelemetryPolicySnapshot> {
		if (!this.loaded) await this.load();
		if (this.current.acknowledged) return this.snapshot();
		if (!this.durabilitySupported) {
			throw new Error("telemetry policy durable replacement is unsupported on Windows");
		}
		if (!this.writable) throw new Error("telemetry policy authority is unsafe and cannot be replaced");
		await this.replace({
			schema_version: 1,
			events_enabled: this.current.eventsEnabled,
			consent_generation: this.current.consentGeneration,
			updated_at: this.current.updatedAt,
		});
		return this.snapshot();
	}

	private newRecord(eventsEnabled: boolean): TelemetryPolicyDiskRecord {
		return {
			schema_version: 1,
			events_enabled: eventsEnabled,
			consent_generation: (this.options.newGeneration ?? randomUUID)(),
			updated_at: (this.options.now?.() ?? new Date()).toISOString(),
		};
	}

	private async replace(record: TelemetryPolicyDiskRecord): Promise<void> {
		const fs = this.options.fs ?? nodeTelemetryPolicyFileSystem;
		const temporary = path.join(this.options.dataDir, `.${TELEMETRY_POLICY_FILE}.${process.pid}.${randomUUID()}.tmp`);
		let handle: WritablePolicyHandle | undefined;
		try {
			handle = await fs.openExclusive(temporary, 0o600);
			await handle.writeFile(`${JSON.stringify(record)}\n`, "utf8");
			await handle.sync();
			await handle.close();
			handle = undefined;
			await fs.rename(temporary, this.policyPath);
			await fs.syncDirectory(this.options.dataDir);
			this.current = telemetryPolicySnapshot(record, true);
		} finally {
			if (handle) await handle.close().catch(() => undefined);
			await fs.remove(temporary).catch(() => undefined);
		}
	}
}

function isNotFound(error: unknown): boolean {
	return typeof error === "object" && error !== null && "code" in error && (error as { code?: string }).code === "ENOENT";
}
