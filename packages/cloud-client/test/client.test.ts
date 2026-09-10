import { describe, expect, it, vi } from "vitest";

import {
  CloudApiError,
  createCloudClient,
  createWorkerClient,
  type AgentProfile,
  type ClientEvent,
  type PullRequestSummary,
  type SessionReviewState,
} from "../src/index.js";

describe("CloudClient", () => {
  it("loads the authenticated account and organization memberships", async () => {
    const account = {
      user: {
        id: "aa4c5117-d075-4a4e-a384-149e75f7dc45",
        email: "alice@example.com",
        displayName: "Alice",
        authProvider: "workos",
      },
      organizations: [
        {
          id: "4165753c-c6ad-4ac2-8f12-e0cbb24d9750",
          slug: "acme",
          displayName: "Acme",
          role: "admin",
        },
      ],
    } as const;
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse(account),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(client.getCurrentAccount()).resolves.toEqual(account);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/me",
    );
  });

  it("requests durable deletion on the session resource", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse(
          {
            session: {
              id: "a9dc6493-bd04-4c03-bb45-55733ed83784",
              desiredState: "deleted",
            },
          },
          202,
        ),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await client.deleteSession(
      "4165753c-c6ad-4ac2-8f12-e0cbb24d9750",
      "a9dc6493-bd04-4c03-bb45-55733ed83784",
    );

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/4165753c-c6ad-4ac2-8f12-e0cbb24d9750/sessions/a9dc6493-bd04-4c03-bb45-55733ed83784",
    );
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("DELETE");
  });

  it("updates editable project settings on the project resource", async () => {
    const project = {
      id: "project one",
      orgId: "org one",
      displayName: "Cloud API",
      repositoryUrl: "https://github.com/acme/cloud",
      defaultBranch: "develop",
      config: {},
      createdAt: "2026-08-12T00:00:00Z",
      updatedAt: "2026-08-12T01:00:00Z",
    };
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ project }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.updateProject("org one", "project one", {
        displayName: "Cloud API",
        defaultBranch: "develop",
      }),
    ).resolves.toEqual({ project });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/org%20one/projects/project%20one",
    );
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("PATCH");
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      displayName: "Cloud API",
      defaultBranch: "develop",
    });
  });

  it("requests durable project deletion on the project resource", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse(
          { project: { id: "project one", deleted: true } },
          202,
        ),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.deleteProject("org one", "project one"),
    ).resolves.toEqual({
      project: { id: "project one", deleted: true },
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/org%20one/projects/project%20one",
    );
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("DELETE");
  });

  it("lists runtime-supplied agent profiles for an organization", async () => {
    const profile: AgentProfile = {
      id: "runtime-agent",
      label: "Runtime Agent",
      capabilities: ["interface.chat", "model.custom"],
      availability: {
        available: false,
        installation: "installed",
        authentication: "unauthorized",
        organizationPolicy: "denied",
        reason: "Disabled for this organization.",
      },
    };
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ agents: [profile] }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(client.listAgents("tenant one/blue")).resolves.toEqual([
      profile,
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one%2Fblue/agents",
    );
  });

  it("manages organization-scoped GitHub App installations and repositories", async () => {
    const installation = {
      id: "d9916dbe-486c-43ec-91b8-379419767719",
      githubInstallationId: "12345",
      accountLogin: "acme",
      accountType: "Organization",
      status: "active",
      repositorySelection: "selected",
      syncStatus: "ready",
      createdAt: "2026-08-11T00:00:00Z",
      updatedAt: "2026-08-11T00:00:00Z",
    } as const;
    const fetchMock = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(jsonResponse({ installations: [installation] }))
      .mockResolvedValueOnce(
        jsonResponse({
          installationUrl: "https://github.com/apps/ao/installations/new",
          expiresAt: "2026-08-11T00:10:00Z",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          items: [],
          page: { hasMore: false },
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ installation }))
      .mockResolvedValueOnce(jsonResponse({ installation }))
      .mockResolvedValueOnce(jsonResponse({ project: { id: "project-1" } }));
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.listGitHubInstallations("tenant one"),
    ).resolves.toEqual([installation]);
    await client.startGitHubInstallation("tenant one");
    await client.listGitHubRepositories("tenant one", {
      cursor: "next page",
      limit: 25,
    });
    await client.syncGitHubInstallation(
      "tenant one",
      "d9916dbe-486c-43ec-91b8-379419767719",
    );
    await client.disconnectGitHubInstallation(
      "tenant one",
      "d9916dbe-486c-43ec-91b8-379419767719",
    );
    await client.createProjectFromGitHub(
      "tenant one",
      { githubRepositoryId: "98765" },
      { idempotencyKey: "github-project-1" },
    );

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/installations",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/installations/start",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/repositories?cursor=next+page&limit=25",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/installations/d9916dbe-486c-43ec-91b8-379419767719/sync",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/installations/d9916dbe-486c-43ec-91b8-379419767719/disconnect",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/github/projects",
    ]);
    expect(fetchMock.mock.calls[1]?.[1]?.method).toBe("POST");
    expect(fetchMock.mock.calls[3]?.[1]?.method).toBe("POST");
    expect(fetchMock.mock.calls[4]?.[1]?.method).toBe("POST");
    expect(requestHeaders(fetchMock, 5).get("Idempotency-Key")).toBe(
      "github-project-1",
    );
  });

  it("manages user GitHub authorization and scratch project creation", async () => {
    const connection = {
      connected: true,
      login: "octocat",
      installations: [
        {
          githubInstallationId: "12345",
          accountLogin: "octocat",
          accountType: "User",
          repositorySelection: "all",
          canCreateRepository: true,
        },
      ],
    } as const;
    const fetchMock = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(jsonResponse(connection))
      .mockResolvedValueOnce(
        jsonResponse({
          authorizeUrl: "https://github.com/login/oauth/authorize",
          expiresAt: "2026-08-12T12:10:00Z",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          project: { id: "project-1" },
          repository: { githubRepositoryId: "98765" },
          session: { id: "session-1" },
        }),
      )
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(client.getGitHubUserConnection()).resolves.toEqual(connection);
    await client.startGitHubUserAuthorization();
    await client.createGitHubScratchProject(
      "tenant one",
      {
        displayName: "Scratch project",
        githubInstallationId: "12345",
        private: true,
        orchestrator: { harness: "claude-code", prompt: "Start here" },
      },
      { idempotencyKey: "scratch-project-1" },
    );
    await expect(client.disconnectGitHubUser()).resolves.toBeUndefined();

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "https://cloud.example.com/api/cloud/v1/github/user",
      "https://cloud.example.com/api/cloud/v1/github/user/authorize",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/projects/scratch",
      "https://cloud.example.com/api/cloud/v1/github/user",
    ]);
    expect(requestHeaders(fetchMock, 2).get("Idempotency-Key")).toBe(
      "scratch-project-1",
    );
    expect(fetchMock.mock.calls[3]?.[1]?.method).toBe("DELETE");
  });

  it("scopes and encodes organization and session URLs", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ session: {} }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com/",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await client.getSession("tenant one/blue", "session one?");

    expect(fetchMock).toHaveBeenCalledOnce();
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one%2Fblue/sessions/session%20one%3F",
    );
  });

  it("writes a workspace file through the typed client", async () => {
    const file = { path: "src/main.ts", content: "export {};\n", size: 11 };
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse(file),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.writeWorkspaceFile("tenant one", "session one", {
        path: file.path,
        content: file.content,
      }),
    ).resolves.toEqual(file);

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant%20one/sessions/session%20one/workspace/file",
    );
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("PUT");
    expect(JSON.parse(String(fetchMock.mock.calls[0]?.[1]?.body))).toEqual({
      path: file.path,
      content: file.content,
    });
  });

  it("reads normalized pull requests and AO review state for a session", async () => {
    const pullRequest = {
      url: "github://o/r/pull/7",
      number: 7,
      title: "Cloud contract",
      state: "open",
      provider: "github",
      repository: "o/r",
      author: "alice",
      sourceBranch: "feat/cloud",
      targetBranch: "main",
      headSha: "current-sha",
      additions: 12,
      deletions: 3,
      changedFiles: 2,
      ci: { state: "passing", failingChecks: [] },
      review: {
        decision: "approved",
        hasUnresolvedHumanComments: false,
        unresolvedBy: [],
        reviews: [],
      },
      mergeability: {
        state: "mergeable",
        reasons: [],
        pullRequestUrl: "https://github.com/o/r/pull/7",
        conflictFiles: [],
      },
      updatedAt: "2026-08-09T12:00:00Z",
      observedAt: "2026-08-09T12:00:01Z",
      ciObservedAt: "2026-08-09T12:00:02Z",
      reviewObservedAt: "2026-08-09T12:00:03Z",
    } satisfies PullRequestSummary;
    const reviewState = {
      sessionId: "session one?",
      reviews: [
        {
          pullRequestUrl: pullRequest.url,
          pullRequestNumber: 7,
          title: pullRequest.title,
          targetSha: "current-sha",
          staleTargetSha: "stale-sha",
          status: "needs_review",
        },
      ],
      runs: [],
    } satisfies SessionReviewState;
    const fetchMock = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(
        jsonResponse({
          sessionId: "session one?",
          pullRequests: [pullRequest],
        }),
      )
      .mockResolvedValueOnce(jsonResponse(reviewState));
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.listSessionPullRequests("tenant", "session one?"),
    ).resolves.toEqual({
      sessionId: "session one?",
      pullRequests: [pullRequest],
    });
    await expect(
      client.getSessionReviewState("tenant", "session one?"),
    ).resolves.toEqual(reviewState);
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session%20one%3F/pull-requests",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session%20one%3F/reviews",
    ]);
  });

  it("injects the latest access token into every request", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ providerConnections: [] }),
    );
    const getAccessToken = vi
      .fn<() => Promise<string>>()
      .mockResolvedValueOnce("first-token")
      .mockResolvedValueOnce("second-token");
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken,
      fetch: fetchMock as typeof fetch,
    });

    await client.listProviderConnections("tenant");
    await client.listProviderConnections("tenant");

    expect(getAccessToken).toHaveBeenCalledTimes(2);
    expect(requestHeaders(fetchMock, 0).get("Authorization")).toBe(
      "Bearer first-token",
    );
    expect(requestHeaders(fetchMock, 1).get("Authorization")).toBe(
      "Bearer second-token",
    );
  });

  it("connects and disconnects coding-agent credentials", async () => {
    const providerConnection = {
      id: "connection-1",
      provider: "claude-code",
      label: "default",
      config: { credentialType: "api_key" },
      validationState: "valid",
      createdAt: "2026-08-12T00:00:00Z",
      updatedAt: "2026-08-12T00:00:00Z",
    };
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ providerConnection }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }));
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.putAgentProviderConnection("tenant", "claude-code", {
        credentialType: "api_key",
        secret: "secret-value",
      }),
    ).resolves.toEqual({ providerConnection });
    await expect(
      client.deleteAgentProviderConnection("tenant", "claude-code"),
    ).resolves.toBeUndefined();

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/provider-connections/agents/claude-code",
    );
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "PUT",
      body: JSON.stringify({
        credentialType: "api_key",
        secret: "secret-value",
      }),
    });
    expect(fetchMock.mock.calls[1]?.[1]).toMatchObject({ method: "DELETE" });
  });

  it("throws a typed error with the standard error envelope", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse(
          {
            error: "Conflict",
            code: "IDEMPOTENCY_CONFLICT",
            message: "That key was used for a different command.",
            requestId: "request-123",
            details: { field: "Idempotency-Key" },
          },
          409,
        ),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    const request = client.sendMessage("tenant", "session", "Hello", {
      idempotencyKey: "message-1",
    });

    const error: unknown = await request.catch((failure: unknown) => failure);
    expect(error).toBeInstanceOf(CloudApiError);
    expect(error).toMatchObject({
      name: "CloudApiError",
      status: 409,
      code: "IDEMPOTENCY_CONFLICT",
      requestId: "request-123",
      details: { field: "Idempotency-Key" },
    });
  });

  it("passes the replay cursor and page limit", async () => {
    const event: ClientEvent = {
      sessionId: "session",
      sequence: 42,
      type: "chat.assistant_delta",
      payload: {
        turnId: "00000000-0000-0000-0000-000000000001",
        attempt: 1,
        stream: "stdout",
        text: "Hello",
      },
      createdAt: "2026-08-09T00:00:00Z",
    };
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({ events: [event], hasMore: true, nextAfter: 42 }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await expect(
      client.replayEvents("tenant", "session", { after: 41, limit: 25 }),
    ).resolves.toEqual({
      events: [event],
      hasMore: true,
      nextAfter: 42,
    });
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session/chat-events?after=41&limit=25",
    );
  });

  it("sends idempotency keys on mutating commands", async () => {
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        jsonResponse({
          event: {
            sessionId: "session",
            sequence: 1,
            type: "chat.user_message",
            payload: { text: "Ship it" },
            createdAt: "2026-08-09T00:00:00Z",
          },
        }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await client.sendMessage("tenant", "session", "Ship it", {
      idempotencyKey: "message-command-1",
    });

    expect(requestHeaders(fetchMock, 0).get("Idempotency-Key")).toBe(
      "message-command-1",
    );
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({
      method: "POST",
      body: JSON.stringify({ text: "Ship it" }),
    });
  });

  it("streams replayed SSE events from an explicit cursor", async () => {
    const abort = new AbortController();
    const encoder = new TextEncoder();
    const body = new ReadableStream<Uint8Array>({
      start(controller) {
        controller.enqueue(
          encoder.encode(
            'id: 8\nevent: chat.assistant_delta\ndata: {"sessionId":"session","sequence":8,',
          ),
        );
        controller.enqueue(
          encoder.encode(
            '"type":"chat.assistant_delta","payload":{"turnId":"00000000-0000-0000-0000-000000000001","attempt":1,"stream":"stdout","text":"Hi"},"createdAt":"2026-08-09T00:00:00Z"}\n\n',
          ),
        );
        controller.close();
      },
    });
    const fetchMock = vi.fn(
      async (_input: RequestInfo | URL, _init?: RequestInit) =>
        new Response(body, { status: 200 }),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    const events: ClientEvent[] = [];
    for await (const event of client.streamEvents("tenant", "session", {
      after: 7,
      signal: abort.signal,
    })) {
      events.push(event);
      abort.abort();
    }

    expect(events).toEqual([
      expect.objectContaining({
        sequence: 8,
        type: "chat.assistant_delta",
        payload: {
          turnId: "00000000-0000-0000-0000-000000000001",
          attempt: 1,
          stream: "stdout",
          text: "Hi",
        },
      }),
    ]);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session/events?after=7",
    );
    expect(requestHeaders(fetchMock, 0).get("Accept")).toBe(
      "text/event-stream",
    );
  });

  it("reconnects event streams from the greatest consumed sequence", async () => {
    const abort = new AbortController();
    const fetchMock = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(
        sseResponse({
          sessionId: "session",
          sequence: 8,
          type: "chat.assistant_delta",
          payload: {
            turnId: "00000000-0000-0000-0000-000000000001",
            attempt: 1,
            stream: "stdout",
            text: "Hi",
          },
          createdAt: "2026-08-09T00:00:00Z",
        }),
      )
      .mockResolvedValueOnce(
        sseResponse(
          {
            sessionId: "session",
            sequence: 8,
            type: "chat.assistant_delta",
            payload: {
              turnId: "00000000-0000-0000-0000-000000000001",
              attempt: 1,
              stream: "stdout",
              text: "duplicate",
            },
            createdAt: "2026-08-09T00:00:00Z",
          },
          {
            sessionId: "session",
            sequence: 9,
            type: "chat.turn_completed",
            payload: {
              turnId: "00000000-0000-0000-0000-000000000001",
              attempt: 1,
            },
            createdAt: "2026-08-09T00:00:01Z",
          },
        ),
      );
    const getAccessToken = vi
      .fn<() => string>()
      .mockReturnValueOnce("first-token")
      .mockReturnValueOnce("second-token");
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken,
      fetch: fetchMock as typeof fetch,
    });

    const sequences: number[] = [];
    for await (const event of client.streamEvents("tenant", "session", {
      after: 7,
      signal: abort.signal,
    })) {
      sequences.push(event.sequence);
      if (event.sequence === 9) abort.abort();
    }

    expect(sequences).toEqual([8, 9]);
    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session/events?after=7",
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session/events?after=8",
    ]);
    expect(requestHeaders(fetchMock, 0).get("Authorization")).toBe(
      "Bearer first-token",
    );
    expect(requestHeaders(fetchMock, 1).get("Authorization")).toBe(
      "Bearer second-token",
    );
  });

  it("does not reconnect event streams after non-retryable client errors", async () => {
    const fetchMock = vi.fn(async () =>
      jsonResponse(
        {
          error: "unauthorized",
          code: "unauthorized",
          message: "Sign in again.",
          requestId: "request-1",
        },
        401,
      ),
    );
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "expired-token",
      fetch: fetchMock as typeof fetch,
    });

    const stream = client.streamEvents("tenant", "session");
    await expect(stream.next()).rejects.toMatchObject({
      status: 401,
      code: "unauthorized",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it("durably requests turn cancellation with an idempotency key", async () => {
    const fetchMock = vi.fn<
      (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>
    >(async () => jsonResponse({ turn: { id: "turn-1" } }, 202));
    const client = createCloudClient({
      baseUrl: "https://cloud.example.com",
      getAccessToken: () => "access-token",
      fetch: fetchMock as typeof fetch,
    });

    await client.cancelTurn("tenant", "session", "turn one", {
      idempotencyKey: "cancel-turn-1",
    });

    expect(fetchMock.mock.calls[0]?.[0]).toBe(
      "https://cloud.example.com/api/cloud/v1/orgs/tenant/sessions/session/turns/turn%20one/cancel",
    );
    expect(fetchMock.mock.calls[0]?.[1]?.method).toBe("POST");
    expect(requestHeaders(fetchMock, 0).get("Idempotency-Key")).toBe(
      "cancel-turn-1",
    );
  });
});

describe("WorkerClient", () => {
  it("matches the worker lifecycle, execution, orchestration, and transport routes", async () => {
    const turn = {
      id: "1933df78-7495-492a-bfde-31448c448e12",
      prompt: "fix auth",
      mode: "standard",
      deniedCommands: [],
      harness: "claude-code",
      attempt: 2,
      cancelRequested: false,
    };
    const transport = {
      id: "3d75e11c-c450-4d7e-8daf-ab2af9087c8f",
      kind: "workspace.read",
      attempt: 2,
      payload: { path: "README.md" },
    };
    const fetchMock = vi
      .fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>()
      .mockResolvedValueOnce(
        jsonResponse({
          workerToken: "[redacted-bootstrap-token]",
          workerId: "session:4",
          epoch: 4,
          expiresIn: 900,
          sessionId: "678ae2c1-d9d3-4f87-be65-338818505299",
          launch: {
            sessionId: "678ae2c1-d9d3-4f87-be65-338818505299",
            projectId: "fb351a69-df93-40ab-8982-bd5ea3a231eb",
            kind: "worker",
            harness: "claude-code",
            displayName: "Auth",
            branch: "feat/auth",
            repositoryUrl: "https://github.com/acme/api",
            defaultBranch: "main",
          },
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          ok: true,
          workerToken: "[redacted-renewed-token]",
          expiresIn: 900,
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ ok: true }, 202))
      .mockResolvedValueOnce(jsonResponse({ turn }))
      .mockResolvedValueOnce(jsonResponse({ requested: false }))
      .mockResolvedValueOnce(jsonResponse({ ok: true, alreadyFinished: false }))
      .mockResolvedValueOnce(
        jsonResponse({ ok: true, alreadyFinished: false }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          provider: "claude-code",
          credentialType: "api_key",
          secret: "[redacted-agent-secret]",
        }),
      )
      .mockResolvedValueOnce(
        jsonResponse({
          cloneUrl: "https://github.com/acme/api.git",
          token: "[redacted-checkout-token]",
          expiresAt: "2026-08-12T12:00:00Z",
        }),
      )
      .mockResolvedValueOnce(jsonResponse({ items: [], page: { hasMore: false } }))
      .mockResolvedValueOnce(jsonResponse({ session: { id: "child" } }, 201))
      .mockResolvedValueOnce(jsonResponse({ event: { sequence: 1 } }, 202))
      .mockResolvedValueOnce(
        jsonResponse(
          { session: { id: "child", desiredState: "deleted" } },
          202,
        ),
      )
      .mockResolvedValueOnce(jsonResponse({ request: transport }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }))
      .mockResolvedValueOnce(jsonResponse({ ok: true }))
      .mockResolvedValueOnce(jsonResponse({ terminalId: "agent-terminal" }))
      .mockResolvedValueOnce(jsonResponse({ sequence: 7 }, 202))
      .mockResolvedValueOnce(jsonResponse({ ok: true }));
    const client = createWorkerClient({
      baseUrl: "https://cloud.example.com",
      getWorkerToken: () => "worker-token",
      fetch: fetchMock as typeof fetch,
    });

    await client.bootstrap({
      bootstrapToken: "[redacted-one-time-ticket]",
      version: "0.1.0",
      capabilities: ["worker.turns"],
    });
    await client.heartbeat({
      version: "0.1.0",
      capabilities: ["worker.turns"],
    });
    await client.publishEvent({
      type: "chat.assistant_delta",
      payload: {
        turnId: turn.id,
        attempt: turn.attempt,
        stream: "stdout",
        text: "done",
      },
    });
    await expect(client.claimTurn()).resolves.toEqual(turn);
    await client.getTurnCancellation(turn.id, turn.attempt);
    await client.completeTurn(turn.id, { attempt: turn.attempt });
    await client.failTurn(turn.id, {
      attempt: turn.attempt,
      error: "harness exited",
    });
    await client.getCredential();
    await client.createCheckoutGrant();
    await client.listChildren({ cursor: "next page", limit: 25 });
    await client.createChild(
      { harness: "codex", displayName: "Child", prompt: "inspect auth" },
      { idempotencyKey: "child-1" },
    );
    await client.sendChildMessage("child one", "Report status", {
      idempotencyKey: "message-1",
    });
    await client.deleteChild("child one");
    await expect(client.claimTransport()).resolves.toEqual(transport);
    await client.completeTransport(transport.id, {
      attempt: transport.attempt,
      response: { path: "README.md", content: "# API", size: 5 },
    });
    await client.failTransport("request one", {
      attempt: 3,
      code: "WORKER_OPERATION_FAILED",
      message: "Operation failed.",
    });
    await expect(client.ensureAgentTerminal()).resolves.toEqual({
      terminalId: "agent-terminal",
    });
    await client.publishTerminalOutput("terminal one", { data: "b2sK" });
    await client.publishTerminalExit("terminal one", { exitCode: 0 });

    expect(fetchMock.mock.calls.map((call) => call[0])).toEqual([
      "https://cloud.example.com/api/cloud/v1/worker/bootstrap",
      "https://cloud.example.com/api/cloud/v1/worker/heartbeat",
      "https://cloud.example.com/api/cloud/v1/worker/events",
      "https://cloud.example.com/api/cloud/v1/worker/turns/claim",
      `https://cloud.example.com/api/cloud/v1/worker/turns/${turn.id}/cancellation?attempt=2`,
      `https://cloud.example.com/api/cloud/v1/worker/turns/${turn.id}/complete`,
      `https://cloud.example.com/api/cloud/v1/worker/turns/${turn.id}/fail`,
      "https://cloud.example.com/api/cloud/v1/worker/credential",
      "https://cloud.example.com/api/cloud/v1/worker/checkout-grant",
      "https://cloud.example.com/api/cloud/v1/worker/children?cursor=next+page&limit=25",
      "https://cloud.example.com/api/cloud/v1/worker/children",
      "https://cloud.example.com/api/cloud/v1/worker/children/child%20one/messages",
      "https://cloud.example.com/api/cloud/v1/worker/children/child%20one",
      "https://cloud.example.com/api/cloud/v1/worker/transport/claim",
      `https://cloud.example.com/api/cloud/v1/worker/transport/${transport.id}/complete`,
      "https://cloud.example.com/api/cloud/v1/worker/transport/request%20one/fail",
      "https://cloud.example.com/api/cloud/v1/worker/terminals/agent",
      "https://cloud.example.com/api/cloud/v1/worker/terminals/terminal%20one/output",
      "https://cloud.example.com/api/cloud/v1/worker/terminals/terminal%20one/exit",
    ]);
    expect(requestHeaders(fetchMock, 0).has("Authorization")).toBe(false);
    for (let index = 1; index < fetchMock.mock.calls.length; index += 1) {
      expect(requestHeaders(fetchMock, index).get("Authorization")).toBe(
        "Worker worker-token",
      );
    }
    expect(fetchMock.mock.calls[0]?.[1]?.cache).toBe("no-store");
    expect(fetchMock.mock.calls[1]?.[1]?.cache).toBe("no-store");
    expect(fetchMock.mock.calls[7]?.[1]?.cache).toBe("no-store");
    expect(fetchMock.mock.calls[8]?.[1]?.cache).toBe("no-store");
    expect(requestHeaders(fetchMock, 10).get("Idempotency-Key")).toBe("child-1");
    expect(requestHeaders(fetchMock, 11).get("Idempotency-Key")).toBe(
      "message-1",
    );
    expect(JSON.parse(String(fetchMock.mock.calls[5]?.[1]?.body))).toEqual({
      attempt: 2,
    });
    expect(JSON.parse(String(fetchMock.mock.calls[14]?.[1]?.body))).toEqual({
      attempt: 2,
      response: { path: "README.md", content: "# API", size: 5 },
    });
    expect(JSON.parse(String(fetchMock.mock.calls[15]?.[1]?.body))).toEqual({
      attempt: 3,
      code: "WORKER_OPERATION_FAILED",
      message: "Operation failed.",
    });
  });

  it("unwraps empty claims from HTTP 200 response bodies", async () => {
    const client = createWorkerClient({
      baseUrl: "https://cloud.example.com",
      getWorkerToken: () => "worker-token",
      fetch: vi
        .fn()
        .mockResolvedValueOnce(jsonResponse({ turn: null }))
        .mockResolvedValueOnce(
          jsonResponse({ request: null }),
        ) as typeof fetch,
    });

    await expect(client.claimTurn()).resolves.toBeNull();
    await expect(client.claimTransport()).resolves.toBeNull();
  });
});

function jsonResponse(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

function sseResponse(...events: ClientEvent[]): Response {
  return new Response(
    events
      .map(
        (event) =>
          `id: ${event.sequence}\ndata: ${JSON.stringify(event)}\n\n`,
      )
      .join(""),
    {
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
    },
  );
}

function requestHeaders(
  fetchMock: ReturnType<typeof vi.fn>,
  call: number,
): Headers {
  return new Headers((fetchMock.mock.calls[call]?.[1] as RequestInit).headers);
}
