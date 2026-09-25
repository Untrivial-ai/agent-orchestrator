// Typed client for the cloud control plane's renderer-facing v0 surface
// (`/api/cloud/v1/*`). The transport is injected — `getToken` supplies the
// WorkOS bearer token and `fetchImpl` can be `window.fetch` today or an
// Electron-main-proxied fetch later — so this module never imports Electron
// APIs. Every call attaches `Authorization: Bearer <token>`; a 401 (or a
// missing token) surfaces as `CloudCpAuthError`, any other non-2xx as
// `CloudCpError` carrying the status and the control plane's envelope fields.
// No retries in v0: callers own retry policy.

import { CloudCpAuthError, CloudCpError } from "./errors";
import { createSseFrameParser } from "./sse";
import type {
	CloudCpAgentProvider,
	CloudCpCancelTurnResponse,
	CloudCpBrowserViewerTicketResponse,
	CloudCpChatEventsQuery,
	CloudCpChatEventsResponse,
	CloudCpCoderTemplatesResponse,
	CloudCpClientEvent,
	CloudCpCreateOrganizationRequest,
	CloudCpCreateOrganizationResponse,
	CloudCpCreateProjectRequest,
	CloudCpCreateSessionRequest,
	CloudCpErrorEnvelope,
	CloudCpInvitationsResponse,
	CloudCpListQuery,
	CloudCpListSessionsQuery,
	CloudCpMeResponse,
	CloudCpProjectDeletedResponse,
	CloudCpProjectListResponse,
	CloudCpProjectResponse,
	CloudCpProviderConnectionResponse,
	CloudCpProviderConnectionsResponse,
	CloudCpGitHubReposResponse,
	CloudCpStartGitHubInstallationResponse,
	CloudCpGitHubInstallationsResponse,
	CloudCpSyncGitHubInstallationResponse,
	CloudCpGitHubUserConnection,
	CloudCpGitHubRepositoriesPage,
	CloudCpCreateGitHubProjectRequest,
	CloudCpPutAgentConnectionRequest,
	CloudCpPutGitHubPATRequest,
	CloudCpSendMessageRequest,
	CloudCpSendMessageResponse,
	CloudCpSessionChildrenResponse,
	CloudCpSessionDeletedResponse,
	CloudCpSessionListResponse,
	CloudCpResumeSessionResponse,
	CloudCpRestoreSessionResponse,
	CloudCpSessionResponse,
	CloudCpWorkspaceDiff,
	CloudCpWorkspaceDiffFileDetail,
	CloudCpWorkspaceReviewDiffsRequest,
	CloudCpWorkspaceReviewDiffsResponse,
	CloudCpWorkspaceReviewFileQuery,
	CloudCpWorkspaceReviewFileResponse,
	CloudCpWorkspaceReviewResponse,
	CloudCpWorkspaceReviewRevisionQuery,
	CloudCpWorkspaceReviewRevisionResponse,
	CloudCpWorkspaceReviewSearchQuery,
	CloudCpWorkspaceReviewSearchResponse,
	CloudCpWorkspaceReviewTreeResponse,
	CloudCpWorkspaceReviewWriteRequest,
	CloudCpWorkspaceReviewWriteResponse,
	CloudCpTerminalTicketRequest,
	CloudCpTerminalTicketResponse,
	CloudCpUpdateProjectRequest,
	CloudCpValidateRepositoryAccessRequest,
	CloudCpValidateRepositoryAccessResponse,
} from "./types";

const API_PREFIX = "/api/cloud/v1";

export interface CloudCpClientOptions {
	/** Control-plane origin, e.g. "https://cloud.example.com". A trailing slash is tolerated. */
	baseUrl: string;
	/** Returns the current bearer token, or null when signed out. Called per request. */
	getToken: () => Promise<string | null>;
	/** Transport override; defaults to the global fetch. */
	fetchImpl?: typeof fetch;
	/**
	 * Called whenever a request is rejected with 403. A 403 from the control
	 * plane means the caller is not a member of the org the request was scoped
	 * to ("You do not have access to this organization") — i.e. the app's
	 * selected org is stale (membership changed). The renderer wires this to
	 * re-resolve the current org so callers stop hammering a dead org.
	 */
	onForbidden?: () => void;
}

