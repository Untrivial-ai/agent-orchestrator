import { ipcMain } from "electron";
import { getCloudAccessToken, getCloudSession } from "./cloud-auth";
import { readClaudeCredentials } from "./claude-credentials";
import type { CloudWorkspaceResponse } from "../shared/cloud-workspace";

const CLOUD_API_URL =
	import.meta.env.VITE_AO_CLOUD_API_URL?.trim().replace(/\/+$/, "") ||
	process.env.AO_CLOUD_API_URL?.trim().replace(/\/+$/, "") ||
	"";

async function requestWorkspace(
	dataDir: string,
	path: string,
	init: RequestInit,
): Promise<CloudWorkspaceResponse> {
	const accessToken = await getCloudAccessToken(dataDir);
	const response = await fetch(`${CLOUD_API_URL}${path}`, {
		...init,
		headers: {
			Authorization: `Bearer ${accessToken}`,
			"Content-Type": "application/json",
			...init.headers,
		},
	});
	const body = (await response.json().catch(() => null)) as
		| (CloudWorkspaceResponse & { message?: string })
		| null;
	if (!response.ok || !body?.workspace) {
		throw new Error(body?.message || `AO Cloud workspace request failed (${response.status}).`);
	}
	return body;
}

export function installCloudWorkspaceIPC(getDataDir: () => string): void {
	ipcMain.handle(
		"cloud:createWorkspace",
		async (_event, input: { repositoryUrl: string; repositoryRef?: string }) => {
			const dataDir = getDataDir();
			const account = await getCloudSession(dataDir);
			const orgID = account?.organizations[0]?.id;
			if (!orgID) throw new Error("Your AO Cloud account has no active organization.");
			const claudeCredentials = await readClaudeCredentials();
			return requestWorkspace(
				dataDir,
				`/api/cloud/v1/orgs/${encodeURIComponent(orgID)}/workspaces`,
				{
					method: "POST",
					body: JSON.stringify({
						...input,
						claudeCredentialsBase64: claudeCredentials.toString("base64"),
					}),
				},
			);
		},
	);
	ipcMain.handle(
		"cloud:getWorkspace",
		(_event, input: { orgId: string; workspaceId: string }) =>
			requestWorkspace(
				getDataDir(),
				`/api/cloud/v1/orgs/${encodeURIComponent(input.orgId)}/workspaces/${encodeURIComponent(input.workspaceId)}`,
				{ method: "GET" },
			),
	);
}
