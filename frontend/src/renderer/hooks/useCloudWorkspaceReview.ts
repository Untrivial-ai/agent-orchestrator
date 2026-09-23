import { type QueryClient, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import type {
	CloudCpClient,
	CloudCpClientEvent,
	CloudCpWorkspaceReviewDiffsRequest,
	CloudCpWorkspaceReviewFileQuery,
	CloudCpWorkspaceReviewRevisionQuery,
	CloudCpWorkspaceReviewScope,
	CloudCpWorkspaceReviewSearchQuery,
} from "../lib/cloud-cp";
import { CloudCpError } from "../lib/cloud-cp";
import { subscribeSessionEventsBridged } from "../lib/cloud-cp/stream-bridge";

export const cloudWorkspaceReviewQueryKey = (baseUrl: string, orgId: string, sessionId: string) =>
	["cloud-workspace-review", baseUrl, orgId, sessionId] as const;

export function cloudWorkspaceReviewSummaryQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	enabled?: boolean;
	visible?: boolean;
}) {
	const { client, baseUrl, orgId, sessionId } = args;
	return {
		queryKey: [...cloudWorkspaceReviewQueryKey(baseUrl, orgId, sessionId), "summary"] as const,
		queryFn: () => client.getWorkspaceReview(orgId, sessionId),
		enabled: args.enabled ?? true,
		refetchInterval: args.visible === false ? false as const : 5_000,
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewTreeQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	path?: string;
}) {
	return {
		queryKey: [...cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId), "tree", args.path ?? ""] as const,
		queryFn: () => args.client.getWorkspaceReviewTree(args.orgId, args.sessionId, args.path),
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewSearchQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	query: CloudCpWorkspaceReviewSearchQuery;
}) {
	return {
		queryKey: [...cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId), "search", args.query.query, args.query.cursor ?? "", args.query.limit ?? 50] as const,
		queryFn: () => args.client.searchWorkspaceReview(args.orgId, args.sessionId, args.query),
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewFileQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	path: string;
	scope?: CloudCpWorkspaceReviewScope;
	commitSha?: string;
}) {
	const query: CloudCpWorkspaceReviewFileQuery = { path: args.path, scope: args.scope, commitSha: args.commitSha };
	return {
		queryKey: [...cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId), "file", args.scope ?? "combined", args.commitSha ?? "", args.path] as const,
		queryFn: () => args.client.getWorkspaceReviewFile(args.orgId, args.sessionId, query),
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewDiffsQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	paths: readonly string[];
	scope: CloudCpWorkspaceReviewScope;
	contextLines?: number;
	ignoreWhitespace?: boolean;
	workspaceVersion?: string;
	commitSha?: string;
}) {
	const body: CloudCpWorkspaceReviewDiffsRequest = {
		scope: args.scope,
		paths: [...args.paths],
		contextLines: args.contextLines ?? 3,
		ignoreWhitespace: args.ignoreWhitespace ?? false,
		workspaceVersion: args.workspaceVersion,
		commitSha: args.commitSha,
	};
	return {
		queryKey: [
			...cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId), "diffs", args.scope,
			args.commitSha ?? "", [...args.paths], body.contextLines, body.ignoreWhitespace, args.workspaceVersion ?? "",
		] as const,
		queryFn: () => args.client.getWorkspaceReviewDiffs(args.orgId, args.sessionId, body),
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewRevisionQueryOptions(args: {
	client: CloudCpClient;
	baseUrl: string;
	orgId: string;
	sessionId: string;
	query: CloudCpWorkspaceReviewRevisionQuery;
}) {
	return {
		queryKey: [
			...cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId), "revision", args.query.scope ?? "combined",
			args.query.commitSha ?? "", args.query.side ?? "after", args.query.path, args.query.workspaceVersion ?? "", args.query.expectedRevision ?? "",
		] as const,
		queryFn: () => args.client.getWorkspaceReviewRevision(args.orgId, args.sessionId, args.query),
		retry: cloudWorkspaceReviewRetry,
	};
}

export function cloudWorkspaceReviewRetry(failureCount: number, error: Error): boolean {
	if (error instanceof CloudCpError && error.status === 409) return false;
	return failureCount < 2;
}

export async function invalidateCloudWorkspaceReviewEvent(
	queryClient: QueryClient,
	queryKey: ReturnType<typeof cloudWorkspaceReviewQueryKey>,
	event: CloudCpClientEvent,
): Promise<void> {
	if (event.type !== "workspace.changed") return;
	await queryClient.invalidateQueries({ queryKey });
}

export function useCloudWorkspaceReviewEvents(args: {
	baseUrl: string;
	orgId?: string;
	sessionId: string;
	enabled: boolean;
}): void {
	const queryClient = useQueryClient();
	useEffect(() => {
		if (!args.enabled || !args.orgId || !args.baseUrl) return;
		const abort = new AbortController();
		const key = cloudWorkspaceReviewQueryKey(args.baseUrl, args.orgId, args.sessionId);
		void subscribeSessionEventsBridged({
			baseUrl: args.baseUrl,
			orgId: args.orgId,
			sessionId: args.sessionId,
			signal: abort.signal,
			onEvent: (event) => { void invalidateCloudWorkspaceReviewEvent(queryClient, key, event); },
		});
		return () => abort.abort();
	}, [args.baseUrl, args.enabled, args.orgId, args.sessionId, queryClient]);
}
