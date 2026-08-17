import type {
  AgentProfile,
  ClientEvent,
  ClientEventPage,
  CreateWorkerChildInput,
  CreateGitHubProjectInput,
  CreateGitHubScratchProjectInput,
  CreateGitHubScratchProjectResponse,
  CreateProjectInput,
  CreateSessionInput,
  CurrentAccount,
  DeleteProjectResponse,
  DeleteSessionResponse,
  ErrorEnvelope,
  EventReplayOptions,
  GitHubInstallation,
  GitHubInstallationStart,
  GitHubRepositoryPage,
  GitHubUserAuthorizationStart,
  GitHubUserConnection,
  IdempotentRequestOptions,
  PaginationOptions,
  Project,
  ProjectPage,
  PutAgentProviderConnectionInput,
  RedactedProviderConnection,
  RequestOptions,
  Session,
  SessionPage,
  SessionPullRequests,
  SessionReviewState,
  TerminalKind,
  TerminalTicket,
  UpdateProjectInput,
  UserMessageEvent,
  WorkerBootstrapInput,
  WorkerBootstrapResponse,
  WorkerCancellationResponse,
  WorkerCheckoutGrantResponse,
  WorkerCompleteTransportInput,
  WorkerCompleteTurnInput,
  WorkerCredentialResponse,
  WorkerEventInput,
  WorkerFailTransportInput,
  WorkerFailTurnInput,
  WorkerFinishTurnResponse,
  WorkerHeartbeatInput,
  WorkerHeartbeatResponse,
  WorkerAgentTerminalResponse,
  WorkerOKResponse,
  WorkerTerminalExitInput,
  WorkerTerminalOutputInput,
  WorkerTerminalOutputResponse,
  WorkerTransportRequest,
  WorkerTurn,
  WorkspaceDiff,
  WorkspaceEntryPage,
  WorkspaceFile,
  WorkspaceFileWriteInput,
} from "./types.js";

type MaybePromise<T> = T | Promise<T>;

export interface CloudClientConfig {
  baseUrl: string;
  getAccessToken: () => MaybePromise<string | null | undefined>;
  fetch?: typeof globalThis.fetch;
}

export interface WorkerClientConfig {
  baseUrl: string;
  getWorkerToken: () => MaybePromise<string | null | undefined>;
  fetch?: typeof globalThis.fetch;
}

interface JSONRequestOptions extends RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE";
  body?: unknown;
  idempotencyKey?: string;
  cache?: RequestCache;
}

export class CloudApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId: string;
  readonly details?: Record<string, unknown>;
  readonly envelope: ErrorEnvelope;

  constructor(status: number, envelope: ErrorEnvelope) {
    super(envelope.message);
    this.name = "CloudApiError";
    this.status = status;
    this.code = envelope.code;
    this.requestId = envelope.requestId;
    this.details = envelope.details;
    this.envelope = envelope;
  }
}

export class CloudClient {
  readonly baseUrl: string;

  private readonly getAccessToken: CloudClientConfig["getAccessToken"];
  private readonly fetch: typeof globalThis.fetch;

  constructor(config: CloudClientConfig) {
    const baseUrl = new URL(config.baseUrl);
    if (baseUrl.search || baseUrl.hash) {
      throw new TypeError("Cloud API baseUrl must not contain a query or fragment.");
    }

    this.baseUrl = baseUrl.toString().replace(/\/+$/, "");
    this.getAccessToken = config.getAccessToken;
    this.fetch = config.fetch ?? globalThis.fetch.bind(globalThis);
  }

  getCurrentAccount(options: RequestOptions = {}): Promise<CurrentAccount> {
    return this.request("/api/cloud/v1/me", options);
  }

  async listAgents(
    orgId: string,
    options: RequestOptions = {},
  ): Promise<AgentProfile[]> {
    const response = await this.request<{ agents: AgentProfile[] }>(
      this.orgPath(orgId, "/agents"),
      options,
    );
    return response.agents;
  }

  listProjects(
    orgId: string,
    options: PaginationOptions = {},
  ): Promise<ProjectPage> {
    return this.request(
      this.withQuery(this.orgPath(orgId, "/projects"), {
        cursor: options.cursor,
        limit: options.limit,
      }),
      { signal: options.signal },
    );
  }

