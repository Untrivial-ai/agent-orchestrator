// Package specgen builds the code-first OpenAPI document from the Go contract
// types. It lives outside apispec because it imports the controllers (to
// reflect their request/response shapes), and controllers import apispec (for
// the 501 stub) — keeping Build here breaks that cycle. apispec only embeds and
// serves the committed openapi.yaml; specgen produces it.
package specgen

import (
	"fmt"
	"net/http"
	"reflect"
	"strings"

	jsonschema "github.com/swaggest/jsonschema-go"
	openapi "github.com/swaggest/openapi-go"
	"github.com/swaggest/openapi-go/openapi31"

	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/controllers"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/envelope"
	importsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/importer"
	projectsvc "github.com/aoagents/agent-orchestrator/backend/internal/service/project"
)

// Build reflects the Go contract types and the operation registry below into
// the OpenAPI document. It is the single source of truth for the /api/v1
// contract: `cmd/genspec` writes its output to apispec/openapi.yaml (the
// committed, embedded artifact) and TestBuild_MatchesEmbedded asserts the embed
// equals fresh Build() output so the two can never drift. Schema facets live as
// struct tags on the service.*/controllers.* types; operation metadata (path,
// status codes, summaries) lives here.
//
// Every wire shape is reflected straight from where it is used at runtime — the
// request bodies, path params, and response envelopes from controllers, the
// error envelope from httpd/envelope — so the served responses and the
// generated schema share one definition each.
func Build() ([]byte, error) {
	r := openapi31.NewReflector()
	// Derive `required` from the idiomatic Go convention: a JSON field without
	// `omitempty` is required. swaggest does not infer this on its own, so the
	// structs stay clean (only description/enum tags) and this hook adds the
	// required array. nonNullableSlices drops the spurious "null" type swaggest
	// stamps on every Go slice.
	r.DefaultOptions = append(r.DefaultOptions,
		func(rc *jsonschema.ReflectContext) { rc.EnvelopNullability = true },
		jsonschema.InterceptProp(requiredFromJSONTag),
		jsonschema.InterceptNullability(nonNullableSlices),
		jsonschema.InterceptNullability(validNullableReferenceUnions),
		// Clean component schema names (which become the generated TS type names):
		// swaggest defaults to PackageType, e.g. "ProjectProject", "EnvelopeAPIError".
		jsonschema.InterceptDefName(schemaName),
	)

	r.Spec.SetTitle("Agent Orchestrator HTTP daemon")
	r.Spec.SetVersion("0.1.0-route-shell")
	r.Spec.SetDescription("Loopback-only HTTP surface served by the Go daemon. " +
		"Generated from Go (code-first) — do not edit by hand; run `go generate ./...`.")
	r.Spec.Servers = []openapi31.Server{
		*(&openapi31.Server{URL: "http://127.0.0.1:3001"}).WithDescription("Local daemon (loopback only)"),
	}
	r.Spec.Tags = []openapi31.Tag{
		*(&openapi31.Tag{Name: "agents"}).WithDescription(
			"Supported and locally runnable agent adapters"),
		*(&openapi31.Tag{Name: "projects"}).WithDescription(
			"Project registry, configuration, and lifecycle administration"),
		*(&openapi31.Tag{Name: "sessions"}).WithDescription(
			"Agent session lifecycle and messaging"),
		*(&openapi31.Tag{Name: "prs"}).WithDescription(
			"Pull-request actions (SCM lane)"),
		*(&openapi31.Tag{Name: "reviews"}).WithDescription(
			"Code-review runs and findings"),
		*(&openapi31.Tag{Name: "notifications"}).WithDescription(
			"Durable dashboard notifications"),
		*(&openapi31.Tag{Name: "reports"}).WithDescription(
			"Durable worker reports"),
		*(&openapi31.Tag{Name: "usage"}).WithDescription(
			"Token usage telemetry for AO sessions"),
		*(&openapi31.Tag{Name: "push"}).WithDescription(
			"Mobile push-device registration for OS push notifications"),
		*(&openapi31.Tag{Name: "events"}).WithDescription(
			"Server-sent CDC event stream with durable replay"),
		*(&openapi31.Tag{Name: "import"}).WithDescription(
			"Legacy AO project import (availability probe and run)"),
		*(&openapi31.Tag{Name: "dev"}).WithDescription(
			"Developer-only maintenance operations"),
		*(&openapi31.Tag{Name: "mobile"}).WithDescription(
			"Connect Mobile LAN bridge control (loopback/desktop only)"),
		*(&openapi31.Tag{Name: "browser"}).WithDescription(
			"Target-isolated desktop browser runtime (loopback only)"),
		*(&openapi31.Tag{Name: "system"}).WithDescription(
			"Local machine readiness checks the desktop app runs before showing the board"),
	}

	for _, op := range operations() {
		oc, err := r.NewOperationContext(op.method, op.path)
		if err != nil {
			return nil, fmt.Errorf("new operation %s %s: %w", op.method, op.path, err)
		}
		oc.SetID(op.id)
		oc.SetSummary(op.summary)
		oc.SetTags(op.tag)
		for _, param := range op.pathParams {
			oc.AddReqStructure(param)
		}
		if op.reqBody != nil {
			// AddReqStructure leaves requestBody.required absent, which
			// OpenAPI reads as optional. Most of these bodies are mandatory, so
			// force it — otherwise validators/generators treat the body as
			// skippable. Ops that genuinely accept an empty body opt out.
			if op.optionalReqBody {
				oc.AddReqStructure(op.reqBody)
			} else {
				oc.AddReqStructure(op.reqBody, openapi.WithCustomize(markRequestBodyRequired))
			}
		}
		for _, resp := range op.resps {
			opts := []openapi.ContentOption{openapi.WithHTTPStatus(resp.status)}
			if op.contentTypes != nil && op.contentTypes[resp.status] != "" {
				opts = append(opts, openapi.WithContentType(op.contentTypes[resp.status]))
			}
			oc.AddRespStructure(resp.body, opts...)
		}
		if err := r.AddOperation(oc); err != nil {
			return nil, fmt.Errorf("add operation %s %s: %w", op.method, op.path, err)
		}
	}

	return r.Spec.MarshalYAML()
}

// schemaName maps swaggest's default PackageType component names (e.g.
// "ProjectProject", "EnvelopeAPIError") to the clean, stable schema names that
// become the generated TypeScript type names. Every reflected type is listed
// explicitly: an unrecognised default name is returned verbatim, so a new type
// surfaces as a visibly-wrong "PackageType" name in the diff (and the drift
// test) rather than silently colliding with an existing schema via a
// TrimPrefix catch-all.
func schemaName(_ reflect.Type, defaultName string) string {
	if clean, ok := schemaNames[defaultName]; ok {
		return clean
	}
	return defaultName
}

