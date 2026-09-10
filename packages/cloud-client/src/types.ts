import type { components } from "./schema.js";

type Schemas = components["schemas"];

export type ErrorEnvelope = Schemas["ErrorEnvelope"];
export type PageInfo = Schemas["PageInfo"];
export type AuthProvider = Schemas["AuthProvider"];
export type OrganizationRole = Schemas["OrganizationRole"];
export type CurrentUser = Schemas["CurrentUser"];
export type OrganizationMembership = Schemas["OrganizationMembership"];
export type CurrentAccount = Schemas["CurrentAccount"];

export type AgentCapability = Schemas["AgentCapability"];
export type AgentInstallationState = Schemas["AgentInstallationState"];
export type AgentAuthenticationState = Schemas["AgentAuthenticationState"];
export type AgentOrganizationPolicy = Schemas["AgentOrganizationPolicy"];
export type AgentAvailability = Schemas["AgentAvailability"];
export type AgentProfile = Schemas["AgentProfile"];

export type Project = Schemas["Project"];
export type CreateProjectInput = Schemas["CreateProjectInput"];
export type UpdateProjectInput = Schemas["UpdateProjectInput"];
export type DeleteProjectResponse = Schemas["DeleteProjectResponse"];
export type ProjectPage = Schemas["ProjectPage"];

export type GitHubInstallationStatus = Schemas["GitHubInstallationStatus"];
export type GitHubSyncStatus = Schemas["GitHubSyncStatus"];
export type GitHubInstallation = Schemas["GitHubInstallation"];
export type GitHubInstallationStart = Schemas["GitHubInstallationStart"];
export type GitHubUserInstallation = Schemas["GitHubUserInstallation"];
export type GitHubUserConnection = Schemas["GitHubUserConnection"];
export type GitHubUserAuthorizationStart =
  Schemas["GitHubUserAuthorizationStart"];
export type GitHubRepositoryAccessState =
  Schemas["GitHubRepositoryAccessState"];
export type GitHubRepository = Schemas["GitHubRepository"];
export type GitHubRepositoryPage = Schemas["GitHubRepositoryPage"];
export type CreateGitHubProjectInput = Schemas["CreateGitHubProjectInput"];
export type CreateGitHubScratchProjectInput =
  Schemas["CreateGitHubScratchProjectInput"];
export type CreateGitHubScratchProjectResponse =
  Schemas["CreateGitHubScratchProjectResponse"];

export type Session = Schemas["Session"];
export type SessionKind = Schemas["SessionKind"];
export type SessionMode = Schemas["SessionMode"];
export type SessionActivityState = Schemas["SessionActivityState"];
export type SessionStatus = Schemas["SessionStatus"];
export type Turn = Schemas["Turn"];
export type CreateSessionInput = Schemas["CreateSessionInput"];
export type DeleteSessionResponse = Schemas["DeleteSessionResponse"];
export type SessionPage = Schemas["SessionPage"];

export type PullRequestState = Schemas["PullRequestState"];
export type CIState = Schemas["CIState"];
export type PullRequestCheckStatus = Schemas["PullRequestCheckStatus"];
export type ReviewDecision = Schemas["ReviewDecision"];
export type MergeabilityState = Schemas["MergeabilityState"];
export type PullRequestFailingCheck = Schemas["PullRequestFailingCheck"];
export type PullRequestCISummary = Schemas["PullRequestCISummary"];
export type PullRequestReviewCommentLink =
  Schemas["PullRequestReviewCommentLink"];
export type PullRequestUnresolvedReviewer =
  Schemas["PullRequestUnresolvedReviewer"];
export type PullRequestSubmittedReview =
  Schemas["PullRequestSubmittedReview"];
export type PullRequestReviewSummary = Schemas["PullRequestReviewSummary"];
export type PullRequestConflictFile = Schemas["PullRequestConflictFile"];
export type PullRequestMergeabilitySummary =
  Schemas["PullRequestMergeabilitySummary"];
export type PullRequestSummary = Schemas["PullRequestSummary"];
export type SessionPullRequests = Schemas["SessionPullRequests"];

export type AOReviewRunStatus = Schemas["AOReviewRunStatus"];
export type AOReviewVerdict = Schemas["AOReviewVerdict"];
export type AOReviewState = Schemas["AOReviewState"];
export type AOReviewRun = Schemas["AOReviewRun"];
export type AOPullRequestReviewState = Schemas["AOPullRequestReviewState"];
export type SessionReviewState = Schemas["SessionReviewState"];

export type ClientEvent = Schemas["ClientEvent"];
export type ClientEventPage = Schemas["ClientEventPage"];
export type UserMessageEvent = Schemas["UserMessageEvent"];
export type AssistantDeltaEvent = Schemas["AssistantDeltaEvent"];
export type TurnStartedEvent = Schemas["TurnStartedEvent"];
export type TurnCompletedEvent = Schemas["TurnCompletedEvent"];
export type TurnInterruptedEvent = Schemas["TurnInterruptedEvent"];
export type TurnAbortedEvent = Schemas["TurnAbortedEvent"];
export type InterruptRequestedEvent = Schemas["InterruptRequestedEvent"];

export type TerminalKind = Schemas["TerminalKind"];
export type TerminalScope = Schemas["TerminalScope"];
export type TerminalTicket = Schemas["TerminalTicket"];

export type WorkspaceEntry = Schemas["WorkspaceEntry"];
export type WorkspaceEntryPage = Schemas["WorkspaceEntryPage"];
export type WorkspaceFile = Schemas["WorkspaceFile"];
export type WorkspaceFileWriteInput = Schemas["WorkspaceFileWriteInput"];
export type WorkspaceFileStatus = Schemas["WorkspaceFileStatus"];
export type WorkspaceDiffFile = Schemas["WorkspaceDiffFile"];
export type WorkspaceDiff = Schemas["WorkspaceDiff"];

