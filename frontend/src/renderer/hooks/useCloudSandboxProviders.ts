/**
 * The sandbox providers this control plane offers (GET /me `sandboxProviders`),
 * so the settings UI can present a selector limited to what the deployment runs.
 * A control plane that predates multi-provider omits the field: it reads as an
 * empty list, and the selector has nothing to choose.
 */

import { useQuery } from "@tanstack/react-query";
import { useCloudCp } from "./useCloudCp";

export const cloudSandboxProvidersQueryKey = ["cloud-sandbox-providers"] as const;

export interface UseCloudSandboxProvidersResult {
	/** Every provider a session may select on this control plane. */
	available: string[];
	/** The provider used when a session does not specify one. */
	default: string;
	/** Mirrors useCloudCp().ready so callers can gate on one hook. */
	ready: boolean;
	isLoading: boolean;
}

export function useCloudSandboxProviders(): UseCloudSandboxProvidersResult {
	const { client, ready, baseUrl } = useCloudCp();
	const query = useQuery({
		queryKey: [...cloudSandboxProvidersQueryKey, baseUrl],
		enabled: ready,
		// Providers are a deployment property that effectively never changes while
		// the app is open, so mirror useCloudOrg's relaxed staleness.
		staleTime: 5 * 60_000,
		retry: 1,
		queryFn: async (): Promise<{ available: string[]; default: string }> => {
			const me = await client.me();
			return me.sandboxProviders ?? { available: [], default: "" };
		},
	});
	return {
		available: query.data?.available ?? [],
		default: query.data?.default ?? "",
		ready,
		isLoading: query.isLoading,
	};
}
