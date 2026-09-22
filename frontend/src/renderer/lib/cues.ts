import type { components } from "../../api/schema";
import { apiClient, apiErrorMessage } from "./api-client";

export type CueDTO = components["schemas"]["CueResponse"];
export type CueInput = components["schemas"]["CueDefinitionRequest"];
export type CueInvokeResult = components["schemas"]["InvokeCueResponse"];

export const CUE_LIMITS = {
	name: 64,
	description: 240,
	command: 4096,
	prompt: 16384,
} as const;

export const projectCuesQueryKey = (projectId: string) => ["cues", projectId] as const;

export async function fetchProjectCues(projectId: string): Promise<CueDTO[]> {
	const { data, error } = await apiClient.GET("/api/v1/projects/{projectId}/cues", {
		params: { path: { projectId } },
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not load cues"));
	return data?.cues ?? [];
}

export async function createCue(projectId: string, input: CueInput): Promise<CueDTO> {
	const { data, error } = await apiClient.POST("/api/v1/projects/{projectId}/cues", {
		params: { path: { projectId } },
		body: input,
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not create cue"));
	return data.cue;
}

export async function updateCue(cueId: string, input: CueInput): Promise<CueDTO> {
	const { data, error } = await apiClient.PATCH("/api/v1/cues/{cueId}", {
		params: { path: { cueId } },
		body: input,
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not update cue"));
	return data.cue;
}

export async function deleteCue(cueId: string): Promise<void> {
	const { error } = await apiClient.DELETE("/api/v1/cues/{cueId}", {
		params: { path: { cueId } },
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not delete cue"));
}

export async function invokeCue(cueId: string, sessionId?: string, shell?: string): Promise<CueInvokeResult> {
	const body: components["schemas"]["InvokeCueRequest"] = {};
	if (sessionId !== undefined) body.sessionId = sessionId;
	if (shell !== undefined) body.shell = shell;
	const { data, error } = await apiClient.POST("/api/v1/cues/{cueId}/invoke", {
		params: { path: { cueId } },
		body,
	});
	if (error) throw new Error(apiErrorMessage(error, "Could not run cue"));
	return data;
}
