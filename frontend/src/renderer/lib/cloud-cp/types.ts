// Typed contracts for the cloud control plane's renderer-facing v0 surface
// (`/api/cloud/v1/*`, WorkOS Bearer auth). Every shape below is derived from
// the Go handler request/response structs in `cloud/internal/httpapi` — do not
// add fields the control plane does not serve. Go `time.Time` fields arrive as
// RFC 3339 strings and are typed as `string` here.

/** Error envelope the control plane writes for every non-2xx response (`transport.go` errorEnvelope). */
export interface CloudCpErrorEnvelope {
	error: string;
	code: string;
	message: string;
	requestId: string;
	details?: Record<string, unknown>;
}

// ---------------------------------------------------------------------------
// Account (`auth_handlers.go`)
// ---------------------------------------------------------------------------

export interface CloudCpUser {
	id: string;
	email: string;
	displayName: string;
	authProvider: string;
}

export interface CloudCpOrganization {
	id: string;
	slug: string;
	displayName: string;
	role: string;
}

/** Sandbox providers a control plane offers (`/me` sandboxProviders). */
export interface CloudCpSandboxProviders {
	/** Every provider a session may select on this control plane. */
	available: string[];
	/** The provider used when a session does not specify one. */
	default: string;
}

/** GET /me */
export interface CloudCpMeResponse {
	user: CloudCpUser;
	organizations: CloudCpOrganization[];
	/**
	 * Present when the control plane reports its providers. A single-provider
	 * deployment lists exactly one available provider (the default).
	 */
	sandboxProviders?: CloudCpSandboxProviders;
}

// ---------------------------------------------------------------------------
// Organizations and invitations (`org_handlers.go`)
// ---------------------------------------------------------------------------

/** POST /orgs */
export interface CloudCpCreateOrganizationRequest {
	/** Workspace name, 1-80 characters. */
	displayName: string;
}

export interface CloudCpCreateOrganizationResponse {
	organization: CloudCpOrganization;
}

export interface CloudCpInvitation {
	id: string;
	orgId: string;
	email: string;
	invitedByEmail?: string;
	invitedByName?: string;
	role: string;
	status: string;
	expiresAt: string;
	acceptedAt?: string;
	declinedAt?: string;
	revokedAt?: string;
	createdAt: string;
	updatedAt: string;
}

/** GET /invitations */
export interface CloudCpInvitationsResponse {
	invitations: CloudCpInvitation[];
}

// ---------------------------------------------------------------------------
// Pagination (`resource_handlers.go` pageInfo / transport.go cursors)
// ---------------------------------------------------------------------------

export interface CloudCpPageInfo {
	hasMore: boolean;
	/** Opaque cursor for the next page; present only when `hasMore` is true. */
	nextCursor?: string;
}

export interface CloudCpListQuery {
	/** Page size, 1-100 (control-plane default: 50). */
	limit?: number;
	/** Opaque cursor from a previous page's `page.nextCursor`. */
	cursor?: string;
}

// ---------------------------------------------------------------------------
// Projects (`resource_handlers.go`)
// ---------------------------------------------------------------------------

export interface CloudCpProject {
	id: string;
	orgId: string;
	displayName: string;
	repositoryUrl: string;
	defaultBranch: string;
	githubRepositoryId?: string;
	config: Record<string, unknown>;
	createdAt: string;
	updatedAt: string;
}

/** POST /orgs/{orgId}/projects (requires an Idempotency-Key header). */
export interface CloudCpCreateProjectRequest {
	/** 1-120 characters. */
	displayName: string;
	/** Must be an https URL. */
	repositoryUrl: string;
	/** 1-255 characters. */
	defaultBranch: string;
	config?: Record<string, unknown>;
}

/** PATCH /orgs/{orgId}/projects/{projectId} */
export interface CloudCpUpdateProjectRequest {
	displayName: string;
	defaultBranch: string;
}

export interface CloudCpProjectResponse {
	project: CloudCpProject;
}

export interface CloudCpProjectListResponse {
	items: CloudCpProject[];
	page: CloudCpPageInfo;
}

/** DELETE /orgs/{orgId}/projects/{projectId} responds 202: archive is asynchronous. */
export interface CloudCpProjectDeletedResponse {
	project: {
		id: string;
		deleted: boolean;
	};
}

// ---------------------------------------------------------------------------
// Sessions (`resource_handlers.go`)
// ---------------------------------------------------------------------------

