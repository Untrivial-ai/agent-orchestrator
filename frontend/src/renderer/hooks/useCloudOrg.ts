/**
 * Resolves the organization all renderer control-plane calls are scoped to:
 * the first org from GET /me, or — for a user with none — a freshly created
 * one (POST /orgs) named from the account. First-org is a deliberate v0
 * simplification: the UI has no org switcher yet.
 */

import { useQuery } from "@tanstack/react-query";
import type { CloudCpOrganization, CloudCpUser } from "../lib/cloud-cp";
import { useCloudCp } from "./useCloudCp";

export const cloudOrgQueryKey = ["cloud-org"] as const;

/** Workspace name for the auto-created org (the control plane caps it at 80 chars). */
export function orgDisplayNameForAccount(user: Pick<CloudCpUser, "displayName" | "email">): string {
	const name = user.displayName.trim() || user.email.split("@")[0]?.trim() || "";
	return (name === "" ? "Workspace" : name).slice(0, 80);
}

export interface UseCloudOrgResult {
	/** The resolved org, or undefined while loading / not ready / failed. */
	org: CloudCpOrganization | undefined;
	isLoading: boolean;
	error: unknown;
	/** Mirrors useCloudCp().ready so callers can gate on one hook. */
	ready: boolean;
}

export function useCloudOrg(): UseCloudOrgResult {
	const { client, ready, baseUrl } = useCloudCp();
	const query = useQuery({
		queryKey: [...cloudOrgQueryKey, baseUrl],
		enabled: ready,
		// Get-or-create is safe to re-run (a later /me returns the created org),
		// but there is no reason to hammer /me: org membership rarely changes.
		staleTime: 5 * 60_000,
		retry: 1,
		queryFn: async (): Promise<CloudCpOrganization> => {
			const me = await client.me();
			const first = me.organizations[0];
			if (first !== undefined) return first;
			const created = await client.createOrganization({
				displayName: orgDisplayNameForAccount(me.user),
			});
			return created.organization;
		},
	});
	return {
		org: query.data,
		isLoading: query.isLoading,
		error: query.error ?? undefined,
		ready,
	};
}
