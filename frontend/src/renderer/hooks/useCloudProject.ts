/**
 * Resolves a project id against the cloud control plane's project list.
 *
 * A cloud project is never registered with the local daemon, so any surface
 * that would otherwise query `/api/v1/projects/{id}` has to know where the
 * project lives before it asks. The cloud contract exposes no single-project
 * read (only PATCH and DELETE on `/orgs/{orgId}/projects/{projectId}`), so the
 * already-cached list is the source of truth here; React Query dedupes it
 * across the surfaces that ask.
 */

import type { CloudCpProject } from "../lib/cloud-cp";
import { useCloudProjectsQuery } from "./useWorkspaceQuery";

export interface CloudProjectLookup {
	/** The cloud project, when this id belongs to one. */
	project: CloudCpProject | undefined;
	/** Whether the list that decides this is still in flight. */
	isResolving: boolean;
	/**
	 * Whether the id is known not to belong to a cloud project, and so is safe
	 * to look up against the local daemon. A pending list is not proof of that,
	 * which is why this is not simply the absence of a cloud project. Resolves
	 * immediately when the cloud offering is off, because the query is disabled
	 * and a local-only deployment already has its answer.
	 */
	isKnownLocal: boolean;
}

export function useCloudProject(projectId: string): CloudProjectLookup {
	const cloudProjects = useCloudProjectsQuery();
	const project = (cloudProjects.data ?? []).find((item) => item.id === projectId);
	return {
		project,
		isResolving: cloudProjects.isLoading,
		isKnownLocal: project === undefined && !cloudProjects.isLoading,
	};
}