export type ProviderName = Schemas["ProviderName"];
export type ProviderPublicConfig = Schemas["ProviderPublicConfig"];
export type RedactedProviderConnection =
  Schemas["RedactedProviderConnection"];
export type PutAgentProviderConnectionInput =
  Schemas["PutAgentProviderConnectionInput"];

export type WorkerBootstrapInput = Schemas["WorkerBootstrapInput"];
export type WorkerLaunchContext = Schemas["WorkerLaunchContext"];
export type WorkerBootstrapResponse = Schemas["WorkerBootstrapResponse"];
export type WorkerHeartbeatInput = Schemas["WorkerHeartbeatInput"];
export type WorkerHeartbeatResponse = Schemas["WorkerHeartbeatResponse"];
export type WorkerReadyPayload = Schemas["WorkerReadyPayload"];
export type WorkerOutputPayload = Schemas["WorkerOutputPayload"];
export type WorkerActivityPayload = Schemas["WorkerActivityPayload"];
export type WorkerReadyEventInput = Schemas["WorkerReadyEventInput"];
export type WorkerOutputEventInput = Schemas["WorkerOutputEventInput"];
export type WorkerActivityEventInput = Schemas["WorkerActivityEventInput"];
export type WorkerEventInput = Schemas["WorkerEventInput"];
export type WorkerOKResponse = Schemas["WorkerOKResponse"];
export type WorkerTurn = Schemas["WorkerTurn"];
export type WorkerClaimTurnResponse = Schemas["WorkerClaimTurnResponse"];
export type WorkerCancellationResponse =
  Schemas["WorkerCancellationResponse"];
export type WorkerCompleteTurnInput = Schemas["WorkerCompleteTurnInput"];
export type WorkerFailTurnInput = Schemas["WorkerFailTurnInput"];
export type WorkerFinishTurnResponse = Schemas["WorkerFinishTurnResponse"];
export type WorkerCredentialResponse = Schemas["WorkerCredentialResponse"];
export type WorkerCheckoutGrantResponse =
  Schemas["WorkerCheckoutGrantResponse"];
export type CreateWorkerChildInput = Schemas["CreateWorkerChildInput"];
export type SendMessageInput = Schemas["SendMessageInput"];
export type WorkerWorkspaceListPayload =
  Schemas["WorkerWorkspaceListPayload"];
export type WorkerWorkspaceReadPayload =
  Schemas["WorkerWorkspaceReadPayload"];
export type WorkerWorkspaceWritePayload =
  Schemas["WorkerWorkspaceWritePayload"];
export type WorkerWorkspaceEntryPage =
  Schemas["WorkerWorkspaceEntryPage"];
export type WorkerTerminalOpenPayload =
  Schemas["WorkerTerminalOpenPayload"];
export type WorkerTerminalInputPayload =
  Schemas["WorkerTerminalInputPayload"];
export type WorkerTerminalResizePayload =
  Schemas["WorkerTerminalResizePayload"];
export type WorkerTerminalClosePayload =
  Schemas["WorkerTerminalClosePayload"];
export type WorkerTerminalOpenResult =
  Schemas["WorkerTerminalOpenResult"];
export type WorkerTerminalInputResult =
  Schemas["WorkerTerminalInputResult"];
export type WorkerTerminalResizeResult =
  Schemas["WorkerTerminalResizeResult"];
export type WorkerTerminalCloseResult =
  Schemas["WorkerTerminalCloseResult"];
export type WorkerWorkspaceListTransport =
  Schemas["WorkerWorkspaceListTransport"];
export type WorkerWorkspaceReadTransport =
  Schemas["WorkerWorkspaceReadTransport"];
export type WorkerWorkspaceWriteTransport =
  Schemas["WorkerWorkspaceWriteTransport"];
export type WorkerWorkspaceDiffTransport =
  Schemas["WorkerWorkspaceDiffTransport"];
export type WorkerTerminalOpenTransport =
  Schemas["WorkerTerminalOpenTransport"];
export type WorkerTerminalInputTransport =
  Schemas["WorkerTerminalInputTransport"];
export type WorkerTerminalResizeTransport =
  Schemas["WorkerTerminalResizeTransport"];
export type WorkerTerminalCloseTransport =
  Schemas["WorkerTerminalCloseTransport"];
export type WorkerTransportRequest = Schemas["WorkerTransportRequest"];
export type WorkerClaimTransportResponse =
  Schemas["WorkerClaimTransportResponse"];
export type WorkerTransportResult = Schemas["WorkerTransportResult"];
export type WorkerCompleteTransportInput =
  Schemas["WorkerCompleteTransportInput"];
export type WorkerFailTransportInput = Schemas["WorkerFailTransportInput"];
export type WorkerAgentTerminalResponse =
  Schemas["WorkerAgentTerminalResponse"];
export type WorkerTerminalExitInput = Schemas["WorkerTerminalExitInput"];
export type WorkerTerminalOutputInput =
  Schemas["WorkerTerminalOutputInput"];
export type WorkerTerminalOutputResponse =
  Schemas["WorkerTerminalOutputResponse"];

export interface PaginationOptions {
  cursor?: string;
  limit?: number;
  signal?: AbortSignal;
}

export interface EventReplayOptions {
  after?: number;
  limit?: number;
  signal?: AbortSignal;
}

export interface IdempotentRequestOptions {
  idempotencyKey: string;
  signal?: AbortSignal;
}

export interface RequestOptions {
  signal?: AbortSignal;
}
