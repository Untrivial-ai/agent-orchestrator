import type { RendererTelemetryCapture, TelemetryPolicySnapshot, TelemetryPolicyView } from "../shared/telemetry-policy";
import type { DaemonTelemetryPolicyAcknowledgement } from "./daemon-telemetry-policy-client";

const agentSwitchFailureProductionEnabled = false;

export type DesktopTelemetryTransport = {
	closeAndDrain(): Promise<void>;
	clearCache(): Promise<void>;
	capture(request: RendererTelemetryCapture): void;
};

type Authority = {
	durabilitySupported: boolean;
	load(): Promise<TelemetryPolicySnapshot>;
	snapshot(): TelemetryPolicySnapshot;
	setEventsEnabled(enabled: boolean): Promise<TelemetryPolicySnapshot>;
	retryPendingReplacement(): Promise<TelemetryPolicySnapshot>;
};

type DaemonPolicyClient = {
	prepareDisable(): Promise<DaemonTelemetryPolicyAcknowledgement>;
	applyPolicy(generation: string, enabled: boolean): Promise<DaemonTelemetryPolicyAcknowledgement>;
};

export class DesktopTelemetryController {
	private view: TelemetryPolicyView;
	private transport: DesktopTelemetryTransport | null = null;
	private operation: Promise<TelemetryPolicyView>;
	private pendingDesktopCleanup: (() => Promise<boolean>) | null = null;

	constructor(private readonly options: {
		authority: Authority;
		daemon: DaemonPolicyClient;
		transportFactory: () => Promise<DesktopTelemetryTransport | null>;
		environmentAllowsEvents: boolean;
		productionEnabled?: boolean;
		broadcast?: (view: TelemetryPolicyView) => void;
		clearRendererQueues?: () => Promise<void>;
		visibility?: { setPolicy(enabled: boolean, consentGeneration: string): void; disableAndDrain(): Promise<void>; closeAndDrain(): Promise<void> };
	}) {
		this.view = this.toView(options.authority.snapshot(), "applied");
		this.operation = Promise.resolve(this.view);
	}

	snapshot(): TelemetryPolicyView { return { ...this.view }; }

	initialize(): Promise<TelemetryPolicyView> {
		return this.serialize(async () => {
			const snapshot = await this.options.authority.load();
			let applied = false;
			if (snapshot.acknowledged) {
				try {
					const ack = await this.options.daemon.applyPolicy(snapshot.consentGeneration, snapshot.eventsEnabled);
					applied = this.acknowledges(snapshot.eventsEnabled, snapshot.consentGeneration, ack);
				} catch { applied = false; }
			}
			this.view = this.toView({ ...snapshot, acknowledged: snapshot.acknowledged && applied }, snapshot.acknowledged ? (applied ? "applied" : "cleanup_pending") : "cleanup_failed", snapshot.acknowledged ? (applied ? this.baseReason() : "daemon_cleanup_pending") : (this.options.authority.durabilitySupported ? "invalid_authority" : "durability_unsupported"));
			if (applied && this.captureEnabled(snapshot)) this.transport = await this.options.transportFactory();
			this.publish();
			return this.snapshot();
		});
	}

	setEventsEnabled(enabled: boolean, expectedGeneration: string): Promise<TelemetryPolicyView> {
		return this.serialize(async () => {
			if (expectedGeneration !== this.view.consentGeneration) throw new Error("stale telemetry consent generation");
			return enabled ? this.enable() : this.disable();
		});
	}

