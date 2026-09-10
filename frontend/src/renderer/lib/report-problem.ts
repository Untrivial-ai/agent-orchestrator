import { aoBridge } from "./bridge";
import { routeSurface } from "./telemetry";

export type ReportProblemOutput = "github" | "discord" | "email";

export type ReportProblemInput = {
	summary: string;
	details: string;
};

export type ReportProblemDiagnostics = {
	appVersion: string;
	buildMode: string;
	daemonMessage?: string;
	daemonState: string;
	generatedAt: string;
	platform: string;
	routeSurface: string;
};

const REDACTED_LOCAL_PATH = "[redacted-local-path]";
const REDACTED_LOCAL_URL = "[redacted-local-url]";
const REDACTED_SECRET = "[redacted-secret]";
const DISCORD_INVITE_URL = "https://discord.com/invite/UZv7JjxbwG";
const GITHUB_NEW_ISSUE_URL = "https://github.com/Untrivial-ai/agent-orchestrator/issues/new";
const SUPPORT_EMAIL = "prasad@untrivial.ai";
const SUPPORT_CC_EMAIL = "prateek@untrivial.ai";

const LOCAL_URL_PATTERN =
	/(?:\bfile:\/\/\/\S+|\bapp:\/\/renderer\/\S+|\bhttps?:\/\/(?:localhost|127\.0\.0\.1|\[::1\])(?::\d+)?\S*)/gi;
const LOCAL_PATH_PATTERN = /(?:\/Users\/|\/home\/|\/tmp\/|\/private\/var\/|\/var\/folders\/)\S+|\b[A-Za-z]:\\[^\s)]+/g;
const QUERY_SECRET_PATTERN =
	/([?&](?:api[_-]?key|token|secret|password|access[_-]?token|refresh[_-]?token|auth)=)[^&\s)]+/gi;