// schemaNames is the exhaustive default→clean mapping for every type reflected
// by projectOperations(). Add an entry when a new contract type is introduced;
// the drift test fails until the spec is regenerated, which flags the gap.
var schemaNames = map[string]string{ //nolint:gosec // Public OpenAPI type names include reset-credit contracts; no credential value is stored here.
	"ControllersSettingsResponse":                          "SettingsResponse",
	"ControllersDesktopWorkspaceLocationResponse":          "DesktopWorkspaceLocationResponse",
	"ControllersUpdateSessionInterfaceRequest":             "UpdateSessionInterfaceRequest",
	"ControllersConversationSnapshotResponse":              "ConversationSnapshotResponse",
	"ControllersConversationTurnResponse":                  "ConversationTurnResponse",
	"ControllersConversationTurnDiffResponse":              "ConversationTurnDiffResponse",
	"ControllersConversationDiffFileResponse":              "ConversationDiffFileResponse",
	"ControllersConversationMessageResponse":               "ConversationMessageResponse",
	"ControllersConversationActivityResponse":              "ConversationActivityResponse",
	"ControllersSendConversationMessageRequest":            "SendConversationMessageRequest",
	"ControllersConversationImageContentRequest":           "ConversationImageContentRequest",
	"ControllersConversationResourceContentRequest":        "ConversationResourceContentRequest",
	"ControllersSendConversationMessageResponse":           "SendConversationMessageResponse",
	"ControllersEditConversationMessageRequest":            "EditConversationMessageRequest",
	"ControllersEditQueuedConversationMessageRequest":      "EditQueuedConversationMessageRequest",
	"ControllersReorderQueuedConversationTurnsRequest":     "ReorderQueuedConversationTurnsRequest",
	"ControllersConversationContentSummaryResponse":        "ConversationContentSummaryResponse",
	"ControllersEditConversationMessageResponse":           "EditConversationMessageResponse",
	"ControllersActivateConversationBranchResponse":        "ActivateConversationBranchResponse",
	"ControllersConversationBranchPointResponse":           "ConversationBranchPointResponse",
	"ControllersConversationBranchMaterializationResponse": "ConversationBranchMaterializationResponse",
	"ControllersResolveConversationApprovalRequest":        "ResolveConversationApprovalRequest",
	"ControllersResolveConversationInputRequest":           "ResolveConversationInputRequest",
	"ControllersConversationModelsResponse":                "ConversationModelsResponse",
	"ControllersConversationModelResponse":                 "ConversationModelResponse",
	"ControllersConversationConfigOptionsResponse":         "ConversationConfigOptionsResponse",
	"ControllersConversationConfigOptionResponse":          "ConversationConfigOptionResponse",
	"ControllersConversationConfigChoiceResponse":          "ConversationConfigChoiceResponse",
	"ControllersSetConversationConfigOptionRequest":        "SetConversationConfigOptionRequest",
	"ControllersConversationSkillsResponse":                "ConversationSkillsResponse",
	"ControllersConversationSkillResponse":                 "ConversationSkillResponse",
	"ControllersConversationTurnSettingsPayload":           "ConversationTurnSettingsPayload",
	"ControllersConversationUsagePayload":                  "ConversationUsagePayload",
	"ControllersConversationRateLimitsPayload":             "ConversationRateLimitsPayload",
	"ControllersConversationPlanResponse":                  "ConversationPlanResponse",
	"ControllersConversationPlanStepResponse":              "ConversationPlanStepResponse",
	"ControllersConversationModelReroutePayload":           "ConversationModelReroutePayload",
	"ControllersConversationAccountPayload":                "ConversationAccountPayload",
	"ControllersConversationThreadStatePayload":            "ConversationThreadStatePayload",
	"ControllersConversationMCPServerPayload":              "ConversationMCPServerPayload",
	"ControllersReloadConversationMCPServersResponse":      "ReloadConversationMCPServersResponse",
	"ControllersCompactConversationResponse":               "CompactConversationResponse",
	"ControllersRollbackConversationResponse":              "RollbackConversationResponse",
	"ControllersRetryTurnResponse":                         "RetryTurnResponse",
	"ControllersSetConversationTitleRequest":               "SetConversationTitleRequest",
	"ControllersSetConversationTitleResponse":              "SetConversationTitleResponse",
	"ControllersSteerConversationRequest":                  "SteerConversationRequest",
	"ControllersSteerConversationResponse":                 "SteerConversationResponse",
	"ControllersPromoteQueuedTurnResponse":                 "PromoteQueuedTurnResponse",
	// httpd/envelope
	"EnvelopeAPIError": "APIError",
	// observe/ownership
	"OwnershipOwner": "ReportingOwner",
	// domain
	"DomainProjectID":                 "ProjectID",
	"DomainSessionID":                 "SessionID",
	"DomainIssueID":                   "IssueID",
	"DomainSession":                   "Session",
	"DomainProjectConfig":             "ProjectConfig",
	"DomainTrackerIntakeConfig":       "TrackerIntakeConfig",
	"ControllersTriggerReviewRequest": "TriggerReviewRequest",
	"DomainContainerReapConfig":       "ContainerReapConfig",
	"DomainAgentConfig":               "AgentConfig",
	"DomainRoleOverride":              "RoleOverride",
	// httpd/controllers (wire envelopes)
	"ControllersListProjectsResponse":                     "ListProjectsResponse",
	"ControllersProjectResponse":                          "ProjectResponse",
	"ControllersAgentIDParam":                             "AgentIDParam",
	"ControllersCodexAccountIDParam":                      "CodexAccountIDParam",
	"ControllersCodexAccountLoginIDParam":                 "CodexAccountLoginIDParam",
	"ControllersGetProjectResponse":                       "ProjectGetResponse",
	"ControllersProjectOrDegraded":                        "ProjectOrDegraded",
	"ControllersListSessionsQuery":                        "ListSessionsQuery",
	"ControllersCleanupSessionsQuery":                     "CleanupSessionsQuery",
	"ControllersListSessionsResponse":                     "ListSessionsResponse",
	"ControllersSpawnSessionRequest":                      "SpawnSessionRequest",
	"ControllersSpawnSessionResponse":                     "SpawnSessionResponse",
	"ControllersSessionResponse":                          "SessionResponse",
	"ControllersSessionPreviewResponse":                   "SessionPreviewResponse",
	"ControllersSetSessionPreviewRequest":                 "SetSessionPreviewRequest",
	"ControllersStartPreviewServerRequest":                "StartPreviewServerRequest",
	"ControllersPreviewServerStatusResponse":              "PreviewServerStatusResponse",
	"ControllersBrowserStatusQuery":                       "BrowserStatusQuery",
	"ControllersBrowserStatusResponse":                    "BrowserStatusResponse",
	"ControllersBrowserCommandRequest":                    "BrowserCommandRequest",
	"ControllersBrowserCommandResponse":                   "BrowserCommandResponse",
	"ControllersSetSessionMergePolicyRequest":             "SetSessionMergePolicyRequest",
	"ControllersSetSessionMergePolicyResponse":            "SetSessionMergePolicyResponse",
	"ControllersSetSessionAutoInjectReviewRequest":        "SetSessionAutoInjectReviewRequest",
	"ControllersSetSessionAutoInjectReviewResponse":       "SetSessionAutoInjectReviewResponse",
	"ControllersSetSessionAutoInjectCIRequest":            "SetSessionAutoInjectCIRequest",
	"ControllersSetSessionAutoInjectCIResponse":           "SetSessionAutoInjectCIResponse",
	"ControllersRenameSessionRequest":                     "RenameSessionRequest",
	"ControllersSetSessionReviewerRequest":                "SetSessionReviewerRequest",
	"ControllersRenameSessionResponse":                    "RenameSessionResponse",
	"ControllersRestoreSessionResponse":                   "RestoreSessionResponse",
	"ControllersExitAgentResponse":                        "ExitAgentResponse",
	"ControllersResumeAgentResponse":                      "ResumeAgentResponse",
	"ControllersSwitchAgentRequest":                       "SwitchAgentRequest",
	"ControllersAgentSwitchView":                          "AgentSwitch",
	"ControllersAgentSwitchResponse":                      "AgentSwitchResponse",
	"ControllersListAgentSwitchesResponse":                "ListAgentSwitchesResponse",
	"ControllersSubmitAgentHandoffRequest":                "SubmitAgentHandoffRequest",
	"ControllersStartSessionInterfaceTransitionRequest":   "StartSessionInterfaceTransitionRequest",
	"ControllersSessionInterfaceTransitionView":           "SessionInterfaceTransition",
	"ControllersSessionInterfaceTransitionStatusResponse": "SessionInterfaceTransitionStatusResponse",
	"ControllersStartSessionInterfaceTransitionResponse":  "StartSessionInterfaceTransitionResponse",
	"ControllersCancelSessionInterfaceTransitionResponse": "CancelSessionInterfaceTransitionResponse",
	"ControllersInterfaceTransitionNoticeAckResponse":     "AcknowledgeSessionInterfaceTransitionNoticeResponse",
	"ControllersCleanupSessionsResponse":                  "CleanupSessionsResponse",
	"ControllersCleanupSkippedSession":                    "CleanupSkippedSession",
	"ControllersWorkspaceFileQuery":                       "WorkspaceFileQuery",
	"ControllersWorkspaceFileBlobQuery":                   "WorkspaceFileBlobQuery",
	"ControllersStageSessionAttachmentsRequest":           "StageSessionAttachmentsRequest",
	"ControllersStageSessionAttachmentsResponse":          "StageSessionAttachmentsResponse",
	"ControllersAttachmentInput":                          "AttachmentInput",
	"ControllersListWorkspaceFilesResponse":               "ListWorkspaceFilesResponse",
	"ControllersWorkspaceFileSummary":                     "WorkspaceFileSummary",
	"ControllersWorkspaceFileSections":                    "WorkspaceFileSections",
	"ControllersWorkspaceCommitSummary":                   "WorkspaceCommitSummary",
	"ControllersWorkspaceSummary":                         "WorkspaceSummary",
	"ControllersEnsureCodexAccountsRequest":               "EnsureCodexAccountsRequest",
	"ControllersConsumeCodexAccountResetCreditRequest":    "ConsumeCodexAccountResetCreditRequest",
	"ControllersCodexAccountsResponse":                    "CodexAccountsResponse",
	"ControllersCodexAccountResponse":                     "CodexAccountResponse",
	"ControllersCodexAuthenticationResponse":              "CodexAuthenticationResponse",
	"ControllersCodexAccountCapacityResponse":             "CodexAccountCapacityResponse",
	"ControllersCodexCapacityBucketResponse":              "CodexCapacityBucketResponse",
	"ControllersCodexCapacityWindowResponse":              "CodexCapacityWindowResponse",
	"ControllersCodexResetCreditsSummaryResponse":         "CodexResetCreditsSummaryResponse",
	"ControllersCodexAccountUsageSummaryResponse":         "CodexAccountUsageSummaryResponse",
	"ControllersCodexCapabilityObservationResponse":       "CodexCapabilityObservationResponse",
	"ControllersCodexAccountCapabilitiesResponse":         "CodexAccountCapabilitiesResponse",
	"ControllersCodexUnmanagedGlobalAccountResponse":      "CodexUnmanagedGlobalAccountResponse",
	"ControllersCodexAccountLoginResponse":                "CodexAccountLoginResponse",
	"ControllersCodexActiveLoginResponse":                 "CodexActiveLoginResponse",
	"ControllersCodexAccountSwitchResponse":               "CodexAccountSwitchResponse",
	"ControllersCodexAccountSwitchSessionResponse":        "CodexAccountSwitchSessionResponse",
	"ControllersCodexAccountSwitchPhase":                  "CodexAccountSwitchPhase",
	"ControllersStartCodexAccountSwitchRequest":           "StartCodexAccountSwitchRequest",
	"ControllersCodexAccountSwitchIDParam":                "CodexAccountSwitchIDParam",
	"DomainCodexCapacitySummary":                          "CodexCapacitySummary",
	"ControllersWorkspaceFileResponse":                    "WorkspaceFileResponse",
	"ControllersWorkspaceTreeQuery":                       "WorkspaceTreeQuery",
	"ControllersListWorkspaceTreeResponse":                "ListWorkspaceTreeResponse",
	"ControllersWorkspaceTreeEntry":                       "WorkspaceTreeEntry",
	"ControllersListEditorsResponse":                      "ListEditorsResponse",
	"ControllersEditorSummary":                            "EditorSummary",
	"ControllersOpenSessionEditorRequest":                 "OpenSessionEditorRequest",
	"ControllersOpenSessionEditorResponse":                "OpenSessionEditorResponse",
	"ControllersKillSessionResponse":                      "KillSessionResponse",
	"ControllersRollbackSessionResponse":                  "RollbackSessionResponse",
	"ControllersSendSessionMessageRequest":                "SendSessionMessageRequest",
	"ControllersSendSessionMessageResponse":               "SendSessionMessageResponse",
	"ControllersDelegateTaskRequest":                      "DelegateTaskRequest",
	"ControllersDelegateTaskResponse":                     "DelegateTaskResponse",
	"ControllersClaimPRResponse":                          "ClaimPRResponse",
	"ControllersClaimPRRequest":                           "ClaimPRRequest",
	"ControllersSessionPRFacts":                           "SessionPRFacts",
	"ControllersSessionPRSummary":                         "SessionPRSummary",
	"ControllersSessionPRCISummary":                       "SessionPRCISummary",
	"ControllersSessionPRFailingCheck":                    "SessionPRFailingCheck",
	"ControllersSessionPRReviewSummary":                   "SessionPRReviewSummary",
	"ControllersSessionPRReviewEntry":                     "SessionPRReviewEntry",
	"ControllersSessionPRUnresolvedReviewer":              "SessionPRUnresolvedReviewer",
	"ControllersSessionPRReviewCommentLink":               "SessionPRReviewCommentLink",
	"ControllersSessionPRMergeabilitySummary":             "SessionPRMergeabilitySummary",
	"ControllersSessionPRConflictFile":                    "SessionPRConflictFile",
	"ControllersListSessionPRsResponse":                   "ListSessionPRsResponse",
	"ControllersSetActivityRequest":                       "SetActivityRequest",
	"ControllersSetActivityResponse":                      "SetActivityResponse",
	"ControllersSetReviewActivityRequest":                 "SetReviewActivityRequest",
	"ControllersSetReviewActivityResponse":                "SetReviewActivityResponse",
	"ControllersSpawnOrchestratorRequest":                 "SpawnOrchestratorRequest",
	"ControllersSpawnOrchestratorResponse":                "SpawnOrchestratorResponse",
	"ControllersOrchestratorResponse":                     "OrchestratorResponse",
	"AgentInventory":                                      "ListAgentsResponse",
	"AgentInfo":                                           "AgentInfo",
	"AgentProbeResult":                                    "ProbeAgentResponse",
	"AgentReadiness":                                      "AgentReadinessResponse",
	"ControllersEnsureAgentReadinessRequest":              "EnsureAgentReadinessRequest",
	"DomainAgentReadinessSnapshot":                        "AgentReadinessSnapshot",
	"DomainAgentInstallationObservation":                  "AgentInstallationObservation",
	"DomainAgentAuthenticationObservation":                "AgentAuthenticationObservation",
	// service/systemcheck: "SystemcheckReport" is a generic default name that
	// reads like an internal type, not a wire response — rename to match the
	// endpoint it serves, same treatment as AgentInventory above.
	"SystemcheckReport":             "SystemRequirementsResponse",
	"SystemcheckRequirement":        "SystemRequirement",
	"ControllersInstallTargetParam": "InstallTargetParam",
	// service/systeminstall: Job backs both StartInstallResponse and
	// InstallStatusResponse (they're the same Go type), so it reflects to one
	// shared component — name it after the domain concept, not either alias.
	"SysteminstallJob":                            "InstallJob",
	"SysteminstallAgentPlan":                      "AgentInstallPlan",
	"SysteminstallAgentInstallMethod":             "AgentInstallMethod",
	"ControllersAgentInstallerCatalogResponse":    "AgentInstallerCatalogResponse",
	"ControllersStartAgentInstallRequest":         "StartAgentInstallRequest",
	"ControllersAgentInstallJobsResponse":         "AgentInstallJobsResponse",
	"AgentauthAction":                             "AgentAuthAction",
	"AgentauthPlan":                               "AgentAuthPlan",
	"ControllersListAgentAuthPlansResponse":       "ListAgentAuthPlansResponse",
	"ControllersStartAgentAuthResponse":           "StartAgentAuthResponse",
	"PortsAgentModelCatalog":                      "AgentModelsResponse",
	"PortsAgentModelInfo":                         "AgentModelInfo",
	"ControllersListNotificationsQuery":           "ListNotificationsQuery",
	"ControllersNotificationStreamQuery":          "NotificationStreamQuery",
	"ControllersNotificationIDParam":              "NotificationIDParam",
	"ControllersNotificationTarget":               "NotificationTarget",
	"ControllersNotificationResponse":             "NotificationResponse",
	"ControllersListNotificationsResponse":        "ListNotificationsResponse",
	"ControllersMarkNotificationReadRequest":      "MarkNotificationReadRequest",
	"ControllersNotificationEnvelope":             "NotificationEnvelope",
	"ControllersMarkAllNotificationsReadRequest":  "MarkAllNotificationsReadRequest",
	"ControllersMarkAllNotificationsReadResponse": "MarkAllNotificationsReadResponse",
	"ControllersCreateReportRequest":              "CreateReportRequest",
	"ControllersReportOutputRequest":              "ReportOutputRequest",
	"ControllersCreateReportResponse":             "CreateReportResponse",
	"ControllersUsageHookMetadata":                "UsageHookMetadata",
	"ControllersListUsageSessionsQuery":           "ListUsageSessionsQuery",
	"ControllersEstimatedCostResponse":            "EstimatedCostResponse",
	"ControllersCompactSessionUsageResponse":      "CompactSessionUsageResponse",
	"ControllersListCompactSessionUsageResponse":  "ListCompactSessionUsageResponse",
	"ControllersUsageTotalsResponse":              "UsageTotalsResponse",
	"ControllersUsageModelResponse":               "UsageModelResponse",
	"ControllersUsageHarnessResponse":             "UsageHarnessResponse",
	"ControllersSessionUsageResponse":             "SessionUsageResponse",
	// httpd/controllers — standalone shell terminal wire envelopes
	"ControllersShellTerminalHandleIDParam":            "ShellTerminalHandleIDParam",
	"ControllersOpenShellTerminalRequest":              "OpenShellTerminalRequest",
	"ControllersUpdateShellTerminalRequest":            "UpdateShellTerminalRequest",
	"ControllersShellTerminalResponse":                 "ShellTerminalResponse",
	"ControllersListShellTerminalsResponse":            "ListShellTerminalsResponse",
	"ControllersShellTerminalEnvelope":                 "ShellTerminalEnvelope",
	"ControllersOpenCodexAccountLoginTerminalResponse": "OpenCodexAccountLoginTerminalResponse",
	"ControllersCodexAccountLoginTerminalResponse":     "CodexAccountLoginTerminalResponse",
	// httpd/controllers — PR wire envelopes
	"ControllersMergePRRequest":          "MergePRRequest",
	"ControllersMergePRResponse":         "MergePRResponse",
	"ControllersResolveCommentsRequest":  "ResolveCommentsRequest",
	"ControllersResolveCommentsResponse": "ResolveCommentsResponse",
	// httpd/controllers — review wire envelopes
	"ControllersListReviewsResponse":   "ListReviewsResponse",
	"ControllersReviewRunResponse":     "ReviewRunResponse",
	"ControllersTriggerReviewResponse": "TriggerReviewResponse",
	"ControllersCancelReviewResponse":  "CancelReviewResponse",
	"ControllersKillReviewResponse":    "KillReviewResponse",
	"ControllersRestoreReviewResponse": "RestoreReviewResponse",
	"ControllersSubmitReviewItem":      "SubmitReviewItem",
	"ControllersSubmitReviewInput":     "SubmitReviewInput",
	// domain review entities
	"DomainReviewRun":     "ReviewRun",
	"ReviewPRReviewState": "PRReviewState",
	// httpd/controllers: import wire envelopes
	"ControllersImportStatusResponse": "ImportStatusResponse",
	"ControllersImportRunResponse":    "ImportRunResponse",
	// service/importer: project import onboarding DTOs
	"ImporterImportValidationInput":         "ImportValidationInput",
	"ImporterImportValidationResult":        "ImportValidationResult",
	"ImporterRepoGitStatus":                 "RepoGitStatus",
	"ImporterGitPreparationInput":           "GitPreparationInput",
	"ImporterGitPreparationResult":          "GitPreparationResult",
	"ImporterGitPreparationEvent":           "GitPreparationEvent",
	"ImporterGitHubRepositoryPreparation":   "GitHubRepositoryPreparation",
	"ImporterGitRepositoryPreparationInput": "GitRepositoryPreparationInput",
	// httpd/controllers: dev wire envelopes
	"ControllersDevImportProjectsRequest":  "DevImportProjectsRequest",
	"ControllersDevImportProjectsResponse": "DevImportProjectsResponse",
	// httpd/controllers: mobile wire envelopes
	"ControllersMobileStatusResponse":  "MobileStatusResponse",
	"MobilebridgeEndpoint":             "MobileEndpoint",
	"MobilebridgeTunnelStatus":         "MobileTunnelStatus",
	"ControllersIdentityResponse":      "IdentityResponse",
	"ControllersEndpointsResponse":     "EndpointsResponse",
	"ControllersMobileDeviceResponse":  "MobileDeviceResponse",
	"ControllersMobileDevicesResponse": "MobileDevicesResponse",
	"ControllersMuteDeviceRequest":     "MuteDeviceRequest",
	"ControllersInstallIDParam":        "InstallIDParam",
	"ControllersPushPairingIDParam":    "PushPairingIDParam",
	// devimport report
	"DevimportReport":   "DevImportProjectsReport",
	"DevimportConflict": "DevImportProjectsConflict",
	// httpd/controllers: push-device wire envelopes
	"ControllersRegisterPushDeviceRequest":    "RegisterPushDeviceRequest",
	"ControllersPushDeviceEnvelope":           "PushDeviceEnvelope",
	"ControllersPushDeviceResponse":           "PushDeviceResponse",
	"ControllersUnregisterPushDeviceResponse": "UnregisterPushDeviceResponse",
	// legacyimport report
	"LegacyimportReport": "ImportReport",
	// service/project entities + DTOs
	"ProjectProject":                    "Project",
	"ProjectSummary":                    "ProjectSummary",
	"ProjectDegraded":                   "DegradedProject",
	"ProjectAddInput":                   "AddProjectInput",
	"ProjectCloneInput":                 "CloneProjectInput",
	"ProjectClonePreparationResult":     "ClonePreparationResult",
	"ProjectInitializeRepositoryInput":  "InitializeRepositoryInput",
	"ProjectInitializeRepositoryResult": "InitializeRepositoryResult",
	"ProjectRemoveResult":               "RemoveProjectResult",
	"ProjectSetPermissionsInput":        "SetProjectPermissionsInput",
	"ProjectSetConfigInput":             "SetProjectConfigInput",
	"ProjectUpdateSettingsInput":        "UpdateProjectSettingsInput",
	"ProjectWorkspaceRepo":              "WorkspaceRepo",
	"SessionWorkspaceFileStatus":        "WorkspaceFileStatus",
}

