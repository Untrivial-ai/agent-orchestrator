import type { CloudCpClient } from "./cloud-cp";
import { cloudWorkspaceReviewSearchQueryOptions } from "../hooks/useCloudWorkspaceReview";
import { sessionWorkspaceSearchQueryOptions } from "../hooks/useSessionWorkspaceFiles";
import type { WorkspaceFileSearchItem, WorkspaceFileSearchTarget } from "./command-palette";

export const COMMAND_PALETTE_FILE_SEARCH_LIMIT = 20;

export type CommandPaletteFileSearchResponse = {
	results: WorkspaceFileSearchItem[];
	truncated: boolean;
};

type CommandPaletteFileSearchArgs = {
	target: WorkspaceFileSearchTarget;
	query: string;
	errorMessage: string;
	cloud: {
		client: CloudCpClient;
		baseUrl: string;
		ready: boolean;
	};
};

type CommandPaletteFileSearchQueryOptions = {
	queryKey: readonly unknown[];
	queryFn: (context?: { signal?: AbortSignal }) => Promise<CommandPaletteFileSearchResponse>;
	retry?: (failureCount: number, error: Error) => boolean;
};

function normalizeSearchResponse(response: {
	results: Array<{ path: string; status?: string; binary?: boolean }>;
	truncated: boolean;
}): CommandPaletteFileSearchResponse {
	return {
		results: response.results.map((result) => ({
			path: result.path,
			...(result.status ? { status: result.status } : {}),
			...(result.binary !== undefined ? { binary: result.binary } : {}),
		})),
		truncated: response.truncated,
	};
}

export function commandPaletteFileSearchAvailable(
	target: WorkspaceFileSearchTarget | undefined,
	cloudReady: boolean,
): boolean {
	return Boolean(target && (!target.cloudOrgId || cloudReady));
}

export function commandPaletteFileSearchQueryOptions(
	args: CommandPaletteFileSearchArgs,
): CommandPaletteFileSearchQueryOptions {
	const { target } = args;
	if (target.cloudOrgId) {
		const options = cloudWorkspaceReviewSearchQueryOptions({
			client: args.cloud.client,
			baseUrl: args.cloud.baseUrl,
			orgId: target.cloudOrgId,
			sessionId: target.sessionId,
			query: { query: args.query, limit: COMMAND_PALETTE_FILE_SEARCH_LIMIT },
		});
		return {
			...options,
			queryFn: async (context: { signal?: AbortSignal } = {}) =>
				normalizeSearchResponse(await options.queryFn(context)),
		};
	}

	const options = sessionWorkspaceSearchQueryOptions(
		target.sessionId,
		args.query,
		args.errorMessage,
		target.hostId,
		COMMAND_PALETTE_FILE_SEARCH_LIMIT,
	);
	return {
		...options,
		queryFn: async (context: { signal?: AbortSignal } = {}) =>
			normalizeSearchResponse(await options.queryFn(context)),
	};
}