const JSON_SECRET_PATTERN =
	/((["'])(?:api[_-]?key|token|secret|password|access[_-]?token|refresh[_-]?token|auth)\2\s*:\s*)(["'])(?:\\.|(?!\3).)*\3/gi;
const AUTHORIZATION_HEADER_PATTERN = /(\bAuthorization\s*:\s*(?:(?:Bearer|token|Basic)\s+)?)[^\s"',)]+/gi;
const ASSIGNMENT_SECRET_PATTERN =
	/(\b[A-Z0-9_]*(?:API[_-]?KEY|TOKEN|SECRET|PASSWORD|ACCESS[_-]?TOKEN|REFRESH[_-]?TOKEN|AUTH)[A-Z0-9_]*\s*[:=]\s*)(["']?)[^\s"',)]+/gi;
const BEARER_SECRET_PATTERN = /\b(Bearer\s+)[A-Za-z0-9._~+/=-]+/gi;
const OPENAI_KEY_PATTERN = /\bsk-[A-Za-z0-9_-]+/g;
const GITHUB_TOKEN_PATTERN = /\b(?:gh[pousr]_[A-Za-z0-9_]{20,}|github_pat_[A-Za-z0-9_]{20,})\b/g;

export function sanitizeReportText(value: string): string {
	if (!value) return "";
	return value
		.replace(LOCAL_URL_PATTERN, REDACTED_LOCAL_URL)
		.replace(LOCAL_PATH_PATTERN, REDACTED_LOCAL_PATH)
		.replace(QUERY_SECRET_PATTERN, `$1${REDACTED_SECRET}`)
		.replace(JSON_SECRET_PATTERN, `$1$3${REDACTED_SECRET}$3`)
		.replace(AUTHORIZATION_HEADER_PATTERN, `$1${REDACTED_SECRET}`)
		.replace(ASSIGNMENT_SECRET_PATTERN, `$1$2${REDACTED_SECRET}`)
		.replace(BEARER_SECRET_PATTERN, `$1${REDACTED_SECRET}`)
		.replace(OPENAI_KEY_PATTERN, REDACTED_SECRET)
		.replace(GITHUB_TOKEN_PATTERN, REDACTED_SECRET);
}

export async function collectReportProblemDiagnostics(now = new Date()): Promise<ReportProblemDiagnostics> {
	const [versionResult, daemonResult] = await Promise.allSettled([
		aoBridge.app.getVersion(),
		aoBridge.daemon.getStatus(),
	]);
	const daemonStatus = daemonResult.status === "fulfilled" ? daemonResult.value : undefined;

	return {
		appVersion: versionResult.status === "fulfilled" ? versionResult.value : "unknown",
		buildMode: import.meta.env.DEV ? "dev" : "packaged",
		daemonMessage: daemonStatus?.message,
		daemonState: daemonStatus?.state ?? "unknown",
		generatedAt: now.toISOString(),
		platform: typeof navigator === "undefined" ? "unknown" : navigator.platform || "unknown",
		routeSurface: typeof window === "undefined" ? "unknown" : routeSurface(currentRoutePath()),
	};
}

export function formatReportProblemDraft(
	input: ReportProblemInput,
	diagnostics: ReportProblemDiagnostics,
	output: ReportProblemOutput,
): string {
	const fields = normalizeInput(input);
	const diagnosticsBlock = formatDiagnostics(diagnostics);

	if (output === "discord") {
		return [
			"**AO feedback**",
			`Summary: ${fields.summary}`,
			`Details: ${fields.details}`,
			"",
			"Safe diagnostics:",
			diagnosticsBlock,
		].join("\n");
	}

	if (output === "email") {
		return [
			`To: ${SUPPORT_EMAIL}`,
			`Cc: ${SUPPORT_CC_EMAIL}`,
			`Subject: AO feedback: ${fields.summary}`,
			"",
			formatEmailBody(fields, diagnosticsBlock),
		].join("\n");
	}

	return [
		`# ${fields.summary === "Not provided" ? "AO feedback" : fields.summary}`,
		"",
		"## Summary",
		fields.summary,
		"",
		"## Details",
		fields.details,
		"",
		"## Safe diagnostics",
		diagnosticsBlock,
	].join("\n");
}

export function reportProblemDestinationUrl(
	input: ReportProblemInput,
	diagnostics: ReportProblemDiagnostics,
	output: ReportProblemOutput,
): string | null {
	if (output === "discord") return DISCORD_INVITE_URL;
	if (output === "email") {
		const url = new URL(`mailto:${SUPPORT_EMAIL}`);
		url.searchParams.set("cc", SUPPORT_CC_EMAIL);
		url.searchParams.set("subject", `AO feedback: ${reportTitle(input)}`);
		url.searchParams.set("body", formatEmailBody(normalizeInput(input), formatDiagnostics(diagnostics)));
		return url.toString();
	}

	const title = reportTitle(input);
	const draft = formatReportProblemDraft(input, diagnostics, output);

	const url = new URL(GITHUB_NEW_ISSUE_URL);
	url.searchParams.set("title", title);
	url.searchParams.set("body", draft);
	return url.toString();
}

function normalizeInput(input: ReportProblemInput) {
	return {
		summary: valueOrPlaceholder(input.summary),
		details: valueOrPlaceholder(input.details),
	};
}

function formatEmailBody(fields: ReturnType<typeof normalizeInput>, diagnosticsBlock: string): string {
	return [
		"AO feedback",
		"",
		`Summary: ${fields.summary}`,
		"",
		"Details:",
		fields.details,
		"",
		"Safe diagnostics:",
		diagnosticsBlock,
	].join("\n");
}

function reportTitle(input: ReportProblemInput): string {
	const summary = valueOrPlaceholder(input.summary);
	return summary === "Not provided" ? "AO feedback" : summary;
}

function valueOrPlaceholder(value: string): string {
	const safe = sanitizeReportText(value.trim());
	return safe || "Not provided";
}

function formatDiagnostics(diagnostics: ReportProblemDiagnostics): string {
	const lines = [
		`AO version: ${sanitizeReportText(diagnostics.appVersion) || "unknown"}`,
		`Build mode: ${sanitizeReportText(diagnostics.buildMode) || "unknown"}`,
		`Platform: ${sanitizeReportText(diagnostics.platform) || "unknown"}`,
		`Route surface: ${sanitizeReportText(diagnostics.routeSurface) || "unknown"}`,
		`Daemon: ${sanitizeReportText(diagnostics.daemonState) || "unknown"}`,
		`Generated: ${sanitizeReportText(diagnostics.generatedAt) || "unknown"}`,
	];
	if (diagnostics.daemonMessage?.trim()) {
		lines.push(`Daemon message: ${sanitizeReportText(diagnostics.daemonMessage.trim())}`);
	}
	return lines.map((line) => `- ${line}`).join("\n");
}

function currentRoutePath(): string {
	const hashPath = window.location.hash.replace(/^#/, "").split("?")[0];
	if (hashPath?.startsWith("/")) return hashPath;
	return window.location.pathname;
}
