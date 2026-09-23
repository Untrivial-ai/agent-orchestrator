// Renderer Sentry intake is deliberately transport-free. Trusted preload adds
// the latest main-owned generation before forwarding to Electron main.
import { classifyError, type ClassifyInput, type Triage } from "../../shared/observability";
import { aoBridge } from "./bridge";

export type ObservabilityContext = { release?: string; channel?: string; platform?: string; distinctId?: string };
export type CaptureMeta = ClassifyInput & { operation?: string; surface?: string; domain?: string; requestId?: string };
let context: ObservabilityContext = {};
export async function initSentry(next: ObservabilityContext): Promise<void> { context = next; }
function domainOf(operation?: string): string | undefined {
	if (!operation) return undefined;
	const parts = (operation.split(" ").pop() ?? operation).split("/").filter(Boolean);
	const index = parts.indexOf("v1"); return index >= 0 ? parts[index + 1] : parts[0];
}
function tagsFor(meta: CaptureMeta, triage: Triage): Record<string, string> {
	return Object.fromEntries(Object.entries({ platform: context.platform ?? "desktop", surface: meta.surface, domain: meta.domain, operation: meta.operation, category: meta.category, code: meta.code, http_status: meta.httpStatus?.toString(), apierr_kind: meta.kind, severity: triage.severity, owner: triage.owner }).filter((entry): entry is [string, string] => typeof entry[1] === "string"));
}
export function captureExceptionToSentry(error: unknown, meta: CaptureMeta = {}): void {
	const triage = classifyError(meta);
	void aoBridge.telemetry.capture({ kind: triage.report ? "exception" : "breadcrumb", message: String((error as Error)?.message ?? error), level: triage.level, tags: tagsFor(meta, triage) });
}
export function captureApiErrorToSentry(operation: string, category: string, status?: number, code?: string, requestId?: string): void {
	const meta: CaptureMeta = { operation, category, httpStatus: status, code, requestId, domain: domainOf(operation) };
	const triage = classifyError(meta);
	void aoBridge.telemetry.capture({ kind: triage.report ? "message" : "breadcrumb", message: `${operation} ${category}`, level: triage.level, tags: tagsFor(meta, triage) });
}