// markRequestBodyRequired sets requestBody.required: true on the operation's
// JSON body. swaggest leaves it absent (== optional) for AddReqStructure bodies.
func markRequestBodyRequired(cor openapi.ContentOrReference) {
	if rb, ok := cor.(*openapi31.RequestBodyOrReference); ok && rb.RequestBody != nil {
		rb.RequestBody.WithRequired(true)
	}
}

// nonNullableSlices drops the "null" that swaggest unions into every Go slice
// type (a nil slice marshals as JSON null). A required array field should be
// `T[]`, not `T[] | null`; the handlers normalise nil to an empty slice, so
// null never reaches the wire. Byte slices (base64 strings) are left alone.
func nonNullableSlices(p jsonschema.InterceptNullabilityParams) {
	if !p.NullAdded || p.Type == nil || p.Type.Kind() != reflect.Slice {
		return
	}
	if p.Type.Elem().Kind() == reflect.Uint8 {
		return
	}
	p.Schema.TypeEns().WithSimpleTypes(jsonschema.Array)
	p.Schema.Type.SliceOfSimpleTypeValues = nil
}

// validNullableReferenceUnions removes the original concrete type left beside
// an enveloped null-or-reference anyOf. Keeping that sibling type would apply
// it conjunctively and reject null despite the explicit null branch.
func validNullableReferenceUnions(p jsonschema.InterceptNullabilityParams) {
	if p.Schema.Type == nil || len(p.Schema.AnyOf) == 0 {
		return
	}
	for _, option := range p.Schema.AnyOf {
		if option.TypeObject != nil && option.TypeObject.HasType(jsonschema.Null) {
			p.Schema.Type = nil
			return
		}
	}
}