export type CloudCpSessionKind = "worker" | "orchestrator";

export type CloudCpSessionMode = "read-only" | "standard" | "trusted";

/** POST /orgs/{orgId}/sessions (requires an Idempotency-Key header). */
export interface CloudCpCreateSessionRequest {
	projectId: string;
	kind: CloudCpSessionKind;
	/** Coding-agent harness identifier (e.g. "claude-code"), 1-120 characters. */
	harness: string;
	/** 1-80 characters. */
	displayName: string;
	/** Up to 65536 bytes. */
	prompt: string;
	/** Defaults to "trusted" on the control plane when omitted. */
	mode?: CloudCpSessionMode;
	deniedCommands?: string[];
	sandboxProviderConnectionId?: string;
	/**
	 * Sandbox provider for this session. Optional: omitted uses the control
	 * plane default. When set it must be one of `sandboxProviders.available`
	 * from `/me`.
	 */
	provider?: string;
}

export interface CloudCpSession {
	id: string;
	orgId: string;
	projectId: string;
	kind: string;
	harness: string;
	displayName: string;
	branch: string;
	mode: string;
	deniedCommands: string[];
	activityState: string;
	status: string;
	runtimeConnected: boolean;
	sandboxProvider?: string;
	desiredState?: string;
	observedState?: string;
	runtimeState?: string;
	runtimeError?: string;
	isTerminated: boolean;
	/**
	 * Highest worker epoch the session has minted for its agent terminal. It
	 * advances on every fresh worker connection (resume from idle-pause,
	 * restore, re-provision), so the terminal can key on it and re-attach to the
	 * live agent instead of the dead epoch's exited terminal. Absent/0 when no
	 * worker has connected yet.
	 */
	workerEpoch?: number;
	createdAt: string;
	updatedAt: string;
}

export interface CloudCpSessionResponse {
	session: CloudCpSession;
}

export interface CloudCpSessionListResponse {
	items: CloudCpSession[];
	page: CloudCpPageInfo;
}

// ---------------------------------------------------------------------------
// Docker workspace review (`workspace_handlers.go`)
// ---------------------------------------------------------------------------

/** One changed file in a cloud workspace. */
export interface CloudCpWorkspaceDiffFile {
	path: string;
	status: "unmodified" | "modified" | "added" | "deleted" | "renamed" | "untracked" | "copied" | "changed";
	additions: number;
	deletions: number;
	binary: boolean;
}

/** Changed-file summary, compared with the session's HEAD. */
export interface CloudCpWorkspaceDiff {
	files: CloudCpWorkspaceDiffFile[];
	diffBaseRef: string;
	diffBaseSha?: string;
	truncated: { combined: boolean; stats: boolean };
}

/** Selected-file review details. */
export interface CloudCpWorkspaceDiffFileDetail extends CloudCpWorkspaceDiffFile {
	size: number;
	deleted: boolean;
	content: string;
	contentTruncated: boolean;
	diff: string;
	diffTruncated: boolean;
}

// ---------------------------------------------------------------------------
// Provider-neutral workspace review (`workspace_review_handlers.go`)
// ---------------------------------------------------------------------------

export type CloudCpWorkspaceReviewScope = "combined" | "committed" | "staged" | "unstaged" | "untracked";
export type CloudCpWorkspaceReviewStatus = "unmodified" | "modified" | "added" | "deleted" | "renamed" | "copied" | "untracked";
export type CloudCpWorkspaceReviewSide = "before" | "after";

export interface CloudCpWorkspaceReviewFileSummary {
	path: string;
	previousPath?: string;
	status: CloudCpWorkspaceReviewStatus;
	additions: number;
	deletions: number;
	size: number;
	binary: boolean;
	editable: boolean;
	fileFingerprint: string;
}

export interface CloudCpWorkspaceReviewSections {
	staged: CloudCpWorkspaceReviewFileSummary[];
	unstaged: CloudCpWorkspaceReviewFileSummary[];
	untracked: CloudCpWorkspaceReviewFileSummary[];
	committed: CloudCpWorkspaceReviewFileSummary[];
}

export interface CloudCpWorkspaceReviewCommit {
	sha: string;
	subject: string;
	author: string;
	timestamp: string;
	files: CloudCpWorkspaceReviewFileSummary[];
}

