import { useQuery } from "@tanstack/react-query";
import { apiClient, apiErrorMessage } from "../lib/api-client";

// Resolves the worker harness a spawn would actually use for one project: the
// project's worker setup first, then the daemon-wide default. This mirrors the
// resolution the New Task composer performs, so form pickers can preselect the
// real agent instead of parking on a "project default" label. The query key
// matches the composer's project query so both share one cache entry.
export function useProjectDefaultWorker(projectId: string): string {
	const query = useQuery({
		queryKey: ["project", projectId],
		enabled: Boolean(projectId),
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/projects/{id}", {
				params: { path: { id: projectId } },
			});
			if (error) throw new Error(apiErrorMessage(error));
			if (data?.status !== "ok") throw new Error("Project config unavailable");
			return data.project as { agent?: string; config?: { worker?: { agent?: string } } };
		},
	});
	return query.data?.config?.worker?.agent || query.data?.agent || "";
}