// requiredFromJSONTag marks a property required when its json tag lacks
// `omitempty` (the Go convention for "always present"). Runs after default
// processing so ParentSchema exists; skips fields without a json tag (e.g. path
// params, which swaggest marks required on their own).
func requiredFromJSONTag(p jsonschema.InterceptPropParams) error {
	if !p.Processed || p.ParentSchema == nil {
		return nil
	}
	jsonTag := p.Field.Tag.Get("json")
	if jsonTag == "" || jsonTag == "-" {
		return nil
	}
	parts := strings.Split(jsonTag, ",")
	name := parts[0]
	if name == "" {
		name = p.Name
	}
	for _, opt := range parts[1:] {
		if opt == "omitempty" {
			return nil
		}
	}
	for _, existing := range p.ParentSchema.Required {
		if existing == name {
			return nil
		}
	}
	p.ParentSchema.Required = append(p.ParentSchema.Required, name)
	return nil
}

// --- operation registry -----------------------------------------------------

type respUnit struct {
	status int
	body   any
}

type operation struct {
	method, path, id, summary string
	tag                       string
	pathParams                []any // path/query param containers (e.g. ProjectIDParam)
	reqBody                   any   // JSON request body struct, nil when the op takes none
	// optionalReqBody declares the body without marking it required, for the
	// handlers that accept an empty body as a meaningful default.
	optionalReqBody bool
	resps           []respUnit
	contentTypes    map[int]string // optional non-JSON response content types by status
}

func operations() []operation {
	ops := append([]operation{}, eventOperations()...)
	ops = append(ops, agentOperations()...)
	ops = append(ops, projectOperations()...)
	ops = append(ops, sessionOperations()...)
	ops = append(ops, prOperations()...)
	ops = append(ops, reviewOperations()...)
	ops = append(ops, notificationOperations()...)
	ops = append(ops, reportOperations()...)
	ops = append(ops, usageOperations()...)
	ops = append(ops, pushOperations()...)
	ops = append(ops, importOperations()...)
	ops = append(ops, devOperations()...)
	ops = append(ops, mobileOperations()...)
	ops = append(ops, mobileDeviceOperations()...)
	ops = append(ops, browserOperations()...)
	ops = append(ops, shellTerminalOperations()...)
	ops = append(ops, systemOperations()...)
	ops = append(ops, identityOperations()...)
	ops = append(ops, endpointsOperations()...)
	return ops
}