export interface CloudCpRequestOptions {
	signal?: AbortSignal;
}

/**
 * Options for calls the control plane requires an `Idempotency-Key` header on
 * (create project, create session, send message). A random UUID is generated
 * when the caller does not pin one; pin it to make client-side retries safe.
 */
export interface CloudCpMutationOptions extends CloudCpRequestOptions {
	idempotencyKey?: string;
}

export interface CloudCpSessionEventsOptions {
	/** Invoked once per parsed event, in stream order. */
	onEvent: (event: CloudCpClientEvent) => void;
	/** Invoked on setup or mid-stream failure. Aborts via `signal` are silent. */
	onError?: (error: CloudCpError) => void;
	/** Aborting closes the stream and resolves the subscription promise. */
	signal?: AbortSignal;
	/** Resume strictly after this sequence; omit to replay from the beginning. */
	after?: number;
}

export interface CloudCpClient {
	me(options?: CloudCpRequestOptions): Promise<CloudCpMeResponse>;
	createOrganization(
		body: CloudCpCreateOrganizationRequest,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpCreateOrganizationResponse>;
	listMyInvitations(options?: CloudCpRequestOptions): Promise<CloudCpInvitationsResponse>;

	listProjects(
		orgId: string,
		query?: CloudCpListQuery,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpProjectListResponse>;
	createProject(
		orgId: string,
		body: CloudCpCreateProjectRequest,
		options?: CloudCpMutationOptions,
	): Promise<CloudCpProjectResponse>;
	updateProject(
		orgId: string,
		projectId: string,
		body: CloudCpUpdateProjectRequest,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpProjectResponse>;
	deleteProject(
		orgId: string,
		projectId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpProjectDeletedResponse>;

	listSessions(
		orgId: string,
		query?: CloudCpListSessionsQuery,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpSessionListResponse>;
	createSession(
		orgId: string,
		body: CloudCpCreateSessionRequest,
		options?: CloudCpMutationOptions,
	): Promise<CloudCpSessionResponse>;
	getSession(orgId: string, sessionId: string, options?: CloudCpRequestOptions): Promise<CloudCpSessionResponse>;
	/** Lists the Coder templates the picker offers (empty when coder is unavailable/unentitled). */
	listCoderTemplates(orgId: string, options?: CloudCpRequestOptions): Promise<CloudCpCoderTemplatesResponse>;
	/** Lists the sessions an orchestrator spawned, with each child's pull requests. */
	listSessionChildren(
		orgId: string,
		sessionId: string,
		query?: CloudCpListQuery,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpSessionChildrenResponse>;
	deleteSession(
		orgId: string,
		sessionId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpSessionDeletedResponse>;
	resumeSession(
		orgId: string,
		sessionId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpResumeSessionResponse>;
	/** Docker-only changed-file summary for a cloud session. */
	getWorkspaceDiff(orgId: string, sessionId: string, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceDiff>;
	/** Docker-only selected-file review details for a cloud session. */
	readWorkspaceDiffFile(
		orgId: string,
		sessionId: string,
		path: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpWorkspaceDiffFileDetail>;
	getWorkspaceReview(orgId: string, sessionId: string, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewResponse>;
	getWorkspaceReviewTree(orgId: string, sessionId: string, path?: string, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewTreeResponse>;
	searchWorkspaceReview(orgId: string, sessionId: string, query: CloudCpWorkspaceReviewSearchQuery, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewSearchResponse>;
	getWorkspaceReviewFile(orgId: string, sessionId: string, query: CloudCpWorkspaceReviewFileQuery, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewFileResponse>;
	getWorkspaceReviewDiffs(orgId: string, sessionId: string, body: CloudCpWorkspaceReviewDiffsRequest, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewDiffsResponse>;
	getWorkspaceReviewRevision(orgId: string, sessionId: string, query: CloudCpWorkspaceReviewRevisionQuery, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewRevisionResponse>;
	updateWorkspaceReviewFile(orgId: string, sessionId: string, body: CloudCpWorkspaceReviewWriteRequest, options?: CloudCpRequestOptions): Promise<CloudCpWorkspaceReviewWriteResponse>;
	/** Re-provision a deleted session, keeping its conversation and work intact. */
	restoreSession(
		orgId: string,
		sessionId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpRestoreSessionResponse>;

	sendSessionMessage(
		orgId: string,
		sessionId: string,
		body: CloudCpSendMessageRequest,
		options?: CloudCpMutationOptions,
	): Promise<CloudCpSendMessageResponse>;
	cancelTurn(
		orgId: string,
		sessionId: string,
		turnId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpCancelTurnResponse>;
	listChatEvents(
		orgId: string,
		sessionId: string,
		query?: CloudCpChatEventsQuery,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpChatEventsResponse>;
	/**
	 * Subscribe to the session's live SSE stream. Resolves when the stream ends
	 * (server close or abort); failures are reported through `onError`, never as
	 * a rejection, so fire-and-forget callers cannot leak unhandled rejections.
	 */
	subscribeSessionEvents(orgId: string, sessionId: string, options: CloudCpSessionEventsOptions): Promise<void>;

	createTerminalTicket(
		orgId: string,
		sessionId: string,
		body: CloudCpTerminalTicketRequest,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpTerminalTicketResponse>;
	createBrowserViewerTicket(
		orgId: string,
		sessionId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpBrowserViewerTicketResponse>;

	listProviderConnections(
		orgId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpProviderConnectionsResponse>;
	listUserProviderConnections(options?: CloudCpRequestOptions): Promise<CloudCpProviderConnectionsResponse>;
	putAgentConnection(
		orgId: string,
		agent: CloudCpAgentProvider,
		body: CloudCpPutAgentConnectionRequest,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpProviderConnectionResponse>;
	deleteAgentConnection(orgId: string, agent: CloudCpAgentProvider, options?: CloudCpRequestOptions): Promise<void>;
	putGitHubPAT(body: CloudCpPutGitHubPATRequest, options?: CloudCpRequestOptions): Promise<CloudCpProviderConnectionResponse>;
	deleteGitHubPAT(options?: CloudCpRequestOptions): Promise<void>;
	listGitHubRepos(options?: CloudCpRequestOptions): Promise<CloudCpGitHubReposResponse>;
	validateSavedRepositoryAccess(
		body: CloudCpValidateRepositoryAccessRequest,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpValidateRepositoryAccessResponse>;

	// GitHub App connect flow. See the type comments in ./types for the flow.
	startGitHubInstallation(
		orgId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpStartGitHubInstallationResponse>;
	getGitHubUser(options?: CloudCpRequestOptions): Promise<CloudCpGitHubUserConnection>;
	listGitHubInstallations(
		orgId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpGitHubInstallationsResponse>;
	syncGitHubInstallation(
		orgId: string,
		installationId: string,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpSyncGitHubInstallationResponse>;
	listGitHubRepositories(
		orgId: string,
		query?: CloudCpListQuery,
		options?: CloudCpRequestOptions,
	): Promise<CloudCpGitHubRepositoriesPage>;
	createGitHubProject(
		orgId: string,
		body: CloudCpCreateGitHubProjectRequest,
		options?: CloudCpMutationOptions,
	): Promise<CloudCpProjectResponse>;
}

type QueryParams = Record<string, string | number | undefined>;

interface SendOptions {
	body?: unknown;
	query?: QueryParams;
	signal?: AbortSignal;
	idempotencyKey?: string;
	accept?: string;
}

function buildUrl(baseUrl: string, path: string, query?: QueryParams): string {
	let url = `${baseUrl.replace(/\/+$/, "")}${API_PREFIX}${path}`;
	if (query !== undefined) {
		const params = new URLSearchParams();
		for (const [key, value] of Object.entries(query)) {
			if (value !== undefined) params.set(key, String(value));
		}
		const search = params.toString();
		if (search !== "") url += `?${search}`;
	}
	return url;
}

/** Build one path segment from a caller-supplied identifier. */
function seg(value: string): string {
	return encodeURIComponent(value);
}

async function errorFromResponse(response: Response): Promise<CloudCpError> {
	let message = `Cloud control plane request failed with status ${response.status}.`;
	let code: string | undefined;
	let requestId: string | undefined;
	try {
		const body = (await response.json()) as Partial<CloudCpErrorEnvelope> | null;
		if (typeof body?.message === "string" && body.message !== "") {
			message = body.message;
		} else if (typeof body?.error === "string" && body.error !== "") {
			message = body.error;
		}
		if (typeof body?.code === "string" && body.code !== "") code = body.code;
		if (typeof body?.requestId === "string" && body.requestId !== "") requestId = body.requestId;
	} catch {
		// Non-JSON or empty body: keep the status-derived message.
	}
	const options = { status: response.status, code, requestId };
	return response.status === 401 ? new CloudCpAuthError(message, options) : new CloudCpError(message, options);
}

function toCloudCpError(error: unknown): CloudCpError {
	if (error instanceof CloudCpError) return error;
	const message = error instanceof Error ? error.message : String(error);
	return new CloudCpError(message, { status: 0, cause: error });
}

function isAbortError(error: unknown): boolean {
	return error instanceof Error && error.name === "AbortError";
}

function newIdempotencyKey(): string {
	return crypto.randomUUID();
}

export function createCloudCpClient(options: CloudCpClientOptions): CloudCpClient {
	const { baseUrl, getToken, onForbidden } = options;
	// Wrap the default so the global fetch is never invoked detached from its
	// realm (Chromium throws "Illegal invocation" for a bare fetch reference).
	const doFetch: typeof fetch = options.fetchImpl ?? ((input, init) => fetch(input, init));

	async function send(method: string, path: string, init: SendOptions = {}): Promise<Response> {
		const token = await getToken();
		if (token === null || token === "") {
			throw new CloudCpAuthError("No cloud control-plane token is available. Sign in and try again.", {
				status: 401,
				code: "no_token",
			});
		}
		const headers = new Headers({
			Authorization: `Bearer ${token}`,
			Accept: init.accept ?? "application/json",
		});
		if (init.body !== undefined) headers.set("Content-Type", "application/json");
		if (init.idempotencyKey !== undefined) headers.set("Idempotency-Key", init.idempotencyKey);
		const response = await doFetch(buildUrl(baseUrl, path, init.query), {
			method,
			headers,
			body: init.body === undefined ? undefined : JSON.stringify(init.body),
			signal: init.signal,
		});
		if (!response.ok) {
			// A 403 means the selected org is stale (membership changed) — notify so
			// the renderer re-resolves the current org instead of stranding every
			// org-scoped call. Fire before throwing so callers still see the error.
			if (response.status === 403) onForbidden?.();
			throw await errorFromResponse(response);
		}
		return response;
	}

	async function requestJson<T>(method: string, path: string, init: SendOptions = {}): Promise<T> {
		const response = await send(method, path, init);
		return (await response.json()) as T;
	}

	async function requestVoid(method: string, path: string, init: SendOptions = {}): Promise<void> {
		await send(method, path, init);
	}

	async function subscribeSessionEvents(
		orgId: string,
		sessionId: string,
		subscribeOptions: CloudCpSessionEventsOptions,
	): Promise<void> {
		const { onEvent, onError, signal, after } = subscribeOptions;
		const fail = (error: unknown): void => {
			if (signal?.aborted === true || isAbortError(error)) return;
			onError?.(toCloudCpError(error));
		};

		let response: Response;
		try {
			response = await send("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/events`, {
				query: { after },
				signal,
				accept: "text/event-stream",
			});
		} catch (error) {
			fail(error);
			return;
		}
		if (response.body === null) {
			fail(new CloudCpError("The event stream response has no body.", { status: response.status }));
			return;
		}

		const reader = response.body.getReader();
		const decoder = new TextDecoder();
		const parser = createSseFrameParser();
		const deliver = (data: string): void => {
			let event: CloudCpClientEvent;
			try {
				event = JSON.parse(data) as CloudCpClientEvent;
			} catch {
				fail(new CloudCpError("The event stream sent a frame with malformed JSON.", { status: 200 }));
				return;
			}
			onEvent(event);
		};
		try {
			for (;;) {
				const { done, value } = await reader.read();
				if (value !== undefined) {
					for (const frame of parser.push(decoder.decode(value, { stream: true }))) deliver(frame.data);
				}
				if (done) break;
			}
			for (const frame of parser.push(decoder.decode())) deliver(frame.data);
			for (const frame of parser.flush()) deliver(frame.data);
		} catch (error) {
			fail(error);
		} finally {
			reader.releaseLock();
		}
	}

	return {
		me: (o) => requestJson("GET", "/me", { signal: o?.signal }),
		createOrganization: (body, o) => requestJson("POST", "/orgs", { body, signal: o?.signal }),
		listMyInvitations: (o) => requestJson("GET", "/invitations", { signal: o?.signal }),

		listProjects: (orgId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/projects`, {
				query: { limit: query?.limit, cursor: query?.cursor },
				signal: o?.signal,
			}),
		createProject: (orgId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/projects`, {
				body,
				signal: o?.signal,
				idempotencyKey: o?.idempotencyKey ?? newIdempotencyKey(),
			}),
		updateProject: (orgId, projectId, body, o) =>
			requestJson("PATCH", `/orgs/${seg(orgId)}/projects/${seg(projectId)}`, { body, signal: o?.signal }),
		deleteProject: (orgId, projectId, o) =>
			requestJson("DELETE", `/orgs/${seg(orgId)}/projects/${seg(projectId)}`, { signal: o?.signal }),

		listSessions: (orgId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions`, {
				query: { projectId: query?.projectId, limit: query?.limit, cursor: query?.cursor },
				signal: o?.signal,
			}),
		createSession: (orgId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions`, {
				body,
				signal: o?.signal,
				idempotencyKey: o?.idempotencyKey ?? newIdempotencyKey(),
			}),
		getSession: (orgId, sessionId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}`, { signal: o?.signal }),
		listCoderTemplates: (orgId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sandbox/coder/templates`, { signal: o?.signal }),
		listSessionChildren: (orgId, sessionId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/children`, {
				query: { limit: query?.limit, cursor: query?.cursor },
				signal: o?.signal,
			}),
		deleteSession: (orgId, sessionId, o) =>
			requestJson("DELETE", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}`, { signal: o?.signal }),
		resumeSession: (orgId, sessionId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/resume`, {
				signal: o?.signal,
			}),
		getWorkspaceDiff: (orgId, sessionId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/diff`, {
				signal: o?.signal,
			}),
		readWorkspaceDiffFile: (orgId, sessionId, path, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/file/diff`, {
				query: { path },
				signal: o?.signal,
			}),
		getWorkspaceReview: (orgId, sessionId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/review`, { signal: o?.signal }),
		getWorkspaceReviewTree: (orgId, sessionId, path, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/tree`, {
				query: { path }, signal: o?.signal,
			}),
		searchWorkspaceReview: (orgId, sessionId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/search`, {
				query: { query: query.query, cursor: query.cursor, limit: query.limit }, signal: o?.signal,
			}),
		getWorkspaceReviewFile: (orgId, sessionId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/review/file`, {
				query: { path: query.path, scope: query.scope, commitSha: query.commitSha }, signal: o?.signal,
			}),
		getWorkspaceReviewDiffs: (orgId, sessionId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/review/diffs`, {
				body, signal: o?.signal,
			}),
		getWorkspaceReviewRevision: (orgId, sessionId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/review/revision`, {
				query: {
					path: query.path, scope: query.scope, side: query.side, workspaceVersion: query.workspaceVersion,
					expectedRevision: query.expectedRevision, commitSha: query.commitSha,
				}, signal: o?.signal,
			}),
		updateWorkspaceReviewFile: (orgId, sessionId, body, o) =>
			requestJson("PUT", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/workspace/review/file`, {
				body, signal: o?.signal,
			}),
		restoreSession: (orgId, sessionId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/restore`, {
				signal: o?.signal,
			}),

		sendSessionMessage: (orgId, sessionId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/messages`, {
				body,
				signal: o?.signal,
				idempotencyKey: o?.idempotencyKey ?? newIdempotencyKey(),
			}),
		cancelTurn: (orgId, sessionId, turnId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/turns/${seg(turnId)}/cancel`, {
				signal: o?.signal,
			}),
		listChatEvents: (orgId, sessionId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/chat-events`, {
				query: { after: query?.after, limit: query?.limit },
				signal: o?.signal,
			}),
		subscribeSessionEvents,

		createTerminalTicket: (orgId, sessionId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/terminal-ticket`, {
				body,
				signal: o?.signal,
			}),
		createBrowserViewerTicket: (orgId, sessionId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/sessions/${seg(sessionId)}/browser-view-ticket`, {
				signal: o?.signal,
			}),

		listProviderConnections: (orgId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/provider-connections`, { signal: o?.signal }),
		listUserProviderConnections: (o) => requestJson("GET", "/me/providers", { signal: o?.signal }),
		putAgentConnection: (orgId, agent, body, o) =>
			requestJson("PUT", `/orgs/${seg(orgId)}/provider-connections/agents/${seg(agent)}`, {
				body,
				signal: o?.signal,
			}),
		deleteAgentConnection: (orgId, agent, o) =>
			requestVoid("DELETE", `/orgs/${seg(orgId)}/provider-connections/agents/${seg(agent)}`, {
				signal: o?.signal,
			}),
		putGitHubPAT: (body, o) => requestJson("PUT", "/me/github-pat", { body, signal: o?.signal }),
		deleteGitHubPAT: (o) => requestVoid("DELETE", "/me/github-pat", { signal: o?.signal }),
		listGitHubRepos: (o) => requestJson("GET", "/me/github/repos", { signal: o?.signal }),
		validateSavedRepositoryAccess: (body, o) =>
			requestJson("POST", "/me/github-pat/validate-saved-repository", { body, signal: o?.signal }),

		startGitHubInstallation: (orgId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/github/installations/start`, { signal: o?.signal }),
		getGitHubUser: (o) => requestJson("GET", "/github/user", { signal: o?.signal }),
		listGitHubInstallations: (orgId, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/github/installations`, { signal: o?.signal }),
		syncGitHubInstallation: (orgId, installationId, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/github/installations/${seg(installationId)}/sync`, {
				signal: o?.signal,
			}),
		listGitHubRepositories: (orgId, query, o) =>
			requestJson("GET", `/orgs/${seg(orgId)}/github/repositories`, {
				query: { limit: query?.limit, cursor: query?.cursor },
				signal: o?.signal,
			}),
		createGitHubProject: (orgId, body, o) =>
			requestJson("POST", `/orgs/${seg(orgId)}/github/projects`, {
				body,
				signal: o?.signal,
				idempotencyKey: o?.idempotencyKey ?? newIdempotencyKey(),
			}),
	};
}
