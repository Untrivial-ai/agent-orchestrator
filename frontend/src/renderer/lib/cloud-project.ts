import type { QueryClient } from "@tanstack/react-query";
import { cloudProjectsQueryKey, cloudSessionsQueryKey } from "../hooks/useWorkspaceQuery";
import type { CloudCpClient } from "./cloud-cp";

/**
 * Removes a cloud project through the control plane.
 *
 * A cloud project has no local daemon record, so the daemon's project DELETE
 * can only ever answer "project not found" for one. The control-plane DELETE
 * archives the project and queues every sandbox its sessions own for
 * reconciler teardown, so the project's compute is released with it; durable
 * session history is retained on purpose.
 */
export async function deleteCloudProject(
	queryClient: QueryClient,
	client: CloudCpClient,
	orgId: string | undefined,
	projectId: string,
): Promise<void> {
	if (orgId === undefined) throw new Error("No cloud organization is available.");
	await client.deleteProject(orgId, projectId);
	// The local workspace cache never held this project: the cloud project and
	// session queries are what render it, so refetch those to drop the project
	// and the sessions the board attached to it.
	await queryClient.invalidateQueries({ queryKey: cloudProjectsQueryKey });
	await queryClient.invalidateQueries({ queryKey: cloudSessionsQueryKey });
}