// endpointsOperations declares the phone's endpoint refresh. Not under
// /api/v1/mobile, which lanControlBlock 404s on the only listener a phone can
// reach.
func endpointsOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/endpoints", id: "getEndpoints", tag: "identity",
			summary: "List the ways this daemon can currently be reached",
			resps: []respUnit{
				{http.StatusOK, controllers.EndpointsResponse{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

// identityOperations declares the unauthenticated host-identity probe. It is
// the one route the Connect Mobile LAN listener serves without the connection
// password, so the phone can confirm which machine answered before presenting
// a credential. See docs/adr/0003-unauthenticated-identity-probe.md.
func identityOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/identity", id: "getIdentity", tag: "identity",
			summary: "Identify the daemon so a client can confirm which machine answered",
			resps: []respUnit{
				{http.StatusOK, controllers.IdentityResponse{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

func reportOperations() []operation {
	return []operation{{
		method: http.MethodPost, path: "/api/v1/reports", id: "createReport", tag: "reports",
		summary: "Persist a worker report for later orchestrator delivery",
		reqBody: controllers.CreateReportRequest{},
		resps: []respUnit{
			{http.StatusCreated, controllers.CreateReportResponse{}},
			{http.StatusBadRequest, envelope.APIError{}},
			{http.StatusNotFound, envelope.APIError{}},
			{http.StatusInternalServerError, envelope.APIError{}},
			{http.StatusNotImplemented, envelope.APIError{}},
		},
	}}
}

// systemOperations declares the startup requirements gate the desktop loading
// screen polls before showing the board, plus the real-install operations for
// the fixed system/install target allowlist.
func systemOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/system/requirements", id: "getSystemRequirements", tag: "system",
			summary: "Check local machine readiness (git, tmux, agent harness, gh)",
			resps: []respUnit{
				{http.StatusOK, controllers.SystemRequirementsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/system/github-auth", id: "getGitHubAuthRequirement", tag: "system",
			summary: "Check advisory GitHub CLI authentication without blocking startup",
			resps: []respUnit{
				{http.StatusOK, controllers.GitHubAuthRequirementResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/system/github-auth/terminal", id: "openGitHubAuthTerminal", tag: "system",
			summary: "Open a trusted terminal running the GitHub CLI login flow",
			resps: []respUnit{
				{http.StatusCreated, controllers.ShellTerminalEnvelope{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/system/install/{target}", id: "startSystemInstall", tag: "system",
			summary:    "Start (or return the already-running) install job for a fixed system target",
			pathParams: []any{controllers.InstallTargetParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.StartInstallResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/system/install/{target}", id: "getSystemInstallStatus", tag: "system",
			summary:    "Get the current or last known install job status for a system target",
			pathParams: []any{controllers.InstallTargetParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.InstallStatusResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

func browserOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/browser/status", id: "getBrowserStatus", tag: "browser",
			summary:    "Check whether the desktop browser runtime is connected for a session",
			pathParams: []any{controllers.BrowserStatusQuery{}, controllers.BrowserCapabilityHeader{}},
			resps: []respUnit{
				{http.StatusOK, controllers.BrowserStatusResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/browser/commands", id: "executeBrowserCommand", tag: "browser",
			summary:    "Execute a target-scoped command in a session's desktop browser",
			pathParams: []any{controllers.BrowserCapabilityHeader{}},
			reqBody:    controllers.BrowserCommandRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.BrowserCommandResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusServiceUnavailable, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

type conversationSnapshotQuery struct {
	BeforeSequence *int64 `query:"beforeSequence,omitempty" minimum:"1" description:"Read items older than this conversation sequence. Omit for the newest page."`
	Limit          *int64 `query:"limit,omitempty" minimum:"1" maximum:"500" description:"Maximum combined messages and activities to return. Defaults to 200."`
}

func usageOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/usage/sessions", id: "listCompactSessionUsage", tag: "usage",
			summary:    "List compact token and estimated cost usage for session cards",
			pathParams: []any{controllers.ListUsageSessionsQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListCompactSessionUsageResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/usage/sessions/{sessionId}", id: "getSessionUsage", tag: "usage",
			summary:    "Get detailed token and estimated cost usage for one session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionUsageResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

// shellTerminalOperations describes the standalone shell terminal surface:
// shells the user opens by hand, with no agent session behind them.
func shellTerminalOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/settings", id: "getSettings", tag: "settings",
			summary: "Read the daemon-owned user preferences",
			resps: []respUnit{
				{http.StatusOK, controllers.SettingsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/settings/session-interface", id: "updateSessionInterface", tag: "settings",
			summary: "Choose the default interface for new sessions",
			reqBody: controllers.UpdateSessionInterfaceRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SettingsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/settings/cloud-offering", id: "updateCloudOffering", tag: "settings",
			summary: "Turn the cloud offering on or off for this machine",
			reqBody: controllers.UpdateCloudOfferingRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SettingsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/conversation", id: "getSessionConversation", tag: "conversations",
			summary:    "Read a chat session's durable conversation",
			pathParams: []any{controllers.SessionIDParam{}, conversationSnapshotQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationSnapshotResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/messages", id: "sendSessionConversationMessage", tag: "conversations",
			summary:    "Send a message to a chat session's agent",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SendConversationMessageRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.SendConversationMessageResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/edit", id: "editSessionConversationMessage", tag: "conversations",
			summary:    "Branch before and replace an earlier human prompt",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			reqBody:    controllers.EditConversationMessageRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.EditConversationMessageResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/branches/{branchId}/activate", id: "activateSessionConversationBranch", tag: "conversations",
			summary:    "Resume a durable conversation branch without sending",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationBranchIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.ActivateConversationBranchResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/approvals/{requestId}/resolve", id: "resolveSessionConversationApproval", tag: "conversations",
			summary:    "Answer a pending approval in a chat session",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationRequestIDParam{}},
			reqBody:    controllers.ResolveConversationApprovalRequest{},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/inputs/{requestId}/resolve", id: "resolveSessionConversationInput", tag: "conversations",
			summary:    "Answer a structured input request in a chat session",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationRequestIDParam{}},
			reqBody:    controllers.ResolveConversationInputRequest{},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/compact", id: "compactSessionConversation", tag: "conversations",
			summary:    "Summarize earlier history to reclaim context in a chat session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.CompactConversationResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/mcp/reload", id: "reloadSessionConversationMcpServers", tag: "conversations",
			summary:    "Restart the tool servers a chat session can reach",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ReloadConversationMCPServersResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/conversation/models", id: "listSessionConversationModels", tag: "conversations",
			summary:    "List the models the provider offers for a chat session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationModelsResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/conversation/config-options", id: "listSessionConversationConfigOptions", tag: "conversations",
			summary:    "List the live session controls the provider advertises",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationConfigOptionsResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}/conversation/config-options/{configId}", id: "setSessionConversationConfigOption", tag: "conversations",
			summary:    "Choose one provider-advertised session configuration value",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationConfigIDParam{}},
			reqBody:    controllers.SetConversationConfigOptionRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationConfigOptionsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/conversation/skills", id: "listSessionConversationSkills", tag: "conversations",
			summary:    "List the named skills the provider offers for a chat session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationSkillsResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}/conversation/settings", id: "setSessionConversationTurnSettings", tag: "conversations",
			summary:    "Choose the model, reasoning effort and approval mode for the next turn",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.ConversationTurnSettingsPayload{},
			resps: []respUnit{
				{http.StatusOK, controllers.ConversationTurnSettingsPayload{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/interrupt", id: "interruptSessionConversationTurn", tag: "conversations",
			summary:    "Cancel the in-flight turn in a chat session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/steer", id: "steerSessionConversationTurn", tag: "conversations",
			summary:    "Send guidance into the in-flight turn of a chat session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SteerConversationRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.SteerConversationResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/steer", id: "promoteQueuedSessionConversationTurn", tag: "conversations",
			summary:    "Promote a queued message into the in-flight turn",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.PromoteQueuedTurnResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/cancel", id: "cancelQueuedSessionConversationTurn", tag: "conversations",
			summary:    "Remove one queued message without stopping the running turn",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/queue/edit", id: "editQueuedSessionConversationTurn", tag: "conversations",
			summary:    "Rewrite one queued message before it dispatches",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			reqBody:    controllers.EditQueuedConversationMessageRequest{},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/queue/reorder", id: "reorderQueuedSessionConversationTurns", tag: "conversations",
			summary:    "Rewrite the durable queue order for undispatched turns",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.ReorderQueuedConversationTurnsRequest{},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/rollback", id: "rollbackSessionConversation", tag: "conversations",
			summary:    "Discard a turn and everything after it from the agent's memory",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.RollbackConversationResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/conversation/turns/{turnId}/retry", id: "retrySessionConversationTurn", tag: "conversations",
			summary:    "Re-dispatch a failed turn's durable prompt as a new turn",
			pathParams: []any{controllers.SessionIDParam{}, controllers.ConversationTurnIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.RetryTurnResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/sessions/{sessionId}/conversation/title", id: "setSessionConversationTitle", tag: "conversations",
			summary:    "Name the provider's conversation thread",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetConversationTitleRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.SetConversationTitleResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/shell-terminals", id: "listShellTerminals", tag: "shellTerminals",
			summary: "List the standalone shell terminals owned by the current app run",
			resps: []respUnit{
				{http.StatusOK, controllers.ListShellTerminalsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/shell-terminals", id: "openShellTerminal", tag: "shellTerminals",
			summary: "Open a standalone shell terminal",
			reqBody: controllers.OpenShellTerminalRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.ShellTerminalEnvelope{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/shell-terminals/{handleId}", id: "renameShellTerminal", tag: "shellTerminals",
			summary:    "Rename a standalone shell terminal tab",
			pathParams: []any{controllers.ShellTerminalHandleIDParam{}},
			reqBody:    controllers.UpdateShellTerminalRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.ShellTerminalEnvelope{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/shell-terminals/{handleId}", id: "closeShellTerminal", tag: "shellTerminals",
			summary:    "Close a standalone shell terminal and destroy its PTY",
			pathParams: []any{controllers.ShellTerminalHandleIDParam{}},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

func agentOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/agents/auth-plans", id: "listAgentAuthPlans", tag: "agents",
			summary: "Return display-safe native authentication plans for supported agents",
			resps: []respUnit{
				{http.StatusOK, controllers.ListAgentAuthPlansResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/{agent}/auth", id: "startAgentAuth", tag: "agents",
			summary:    "Open the fixed native authentication flow for one agent",
			pathParams: []any{controllers.AgentIDParam{}},
			resps: []respUnit{
				{http.StatusCreated, controllers.StartAgentAuthResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents", id: "listAgents", tag: "agents",
			summary: "Return cached supported and locally installed agent adapters",
			resps: []respUnit{
				{http.StatusOK, controllers.ListAgentsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/readiness", id: "getAgentReadiness", tag: "agents",
			summary: "Return cached normalized agent readiness without running native checks",
			resps: []respUnit{
				{http.StatusOK, controllers.AgentReadinessResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/readiness/ensure", id: "ensureAgentReadiness", tag: "agents",
			summary: "Ensure normalized readiness for selected agent adapters",
			reqBody: controllers.EnsureAgentReadinessRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.AgentReadinessResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/codex/accounts", id: "getCodexAccounts", tag: "agents",
			summary: "Return cached AO Codex accounts and active-account state",
			resps:   []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusServiceUnavailable, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/ensure", id: "ensureCodexAccounts", tag: "agents",
			summary: "Discover Codex accounts and ensure authentication, capacity, and optional usage",
			reqBody: controllers.EnsureCodexAccountsRequest{},
			resps:   []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/{accountId}/reset-credit/consume", id: "consumeCodexAccountResetCredit", tag: "agents",
			summary: "Consume one provider-reported Codex usage-limit reset credit", pathParams: []any{controllers.CodexAccountIDParam{}},
			reqBody: controllers.ConsumeCodexAccountResetCreditRequest{},
			resps:   []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/{accountId}/login-terminal", id: "openCodexAccountReauthenticationTerminal", tag: "agents",
			summary: "Open native Codex sign-in for one retained account", pathParams: []any{controllers.CodexAccountIDParam{}},
			resps: []respUnit{{http.StatusAccepted, controllers.OpenCodexAccountLoginTerminalResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/{accountId}/logout", id: "logoutCodexAccount", tag: "agents",
			summary: "Log out one retained Codex account", pathParams: []any{controllers.CodexAccountIDParam{}},
			resps: []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodDelete, path: "/api/v1/agents/codex/accounts/{accountId}", id: "deleteCodexAccount", tag: "agents",
			summary: "Delete one inactive signed-out Codex account", pathParams: []any{controllers.CodexAccountIDParam{}},
			resps: []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/login-terminal", id: "openCodexAccountLoginTerminal", tag: "agents",
			summary: "Open an inline native login terminal for a new AO Codex account",
			resps:   []respUnit{{http.StatusAccepted, controllers.OpenCodexAccountLoginTerminalResponse{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/login-operations/{operationId}/verify", id: "verifyCodexAccountLogin", tag: "agents",
			summary: "Verify one native Codex account login operation", pathParams: []any{controllers.CodexAccountLoginIDParam{}},
			resps: []respUnit{{http.StatusOK, controllers.CodexAccountLoginResponse{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/accounts/login-operations/{operationId}/cancel", id: "cancelCodexAccountLogin", tag: "agents",
			summary: "Cancel one native Codex account login operation", pathParams: []any{controllers.CodexAccountLoginIDParam{}},
			resps: []respUnit{{http.StatusOK, controllers.CodexAccountLoginResponse{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/codex/accounts/events", id: "streamCodexAccounts", tag: "agents",
			summary:      "Stream cached and live Codex account state",
			resps:        []respUnit{{http.StatusOK, controllers.CodexAccountsResponse{}}, {http.StatusServiceUnavailable, envelope.APIError{}}, {http.StatusNotImplemented, envelope.APIError{}}},
			contentTypes: map[int]string{http.StatusOK: "text/event-stream"},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/account-switches", id: "startCodexAccountSwitch", tag: "agents",
			summary: "Start a global AO Codex account switch", reqBody: controllers.StartCodexAccountSwitchRequest{},
			resps: []respUnit{{http.StatusAccepted, controllers.CodexAccountSwitchResponse{}}, {http.StatusBadRequest, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}, {http.StatusServiceUnavailable, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/codex/account-switches/{switchId}/recover", id: "recoverCodexAccountSwitch", tag: "agents",
			summary: "Retry incomplete restarts for one Codex account switch", pathParams: []any{controllers.CodexAccountSwitchIDParam{}},
			resps: []respUnit{{http.StatusOK, controllers.CodexAccountSwitchResponse{}}, {http.StatusNotFound, envelope.APIError{}}, {http.StatusConflict, envelope.APIError{}}},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/refresh", id: "refreshAgents", tag: "agents",
			summary: "Refresh the cached local agent adapter catalog",
			resps: []respUnit{
				{http.StatusOK, controllers.RefreshAgentsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/{agent}/probe", id: "probeAgent", tag: "agents",
			summary:    "Ensure launch-fresh readiness for one agent adapter",
			pathParams: []any{controllers.AgentIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ProbeAgentResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/installers", id: "listAgentInstallers", tag: "agents",
			summary: "Resolve the safe installation plan for every supported agent harness",
			resps: []respUnit{
				{http.StatusOK, controllers.AgentInstallerCatalogResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/{agent}/install", id: "startAgentInstall", tag: "agents",
			summary:    "Start an asynchronous install for one fixed agent harness",
			pathParams: []any{controllers.AgentIDParam{}},
			reqBody:    controllers.StartAgentInstallRequest{}, optionalReqBody: true,
			resps: []respUnit{
				{http.StatusAccepted, controllers.AgentInstallResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/install-jobs", id: "listAgentInstallJobs", tag: "agents",
			summary: "Return the latest durable install job for every agent harness",
			resps: []respUnit{
				{http.StatusOK, controllers.AgentInstallJobsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/{agent}/verify", id: "verifyAgentInstall", tag: "agents",
			summary:    "Verify an installed harness without reinstalling or probing authentication",
			pathParams: []any{controllers.AgentIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.AgentInstallResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/{agent}/install", id: "getAgentInstallStatus", tag: "agents",
			summary:    "Get the current or last install job for one agent harness",
			pathParams: []any{controllers.AgentIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.AgentInstallResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/agents/{agent}/models", id: "getAgentModels", tag: "agents",
			summary:    "Return the cached model picker for one agent, discovering it on first use",
			pathParams: []any{controllers.AgentIDParam{}, controllers.AgentModelsQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.AgentModelsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/agents/{agent}/models/refresh", id: "refreshAgentModels", tag: "agents",
			summary:    "Refresh and cache the model picker for one agent",
			pathParams: []any{controllers.AgentIDParam{}, controllers.AgentModelsRefreshQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.AgentModelsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

// mobileOperations declares the 5 /mobile control operations. These are
// mounted on the loopback router (mountMobile in router.go), not the REST
// /api/v1 group — only the desktop/CLI may enable, disable, or regenerate the
// phone's LAN access; the phone never toggles its own connection. Must stay
// 1:1 with the routes mountMobile registers (enforced by the parity test).
func mobileOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/mobile/status", id: "getMobileStatus", tag: "mobile",
			summary: "Check whether Connect Mobile's LAN bridge is enabled",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/mobile/remote-access", id: "startMobileRemoteAccess", tag: "mobile",
			summary: "Look for a connector again and start it, without rotating the password",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/mobile/enable", id: "enableMobile", tag: "mobile",
			summary: "Enable the Connect Mobile LAN bridge and issue a fresh password",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/mobile/disable", id: "disableMobile", tag: "mobile",
			summary: "Disable the Connect Mobile LAN bridge",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/mobile/regenerate", id: "regenerateMobile", tag: "mobile",
			summary: "Rotate the Connect Mobile password, dropping any connected phone",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/mobile/secure-pairing", id: "setMobileSecurePairing", tag: "mobile",
			summary: "Turn TLS-over-Tailscale secure pairing on or off",
			reqBody: controllers.SetSecurePairingRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.MobileStatusResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
	}
}

// mobileDeviceOperations declares the desktop-only mobile device roster
// routes. These sit under /api/v1/mobile — like mobileOperations above — so
// they inherit the LAN listener's transport-level block; a paired phone can
// neither list nor manage the household's other devices. Must stay 1:1 with
// the routes mountMobileDevices registers (enforced by the parity test).
func mobileDeviceOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/mobile/devices", id: "listMobileDevices", tag: "mobile",
			summary: "List paired mobile devices with their live/muted status",
			resps: []respUnit{
				{http.StatusOK, controllers.MobileDevicesResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusServiceUnavailable, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/mobile/devices/{installId}", id: "muteMobileDevice", tag: "mobile",
			summary:    "Mute or unmute push notifications for a paired device",
			pathParams: []any{controllers.InstallIDParam{}},
			reqBody:    controllers.MuteDeviceRequest{},
			resps: []respUnit{
				{http.StatusOK, map[string]bool{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusServiceUnavailable, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/mobile/devices/{installId}", id: "removeMobileDevice", tag: "mobile",
			summary:    "Remove a paired device from the roster",
			pathParams: []any{controllers.InstallIDParam{}},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusServiceUnavailable, envelope.APIError{}},
			},
		},
	}
}

// importOperations declares the /import operations. Must stay 1:1 with
// the routes ImportController.Register mounts (enforced by the parity test).
func importOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/import", id: "getImportStatus", tag: "import",
			summary: "Check whether a legacy AO install is available to import",
			resps: []respUnit{
				{http.StatusOK, controllers.ImportStatusResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/import", id: "runImport", tag: "import",
			summary: "Run the legacy AO project import through the daemon store",
			resps: []respUnit{
				{http.StatusOK, controllers.ImportRunResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/imports/validate", id: "validateImport", tag: "import",
			summary: "Validate a selected folder for project import onboarding",
			reqBody: importsvc.ImportValidationInput{},
			resps: []respUnit{
				{http.StatusOK, importsvc.ImportValidationResult{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/imports/prepare-git", id: "prepareImportGit", tag: "import",
			summary: "Run approved Git preparation actions for project import onboarding",
			reqBody: importsvc.GitPreparationInput{},
			resps: []respUnit{
				{http.StatusOK, importsvc.GitPreparationResult{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

// devOperations declares developer-only API operations. Must stay 1:1 with
// the routes DevController.Register mounts (enforced by the parity test).
func devOperations() []operation {
	return []operation{
		{
			method: http.MethodPost, path: "/api/v1/dev/import-projects", id: "runDevImportProjects", tag: "dev",
			summary: "Run the developer project-registry import through the daemon store",
			reqBody: controllers.DevImportProjectsRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.DevImportProjectsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

func notificationOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/notifications", id: "listNotifications", tag: "notifications",
			summary:    "List notification history",
			pathParams: []any{controllers.ListNotificationsQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListNotificationsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/notifications/{id}", id: "markNotificationRead", tag: "notifications",
			summary:    "Mark a notification read",
			pathParams: []any{controllers.NotificationIDParam{}},
			reqBody:    controllers.MarkNotificationReadRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.NotificationEnvelope{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/notifications/read-all", id: "markAllNotificationsRead", tag: "notifications",
			summary: "Mark notifications read",
			reqBody: controllers.MarkAllNotificationsReadRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.MarkAllNotificationsReadResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/notifications/stream", id: "streamNotifications", tag: "notifications",
			summary:    "Stream created notifications",
			pathParams: []any{controllers.NotificationStreamQuery{}},
			resps: []respUnit{
				{http.StatusOK, ""},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
			contentTypes: map[int]string{http.StatusOK: "text/event-stream"},
		},
	}
}

// reviewOperations declares the session-scoped /reviews operations. Must stay
// 1:1 with the routes ReviewsController.Register mounts (enforced by the parity
// test).
// pushOperations declares the /push/devices operations. Must stay 1:1 with the
// routes PushController.Register mounts (enforced by the parity test).
func pushOperations() []operation {
	return []operation{
		{
			method: http.MethodPost, path: "/api/v1/push/devices", id: "registerPushDevice", tag: "push",
			summary: "Register (upsert) a phone's Expo push token",
			reqBody: controllers.RegisterPushDeviceRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.PushDeviceEnvelope{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/push/devices/{token}", id: "unregisterPushDevice", tag: "push",
			summary:    "Unregister a phone's Expo push token, leaving it paired",
			pathParams: []any{controllers.PushDeviceTokenParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.UnregisterPushDeviceResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/push/pairings/{id}", id: "unpairPushDevice", tag: "push",
			summary:    "Unpair this phone from the daemon, removing it from the roster",
			pathParams: []any{controllers.PushPairingIDParam{}},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

func reviewOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/reviews", id: "listReviews", tag: "reviews",
			summary:    "List a worker's code-review runs",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListReviewsResponse{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/trigger", id: "triggerReview", tag: "reviews",
			summary:    "Trigger a code review of a worker's PR",
			pathParams: []any{controllers.SessionIDParam{}},
			// Optional: an empty body runs under the project's configured reviewer.
			reqBody:         controllers.TriggerReviewRequest{},
			optionalReqBody: true,
			resps: []respUnit{
				{http.StatusOK, controllers.TriggerReviewResponse{}},
				{http.StatusCreated, controllers.TriggerReviewResponse{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/comments/resolve", id: "resolveReviewComment", tag: "reviews",
			summary:    "Resolve an external review comment thread",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.ResolveReviewCommentRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.ResolveReviewCommentResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/rerequest", id: "requestRereview", tag: "reviews",
			summary:    "Ask an external reviewer to re-review a worker's PR",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.RequestRereviewRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.RequestRereviewResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/cancel", id: "cancelReview", tag: "reviews",
			summary:    "Cancel a running code review",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.CancelReviewResponse{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/kill", id: "killReviewSession", tag: "reviews",
			summary:    "Kill a worker's reviewer terminal session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.KillReviewResponse{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/restore", id: "restoreReviewSession", tag: "reviews",
			summary:    "Restore a worker's reviewer terminal session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.RestoreReviewResponse{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/switch", id: "switchReviewSession", tag: "reviews",
			summary:    "Switch a worker's reviewer harness",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionReviewerRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.ListReviewsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/reviews/submit", id: "submitReview", tag: "reviews",
			summary:    "Record a reviewer's result for a worker's PR",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SubmitReviewInput{},
			resps: []respUnit{
				{http.StatusOK, controllers.ReviewRunResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

type eventsQuery struct {
	After *int64 `query:"after,omitempty" minimum:"0" description:"Replay events with seq greater than this cursor. When omitted, clients may send Last-Event-ID instead."`
}

func eventOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/events", id: "streamEvents", tag: "events",
			summary:    "Stream CDC events with durable replay",
			pathParams: []any{eventsQuery{}},
			resps: []respUnit{
				{http.StatusOK, ""},
				{status: http.StatusBadRequest, body: envelope.APIError{}},
				{status: http.StatusInternalServerError, body: envelope.APIError{}},
				{status: http.StatusNotImplemented, body: envelope.APIError{}},
			},
			contentTypes: map[int]string{http.StatusOK: "text/event-stream"},
		},
	}
}

// projectOperations declares the canonical /projects operations. The set must
// stay 1:1 with the routes ProjectsController.Register mounts —
// TestRouteSpecParity fails the build otherwise.
func projectOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/projects", id: "listProjects", tag: "projects",
			summary: "List all registered projects (active + degraded)",
			resps: []respUnit{
				{http.StatusOK, controllers.ListProjectsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/projects", id: "addProject", tag: "projects",
			summary: "Register a new project from a git repository path",
			reqBody: projectsvc.AddInput{},
			resps: []respUnit{
				{http.StatusCreated, controllers.ProjectResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/projects/clone", id: "cloneProject", tag: "projects",
			summary: "Clone and register a project from a git repository URL",
			reqBody: projectsvc.CloneInput{},
			resps: []respUnit{
				{http.StatusCreated, controllers.ProjectResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/projects/clone/prepare", id: "prepareCloneProject", tag: "projects",
			summary: "Clone a project without registering it so Git setup can be completed",
			reqBody: projectsvc.CloneInput{},
			resps: []respUnit{
				{http.StatusOK, projectsvc.ClonePreparationResult{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/projects/clone/cleanup", id: "cleanupPreparedClone", tag: "projects",
			summary: "Remove an abandoned clone created for project preparation",
			reqBody: projectsvc.ClonePreparationCleanupInput{},
			resps: []respUnit{
				{http.StatusNoContent, nil},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/projects/initialize", id: "initializeProjectRepository", tag: "projects",
			summary: "Initialize a selected folder as a Git repository with an initial commit",
			reqBody: projectsvc.InitializeRepositoryInput{},
			resps: []respUnit{
				{http.StatusOK, projectsvc.InitializeRepositoryResult{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		}, {
			method: http.MethodGet, path: "/api/v1/projects/{id}", id: "getProject", tag: "projects",
			summary:    "Fetch one project; discriminates ok vs degraded",
			pathParams: []any{controllers.ProjectIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.GetProjectResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/projects/{id}", id: "updateProjectSettings", tag: "projects",
			summary:    "Atomically replace a project's display name and config",
			pathParams: []any{controllers.ProjectIDParam{}},
			reqBody:    projectsvc.UpdateSettingsInput{},
			resps: []respUnit{
				{http.StatusOK, controllers.ProjectResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/projects/{id}/config", id: "setProjectConfig", tag: "projects",
			summary:    "Replace a project's per-project config",
			pathParams: []any{controllers.ProjectIDParam{}},
			reqBody:    projectsvc.SetConfigInput{},
			resps: []respUnit{
				{http.StatusOK, controllers.ProjectResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/projects/{id}/permissions", id: "setProjectPermissions", tag: "projects",
			summary:    "Remember project permissions for future sessions",
			pathParams: []any{controllers.ProjectIDParam{}},
			reqBody:    projectsvc.SetPermissionsInput{},
			resps: []respUnit{
				{http.StatusOK, controllers.ProjectResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/projects/{id}", id: "removeProject", tag: "projects",
			summary:    "Remove a project; stops sessions, cleans workspaces, unregisters",
			pathParams: []any{controllers.ProjectIDParam{}},
			resps: []respUnit{
				{http.StatusOK, projectsvc.RemoveResult{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
	}
}

func sessionOperations() []operation {
	return []operation{
		{
			method: http.MethodGet, path: "/api/v1/sessions", id: "listSessions", tag: "sessions",
			summary:    "List sessions",
			pathParams: []any{controllers.ListSessionsQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListSessionsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions", id: "spawnSession", tag: "sessions",
			summary: "Spawn a new agent session",
			reqBody: controllers.SpawnSessionRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.SpawnSessionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}", id: "getSession", tag: "sessions",
			summary:    "Fetch one session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/pin", id: "pinSession", tag: "sessions",
			summary:    "Pin a session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/sessions/{sessionId}/pin", id: "unpinSession", tag: "sessions",
			summary:    "Unpin a session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/preview", id: "getSessionPreview", tag: "sessions",
			summary:    "Discover a browser preview URL for a session workspace",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionPreviewResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/preview", id: "setSessionPreview", tag: "sessions",
			summary:    "Set (or autodetect) the browser preview URL for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionPreviewRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/sessions/{sessionId}/preview", id: "clearSessionPreview", tag: "sessions",
			summary:    "Clear the browser preview URL for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/preview/server", id: "getSessionPreviewServer", tag: "sessions",
			summary:    "Get the managed preview server status for a session",
			pathParams: []any{controllers.SessionIDParam{}, controllers.BrowserCapabilityHeader{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PreviewServerStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/preview/server", id: "startSessionPreviewServer", tag: "sessions",
			summary:    "Start a session-owned server from .ao/launch.json and open its application preview",
			pathParams: []any{controllers.SessionIDParam{}, controllers.BrowserCapabilityHeader{}},
			reqBody:    controllers.StartPreviewServerRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.PreviewServerStatusResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusRequestTimeout, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusGatewayTimeout, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/sessions/{sessionId}/preview/server", id: "stopSessionPreviewServer", tag: "sessions",
			summary:    "Stop the managed preview server for a session",
			pathParams: []any{controllers.SessionIDParam{}, controllers.BrowserCapabilityHeader{}},
			resps: []respUnit{
				{http.StatusOK, controllers.PreviewServerStatusResponse{}},
				{http.StatusForbidden, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/preview/files/*", id: "getSessionPreviewFile", tag: "sessions",
			summary:    "Serve a static browser preview file from a session workspace",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, ""},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
			contentTypes: map[int]string{http.StatusOK: "text/html"},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/attachments", id: "stageSessionAttachments", tag: "sessions",
			summary:    "Write images into a running session's worktree and return their paths",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.StageSessionAttachmentsRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.StageSessionAttachmentsResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/workspace/files", id: "listSessionWorkspaceFiles", tag: "sessions",
			summary:    "List files in a session workspace with git change status",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListWorkspaceFilesResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/workspace/events", id: "streamSessionWorkspaceChanges", tag: "sessions",
			summary:    "Stream session workspace file changes",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, ""},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
			contentTypes: map[int]string{http.StatusOK: "text/event-stream"},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/workspace/file", id: "getSessionWorkspaceFile", tag: "sessions",
			summary:    "Read one session workspace file and its git diff",
			pathParams: []any{controllers.SessionIDParam{}, controllers.WorkspaceFileQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.WorkspaceFileResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/desktop/sessions/{sessionId}/workspace", id: "getDesktopSessionWorkspace", tag: "sessions",
			summary:    "Resolve a session workspace for the loopback desktop supervisor",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.DesktopWorkspaceLocationResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/workspace/file/blob", id: "getSessionWorkspaceFileBlob", tag: "sessions",
			summary:    "Read one side of a session workspace image file",
			pathParams: []any{controllers.SessionIDParam{}, controllers.WorkspaceFileBlobQuery{}},
			resps: []respUnit{
				{http.StatusOK, ""},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
			contentTypes: map[int]string{http.StatusOK: "application/octet-stream"},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/workspace/tree", id: "listSessionWorkspaceTree", tag: "sessions",
			summary:    "List one directory level of a session workspace's full file tree, git-status decorated",
			pathParams: []any{controllers.SessionIDParam{}, controllers.WorkspaceTreeQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListWorkspaceTreeResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/pr", id: "listSessionPRs", tag: "sessions",
			summary:    "List pull requests owned by a session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListSessionPRsResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/pr/claim", id: "claimSessionPR", tag: "sessions",
			summary:    "Claim an existing pull request for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.ClaimPRRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.ClaimPRResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusServiceUnavailable, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}", id: "renameSession", tag: "sessions",
			summary:    "Rename a session display name",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.RenameSessionRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.RenameSessionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}/merge-policy", id: "setSessionMergePolicy", tag: "sessions",
			summary:    "Configure whether PR completion terminates the session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionMergePolicyRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SetSessionMergePolicyResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}/auto-inject-review", id: "setSessionAutoInjectReview", tag: "sessions",
			summary:    "Set the auto-inject review setting for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionAutoInjectReviewRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SetSessionAutoInjectReviewResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPatch, path: "/api/v1/sessions/{sessionId}/auto-inject-ci", id: "setSessionAutoInjectCI", tag: "sessions",
			summary:    "Set automatic CI-failure injection for a session and its PRs",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionAutoInjectCIRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SetSessionAutoInjectCIResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/sessions/{sessionId}/reviewer", id: "setSessionReviewer", tag: "sessions",
			summary:    "Set the reviewer harness for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionReviewerRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/sessions/{sessionId}/auto-review", id: "setSessionAutoReview", tag: "sessions",
			summary:    "Enable or disable automatic review for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetSessionAutoReviewRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/cleanup", id: "cleanupSessions", tag: "sessions",
			summary:    "Clean up terminated session workspaces",
			pathParams: []any{controllers.CleanupSessionsQuery{}},
			resps: []respUnit{
				{http.StatusOK, controllers.CleanupSessionsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/restore", id: "restoreSession", tag: "sessions",
			summary:    "Restore a terminated session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.RestoreSessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/exit-agent", id: "exitAgent", tag: "sessions",
			summary:    "Exit the agent while preserving its AO session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ExitAgentResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/resume-agent", id: "resumeAgent", tag: "sessions",
			summary:    "Resume an exited agent in its existing session",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ResumeAgentResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/switch-agent", id: "switchSessionAgent", tag: "sessions",
			summary:    "Switch a logical AO session to another agent harness",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SwitchAgentRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.AgentSwitchResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/agent-switches", id: "listSessionAgentSwitches", tag: "sessions",
			summary:    "List a session's durable agent-switch history",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.ListAgentSwitchesResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/agent-switches/{switchId}/recover", id: "recoverSessionAgentSwitch", tag: "sessions",
			summary:    "Retry safe source restoration for an agent switch",
			pathParams: []any{controllers.SessionIDParam{}, controllers.AgentSwitchIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.AgentSwitchResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/agent-switches/{switchId}/handoff", id: "submitSessionAgentHandoff", tag: "sessions",
			summary:    "Submit a generation-fenced source-agent handoff",
			pathParams: []any{controllers.SessionIDParam{}, controllers.AgentSwitchIDParam{}},
			reqBody:    controllers.SubmitAgentHandoffRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.AgentSwitchResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/sessions/{sessionId}/interface-transition", id: "getSessionInterfaceTransition", tag: "sessions",
			summary:    "Inspect TUI and Chat interface handoff support and progress",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionInterfaceTransitionStatusResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/interface-transition", id: "startSessionInterfaceTransition", tag: "sessions",
			summary:    "Switch a live session between its TUI and Chat controllers",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.StartSessionInterfaceTransitionRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.StartSessionInterfaceTransitionResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodDelete, path: "/api/v1/sessions/{sessionId}/interface-transition", id: "cancelSessionInterfaceTransition", tag: "sessions",
			summary:    "Cancel an interface handoff before its source controller stops",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusAccepted, controllers.CancelSessionInterfaceTransitionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPut, path: "/api/v1/sessions/{sessionId}/interface-transition/{transitionId}/notice-acknowledgement", id: "acknowledgeSessionInterfaceTransitionNotice", tag: "sessions",
			summary:    "Acknowledge a failed or recovered interface handoff notice",
			pathParams: []any{controllers.SessionIDParam{}, controllers.SessionInterfaceTransitionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.InterfaceTransitionNoticeAckResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/kill", id: "killSession", tag: "sessions",
			summary:    "Mark a session terminated and tear down runtime/workspace resources",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.KillSessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/rollback", id: "rollbackSession", tag: "sessions",
			summary:    "Undo a partially-completed spawn (delete seed row, or kill if spawn output exists)",
			pathParams: []any{controllers.SessionIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.RollbackSessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/send", id: "sendSessionMessage", tag: "sessions",
			summary:    "Send a message to a running session's agent",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SendSessionMessageRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SendSessionMessageResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				// Conflict: the session is terminated, or paused on a permission
				// decision (SESSION_AWAITING_DECISION) — the guarded send refuses
				// to paste into a pending dialog.
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/sessions/{sessionId}/activity", id: "setSessionActivity", tag: "sessions",
			summary:    "Report an agent activity-state signal for a session",
			pathParams: []any{controllers.SessionIDParam{}},
			reqBody:    controllers.SetActivityRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SetActivityResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/reviews/{reviewSessionID}/activity", id: "setReviewActivity", tag: "reviews",
			summary:    "Report a reviewer-owned hook signal",
			pathParams: []any{controllers.ReviewSessionIDParam{}},
			reqBody:    controllers.SetReviewActivityRequest{},
			resps: []respUnit{
				{http.StatusOK, controllers.SetReviewActivityResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/orchestrators", id: "listOrchestrators", tag: "sessions",
			summary: "List orchestrator sessions across projects",
			resps: []respUnit{
				{http.StatusOK, controllers.ListSessionsResponse{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/orchestrators", id: "spawnOrchestrator", tag: "sessions",
			summary: "Spawn an orchestrator session",
			reqBody: controllers.SpawnOrchestratorRequest{},
			resps: []respUnit{
				{http.StatusCreated, controllers.SpawnOrchestratorResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/orchestrators/delegate", id: "delegateTask", tag: "sessions",
			summary: "Start a worker task and ask the orchestrator to title it",
			reqBody: controllers.DelegateTaskRequest{},
			resps: []respUnit{
				{http.StatusAccepted, controllers.DelegateTaskResponse{}},
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodGet, path: "/api/v1/orchestrators/{id}", id: "getOrchestrator", tag: "sessions",
			summary:    "Fetch one orchestrator session",
			pathParams: []any{controllers.OrchestratorIDParam{}},
			resps: []respUnit{
				{http.StatusOK, controllers.SessionResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusInternalServerError, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}

// prOperations declares the PR action operations. These live in the SCM lane:
// the handler delegates to a PRService backed by the SCM provider. A nil
// PRService (SCM not configured) returns 501 for both routes.
func prOperations() []operation {
	return []operation{
		{
			method: http.MethodPost, path: "/api/v1/prs/{id}/merge", id: "mergePR", tag: "prs",
			summary:    "Squash-merge a pull request",
			pathParams: []any{controllers.PRIDParam{}},
			reqBody:    controllers.MergePRRequest{},
			resps: []respUnit{
				{http.StatusBadRequest, envelope.APIError{}},
				{http.StatusOK, controllers.MergePRResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusConflict, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
		{
			method: http.MethodPost, path: "/api/v1/prs/{id}/resolve-comments", id: "resolveComments", tag: "prs",
			summary:    "Resolve review threads on a pull request",
			pathParams: []any{controllers.PRIDParam{}},
			reqBody:    nil, // body is optional: omitting it resolves all unresolved threads
			resps: []respUnit{
				{http.StatusOK, controllers.ResolveCommentsResponse{}},
				{http.StatusNotFound, envelope.APIError{}},
				{http.StatusUnprocessableEntity, envelope.APIError{}},
				{http.StatusNotImplemented, envelope.APIError{}},
			},
		},
	}
}
