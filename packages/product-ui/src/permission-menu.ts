/** Portable permission presentation. Values remain owned by the provider/API. */
export type PermissionChoice = { value: string; name: string; description?: string };
export type PermissionMenuItem = { id: string; label: string; value?: string; hint: string };

const MODES = [
	{ id: "auto", label: "Auto" },
	{ id: "manual", label: "Manual" },
	{ id: "accept-edits", label: "Accept Edits" },
	{ id: "dont-ask", label: "Don't Ask" },
	{ id: "bypass-permissions", label: "Bypass Permissions" },
] as const;

// Exact native identifiers, not fuzzy name matching: custom provider modes must
// never acquire a more permissive meaning merely because of their display name.
const NATIVE_IDS: Record<string, string[]> = {
	auto: ["auto"],
	manual: ["manual"],
	"accept-edits": ["acceptEdits", "accept-edits", "accept_edits"],
	"dont-ask": ["dontAsk", "dont-ask", "dont_ask"],
	"bypass-permissions": ["bypassPermissions", "bypass-permissions", "bypass"],
};

// Ambiguous names only acquire an AO meaning for a verified harness contract.
// Pinned claude-agent-acp 0.70.0 declares id default, name Manual in acp-agent.js.
// Kimchi and OMP explicitly map bypass-permissions to yolo in their backend ACP adapters.
const HARNESS_NATIVE_IDS: Record<string, Record<string, string[]>> = {
	"claude-code": { manual: ["default"] },
	kimchi: { "bypass-permissions": ["yolo"] },
	omp: { "bypass-permissions": ["yolo"] },
};

export function providerPermissionMenu(choices: PermissionChoice[], harness?: string): PermissionMenuItem[] {
	const used = new Set<string>();
	const result: PermissionMenuItem[] = MODES.map((mode) => {
		const ids = [...NATIVE_IDS[mode.id], ...(HARNESS_NATIVE_IDS[harness ?? ""]?.[mode.id] ?? [])];
		const choice = choices.find((choice) => ids.includes(choice.value));
		if (choice) used.add(choice.value);
		return { ...mode, value: choice?.value, hint: choice?.description ?? (choice ? "" : "Not supported by this provider") };
	});
	for (const choice of choices) {
		if (!used.has(choice.value)) result.push({ id: `provider:${choice.value}`, label: choice.name, value: choice.value, hint: choice.description ?? "Provider mode" });
	}
	return result;
}

export function nativePermissionMenu(harness?: string): PermissionMenuItem[] {
	// Only Codex exposes these per-turn policies without a live mode catalog.
	// Default is intentionally separate: native configuration can itself bypass
	// approvals, so presenting it as Manual would make a false safety promise.
	const supported: Record<string, string> = harness === "codex" ? {
		manual: "Ask before writes; use a read-only sandbox",
		"dont-ask": "Allow workspace edits; deny actions that require approval",
		auto: "Use automatic approval review within the workspace sandbox",
		"accept-edits": "Allow workspace edits; ask for approval when escalation is needed",
		"bypass-permissions": "Disable approvals and sandbox protections",
	} : {};
	return [
		...MODES.map((mode) => ({ ...mode, value: supported[mode.id] ? mode.id : undefined, hint: supported[mode.id] ?? "Not supported by this provider" })),
		{ id: "default", label: "Provider configuration", value: "default", hint: "Restore the provider's configured approvals and sandbox" },
	];
}

export function permissionMenuLabel(items: PermissionMenuItem[], value?: string): string {
	return items.find((item) => item.value !== undefined && item.value === value)?.label ?? items.find((item) => item.id === value)?.label ?? value ?? "Permissions";
}
