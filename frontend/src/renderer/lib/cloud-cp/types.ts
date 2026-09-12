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

/** GET /me */
export interface CloudCpMeResponse {
	user: CloudCpUser;
	organizations: CloudCpOrganization[];
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
	runtimeState?: string;
	runtimeError?: string;
	isTerminated: boolean;
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

/** POST /orgs/{orgId}/sessions/wake responds 202 with the number of sandboxes queued to resume. */
export interface CloudCpWakeSessionsResponse {
	woken: number;
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
 * "api_key" | "access_token"; cursor accepts "api_key".
 */
export interface CloudCpPutAgentConnectionRequest {
	credentialType: string;
	/** Raw credential secret; validated then stored encrypted, never echoed back. */
	secret: string;
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
