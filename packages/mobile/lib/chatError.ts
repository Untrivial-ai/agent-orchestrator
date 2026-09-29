import { daemonDetail } from "./connectionError";

const CHAT_PREFLIGHT_CODES = new Set([
	"SESSION_MODE_UNSUPPORTED",
	"CHAT_DRIVER_UNAVAILABLE",
	"CHAT_DRIVER_INCOMPATIBLE",
	"CHAT_AUTH_REQUIRED",
]);

type ChatAPIError = Error & { code?: string };

export function isChatPreflightError(error: unknown): error is ChatAPIError {
	return error instanceof Error && "code" in error && Boolean(error.code && CHAT_PREFLIGHT_CODES.has(String(error.code)));
}

/** Remove the HTTP envelope prefix while preserving the daemon's useful detail. */
export function chatErrorCopy(error: ChatAPIError): string {
	return daemonDetail(error) ?? error.message.replace(/^\d+\s+[^-]+\s+-\s+/, "");
}