	async retryPendingCleanup(): Promise<TelemetryPolicyView> {
		return this.serialize(async () => {
			if (this.view.state === "applied") return this.snapshot();
			let desktopCleanupFailed = false;
			let authorityVerified = false;
			try {
				let snapshot = this.options.authority.snapshot();
				if (!snapshot.acknowledged) snapshot = await this.options.authority.retryPendingReplacement();
				authorityVerified = snapshot.acknowledged &&
					snapshot.consentGeneration === this.view.consentGeneration &&
					snapshot.eventsEnabled === this.view.eventsEnabled;
				if (!authorityVerified) throw new Error("durable policy authority was not acknowledged");
				const ack = await this.options.daemon.applyPolicy(snapshot.consentGeneration, snapshot.eventsEnabled);
				if (!this.acknowledges(snapshot.eventsEnabled, snapshot.consentGeneration, ack)) throw new Error("policy was not acknowledged");
				if (!snapshot.eventsEnabled && this.pendingDesktopCleanup && !(await this.pendingDesktopCleanup())) { desktopCleanupFailed = true; throw new Error("desktop cleanup is incomplete"); }
				this.pendingDesktopCleanup = null;
				this.view = this.toView(snapshot, "applied", this.baseReason());
				if (this.captureEnabled(this.view) && !this.transport) this.transport = await this.options.transportFactory();
			} catch {
				this.view = {
					...this.view,
					state: authorityVerified ? "cleanup_pending" : "cleanup_failed",
					acknowledged: false,
					reason: desktopCleanupFailed || !authorityVerified ? "cleanup_failed" : "daemon_cleanup_pending",
				};
			}
			this.publish(); return this.snapshot();
		});
	}

	capture(request: RendererTelemetryCapture): boolean {
		if (!this.captureEnabled(this.view) || request.consentGeneration !== this.view.consentGeneration || !this.transport) return false;
		this.transport.capture(request); return true;
	}

	async close(): Promise<void> {
		const transport = this.transport;
		this.transport = null;
		await Promise.allSettled([
			this.options.visibility?.closeAndDrain() ?? Promise.resolve(),
			transport?.closeAndDrain() ?? Promise.resolve(),
		]);
	}

	private async disable(): Promise<TelemetryPolicyView> {
		let visibilityDrained = true;
		try {
			this.options.visibility?.setPolicy(false, this.view.consentGeneration);
			await this.options.visibility?.disableAndDrain();
		} catch { visibilityDrained = false; }
		const closingTransport = this.transport;
		let transportDrained = true;
		try { await closingTransport?.closeAndDrain(); } catch { transportDrained = false; }
		this.transport = null;
		this.pendingDesktopCleanup = async () => {
			let purged = true;
			if (!visibilityDrained) {
				try { await this.options.visibility?.disableAndDrain(); visibilityDrained = true; } catch { purged = false; }
			}
			if (!transportDrained) {
				try { await closingTransport?.closeAndDrain(); transportDrained = true; } catch { purged = false; }
			}
			try { await closingTransport?.clearCache(); } catch { purged = false; }
			try { await this.options.clearRendererQueues?.(); } catch { purged = false; }
			return purged;
		};
		try { await this.options.daemon.prepareDisable(); } catch { /* durable off still proceeds */ }
		let snapshot: TelemetryPolicySnapshot;
		try { snapshot = await this.options.authority.setEventsEnabled(false); }
		catch (error) {
			snapshot = { ...this.options.authority.snapshot(), eventsEnabled: false, acknowledged: false };
			this.view = this.toView(snapshot, "cleanup_failed", "cleanup_failed");
			this.publish();
			throw error;
		}
		let applied = false;
		try {
			const ack = await this.options.daemon.applyPolicy(snapshot.consentGeneration, false);
			applied = ack.consentGeneration === snapshot.consentGeneration && !ack.eventsEnabled && ack.gateDrained && ack.purgeConfirmed;
		} catch { applied = false; }
		const desktopPurged = await this.pendingDesktopCleanup();
		if (desktopPurged) this.pendingDesktopCleanup = null;
		const cleanupApplied = applied && desktopPurged;
		this.view = this.toView({ ...snapshot, acknowledged: snapshot.acknowledged && cleanupApplied }, cleanupApplied ? "applied" : "cleanup_pending", cleanupApplied ? this.baseReason() : (desktopPurged ? "daemon_cleanup_pending" : "cleanup_failed"));
		this.publish(); return this.snapshot();
	}