export interface CloudCpWorkspaceReviewResponse {
	workspaceVersion: string;
	compareBaseSha?: string;
	compareBaseRef?: string;
	compareMode?: "base" | "head_fallback";
	files: CloudCpWorkspaceReviewFileSummary[];
	truncated: boolean;
	sections: CloudCpWorkspaceReviewSections;
	commits: CloudCpWorkspaceReviewCommit[];
	summary: { files: number; additions: number; deletions: number };
	ahead?: number;
	behind?: number;
}

export interface CloudCpWorkspaceReviewFileQuery {
	path: string;
	scope?: CloudCpWorkspaceReviewScope;
	commitSha?: string;
}

export interface CloudCpWorkspaceReviewFileResponse extends CloudCpWorkspaceReviewFileSummary {
	deleted: boolean;
	imageMediaType?: string;
	content: string;
	contentTruncated: boolean;
	diff: string;
	diffTruncated: boolean;
	compareBaseSha?: string;
	compareBaseRef?: string;
	compareMode?: "base" | "head_fallback";
	workspaceVersion: string;
	historical?: boolean;
}

export interface CloudCpWorkspaceReviewDiffsRequest {
	scope: CloudCpWorkspaceReviewScope;
	paths: string[];
	contextLines: number;
	ignoreWhitespace: boolean;
	workspaceVersion?: string;
	commitSha?: string;
}

export interface CloudCpWorkspaceReviewDiffsResponse {
	workspaceVersion: string;
	groups: Array<{
		repository?: string;
		patch: string;
		truncated: boolean;
		includedPaths: string[];
		deferred: Array<{ path: string; reason: "binary" | "oversized" | "generated" | "long_line" | "budget_exceeded" }>;
		errors: Array<{ code: string; message: string }>;
	}>;
}

export interface CloudCpWorkspaceReviewRevisionQuery {
	path: string;
	scope?: CloudCpWorkspaceReviewScope;
	side?: CloudCpWorkspaceReviewSide;
	workspaceVersion?: string;
	expectedRevision?: string;
	commitSha?: string;
}

export interface CloudCpWorkspaceReviewRevisionResponse {
	path: string;
	side: CloudCpWorkspaceReviewSide;
	revision?: string;
	workspaceVersion: string;
	mediaType?: string;
	encoding?: string;
	size: number;
	exists: boolean;
	binary: boolean;
	truncated: boolean;
	content: string;
}

export interface CloudCpWorkspaceReviewTreeResponse {
	path: string;
	entries: Array<{
		name: string;
		path: string;
		type: "file" | "dir";
		status?: CloudCpWorkspaceReviewStatus;
		hasChanges?: boolean;
		size?: number;
		binary?: boolean;
	}>;
	truncated: boolean;
}

export interface CloudCpWorkspaceReviewSearchQuery {
	query: string;
	cursor?: string;
	limit?: number;
}

export interface CloudCpWorkspaceReviewSearchResponse {
	query: string;
	results: Array<Pick<CloudCpWorkspaceReviewFileSummary, "path" | "status" | "size" | "binary" | "fileFingerprint">>;
	nextCursor?: string;
	truncated: boolean;
}

export interface CloudCpWorkspaceReviewWriteRequest {
	path: string;
	content: string;
	expectedFileFingerprint: string;
}

export interface CloudCpWorkspaceReviewWriteResponse {
	path: string;
	content: string;
	size: number;
	fileFingerprint: string;
	workspaceVersion: string;
}

/** One pull request on a children listing (GET .../sessions/{id}/children). */
export interface CloudCpSessionPullRequest {
	url: string;
	number: number;
	state: "draft" | "open" | "merged" | "closed";
	ci: string;
	review: string;
	mergeability: string;
	/** Always false today: the control plane does not track unresolved comments yet. */
	reviewComments: boolean;
	sourceBranch?: string;
	targetBranch?: string;
	updatedAt: string;
}

/** A child session as listed under its orchestrator, with its pull requests. */
export interface CloudCpSessionChild extends CloudCpSession {
	prs: CloudCpSessionPullRequest[];
}

export interface CloudCpSessionChildrenResponse {
	items: CloudCpSessionChild[];
	page: CloudCpPageInfo;
}

export interface CloudCpListSessionsQuery extends CloudCpListQuery {
	/** Restrict the listing to one project. */
	projectId?: string;
}

/** DELETE /orgs/{orgId}/sessions/{sessionId} responds 202: teardown is reconciler-owned. */
export interface CloudCpSessionDeletedResponse {
	session: {
		id: string;
		desiredState: string;
	};
}

