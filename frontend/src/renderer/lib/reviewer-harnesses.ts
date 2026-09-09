import type { components } from "../../api/schema";

// Reviewers are a narrower vocabulary than worker agents on purpose: a
// reviewer-only tool must not become a valid worker, and the daemon rejects
// anything outside this set.
//
// The set itself comes from the daemon rather than being maintained here. The
// review trigger's request schema is generated from domain.AllReviewerHarnesses,
// so this union IS the server's list; the array below is checked both directions
// to prevent hiding newly-added reviewers.
export type ReviewerHarnessId = NonNullable<components["schemas"]["TriggerReviewRequest"]["harness"]>;

const REVIEWER_HARNESS_IDS = [
	"agy",
	"aider",
	"amp",
	"auggie",
	"autohand",
	"claude-code",
	"codex",
	"cline",
	"copilot",
	"crush",
	"cursor",
	"devin",
	"droid",
	"grok",
	"kilocode",
	"kiro",
	"kimi",
	"kimchi",
	"muse",
	"opencode",
	"pi",
	"qwen",
	"vibe",
] as const satisfies readonly ReviewerHarnessId[];

type UnlistedReviewerHarness = Exclude<ReviewerHarnessId, (typeof REVIEWER_HARNESS_IDS)[number]>;
const _everyReviewerHarnessIsListed: UnlistedReviewerHarness extends never ? true : never = true;
void _everyReviewerHarnessIsListed;

export const KNOWN_REVIEWER_HARNESS_IDS: ReadonlySet<string> = new Set(REVIEWER_HARNESS_IDS);

export function toReviewerHarnessId(value?: string): ReviewerHarnessId | undefined {
	return value && KNOWN_REVIEWER_HARNESS_IDS.has(value) ? (value as ReviewerHarnessId) : undefined;
}
