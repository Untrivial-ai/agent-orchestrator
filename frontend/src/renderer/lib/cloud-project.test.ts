import { QueryClient } from "@tanstack/react-query";
import { describe, expect, it, vi } from "vitest";
import { cloudProjectsQueryKey, cloudSessionsQueryKey } from "../hooks/useWorkspaceQuery";
import type { CloudCpClient } from "./cloud-cp";
import { deleteCloudProject } from "./cloud-project";

function clientWithDelete(deleteProject: CloudCpClient["deleteProject"]): CloudCpClient {
	return { deleteProject } as unknown as CloudCpClient;
}

describe("deleteCloudProject", () => {
	it("deletes the project in the control plane and refreshes the cloud queries", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const deleteProject = vi.fn().mockResolvedValue({ project: { id: "project-1", deleted: true } });

		await deleteCloudProject(queryClient, clientWithDelete(deleteProject), "org-1", "project-1");

		expect(deleteProject).toHaveBeenCalledWith("org-1", "project-1");
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: cloudProjectsQueryKey });
		expect(invalidateSpy).toHaveBeenCalledWith({ queryKey: cloudSessionsQueryKey });
	});

	it("fails without deleting anything when no organization is resolved", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const deleteProject = vi.fn();

		await expect(
			deleteCloudProject(queryClient, clientWithDelete(deleteProject), undefined, "project-1"),
		).rejects.toThrow("No cloud organization is available.");
		expect(deleteProject).not.toHaveBeenCalled();
	});

	it("surfaces a control-plane failure and leaves the cloud queries alone", async () => {
		const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
		const invalidateSpy = vi.spyOn(queryClient, "invalidateQueries").mockResolvedValue();
		const deleteProject = vi.fn().mockRejectedValue(new Error("The project could not be deleted."));

		await expect(
			deleteCloudProject(queryClient, clientWithDelete(deleteProject), "org-1", "project-1"),
		).rejects.toThrow("The project could not be deleted.");
		expect(invalidateSpy).not.toHaveBeenCalled();
	});
});
