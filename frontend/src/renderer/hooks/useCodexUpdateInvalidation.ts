import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect, useRef } from "react";
import { apiClient, apiErrorMessage } from "../lib/api-client";

// Keep observing daemon-owned jobs after Settings unmounts. All project/model
// queries are invalidated on every terminal update outcome, including failures
// that may have partially replaced executable files.
export function useCodexUpdateInvalidation() {
	const client = useQueryClient();
	const seen = useRef("");
	const jobs = useQuery({
		queryKey: ["agent-install-jobs"],
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/agents/install-jobs");
			if (error || !data) throw new Error(apiErrorMessage(error));
			return data.jobs;
		},
		refetchInterval: (query) => query.state.data?.some((job) => ["queued", "installing", "verifying"].includes(job.status)) ? 1_000 : 30_000,
	});
	const job = jobs.data?.find((item) => item.target === "codex" && item.method?.startsWith("update:"));
	const token = job?.finishedAt ? `${job.startedAt}:${job.finishedAt}:${job.status}` : "";
	useEffect(() => {
		if (!token || seen.current === token) return;
		seen.current = token;
		for (const queryKey of [["agent-models", "codex"], ["agent-readiness"], ["codex-update"], ["agent-installers"]]) {
			void client.invalidateQueries({ queryKey });
		}
	}, [client, token]);
}
