import { useMutation, useQuery } from "@tanstack/react-query";
import { apiClient, apiErrorMessage, hasTrustedApiBaseUrl } from "../lib/api-client";
import { captureRendererEvent } from "../lib/telemetry";

export type DetectedEditor = { id: string; name: string };

export const editorsQueryKey = ["editors"] as const;

// Which external editors are installed. Detection shells out to PATH/app-bundle
// lookups on the daemon, so the result is stable for the life of the app —
// cached indefinitely rather than polled.
export function useEditorsQuery() {
	return useQuery({
		queryKey: editorsQueryKey,
		enabled: hasTrustedApiBaseUrl(),
		staleTime: Number.POSITIVE_INFINITY,
		retry: false,
		queryFn: async (): Promise<DetectedEditor[]> => {
			const { data, error, response } = await apiClient.GET("/api/v1/editors", {});
			if (error) {
				throw new Error(apiErrorMessage(error, `Failed to list editors (${response?.status ?? "network"})`));
			}
			return data?.editors ?? [];
		},
	});
}

export type OpenInEditorInput = {
	sessionId: string;
	projectId: string;
	/** A detected editor id. Omit to let the daemon pick its preferred one. */
	editorId?: string;
	/** Workspace-relative file to focus, or "." to open the folder with no files. */
	path?: string;
};

// Open a session's workspace in an external editor. The daemon owns both the
// path resolution and the launch: the renderer never sees a filesystem path.
export function useOpenInEditor() {
	return useMutation({
		mutationFn: async ({ sessionId, projectId, editorId, path }: OpenInEditorInput) => {
			void captureRendererEvent("ao.renderer.open_in_editor_requested", {
				project_id: projectId,
				editor_id: editorId ?? "auto",
				target: path === "." ? "folder" : path ? "file" : "auto",
			});
			const { data, error, response } = await apiClient.POST("/api/v1/sessions/{sessionId}/open-editor", {
				params: { path: { sessionId } },
				body: { ...(editorId ? { editorId } : {}), ...(path ? { path } : {}) },
			});
			if (error) {
				const fallback = response ? `Failed to open editor (${response.status})` : "Failed to open editor";
				throw new Error(apiErrorMessage(error, fallback));
			}
			return data;
		},
		onSuccess: (data, input) => {
			void captureRendererEvent("ao.renderer.open_in_editor_succeeded", {
				project_id: input.projectId,
				editor_id: data?.editorId ?? "unknown",
				scope: data?.scope ?? "unknown",
				focused_file: Boolean(data?.file),
			});
		},
		onError: (_error, input) => {
			void captureRendererEvent("ao.renderer.open_in_editor_failed", { project_id: input.projectId });
		},
	});
}
