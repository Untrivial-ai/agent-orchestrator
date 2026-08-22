export interface CloudWorkspace {
	id: string;
	orgId: string;
	repositoryUrl: string;
	repositoryRef?: string;
	sandboxId?: string;
	state: "pending" | "provisioning" | "ready" | "failed";
	error?: string;
	createdAt: string;
	updatedAt: string;
}

export interface CloudWorkspaceResponse {
	workspace: CloudWorkspace;
	previewUrl?: string;
}