  createProject(
    orgId: string,
    input: CreateProjectInput,
    options: IdempotentRequestOptions,
  ): Promise<{ project: Project }> {
    return this.request(this.orgPath(orgId, "/projects"), {
      method: "POST",
      body: input,
      idempotencyKey: options.idempotencyKey,
      signal: options.signal,
    });
  }

  updateProject(
    orgId: string,
    projectId: string,
    input: UpdateProjectInput,
    options: RequestOptions = {},
  ): Promise<{ project: Project }> {
    return this.request(
      this.orgPath(orgId, `/projects/${encodeURIComponent(projectId)}`),
      {
        method: "PATCH",
        body: input,
        signal: options.signal,
      },
    );
  }

  deleteProject(
    orgId: string,
    projectId: string,
    options: RequestOptions = {},
  ): Promise<DeleteProjectResponse> {
    return this.request(
      this.orgPath(orgId, `/projects/${encodeURIComponent(projectId)}`),
      {
        method: "DELETE",
        signal: options.signal,
      },
    );
  }

  getGitHubUserConnection(
    options: RequestOptions = {},
  ): Promise<GitHubUserConnection> {
    return this.request("/api/cloud/v1/github/user", options);
  }

  startGitHubUserAuthorization(
    options: RequestOptions = {},
  ): Promise<GitHubUserAuthorizationStart> {
    return this.request("/api/cloud/v1/github/user/authorize", {
      method: "POST",
      signal: options.signal,
    });
  }

  disconnectGitHubUser(options: RequestOptions = {}): Promise<void> {
    return this.request("/api/cloud/v1/github/user", {
      method: "DELETE",
      signal: options.signal,
    });
  }

  async listGitHubInstallations(
    orgId: string,
    options: RequestOptions = {},
  ): Promise<GitHubInstallation[]> {
    const response = await this.request<{
      installations: GitHubInstallation[];
    }>(this.orgPath(orgId, "/github/installations"), options);
    return response.installations;
  }

  startGitHubInstallation(
    orgId: string,
    options: RequestOptions = {},
  ): Promise<GitHubInstallationStart> {
    return this.request(this.orgPath(orgId, "/github/installations/start"), {
      method: "POST",
      signal: options.signal,
    });
  }

  syncGitHubInstallation(
    orgId: string,
    installationId: string,
    options: RequestOptions = {},
  ): Promise<{ installation: GitHubInstallation }> {
    return this.request(
      this.orgPath(
        orgId,
        `/github/installations/${encodeURIComponent(installationId)}/sync`,
      ),
      { method: "POST", signal: options.signal },
    );
  }

  disconnectGitHubInstallation(
    orgId: string,
    installationId: string,
    options: RequestOptions = {},
  ): Promise<{ installation: GitHubInstallation }> {
    return this.request(
      this.orgPath(
        orgId,
        `/github/installations/${encodeURIComponent(installationId)}/disconnect`,
      ),
      { method: "POST", signal: options.signal },
    );
  }

  listGitHubRepositories(
    orgId: string,
    options: PaginationOptions = {},
  ): Promise<GitHubRepositoryPage> {
    return this.request(
      this.withQuery(this.orgPath(orgId, "/github/repositories"), {
        cursor: options.cursor,
        limit: options.limit,
      }),
      { signal: options.signal },
    );
  }

  createProjectFromGitHub(
    orgId: string,
    input: CreateGitHubProjectInput,
    options: IdempotentRequestOptions,
  ): Promise<{ project: Project }> {
    return this.request(this.orgPath(orgId, "/github/projects"), {
      method: "POST",
      body: input,
      idempotencyKey: options.idempotencyKey,
      signal: options.signal,
    });
  }

  createGitHubScratchProject(
    orgId: string,
    input: CreateGitHubScratchProjectInput,
    options: IdempotentRequestOptions,
  ): Promise<CreateGitHubScratchProjectResponse> {
    return this.request(this.orgPath(orgId, "/projects/scratch"), {
      method: "POST",
      body: input,
      idempotencyKey: options.idempotencyKey,
      signal: options.signal,
    });
  }

  listSessions(
    orgId: string,
    options: PaginationOptions & { projectId?: string } = {},
  ): Promise<SessionPage> {
    return this.request(
      this.withQuery(this.orgPath(orgId, "/sessions"), {
        cursor: options.cursor,
        limit: options.limit,
        projectId: options.projectId,
      }),
      { signal: options.signal },
    );
  }

