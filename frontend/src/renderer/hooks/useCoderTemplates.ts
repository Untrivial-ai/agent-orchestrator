/**
 * The Coder templates the picker offers for a new session
 * (GET /orgs/{orgId}/sandbox/coder/templates). Empty when the deployment does
 * not offer coder or the org is not entitled — the picker then shows only
 * "Default". Enabled only when the caller has already resolved that coder is the
 * active provider, to avoid a needless call on nodeops/docker deployments.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpCoderTemplate } from "../lib/cloud-cp/types";
import { useCloudCp } from "./useCloudCp";

export interface UseCoderTemplatesResult {
	templates: CloudCpCoderTemplate[];
	isLoading: boolean;
}

export function useCoderTemplates(orgId: string | undefined, enabled: boolean): UseCoderTemplatesResult {
	const { client, ready, baseUrl } = useCloudCp();
	const query = useQuery({
		queryKey: ["cloud-coder-templates", baseUrl, orgId],
		enabled: ready && enabled && Boolean(orgId),
		staleTime: 5 * 60_000,
		retry: 1,
		queryFn: async (): Promise<CloudCpCoderTemplate[]> => {
			const response = await client.listCoderTemplates(orgId as string);
			return response.templates ?? [];
		},
	});
	return {
		templates: query.data ?? [],
		isLoading: query.isLoading,
	};
}
