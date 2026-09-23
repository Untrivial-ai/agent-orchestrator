import type { ConversationActivity } from "./types";

/**
 * Split a provider's error into a headline and, only when it adds something,
 * a detail.
 *
 * Ported from the renderer's providerErrorCopy. Providers often set several
 * fields to the same sentence — Codex sends the usage-limit text as both the
 * activity summary and detail.error — and rendering each field in turn printed
 * one failure twice inside a single card. Some wrap the real message in JSON
 * instead, so that is unwrapped before comparing.
 */
export type ProviderErrorCopy = { headline: string; detail?: string };

export function providerErrorCopy(activity: ConversationActivity): ProviderErrorCopy {
	const candidates = [activity.detail?.message, activity.summary, activity.detail?.error];
	for (const candidate of candidates) {
		const raw = String(candidate ?? "").trim();
		if (!raw) continue;
		const unwrapped = unwrapProviderErrorJson(raw);
		if (unwrapped) return unwrapped;
	}

	const headline = String(activity.detail?.message ?? activity.summary ?? "").trim();
	const extra = String(activity.detail?.error ?? "").trim();
	if (!headline) return { headline: extra || "Provider error" };
	// The dedupe: identical fields say it once.
	if (extra && extra !== headline) return { headline, detail: extra };
	return { headline };
}

function unwrapProviderErrorJson(raw: string): ProviderErrorCopy | undefined {
	const parsed = parseJsonObjectSuffix(raw);
	const err = parsed
		? parsed.error && typeof parsed.error === "object" && !Array.isArray(parsed.error)
			? (parsed.error as Record<string, unknown>)
			: parsed
		: undefined;
	const message =
		typeof err?.message === "string" ? err.message.trim() : readJsonStringField(raw, "message");
	const additional =
		typeof err?.additionalDetails === "string"
			? err.additionalDetails.trim()
			: readJsonStringField(raw, "additionalDetails");
	if (!message && !additional) return undefined;
	if (message && additional && additional !== message) return { headline: message, detail: additional };
	return { headline: message || additional };
}

function readJsonStringField(raw: string, field: "message" | "additionalDetails"): string {
	const match = new RegExp(`"${field}"\\s*:\\s*("(?:\\\\.|[^"\\\\])*")`).exec(raw);
	if (!match?.[1]) return "";
	try {
		const value = JSON.parse(match[1]) as unknown;
		return typeof value === "string" ? value.trim() : "";
	} catch {
		return "";
	}
}

function parseJsonObjectSuffix(raw: string): Record<string, unknown> | undefined {
	const start = raw.indexOf("{");
	if (start < 0) return undefined;
	const slice = raw.slice(start).trim();
	const asObject = (value: unknown): Record<string, unknown> | undefined => {
		if (!value || typeof value !== "object" || Array.isArray(value)) return undefined;
		return value as Record<string, unknown>;
	};
	try {
		return asObject(JSON.parse(slice));
	} catch {
		const end = slice.lastIndexOf("}");
		if (end <= 0) return undefined;
		try {
			return asObject(JSON.parse(slice.slice(0, end + 1)));
		} catch {
			return undefined;
		}
	}
}

/**
 * Whether an error activity is just restating the failure its own turn already
 * reports.
 *
 * A provider failure arrives twice: once as an error activity and again as the
 * turn's errorMessage. Desktop shows the turn's version — headline, message and
 * a Retry — so the activity is the copy to drop.
 */
export function errorActivityDuplicatesTurn(activity: ConversationActivity, turnErrorMessage?: string): boolean {
	if (activity.activityKind !== "error") return false;
	const turnError = (turnErrorMessage ?? "").trim();
	if (!turnError) return false;
	const { headline, detail } = providerErrorCopy(activity);
	const summary = String(activity.summary ?? "").trim();
	const raw = String(activity.detail?.error ?? "").trim();
	return [headline.trim(), detail?.trim(), summary, raw].some((value) => Boolean(value) && value === turnError);
}