  getSession(
    orgId: string,
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<{ session: Session }> {
    return this.request(
      this.orgPath(orgId, `/sessions/${encodeURIComponent(sessionId)}`),
      options,
    );
  }

  createSession(
    orgId: string,
    input: CreateSessionInput,
    options: IdempotentRequestOptions,
  ): Promise<{ session: Session }> {
    return this.request(this.orgPath(orgId, "/sessions"), {
      method: "POST",
      body: input,
      idempotencyKey: options.idempotencyKey,
      signal: options.signal,
    });
  }

  deleteSession(
    orgId: string,
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<DeleteSessionResponse> {
    return this.request(
      this.orgPath(orgId, `/sessions/${encodeURIComponent(sessionId)}`),
      { method: "DELETE", signal: options.signal },
    );
  }

  listSessionPullRequests(
    orgId: string,
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<SessionPullRequests> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/pull-requests`,
      ),
      options,
    );
  }

  getSessionReviewState(
    orgId: string,
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<SessionReviewState> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/reviews`,
      ),
      options,
    );
  }

  sendMessage(
    orgId: string,
    sessionId: string,
    text: string,
    options: IdempotentRequestOptions,
  ): Promise<{ event: UserMessageEvent }> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/messages`,
      ),
      {
        method: "POST",
        body: { text },
        idempotencyKey: options.idempotencyKey,
        signal: options.signal,
      },
    );
  }

  cancelTurn(
    orgId: string,
    sessionId: string,
    turnId: string,
    options: IdempotentRequestOptions,
  ): Promise<WorkerOKResponse> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/turns/${encodeURIComponent(turnId)}/cancel`,
      ),
      {
        method: "POST",
        idempotencyKey: options.idempotencyKey,
        signal: options.signal,
      },
    );
  }

  replayEvents(
    orgId: string,
    sessionId: string,
    options: EventReplayOptions = {},
  ): Promise<ClientEventPage> {
    const path = this.orgPath(
      orgId,
      `/sessions/${encodeURIComponent(sessionId)}/chat-events`,
    );
    return this.request(
      this.withQuery(path, {
        after: options.after ?? 0,
        limit: options.limit,
      }),
      { signal: options.signal },
    );
  }

  async *streamEvents(
    orgId: string,
    sessionId: string,
    options: Omit<EventReplayOptions, "limit"> = {},
  ): AsyncGenerator<ClientEvent, void, void> {
    const endpoint = this.orgPath(
      orgId,
      `/sessions/${encodeURIComponent(sessionId)}/events`,
    );
    let after = options.after ?? 0;
    let retryAttempt = 0;

    while (!options.signal?.aborted) {
      let receivedEvent = false;
      try {
        const path = this.withQuery(endpoint, { after });
        const response = await this.authorizedFetch(path, {
          headers: { Accept: "text/event-stream" },
          signal: options.signal,
        });
        await this.throwIfError(response);
        if (!response.body) {
          throw this.invalidResponse(
            response.status,
            "Cloud event stream returned no response body.",
          );
        }

        const reader = response.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        try {
          while (!options.signal?.aborted) {
            const { done, value } = await reader.read();
            buffer += decoder.decode(value, { stream: !done });
            buffer = buffer.replaceAll("\r\n", "\n");

            let boundary = buffer.indexOf("\n\n");
            while (boundary >= 0) {
              const event = parseSSEBlock(buffer.slice(0, boundary));
              buffer = buffer.slice(boundary + 2);
              if (event && event.sequence > after) {
                after = event.sequence;
                receivedEvent = true;
                yield event;
              }
              boundary = buffer.indexOf("\n\n");
            }

            if (done) {
              const event = parseSSEBlock(buffer);
              if (event && event.sequence > after) {
                after = event.sequence;
                receivedEvent = true;
                yield event;
              }
              break;
            }
          }
        } finally {
          await reader.cancel().catch(() => undefined);
          reader.releaseLock();
        }
      } catch (error) {
        if (options.signal?.aborted) return;
        if (!isRetryableStreamError(error)) throw error;
      }

      if (options.signal?.aborted) return;
      retryAttempt = receivedEvent ? 0 : retryAttempt + 1;
      await waitForRetry(retryAttempt, options.signal);
    }
  }

  createTerminalTicket(
    orgId: string,
    sessionId: string,
    kind: TerminalKind = "workspace",
    options: RequestOptions = {},
  ): Promise<TerminalTicket> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/terminal-ticket`,
      ),
      { method: "POST", body: { kind }, signal: options.signal },
    );
  }

  terminalUrl(
    ticket: string,
    options: { after?: number; kind?: TerminalKind } = {},
  ): string {
    const url = new URL(`${this.baseUrl}/api/cloud/v1/terminal`);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.searchParams.set("ticket", ticket);
    url.searchParams.set("after", String(options.after ?? 0));
    url.searchParams.set("kind", options.kind ?? "workspace");
    return url.toString();
  }

  listWorkspaceFiles(
    orgId: string,
    sessionId: string,
    path = "",
    options: PaginationOptions = {},
  ): Promise<WorkspaceEntryPage> {
    const endpoint = this.orgPath(
      orgId,
      `/sessions/${encodeURIComponent(sessionId)}/workspace/files`,
    );
    return this.request(
      this.withQuery(endpoint, {
        path,
        cursor: options.cursor,
        limit: options.limit,
      }),
      { signal: options.signal },
    );
  }

  readWorkspaceFile(
    orgId: string,
    sessionId: string,
    path: string,
    options: RequestOptions = {},
  ): Promise<WorkspaceFile> {
    const endpoint = this.orgPath(
      orgId,
      `/sessions/${encodeURIComponent(sessionId)}/workspace/file`,
    );
    return this.request(this.withQuery(endpoint, { path }), options);
  }

  writeWorkspaceFile(
    orgId: string,
    sessionId: string,
    input: WorkspaceFileWriteInput,
    options: RequestOptions = {},
  ): Promise<WorkspaceFile> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/workspace/file`,
      ),
      { ...options, method: "PUT", body: input },
    );
  }

  getWorkspaceDiff(
    orgId: string,
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<WorkspaceDiff> {
    return this.request(
      this.orgPath(
        orgId,
        `/sessions/${encodeURIComponent(sessionId)}/workspace/diff`,
      ),
      options,
    );
  }

  async listProviderConnections(
    orgId: string,
    options: RequestOptions = {},
  ): Promise<RedactedProviderConnection[]> {
    const response = await this.request<{
      providerConnections: RedactedProviderConnection[];
    }>(this.orgPath(orgId, "/provider-connections"), options);
    return response.providerConnections;
  }

  putAgentProviderConnection(
    orgId: string,
    provider: "claude-code" | "codex" | "cursor",
    input: PutAgentProviderConnectionInput,
    options: RequestOptions = {},
  ): Promise<{ providerConnection: RedactedProviderConnection }> {
    return this.request(
      this.orgPath(
        orgId,
        `/provider-connections/agents/${encodeURIComponent(provider)}`,
      ),
      {
        method: "PUT",
        body: input,
        signal: options.signal,
      },
    );
  }

  async deleteAgentProviderConnection(
    orgId: string,
    provider: "claude-code" | "codex" | "cursor",
    options: RequestOptions = {},
  ): Promise<void> {
    const response = await this.authorizedFetch(
      this.orgPath(
        orgId,
        `/provider-connections/agents/${encodeURIComponent(provider)}`,
      ),
      {
        method: "DELETE",
        headers: new Headers({ Accept: "application/json" }),
        signal: options.signal,
      },
    );
    await this.throwIfError(response);
  }

  private orgPath(orgId: string, path: string): string {
    return `/api/cloud/v1/orgs/${encodeURIComponent(orgId)}${path}`;
  }

  private withQuery(
    path: string,
    values: Record<string, string | number | undefined>,
  ): string {
    const query = new URLSearchParams();
    for (const [key, value] of Object.entries(values)) {
      if (value !== undefined) query.set(key, String(value));
    }
    const encoded = query.toString();
    return encoded ? `${path}?${encoded}` : path;
  }

  private async request<T>(
    path: string,
    options: JSONRequestOptions = {},
  ): Promise<T> {
    const headers = new Headers({ Accept: "application/json" });
    if (options.body !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    if (options.idempotencyKey !== undefined) {
      headers.set(
        "Idempotency-Key",
        validateIdempotencyKey(options.idempotencyKey),
      );
    }

    const response = await this.authorizedFetch(path, {
      method: options.method ?? "GET",
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      signal: options.signal,
    });
    await this.throwIfError(response);
    if (response.status === 204) return undefined as T;

    try {
      return (await response.json()) as T;
    } catch {
      throw this.invalidResponse(
        response.status,
        "Cloud API returned an invalid JSON response.",
      );
    }
  }

  private async authorizedFetch(
    path: string,
    init: RequestInit,
  ): Promise<Response> {
    const token = (await this.getAccessToken())?.trim();
    if (!token) {
      throw new CloudApiError(401, {
        error: "Unauthorized",
        code: "AUTH_REQUIRED",
        message: "A Cloud API access token is required.",
        requestId: "",
      });
    }

    const headers = new Headers(init.headers);
    headers.set("Authorization", `Bearer ${token}`);
    return this.fetch(`${this.baseUrl}${path}`, { ...init, headers });
  }

  private async throwIfError(response: Response): Promise<void> {
    if (response.ok) return;

    let value: unknown;
    try {
      value = await response.json();
    } catch {
      value = undefined;
    }
    const envelope = toErrorEnvelope(response, value);
    throw new CloudApiError(response.status, envelope);
  }

  private invalidResponse(status: number, message: string): CloudApiError {
    return new CloudApiError(status, {
      error: "Invalid Response",
      code: "INVALID_RESPONSE",
      message,
      requestId: "",
    });
  }
}

export function createCloudClient(config: CloudClientConfig): CloudClient {
  return new CloudClient(config);
}

export class WorkerClient {
  readonly baseUrl: string;

  private readonly getWorkerToken: WorkerClientConfig["getWorkerToken"];
  private readonly fetch: typeof globalThis.fetch;

  constructor(config: WorkerClientConfig) {
    const baseUrl = new URL(config.baseUrl);
    if (baseUrl.search || baseUrl.hash) {
      throw new TypeError("Cloud API baseUrl must not contain a query or fragment.");
    }
    this.baseUrl = baseUrl.toString().replace(/\/+$/, "");
    this.getWorkerToken = config.getWorkerToken;
    this.fetch = config.fetch ?? globalThis.fetch.bind(globalThis);
  }

  bootstrap(
    input: WorkerBootstrapInput,
    options: RequestOptions = {},
  ): Promise<WorkerBootstrapResponse> {
    return this.unauthenticatedRequest("/api/cloud/v1/worker/bootstrap", {
      method: "POST",
      body: input,
      cache: "no-store",
      signal: options.signal,
    });
  }

  heartbeat(
    input: WorkerHeartbeatInput,
    options: RequestOptions = {},
  ): Promise<WorkerHeartbeatResponse> {
    return this.request("/api/cloud/v1/worker/heartbeat", {
      method: "POST",
      body: input,
      cache: "no-store",
      signal: options.signal,
    });
  }

  publishEvent(
    input: WorkerEventInput,
    options: RequestOptions = {},
  ): Promise<WorkerOKResponse> {
    return this.request("/api/cloud/v1/worker/events", {
      method: "POST",
      body: input,
      signal: options.signal,
    });
  }

  async claimTurn(options: RequestOptions = {}): Promise<WorkerTurn | null> {
    const response = await this.request<{ turn: WorkerTurn | null }>(
      "/api/cloud/v1/worker/turns/claim",
      { method: "POST", body: {}, signal: options.signal },
    );
    return response.turn;
  }

  getTurnCancellation(
    turnId: string,
    attempt: number,
    options: RequestOptions = {},
  ): Promise<WorkerCancellationResponse> {
    const query = new URLSearchParams({ attempt: String(attempt) });
    return this.request(
      `/api/cloud/v1/worker/turns/${encodeURIComponent(turnId)}/cancellation?${query.toString()}`,
      { signal: options.signal },
    );
  }

  completeTurn(
    turnId: string,
    input: WorkerCompleteTurnInput,
    options: RequestOptions = {},
  ): Promise<WorkerFinishTurnResponse> {
    return this.request(
      `/api/cloud/v1/worker/turns/${encodeURIComponent(turnId)}/complete`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  failTurn(
    turnId: string,
    input: WorkerFailTurnInput,
    options: RequestOptions = {},
  ): Promise<WorkerFinishTurnResponse> {
    return this.request(
      `/api/cloud/v1/worker/turns/${encodeURIComponent(turnId)}/fail`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  getCredential(
    options: RequestOptions = {},
  ): Promise<WorkerCredentialResponse> {
    return this.request("/api/cloud/v1/worker/credential", {
      cache: "no-store",
      signal: options.signal,
    });
  }

  createCheckoutGrant(
    options: RequestOptions = {},
  ): Promise<WorkerCheckoutGrantResponse> {
    return this.request("/api/cloud/v1/worker/checkout-grant", {
      method: "POST",
      cache: "no-store",
      signal: options.signal,
    });
  }

  listChildren(
    options: PaginationOptions = {},
  ): Promise<SessionPage> {
    const query = new URLSearchParams();
    if (options.cursor !== undefined) query.set("cursor", options.cursor);
    if (options.limit !== undefined) query.set("limit", String(options.limit));
    const suffix = query.size > 0 ? `?${query.toString()}` : "";
    return this.request(`/api/cloud/v1/worker/children${suffix}`, {
      signal: options.signal,
    });
  }

  createChild(
    input: CreateWorkerChildInput,
    options: IdempotentRequestOptions,
  ): Promise<{ session: Session }> {
    return this.request("/api/cloud/v1/worker/children", {
      method: "POST",
      body: input,
      idempotencyKey: options.idempotencyKey,
      signal: options.signal,
    });
  }

  sendChildMessage(
    sessionId: string,
    text: string,
    options: IdempotentRequestOptions,
  ): Promise<{ event: UserMessageEvent }> {
    return this.request(
      `/api/cloud/v1/worker/children/${encodeURIComponent(sessionId)}/messages`,
      {
        method: "POST",
        body: { text },
        idempotencyKey: options.idempotencyKey,
        signal: options.signal,
      },
    );
  }

  deleteChild(
    sessionId: string,
    options: RequestOptions = {},
  ): Promise<DeleteSessionResponse> {
    return this.request(
      `/api/cloud/v1/worker/children/${encodeURIComponent(sessionId)}`,
      { method: "DELETE", signal: options.signal },
    );
  }

  async claimTransport(
    options: RequestOptions = {},
  ): Promise<WorkerTransportRequest | null> {
    const response = await this.request<{
      request: WorkerTransportRequest | null;
    }>("/api/cloud/v1/worker/transport/claim", {
      method: "POST",
      body: {},
      signal: options.signal,
    });
    return response.request;
  }

  completeTransport(
    requestId: string,
    input: WorkerCompleteTransportInput,
    options: RequestOptions = {},
  ): Promise<WorkerOKResponse> {
    return this.request(
      `/api/cloud/v1/worker/transport/${encodeURIComponent(requestId)}/complete`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  failTransport(
    requestId: string,
    input: WorkerFailTransportInput,
    options: RequestOptions = {},
  ): Promise<WorkerOKResponse> {
    return this.request(
      `/api/cloud/v1/worker/transport/${encodeURIComponent(requestId)}/fail`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  ensureAgentTerminal(
    options: RequestOptions = {},
  ): Promise<WorkerAgentTerminalResponse> {
    return this.request("/api/cloud/v1/worker/terminals/agent", {
      method: "POST",
      body: {},
      signal: options.signal,
    });
  }

  publishTerminalOutput(
    terminalId: string,
    input: WorkerTerminalOutputInput,
    options: RequestOptions = {},
  ): Promise<WorkerTerminalOutputResponse> {
    return this.request(
      `/api/cloud/v1/worker/terminals/${encodeURIComponent(terminalId)}/output`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  publishTerminalExit(
    terminalId: string,
    input: WorkerTerminalExitInput,
    options: RequestOptions = {},
  ): Promise<WorkerOKResponse> {
    return this.request(
      `/api/cloud/v1/worker/terminals/${encodeURIComponent(terminalId)}/exit`,
      { method: "POST", body: input, signal: options.signal },
    );
  }

  private async request<T>(
    path: string,
    options: JSONRequestOptions = {},
  ): Promise<T> {
    const headers = new Headers({ Accept: "application/json" });
    if (options.body !== undefined) {
      headers.set("Content-Type", "application/json");
    }
    if (options.idempotencyKey !== undefined) {
      headers.set(
        "Idempotency-Key",
        validateIdempotencyKey(options.idempotencyKey),
      );
    }
    const response = await this.authorizedFetch(path, {
      method: options.method ?? "GET",
      headers,
      body:
        options.body === undefined ? undefined : JSON.stringify(options.body),
      cache: options.cache,
      signal: options.signal,
    });
    await throwIfErrorResponse(response);
    return readJSONResponse<T>(response);
  }

  private async unauthenticatedRequest<T>(
    path: string,
    options: JSONRequestOptions,
  ): Promise<T> {
    const headers = new Headers({
      Accept: "application/json",
      "Content-Type": "application/json",
    });
    const response = await this.fetch(`${this.baseUrl}${path}`, {
      method: options.method ?? "POST",
      headers,
      body: JSON.stringify(options.body),
      cache: options.cache,
      signal: options.signal,
    });
    await throwIfErrorResponse(response);
    return readJSONResponse<T>(response);
  }

  private async authorizedFetch(
    path: string,
    init: RequestInit,
  ): Promise<Response> {
    const token = (await this.getWorkerToken())?.trim();
    if (!token) {
      throw new CloudApiError(401, {
        error: "Unauthorized",
        code: "WORKER_AUTH_REQUIRED",
        message: "A worker credential is required.",
        requestId: "",
      });
    }
    const headers = new Headers(init.headers);
    headers.set("Authorization", `Worker ${token}`);
    return this.fetch(`${this.baseUrl}${path}`, { ...init, headers });
  }
}

export function createWorkerClient(config: WorkerClientConfig): WorkerClient {
  return new WorkerClient(config);
}

function validateIdempotencyKey(value: string): string {
  const key = value.trim();
  if (!key || key.length > 200) {
    throw new TypeError("idempotencyKey must contain between 1 and 200 characters.");
  }
  return key;
}

function parseSSEBlock(block: string): ClientEvent | undefined {
  const data = block
    .split("\n")
    .filter((line) => line.startsWith("data:"))
    .map((line) => line.slice(5).trimStart())
    .join("\n");
  return data ? (JSON.parse(data) as ClientEvent) : undefined;
}

function isRetryableStreamError(error: unknown): boolean {
  if (!(error instanceof CloudApiError)) return true;
  return (
    error.status === 408 ||
    error.status === 425 ||
    error.status === 429 ||
    error.status >= 500
  );
}

async function waitForRetry(
  attempt: number,
  signal: AbortSignal | undefined,
): Promise<void> {
  if (attempt <= 1 || signal?.aborted) return;
  const base = Math.min(250 * 2 ** Math.min(attempt - 2, 4), 4_000);
  const delay = base + Math.floor(Math.random() * Math.max(1, base / 4));
  await new Promise<void>((resolve) => {
    const finish = () => {
      clearTimeout(timeout);
      signal?.removeEventListener("abort", finish);
      resolve();
    };
    const timeout = setTimeout(finish, delay);
    signal?.addEventListener("abort", finish, { once: true });
  });
}

function toErrorEnvelope(response: Response, value: unknown): ErrorEnvelope {
  const object = isRecord(value) ? value : {};
  return {
    error:
      typeof object.error === "string"
        ? object.error
        : response.statusText || "Request Failed",
    code: typeof object.code === "string" ? object.code : "HTTP_ERROR",
    message:
      typeof object.message === "string"
        ? object.message
        : `Cloud API request failed with status ${response.status}.`,
    requestId:
      typeof object.requestId === "string"
        ? object.requestId
        : response.headers.get("x-request-id") ?? "",
    ...(isRecord(object.details) ? { details: object.details } : {}),
  };
}

async function throwIfErrorResponse(response: Response): Promise<void> {
  if (response.ok) return;
  let value: unknown;
  try {
    value = await response.json();
  } catch {
    value = undefined;
  }
  throw new CloudApiError(response.status, toErrorEnvelope(response, value));
}

async function readJSONResponse<T>(response: Response): Promise<T> {
  try {
    return (await response.json()) as T;
  } catch {
    throw new CloudApiError(response.status, {
      error: "Invalid Response",
      code: "INVALID_RESPONSE",
      message: "Cloud API returned an invalid JSON response.",
      requestId: "",
    });
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
