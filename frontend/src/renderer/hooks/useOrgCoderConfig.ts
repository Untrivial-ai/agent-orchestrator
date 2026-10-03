/**
 * Reads the signed-in org's bring-your-own-Coder connection
 * (GET /orgs/{orgId}/coder-config). The config is org-scoped (it drives every
 * cloud session the org runs on its own Coder), so the query is keyed on the
 * resolved org and reads only the non-secret fields — the API token is never
 * returned by the control plane.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpOrgCoderConfig } from "../lib/cloud-cp";
import { useCloudCp } from "./useCloudCp";
import { useCloudOrg } from "./useCloudOrg";

export const orgCoderConfigQueryKey = ["cloud-org-coder-config"] as const;

export function useOrgCoderConfig() {
	const { client, ready } = useCloudCp();
	const { org } = useCloudOrg();
	const orgId = org?.id ?? "";
	return useQuery({
		queryKey: [...orgCoderConfigQueryKey, orgId],
		enabled: ready && orgId !== "",
		staleTime: 60_000,
		queryFn: async (): Promise<CloudCpOrgCoderConfig | null> =>
			// Default to null (never undefined) so React Query accepts the "no
			// config yet" case — the common state for a newly-onboarded org.
			(await client.getOrgCoderConfig(orgId)).coderConfig ?? null,
	});
}
