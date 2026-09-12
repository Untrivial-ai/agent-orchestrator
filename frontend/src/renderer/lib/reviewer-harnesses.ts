import { queryOptions } from "@tanstack/react-query";
import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "./api-client";
import { usesPreviewWorkspaceData as usePreviewData } from "./preview-mode";
import { agentLabel } from "./agent-options";

export type ReviewerHarnessInfo = components["schemas"]["ReviewerHarnessInfo"];

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
] as const satisfies readonly ReviewerHarnessId[];

type UnlistedReviewerHarness = Exclude<ReviewerHarnessId, (typeof REVIEWER_HARNESS_IDS)[number]>;
const _everyReviewerHarnessIsListed: UnlistedReviewerHarness extends never ? true : never = true;
void _everyReviewerHarnessIsListed;

export const KNOWN_REVIEWER_HARNESS_IDS: ReadonlySet<string> = new Set(REVIEWER_HARNESS_IDS);

export function toReviewerHarnessId(value?: string): ReviewerHarnessId | undefined {
	return value && KNOWN_REVIEWER_HARNESS_IDS.has(value) ? (value as ReviewerHarnessId) : undefined;
}

export function reviewerCatalogQueryOptions() {
	return queryOptions({
		queryKey: ["reviewer-catalog"] as const,
		queryFn: async (): Promise<ReviewerHarnessInfo[]> => {
			if (usePreviewData) {
				return [...KNOWN_REVIEWER_HARNESS_IDS].map((id) => ({
					id: id as ReviewerHarnessInfo["id"],
					label: agentLabel(id),
				}));
			}
			const { data, error } = await apiClient.GET("/api/v1/reviewers");
			if (error) throw new Error(apiErrorMessage(error, "Unable to load reviewer catalog"));
			return data?.reviewers ?? [];
		},
		staleTime: 60_000,
	});
}
