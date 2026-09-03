export type GitPushProposal = {
	approvalId: string;
	projectId: string;
	projectName: string;
	repository: string;
	remote: string;
	remoteUrl: string;
	branch: string;
	headSha: string;
	commits: string[];
	diffSummary: string;
	changedFiles: string[];
	expiresAt: string;
};

export type GitPushResult = {
	approvalId: string;
	status: "CONSUMED";
	result: string;
};

export type GitPushRequestResult =
	| { status: "CANCELLED" }
	| {
			status: "PUSHED";
			approvalId: string;
			headSha: string;
			remote: string;
			branch: string;
			result: string;
	  };
