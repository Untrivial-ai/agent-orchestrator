import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "../lib/api-client";
import type { CloudInspectorTarget } from "../lib/cloud-inspector-target";
import { createRendererCloudCpClient } from "./useCloudCp";
import { isChangedWorkspaceFile, type WorkspaceFileSummary } from "./useSessionWorkspaceFiles";

export type WorkspaceTreeEntry = components["schemas"]["WorkspaceTreeEntry"];
export type WorkspaceTreeResponse = components["schemas"]["ListWorkspaceTreeResponse"];

export const sessionWorkspaceTreeQueryKey = (sessionId: string, dir: string, cloudOrgId?: string) =>
	["session-workspace-tree", sessionId, cloudOrgId ?? "local", dir] as const;

async function fetchLocalSessionWorkspaceTree(sessionId: string, dir: string, errorMessage: string): Promise<WorkspaceTreeResponse> {
	const { data, error } = await apiClient.GET("/api/v1/sessions/{sessionId}/workspace/tree", {
		params: { path: { sessionId }, query: dir ? { path: dir } : {} },
	});
	if (error) throw new Error(apiErrorMessage(error, errorMessage));
	return (data ?? { sessionId, path: dir, entries: [], truncated: false }) as WorkspaceTreeResponse;
}

async function fetchCloudSessionWorkspaceTree(
	sessionId: string,
	dir: string,
	cloud: CloudInspectorTarget,
	_errorMessage: string,
): Promise<WorkspaceTreeResponse> {
	const client = createRendererCloudCpClient(cloud.baseUrl);
	const page = await client.listWorkspaceFiles(cloud.orgId, sessionId, { path: dir, limit: 100 });
	const entries: WorkspaceTreeEntry[] = (page.items ?? []).map((item) => ({
		name: item.name,
		path: item.path,
		type: item.isDir ? "dir" : "file",
		size: item.size,
	}));
	return {
		sessionId,
		path: page.path || dir,
		entries,
		truncated: Boolean(page.page?.hasMore),
	};
}

async function fetchSessionWorkspaceTree(
	sessionId: string,
	dir: string,
	errorMessage: string,
	cloud?: CloudInspectorTarget,
): Promise<WorkspaceTreeResponse> {
	if (cloud) return fetchCloudSessionWorkspaceTree(sessionId, dir, cloud, errorMessage);
	return fetchLocalSessionWorkspaceTree(sessionId, dir, errorMessage);
}

// dir is a directory path relative to the workspace root, "" for the root.
// Unlike the changed-files list this isn't polled: the daemon only pushes a
// coarse "something changed" signal (see workspace-file-events.ts), which
// invalidates every mounted directory query by key prefix — a directory the
// user isn't currently looking at just stays stale until they revisit it.
export function sessionWorkspaceTreeQueryOptions(
	sessionId: string,
	dir: string,
	errorMessage = "Unable to load workspace tree",
	cloud?: CloudInspectorTarget,
) {
	return {
		queryKey: sessionWorkspaceTreeQueryKey(sessionId, dir, cloud?.orgId),
		queryFn: () => fetchSessionWorkspaceTree(sessionId, dir, errorMessage, cloud),
	};
}

export type TreeNode = {
	name: string;
	path: string;
	type: "file" | "dir";
	status?: WorkspaceTreeEntry["status"];
	hasChanges?: boolean;
	binary?: boolean;
	children?: TreeNode[];
};

// Synthesizes a full nested tree from the already-fetched, already-small
// changed-files list — this is the entire data source for "changed only"
// mode. It intentionally does not go through the lazy /workspace/tree
// endpoint: the changed-files list is already warm and small, and a lazy
// per-directory fetch would only add round trips for data already in hand.
export function buildChangedOnlyTree(files: WorkspaceFileSummary[]): TreeNode[] {
	return buildWorkspaceFileTree(files.filter(isChangedWorkspaceFile));
}

/** Builds a compact nested tree from a flat, already-filtered file result set. */
export function buildWorkspaceFileTree(files: Array<Pick<WorkspaceFileSummary, "path" | "status" | "binary">>): TreeNode[] {
	const root: TreeNode[] = [];
	const dirs = new Map<string, TreeNode>();

	const ensureDir = (path: string): TreeNode => {
		const existing = dirs.get(path);
		if (existing) return existing;
		const segments = path.split("/");
		const name = segments[segments.length - 1];
		const node: TreeNode = { name, path, type: "dir", hasChanges: true, children: [] };
		dirs.set(path, node);
		const parentPath = segments.slice(0, -1).join("/");
		if (parentPath) ensureDir(parentPath).children!.push(node);
		else root.push(node);
		return node;
	};

	for (const file of files) {
		const segments = file.path.split("/");
		const name = segments[segments.length - 1];
		const parentPath = segments.slice(0, -1).join("/");
		const fileNode: TreeNode = { name, path: file.path, type: "file", status: file.status, binary: file.binary };
		if (parentPath) ensureDir(parentPath).children!.push(fileNode);
		else root.push(fileNode);
	}

	const sortTree = (nodes: TreeNode[]) => {
		nodes.sort((a, b) => {
			if (a.type !== b.type) return a.type === "dir" ? -1 : 1;
			return a.name.localeCompare(b.name);
		});
		for (const node of nodes) if (node.children) sortTree(node.children);
	};
	sortTree(root);
	return root;
}