	private async enable(): Promise<TelemetryPolicyView> {
		if (!this.options.authority.durabilitySupported) throw new Error("telemetry enablement is unavailable because durable policy replacement is unsupported");
		if (!this.options.environmentAllowsEvents) { this.view = { ...this.view, environmentVeto: true, reason: "environment_veto" }; this.publish(); return this.snapshot(); }
		if (!this.view.eventsEnabled && this.view.state !== "applied") {
			const cleanup = await this.options.daemon.applyPolicy(this.view.consentGeneration, false);
			if (!cleanup.gateDrained || !cleanup.purgeConfirmed || cleanup.eventsEnabled || cleanup.consentGeneration !== this.view.consentGeneration) throw new Error("prior telemetry purge is incomplete");
			if (this.pendingDesktopCleanup && !(await this.pendingDesktopCleanup())) throw new Error("prior desktop telemetry purge is incomplete");
			this.pendingDesktopCleanup = null;
		}
		const snapshot = await this.options.authority.setEventsEnabled(true);
		let transport: DesktopTelemetryTransport | null = null;
		try {
			const ack = await this.options.daemon.applyPolicy(snapshot.consentGeneration, true);
			const releaseEnabled = this.options.productionEnabled ?? agentSwitchFailureProductionEnabled;
			if (ack.consentGeneration !== snapshot.consentGeneration || (releaseEnabled && !ack.eventsEnabled)) {
				throw new Error("telemetry enablement was not acknowledged");
			}
			if (this.captureEnabled(snapshot)) {
				transport = await this.options.transportFactory();
				if (!transport) throw new Error("desktop telemetry transport is unavailable");
			}
		} catch (error) {
			// The durable enabled generation may already have opened the daemon
			// gate even when its response was lost. Reuse the full opt-out path so
			// no process can retain that ambiguous enablement.
			this.view = this.toView({ ...snapshot, acknowledged: false }, "cleanup_pending", "daemon_cleanup_pending");
			await this.disable();
			throw error;
		}
		this.transport = transport;
		this.view = this.toView(snapshot, "applied", this.baseReason()); this.publish(); return this.snapshot();
	}

	private captureEnabled(snapshot: Pick<TelemetryPolicySnapshot, "eventsEnabled" | "acknowledged">): boolean {
		return snapshot.eventsEnabled && snapshot.acknowledged && this.options.authority.durabilitySupported && this.options.environmentAllowsEvents && (this.options.productionEnabled ?? agentSwitchFailureProductionEnabled);
	}

	private toView(snapshot: TelemetryPolicySnapshot, state: TelemetryPolicyView["state"], reason = this.baseReason()): TelemetryPolicyView {
		return { ...snapshot, state, environmentVeto: !this.options.environmentAllowsEvents, durabilitySupported: this.options.authority.durabilitySupported, reason };
	}

	private baseReason(): TelemetryPolicyView["reason"] {
		if (!this.options.authority.durabilitySupported) return "durability_unsupported";
		if (!this.options.environmentAllowsEvents) return "environment_veto";
		if (!(this.options.productionEnabled ?? agentSwitchFailureProductionEnabled)) return "release_blocked";
		return undefined;
	}

	private acknowledges(enabled: boolean, generation: string, ack: DaemonTelemetryPolicyAcknowledgement): boolean {
		if (ack.consentGeneration !== generation) return false;
		if (!enabled) return !ack.eventsEnabled && ack.gateDrained && ack.purgeConfirmed;
		return ack.eventsEnabled || !(this.options.productionEnabled ?? agentSwitchFailureProductionEnabled);
	}

	private publish(): void {
		this.options.visibility?.setPolicy(this.captureEnabled(this.view), this.view.consentGeneration);
		this.options.broadcast?.(this.snapshot());
	}

	private serialize(operation: () => Promise<TelemetryPolicyView>): Promise<TelemetryPolicyView> {
		const next = this.operation.catch(() => this.snapshot()).then(operation);
		this.operation = next; return next;
	}
}
