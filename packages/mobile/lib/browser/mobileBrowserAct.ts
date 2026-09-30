import type { MobileBrowserCommand, MobileBrowserCommandResult } from "./mobileBrowserRuntime";
import { matchMobileBrowserInstruction } from "./mobileBrowserActMatcher";

type RunCommand = (command: MobileBrowserCommand) => Promise<MobileBrowserCommandResult>;
const ACT_VERBS = new Set(["click", "dblclick", "focus", "hover", "fill", "type", "check", "uncheck"]);

function invalid(message: string): MobileBrowserCommandResult {
	return { ok: false, error: { code: "INVALID_ARGUMENT", message } };
}

/** Executes snapshot -> deterministic match -> action inside one agent command. */
export async function executeMobileBrowserAct(command: MobileBrowserCommand, run: RunCommand): Promise<MobileBrowserCommandResult> {
	const args = command.args ?? {};
	const instruction = typeof args.instruction === "string" ? args.instruction.trim() : "";
	if (!instruction) return invalid("instruction is required");
	const action = typeof args.action === "string" && args.action.trim() ? args.action.trim() : "click";
	if (!ACT_VERBS.has(action)) return invalid(`Unsupported act verb: ${action}`);
	const needsValue = action === "fill" || action === "type";
	if (needsValue && typeof args.value !== "string") return invalid("value is required");
	const nth = typeof args.nth === "number" && Number.isFinite(args.nth) ? Math.trunc(args.nth) : undefined;

	const attempt = async (number: number): Promise<MobileBrowserCommandResult> => {
		const snapshot = await run({ ...command, requestId: `${command.requestId}:snapshot:${number}`, action: "snapshot", args: { interactive: true } });
		if (!snapshot.ok) return snapshot;
		const match = matchMobileBrowserInstruction(instruction, snapshot.result.refs, nth);
		if (match.outcome !== "matched") {
			return { ok: true, result: {
				outcome: match.outcome,
				instruction,
				...(match.outcome === "ambiguous" ? { candidates: match.candidates } : {}),
				snapshot: typeof snapshot.result.text === "string" ? snapshot.result.text : "",
				untrustedExternalContent: true,
			} };
		}
		const result = await run({
			...command,
			requestId: `${command.requestId}:action:${number}`,
			action,
			args: needsValue ? { ref: match.candidate.ref, text: args.value } : { ref: match.candidate.ref },
		});
		if (!result.ok && result.error.code === "STALE_REFERENCE" && number === 1) return attempt(2);
		if (!result.ok) return result;
		return { ok: true, result: {
			outcome: "matched",
			resolvedRef: match.candidate.ref,
			candidate: match.candidate,
			result: result.result,
			retried: number > 1,
			untrustedExternalContent: true,
		} };
	};

	return attempt(1);
}