/** POST /orgs/{orgId}/sessions/{sessionId}/resume accepts user resume intent. */
export interface CloudCpResumeSessionResponse {
	session: {
		id: string;
		sandboxProvider: string;
		desiredState: string;
		observedState: string;
	};
}

/**
 * POST /orgs/{orgId}/sessions/{sessionId}/restore responds 202: a deleted
 * session is re-provisioned with its conversation and work intact, and the
 * reconciler owns bringing it back — the response only echoes the new intent.
 */
export interface CloudCpRestoreSessionResponse {
	session: {
		id: string;
		desiredState: string;
	};
}

// ---------------------------------------------------------------------------
// Chat events (`event_handlers.go`)
// ---------------------------------------------------------------------------

/** POST /orgs/{orgId}/sessions/{sessionId}/messages (requires an Idempotency-Key header). */
export interface CloudCpSendMessageRequest {
	/** 1-65536 bytes. */
	text: string;
}

export interface CloudCpClientEvent {
	sessionId: string;
	sequence: number;
	type: string;
	/** Raw event payload (Go `json.RawMessage`); shape depends on `type`. */
	payload: unknown;
	createdAt: string;
}

export interface CloudCpSendMessageResponse {
	event: CloudCpClientEvent;
}

/** POST /orgs/{orgId}/sessions/{sessionId}/turns/{turnId}/cancel responds 202. */
export interface CloudCpCancelTurnResponse {
	ok: boolean;
}

export interface CloudCpChatEventsQuery {
	/** Replay events with sequence strictly greater than this (0-9007199254740991). */
	after?: number;
	/** Page size, 1-500 (control-plane default: 100). */
	limit?: number;
}

/** GET /orgs/{orgId}/sessions/{sessionId}/chat-events */
export interface CloudCpChatEventsResponse {
	events: CloudCpClientEvent[];
	hasMore: boolean;
	nextAfter: number;
}

// ---------------------------------------------------------------------------
// Terminal tickets (`terminal_handlers.go`)
// ---------------------------------------------------------------------------

export type CloudCpTerminalKind = "workspace" | "agent";

/** POST /orgs/{orgId}/sessions/{sessionId}/terminal-ticket */
export interface CloudCpTerminalTicketRequest {
	kind: CloudCpTerminalKind;
}

export interface CloudCpTerminalTicketResponse {
	/** Single-use ticket redeemed by the `/api/cloud/v1/terminal` WebSocket upgrade. */
	ticket: string;
	/** Seconds until the ticket expires. */
	expiresIn: number;
	scopes: string[];
}

// ---------------------------------------------------------------------------
// Provider connections (`provider_handlers.go`)
// ---------------------------------------------------------------------------

/** Coding-agent providers the control plane accepts (`validAgentProvider`). */
export type CloudCpAgentProvider = "claude-code" | "codex" | "cursor";

/**
 * Credential types by provider (`validAgentCredentialType`):
 * claude-code accepts "api_key" | "oauth_token"; codex accepts
 * "api_key" | "access_token" | "auth_json" (the opaque result of a
 * ChatGPT subscription login); cursor accepts "api_key".
 */
export interface CloudCpPutAgentConnectionRequest {
	credentialType: string;
	/** Raw credential secret; validated then stored encrypted, never echoed back. */
	secret: string;
}

/** PUT /orgs/{orgId}/provider-connections/github-pat */
export interface CloudCpPutGitHubPATRequest {
	/** Raw GitHub personal access token; stored encrypted and never echoed. */
	secret: string;
}

/** POST /me/github-pat/validate-saved-repository */
export interface CloudCpValidateRepositoryAccessRequest {
	repositoryUrl: string;
}

export interface CloudCpValidateRepositoryAccessResponse {
	writeAccess: boolean;
}

export interface CloudCpProviderConnection {
	id: string;
	provider: string;
	label: string;
	config: Record<string, unknown>;
	validationState: string;
	validatedAt?: string;
	createdAt: string;
	updatedAt: string;
}

/** GET /orgs/{orgId}/provider-connections */
export interface CloudCpProviderConnectionsResponse {
	providerConnections: CloudCpProviderConnection[];
}

/** PUT /orgs/{orgId}/provider-connections/agents/{agent} */
export interface CloudCpProviderConnectionResponse {
	providerConnection: CloudCpProviderConnection;
}
